package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"pan/backend/internal/httpapi"
	"pan/backend/internal/storage"
)

func (w *worker) finalizeUpload(ctx context.Context, payload []byte) error {
	var input struct {
		UploadID uuid.UUID `json:"uploadId"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return err
	}
	var userID, nodeID, assetID, spaceID uuid.UUID
	var stage, destination, method, state, section, mime string
	var multipartID, expectedDigest *string
	var expected int64
	var partsJSON []byte
	err := w.db.QueryRow(ctx, `SELECT u.user_id,u.node_id,u.asset_id,a.space_id,u.staging_key,a.object_key,u.method,u.state,n.section,a.mime_type,u.upload_id,u.expected_size,u.expected_sha256,u.completion_parts FROM upload_sessions u JOIN assets a ON a.id=u.asset_id JOIN nodes n ON n.id=u.node_id WHERE u.id=$1`, input.UploadID).Scan(&userID, &nodeID, &assetID, &spaceID, &stage, &destination, &method, &state, &section, &mime, &multipartID, &expected, &expectedDigest, &partsJSON)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if state != "completing" {
		return nil
	}
	if err := httpapi.ValidateUploadPermission(ctx, w.db, userID, nodeID); err != nil {
		if errors.Is(err, httpapi.ErrUploadPermission) {
			return fmt.Errorf("%w: %v", storage.ErrIntegrity, err)
		}
		return err
	}
	if method == "multipart" {
		if multipartID == nil {
			return fmt.Errorf("missing multipart ID")
		}
		// A successful CompleteMultipart may outlive a lost response or a
		// worker restart. An existing staging object is already assembled.
		if _, _, err := w.store.Head(ctx, stage); err != nil {
			var parts []storage.CompletedPart
			if err := json.Unmarshal(partsJSON, &parts); err != nil {
				return err
			}
			sort.Slice(parts, func(i, j int) bool { return parts[i].Number < parts[j].Number })
			if err := w.store.CompleteMultipart(ctx, stage, *multipartID, parts); err != nil {
				return err
			}
		}
	}
	// Each attempt writes a fresh key. Even a worker that lost its lease may
	// finish copying later, but it can never overwrite a published attempt.
	destination = "original/" + spaceID.String() + "/" + assetID.String() + "/" + uuid.NewString()
	if _, err := w.db.Exec(ctx, `INSERT INTO object_cleanup(object_key,delete_after) VALUES($1,now()+interval '25 hours')`, destination); err != nil {
		return err
	}
	digest, err := w.store.Seal(ctx, stage, destination, expected)
	if err != nil {
		return err
	}
	if expectedDigest != nil && *expectedDigest != digest {
		return fmt.Errorf("%w: file SHA-256 mismatch", storage.ErrIntegrity)
	}
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `SELECT state FROM upload_sessions WHERE id=$1 FOR UPDATE`, input.UploadID).Scan(&state); err == pgx.ErrNoRows {
		return nil
	} else if err != nil {
		return err
	}
	if state != "completing" {
		return nil
	}
	if err := httpapi.ValidateUploadPermission(ctx, tx, userID, nodeID); err != nil {
		if errors.Is(err, httpapi.ErrUploadPermission) {
			return fmt.Errorf("%w: %v", storage.ErrIntegrity, err)
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE assets SET status='ready',size_bytes=$2,sha256=$3,object_key=$4 WHERE id=$1`, assetID, expected, digest, destination); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET state='ready' WHERE id=$1`, input.UploadID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE spaces SET reserved_bytes=reserved_bytes-$2,used_bytes=used_bytes+$2 WHERE id=$1 AND reserved_bytes >= $2`, spaceID, expected)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("missing quota reservation")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM object_cleanup WHERE object_key=$1`, destination); err != nil {
		return err
	}
	if section == "photos" {
		if _, err := tx.Exec(ctx, `INSERT INTO photo_details(asset_id) VALUES($1) ON CONFLICT DO NOTHING`, assetID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO jobs(id,kind,payload) VALUES($1,'index_photo',jsonb_build_object('assetId',$2::text))`, uuid.New(), assetID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE upload_batches SET completed_files=completed_files+1,state=CASE WHEN completed_files+1>=total_files THEN 'ready' ELSE state END WHERE id=(SELECT batch_id FROM upload_sessions WHERE id=$1)`, input.UploadID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_events(id,household_id,actor_user_id,action,resource_type,resource_id,metadata,visibility) SELECT $1,household_id,$2,'node.upload','node',$3,jsonb_build_object('sizeBytes',$4::bigint,'sha256',$5::text),audit_visibility(household_id,'node',$3,id) FROM spaces WHERE id=$6`, uuid.New(), userID, nodeID, expected, digest, spaceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *worker) failFinalization(ctx context.Context, payload []byte) error {
	var input struct {
		UploadID uuid.UUID `json:"uploadId"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return err
	}
	_, err := w.db.Exec(ctx, `WITH failed AS (UPDATE upload_sessions SET state='failed' WHERE id=$1 AND state='completing' RETURNING asset_id,expected_size), marked AS (UPDATE assets a SET status='failed' FROM failed f WHERE a.id=f.asset_id RETURNING a.space_id,f.expected_size) UPDATE spaces s SET reserved_bytes=GREATEST(0,reserved_bytes-m.expected_size) FROM marked m WHERE s.id=m.space_id`, input.UploadID)
	return err
}

func (w *worker) cleanupObjects(ctx context.Context) {
	// A process may crash after recording its final failed attempt but before
	// releasing quota. Reconcile that durable state on every maintenance pass.
	if _, err := w.db.Exec(ctx, `WITH failed AS (
	 UPDATE upload_sessions u SET state='failed' WHERE u.state='completing'
	 AND EXISTS(SELECT 1 FROM jobs j WHERE j.kind='finalize_upload' AND j.payload->>'uploadId'=u.id::text AND j.state='failed' AND j.attempts>=6)
	 RETURNING asset_id,expected_size
	), marked AS (
	 UPDATE assets a SET status='failed' FROM failed f WHERE a.id=f.asset_id RETURNING a.space_id,f.expected_size
	), totals AS (SELECT space_id,sum(expected_size) AS bytes FROM marked GROUP BY space_id)
	 UPDATE spaces s SET reserved_bytes=GREATEST(0,reserved_bytes-t.bytes) FROM totals t WHERE s.id=t.space_id`); err != nil {
		slog.Error("reconcile failed uploads", "error", err)
	}
	rows, err := w.db.Query(ctx, `SELECT o.object_key,o.multipart_id FROM object_cleanup o WHERE o.delete_after<now() AND NOT EXISTS(SELECT 1 FROM assets a WHERE a.object_key=o.object_key AND a.status='ready') AND NOT EXISTS(SELECT 1 FROM upload_sessions u JOIN assets a ON a.id=u.asset_id WHERE u.state='completing' AND (u.staging_key=o.object_key OR a.object_key=o.object_key)) ORDER BY o.delete_after LIMIT 25`)
	if err != nil {
		slog.Error("list object cleanup", "error", err)
		return
	}
	type item struct {
		key       string
		multipart *string
	}
	var items []item
	for rows.Next() {
		var i item
		if err := rows.Scan(&i.key, &i.multipart); err != nil {
			rows.Close()
			return
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	for _, i := range items {
		cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
		if i.multipart != nil {
			if err := w.store.AbortMultipart(cleanup, i.key, *i.multipart); err != nil {
				cancel()
				slog.Warn("multipart cleanup failed", "error", err)
				continue
			}
		}
		err := w.store.Delete(cleanup, i.key)
		cancel()
		if err != nil {
			slog.Warn("object cleanup failed", "error", err)
			continue
		}
		if _, err := w.db.Exec(ctx, `DELETE FROM object_cleanup WHERE object_key=$1`, i.key); err != nil {
			slog.Warn("record object cleanup", "error", err)
		}
	}
}
