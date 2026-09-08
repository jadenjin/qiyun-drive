package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"pan/backend/internal/config"
	"pan/backend/internal/database"
	"pan/backend/internal/storage"
)

type worker struct {
	db    *pgxpool.Pool
	store *storage.Store
	cfg   config.Config
}

type job struct {
	ID       uuid.UUID
	Kind     string
	Payload  []byte
	Attempts int
}

const jobTimeout = 10 * time.Minute

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	w := &worker{db: db, store: storage.New(cfg), cfg: cfg}
	slog.Info("worker started")
	go func() {
		heartbeat := time.NewTicker(15 * time.Second)
		defer heartbeat.Stop()
		for {
			if _, err := db.Exec(ctx, `INSERT INTO runtime_health(component) VALUES('worker') ON CONFLICT(component) DO UPDATE SET updated_at=now()`); err != nil && ctx.Err() == nil {
				slog.Error("worker heartbeat failed", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
			}
		}
	}()
	go func() {
		maintenance := time.NewTicker(30 * time.Second)
		defer maintenance.Stop()
		for {
			w.runMaintenance(ctx)
			select {
			case <-ctx.Done():
				return
			case <-maintenance.C:
			}
		}
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := w.runOne(ctx); err != nil {
			slog.Error("job failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *worker) runOne(ctx context.Context) error {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var j job
	err = tx.QueryRow(ctx, `
		SELECT id,kind,payload,attempts FROM jobs
		WHERE state IN ('pending','failed') AND run_after<=now() AND attempts<6
		ORDER BY run_after FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&j.ID, &j.Kind, &j.Payload, &j.Attempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET state='running',attempts=attempts+1,locked_at=now(),updated_at=now() WHERE id=$1`, j.ID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	timeout := jobTimeout
	if j.Kind == "finalize_upload" || j.Kind == "seal_legacy_asset" {
		timeout = 24 * time.Hour
	}
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	claimedAttempt := j.Attempts + 1
	// Refresh the lease while a large finalization streams without buffering.
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				result, err := w.db.Exec(jobCtx, `UPDATE jobs SET locked_at=now() WHERE id=$1 AND state='running' AND attempts=$2`, j.ID, claimedAttempt)
				if err == nil && result.RowsAffected() == 0 {
					cancel()
					return
				}
			}
		}
	}()
	switch j.Kind {
	case "finalize_upload":
		err = w.finalizeUpload(jobCtx, j.Payload)
	case "seal_legacy_asset":
		err = w.sealLegacyAsset(jobCtx, j.Payload)
	case "index_photo":
		err = w.indexPhoto(jobCtx, j.Payload)
	case "purge_node":
		err = w.purgeNode(jobCtx, j.ID, j.Payload)
	case "cleanup_upload":
		err = w.cleanupUpload(jobCtx, j.Payload)
	default:
		err = fmt.Errorf("unknown job kind %q", j.Kind)
	}
	if err == nil {
		_, _ = w.db.Exec(ctx, `UPDATE jobs SET state='done',locked_at=NULL,updated_at=now(),last_error=NULL WHERE id=$1 AND state='running' AND attempts=$2`, j.ID, claimedAttempt)
		return nil
	}
	if j.Kind == "finalize_upload" && errors.Is(err, storage.ErrIntegrity) {
		j.Attempts = 5
	}
	backoff := time.Duration(1<<min(j.Attempts, 5)) * time.Minute
	result, updateErr := w.db.Exec(ctx, `UPDATE jobs SET state='failed',locked_at=NULL,last_error=$1,run_after=$2,updated_at=now(),attempts=$4 WHERE id=$3 AND state='running' AND attempts=$5`, truncateError(err), time.Now().Add(backoff), j.ID, j.Attempts+1, claimedAttempt)
	if updateErr != nil {
		return fmt.Errorf("record failed job: %w", updateErr)
	}
	if result.RowsAffected() == 0 {
		return err
	}
	if j.Kind == "finalize_upload" && j.Attempts+1 >= 6 {
		if failErr := w.failFinalization(ctx, j.Payload); failErr != nil {
			slog.Error("release failed upload", "error", failErr)
		}
	}
	if j.Kind == "purge_node" && j.Attempts+1 >= 6 {
		// Give the user a recovery path after the final failed attempt. A later
		// maintenance pass may claim the still-deleted node again.
		_, _ = w.db.Exec(ctx, `UPDATE nodes SET purge_job_id=NULL WHERE purge_job_id=$1`, j.ID)
	}
	return err
}

func truncateError(err error) string {
	message := []rune(err.Error())
	if len(message) > 2000 {
		return string(message[:2000])
	}
	return string(message)
}

func (w *worker) indexPhoto(ctx context.Context, payload []byte) error {
	var input struct {
		AssetID string `json:"assetId"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return err
	}
	assetID, err := uuid.Parse(input.AssetID)
	if err != nil {
		return err
	}
	var key, mime string
	if err := w.db.QueryRow(ctx, `SELECT object_key,mime_type FROM assets WHERE id=$1 AND status='ready'`, assetID).Scan(&key, &mime); err != nil {
		return err
	}
	tempDir, err := os.MkdirTemp("", "pan-photo-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	inputPath := filepath.Join(tempDir, "original")
	body, err := w.store.Get(ctx, key)
	if err != nil {
		return err
	}
	// #nosec G304 -- inputPath is generated beneath the private temporary
	// directory created immediately above, never from request or database data.
	file, err := os.Create(inputPath)
	if err != nil {
		_ = body.Close()
		return err
	}
	_, err = io.Copy(file, body)
	if closeErr := body.Close(); err == nil {
		err = closeErr
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	smallPath, largePath := filepath.Join(tempDir, "small.webp"), filepath.Join(tempDir, "large.webp")
	// #nosec G204 -- Arguments are fixed or generated beneath this job's private
	// temporary directory; CommandContext does not invoke a shell.
	if output, err := exec.CommandContext(ctx, "vips", "thumbnail", inputPath, smallPath, "320", "--height", "320", "--crop", "centre").CombinedOutput(); err != nil {
		return fmt.Errorf("small thumbnail: %w: %s", err, string(output))
	}
	// #nosec G204 -- See the small-thumbnail invocation above.
	if output, err := exec.CommandContext(ctx, "vips", "thumbnail", inputPath, largePath, "1600", "--height", "1600").CombinedOutput(); err != nil {
		return fmt.Errorf("large thumbnail: %w: %s", err, string(output))
	}
	smallKey, largeKey := "derivatives/"+assetID.String()+"/small.webp", "derivatives/"+assetID.String()+"/large.webp"
	if err := putFile(ctx, w.store, smallKey, smallPath); err != nil {
		return err
	}
	if err := putFile(ctx, w.store, largeKey, largePath); err != nil {
		return err
	}
	meta := readMetadata(ctx, inputPath)
	_, err = w.db.Exec(ctx, `UPDATE photo_details SET taken_at=$1,width=$2,height=$3,camera=$4,thumb_small_key=$5,thumb_large_key=$6,indexed_at=now() WHERE asset_id=$7`, meta.TakenAt, meta.Width, meta.Height, meta.Camera, smallKey, largeKey, assetID)
	return err
}

func putFile(ctx context.Context, store *storage.Store, key, path string) error {
	// #nosec G304 -- Every caller passes a path generated beneath a private
	// os.MkdirTemp directory, never a request or database value.
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	return store.Put(ctx, key, "image/webp", file, info.Size())
}

type photoMetadata struct {
	TakenAt *time.Time
	Width   *int
	Height  *int
	Camera  *string
}

func readMetadata(ctx context.Context, inputPath string) photoMetadata {
	// #nosec G204 -- inputPath is generated beneath a private temporary
	// directory and no shell is involved.
	output, err := exec.CommandContext(ctx, "exiftool", "-json", "-DateTimeOriginal", "-OffsetTimeOriginal", "-ImageWidth", "-ImageHeight", "-Make", "-Model", inputPath).Output()
	if err != nil {
		return photoMetadata{}
	}
	var rows []map[string]any
	if json.Unmarshal(output, &rows) != nil || len(rows) == 0 {
		return photoMetadata{}
	}
	row := rows[0]
	meta := photoMetadata{}
	if value, ok := numberToInt(row["ImageWidth"]); ok {
		meta.Width = &value
	}
	if value, ok := numberToInt(row["ImageHeight"]); ok {
		meta.Height = &value
	}
	camera := strings.TrimSpace(fmt.Sprint(row["Make"]) + " " + fmt.Sprint(row["Model"]))
	camera = strings.ReplaceAll(camera, "<nil>", "")
	camera = strings.TrimSpace(camera)
	if camera != "" {
		meta.Camera = &camera
	}
	if raw, ok := row["DateTimeOriginal"].(string); ok {
		layout := "2006:01:02 15:04:05"
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			meta.TakenAt = &parsed
		}
	}
	return meta
}

func numberToInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case string:
		parsed, err := strconv.Atoi(v)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func (w *worker) purgeNode(ctx context.Context, jobID uuid.UUID, payload []byte) error {
	var input struct {
		NodeID string `json:"nodeId"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return err
	}
	nodeID, err := uuid.Parse(input.NodeID)
	if err != nil {
		return err
	}
	claimTx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer claimTx.Rollback(ctx)
	if _, err := claimTx.Exec(ctx, `
		WITH RECURSIVE tree AS (
		  SELECT id FROM nodes WHERE id=$1 AND deleted_at IS NOT NULL AND purge_job_id=$2
		  UNION ALL SELECT n.id FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE n.deleted_at IS NOT NULL
		)
		UPDATE nodes n SET purge_job_id=$2 FROM tree t
		WHERE n.id=t.id AND n.deleted_at IS NOT NULL AND (n.purge_job_id IS NULL OR n.purge_job_id=$2)`, nodeID, jobID); err != nil {
		return err
	}
	var conflictingClaim bool
	if err := claimTx.QueryRow(ctx, `
		WITH RECURSIVE tree AS (
		  SELECT id,purge_job_id FROM nodes WHERE id=$1 AND deleted_at IS NOT NULL AND purge_job_id=$2
		  UNION ALL SELECT n.id,n.purge_job_id FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE n.deleted_at IS NOT NULL
		)
		SELECT EXISTS(SELECT 1 FROM tree WHERE purge_job_id IS DISTINCT FROM $2)`, nodeID, jobID).Scan(&conflictingClaim); err != nil {
		return err
	}
	if conflictingClaim {
		return fmt.Errorf("purge subtree has a conflicting claim")
	}
	if err := claimTx.Commit(ctx); err != nil {
		return err
	}
	rows, err := w.db.Query(ctx, `
		WITH RECURSIVE tree AS (SELECT id FROM nodes WHERE id=$1 AND deleted_at IS NOT NULL AND purge_job_id=$2 UNION ALL SELECT n.id FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE n.deleted_at IS NOT NULL AND n.purge_job_id=$2)
		SELECT a.id,a.space_id,a.object_key,a.size_bytes,p.thumb_small_key,p.thumb_large_key
		FROM nodes n JOIN tree t ON t.id=n.id JOIN assets a ON a.id=n.asset_id LEFT JOIN photo_details p ON p.asset_id=a.id`, nodeID, jobID)
	if err != nil {
		return err
	}
	type doomed struct {
		assetID, spaceID uuid.UUID
		key              string
		size             int64
		small, large     *string
	}
	items := make([]doomed, 0)
	for rows.Next() {
		var item doomed
		if err := rows.Scan(&item.assetID, &item.spaceID, &item.key, &item.size, &item.small, &item.large); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	var claimed bool
	if err := w.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND deleted_at IS NOT NULL AND purge_job_id=$2)`, nodeID, jobID).Scan(&claimed); err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	for _, item := range items {
		if err := w.store.Delete(ctx, item.key); err != nil {
			return err
		}
		if item.small != nil {
			_ = w.store.Delete(ctx, *item.small)
		}
		if item.large != nil {
			_ = w.store.Delete(ctx, *item.large)
		}
	}
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Delete the node tree first. Deleting an asset while a file node still
	// references it would set nodes.asset_id to NULL and violate nodes_check.
	result, err := tx.Exec(ctx, `DELETE FROM nodes WHERE id=$1 AND deleted_at IS NOT NULL AND purge_job_id=$2`, nodeID, jobID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("purge claim was lost")
	}
	for _, item := range items {
		if _, err := tx.Exec(ctx, `DELETE FROM assets WHERE id=$1`, item.assetID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE spaces SET used_bytes=GREATEST(0,used_bytes-$1) WHERE id=$2`, item.size, item.spaceID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (w *worker) runMaintenance(ctx context.Context) {
	_, _ = w.db.Exec(ctx, `INSERT INTO jobs(id,kind,payload) SELECT gen_random_uuid(),'seal_legacy_asset',jsonb_build_object('assetId',a.id::text) FROM assets a WHERE a.status='ready' AND a.sha256 IS NULL AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.kind='seal_legacy_asset' AND j.payload->>'assetId'=a.id::text) ORDER BY a.created_at LIMIT 25`)
	_, _ = w.db.Exec(ctx, `DELETE FROM security_events WHERE created_at<now()-interval '90 days'`)
	w.cleanupObjects(ctx)
	_, _ = w.db.Exec(ctx, `UPDATE jobs SET state='failed',locked_at=NULL,last_error='任务执行中断，已自动恢复',run_after=now(),updated_at=now() WHERE state='running' AND locked_at<now()-interval '15 minutes'`)
	// Reconcile the narrow crash window between a purge job exhausting its
	// retries and runOne releasing the claim, so a deleted node cannot become
	// permanently impossible to restore.
	_, _ = w.db.Exec(ctx, `UPDATE nodes n SET purge_job_id=NULL WHERE n.purge_job_id IS NOT NULL AND NOT EXISTS (
		SELECT 1 FROM jobs j WHERE j.id=n.purge_job_id AND (j.state IN ('pending','running') OR (j.state='failed' AND j.attempts<6))
	)`)
	w.enqueueExpiredUploadCleanup(ctx)
	_, _ = w.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at<now()`)
	_, _ = w.db.Exec(ctx, `DELETE FROM share_access_tokens WHERE expires_at<now()`)
	_, _ = w.db.Exec(ctx, `DELETE FROM invitations WHERE (accepted_at IS NOT NULL OR expires_at<now()) AND created_at<now()-interval '30 days'`)
	_, _ = w.db.Exec(ctx, `DELETE FROM password_resets WHERE (used_at IS NOT NULL OR expires_at<now()) AND created_at<now()-interval '30 days'`)
	_, _ = w.db.Exec(ctx, `
		WITH candidates AS (
			SELECT n.id FROM nodes n WHERE n.deleted_at<now()-$1::interval AND n.purge_job_id IS NULL
		AND NOT EXISTS(SELECT 1 FROM nodes p WHERE p.id=n.parent_id AND p.deleted_at IS NOT NULL)
		AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.kind='purge_node' AND (j.state IN ('pending','running') OR (j.state='failed' AND (j.attempts<6 OR j.updated_at>now()-interval '24 hours'))) AND j.payload->>'nodeId'=n.id::text)
		ORDER BY n.deleted_at LIMIT 25
		), claims AS (
			UPDATE nodes n SET purge_job_id=gen_random_uuid() FROM candidates c WHERE n.id=c.id
			RETURNING n.id,n.purge_job_id
		)
		INSERT INTO jobs(id,kind,payload)
		SELECT purge_job_id,'purge_node',jsonb_build_object('nodeId',id::text) FROM claims`, durationInterval(w.cfg.TrashRetention))
}

func (w *worker) enqueueExpiredUploadCleanup(ctx context.Context) {
	_, _ = w.db.Exec(ctx, `
		INSERT INTO jobs(id,kind,payload)
		SELECT gen_random_uuid(),'cleanup_upload',jsonb_build_object('uploadId',u.id::text)
		FROM upload_sessions u
		WHERE ((u.state IN ('pending','uploading') AND u.expires_at<now()) OR u.state='expired' OR (u.state='failed' AND u.created_at<now()-interval '1 hour'))
		AND NOT EXISTS(
			SELECT 1 FROM jobs j WHERE j.kind='cleanup_upload'
			AND (j.state IN ('pending','running') OR (j.state='failed' AND (j.attempts<6 OR j.updated_at>now()-interval '24 hours')))
			AND j.payload->>'uploadId'=u.id::text
		)
		ORDER BY u.created_at LIMIT 25`)
}

func (w *worker) cleanupUpload(ctx context.Context, payload []byte) error {
	var input struct {
		UploadID string `json:"uploadId"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return err
	}
	uploadID, err := uuid.Parse(input.UploadID)
	if err != nil {
		return err
	}
	var nodeID, assetID, spaceID uuid.UUID
	var key, state string
	var multipartID *string
	var expected int64
	var expiresAt, createdAt time.Time
	err = w.db.QueryRow(ctx, `
		SELECT u.node_id,u.asset_id,a.space_id,COALESCE(u.staging_key,a.object_key),u.upload_id,u.expected_size,u.state,u.expires_at,u.created_at
		FROM upload_sessions u JOIN assets a ON a.id=u.asset_id WHERE u.id=$1`, uploadID).Scan(&nodeID, &assetID, &spaceID, &key, &multipartID, &expected, &state, &expiresAt, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	activeExpired, failedExpired := cleanupEligibility(state, expiresAt, createdAt, time.Now())
	if !activeExpired && !failedExpired {
		return nil
	}
	if state != "expired" && activeExpired {
		claim, err := w.db.Exec(ctx, `UPDATE upload_sessions SET state='expired' WHERE id=$1 AND state IN ('pending','uploading') AND expires_at<now()`, uploadID)
		if err != nil {
			return err
		}
		if claim.RowsAffected() == 0 {
			return nil
		}
		state = "expired"
	}
	if multipartID != nil {
		if err := w.store.AbortMultipart(ctx, key, *multipartID); err != nil {
			return fmt.Errorf("abort expired multipart: %w", err)
		}
	}
	if err := w.store.Delete(ctx, key); err != nil {
		return err
	}
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var lockedState string
	var lockedCreated time.Time
	if err := tx.QueryRow(ctx, `SELECT state,created_at FROM upload_sessions WHERE id=$1 FOR UPDATE`, uploadID).Scan(&lockedState, &lockedCreated); errors.Is(err, pgx.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	releaseReservation := lockedState == "expired"
	canDelete := releaseReservation || (lockedState == "failed" && lockedCreated.Before(time.Now().Add(-time.Hour)))
	if !canDelete {
		return nil
	}
	if releaseReservation {
		if _, err := tx.Exec(ctx, `UPDATE spaces SET reserved_bytes=GREATEST(0,reserved_bytes-$1) WHERE id=$2`, expected, spaceID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM nodes WHERE id=$1`, nodeID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM assets WHERE id=$1`, assetID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func cleanupEligibility(state string, expiresAt, createdAt, now time.Time) (activeExpired, failedExpired bool) {
	activeExpired = ((state == "pending" || state == "uploading") && expiresAt.Before(now)) || state == "expired"
	failedExpired = state == "failed" && createdAt.Before(now.Add(-time.Hour))
	return activeExpired, failedExpired
}

func durationInterval(value time.Duration) string {
	return fmt.Sprintf("%d seconds", int64(value.Seconds()))
}
