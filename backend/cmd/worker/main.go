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
	"path/filepath"
	"strconv"
	"strings"
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
	cfg := config.Load()
	ctx := context.Background()
	db, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	w := &worker{db: db, store: storage.New(cfg), cfg: cfg}
	slog.Info("worker started")
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if err := w.runOne(ctx); err != nil {
			slog.Error("job failed", "error", err)
		}
		w.runMaintenance(ctx)
		<-ticker.C
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
	jobCtx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()
	switch j.Kind {
	case "index_photo":
		err = w.indexPhoto(jobCtx, j.Payload)
	case "purge_node":
		err = w.purgeNode(jobCtx, j.Payload)
	case "cleanup_upload":
		err = w.cleanupUpload(jobCtx, j.Payload)
	default:
		err = fmt.Errorf("unknown job kind %q", j.Kind)
	}
	if err == nil {
		_, _ = w.db.Exec(ctx, `UPDATE jobs SET state='done',locked_at=NULL,updated_at=now(),last_error=NULL WHERE id=$1`, j.ID)
		return nil
	}
	backoff := time.Duration(1<<min(j.Attempts, 5)) * time.Minute
	_, _ = w.db.Exec(ctx, `UPDATE jobs SET state='failed',locked_at=NULL,last_error=$1,run_after=$2,updated_at=now() WHERE id=$3`, truncateError(err), time.Now().Add(backoff), j.ID)
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
	file, err := os.Create(inputPath)
	if err != nil {
		body.Close()
		return err
	}
	_, err = io.Copy(file, body)
	body.Close()
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	smallPath, largePath := filepath.Join(tempDir, "small.webp"), filepath.Join(tempDir, "large.webp")
	if output, err := exec.CommandContext(ctx, "vips", "thumbnail", inputPath, smallPath, "320", "--height", "320", "--crop", "centre").CombinedOutput(); err != nil {
		return fmt.Errorf("small thumbnail: %w: %s", err, string(output))
	}
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

func (w *worker) purgeNode(ctx context.Context, payload []byte) error {
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
	rows, err := w.db.Query(ctx, `
		WITH RECURSIVE tree AS (SELECT id FROM nodes WHERE id=$1 UNION ALL SELECT n.id FROM nodes n JOIN tree t ON n.parent_id=t.id)
		SELECT a.id,a.space_id,a.object_key,a.size_bytes,p.thumb_small_key,p.thumb_large_key
		FROM nodes n JOIN tree t ON t.id=n.id JOIN assets a ON a.id=n.asset_id LEFT JOIN photo_details p ON p.asset_id=a.id`, nodeID)
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
	if _, err := tx.Exec(ctx, `DELETE FROM nodes WHERE id=$1`, nodeID); err != nil {
		return err
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
	_, _ = w.db.Exec(ctx, `UPDATE jobs SET state='failed',locked_at=NULL,last_error='任务执行中断，已自动恢复',run_after=now(),updated_at=now() WHERE state='running' AND locked_at<now()-interval '15 minutes'`)
	w.enqueueExpiredUploadCleanup(ctx)
	_, _ = w.db.Exec(ctx, `DELETE FROM sessions WHERE expires_at<now()`)
	_, _ = w.db.Exec(ctx, `DELETE FROM share_access_tokens WHERE expires_at<now()`)
	_, _ = w.db.Exec(ctx, `
		INSERT INTO jobs(id,kind,payload)
		SELECT gen_random_uuid(),'purge_node',jsonb_build_object('nodeId',n.id::text)
		FROM nodes n WHERE n.deleted_at<now()-$1::interval
		AND NOT EXISTS(SELECT 1 FROM nodes p WHERE p.id=n.parent_id AND p.deleted_at IS NOT NULL)
		AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.kind='purge_node' AND (j.state IN ('pending','running') OR (j.state='failed' AND (j.attempts<6 OR j.updated_at>now()-interval '24 hours'))) AND j.payload->>'nodeId'=n.id::text)
		LIMIT 25`, durationInterval(w.cfg.TrashRetention))
}

func (w *worker) enqueueExpiredUploadCleanup(ctx context.Context) {
	_, _ = w.db.Exec(ctx, `
		INSERT INTO jobs(id,kind,payload)
		SELECT gen_random_uuid(),'cleanup_upload',jsonb_build_object('uploadId',u.id::text)
		FROM upload_sessions u
		WHERE ((u.state IN ('pending','uploading','completing') AND u.expires_at<now()) OR u.state='expired' OR (u.state='failed' AND u.created_at<now()-interval '1 hour'))
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
		SELECT u.node_id,u.asset_id,a.space_id,a.object_key,u.upload_id,u.expected_size,u.state,u.expires_at,u.created_at
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
		claim, err := w.db.Exec(ctx, `UPDATE upload_sessions SET state='expired' WHERE id=$1 AND state IN ('pending','uploading','completing') AND expires_at<now()`, uploadID)
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
			slog.Warn("abort expired multipart", "upload_id", uploadID, "error", err)
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
	activeExpired = ((state == "pending" || state == "uploading" || state == "completing") && expiresAt.Before(now)) || state == "expired"
	failedExpired = state == "failed" && createdAt.Before(now.Add(-time.Hour))
	return activeExpired, failedExpired
}

func durationInterval(value time.Duration) string {
	return fmt.Sprintf("%d seconds", int64(value.Seconds()))
}
