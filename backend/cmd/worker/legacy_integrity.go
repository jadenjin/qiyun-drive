package main

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Bring originals from older releases under the same immutable publication
// model. This also provides a trusted digest for full backup rehearsals.
func (w *worker) sealLegacyAsset(ctx context.Context, payload []byte) error {
	var input struct {
		AssetID uuid.UUID `json:"assetId"`
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return err
	}
	var source string
	var spaceID uuid.UUID
	var size int64
	err := w.db.QueryRow(ctx, `SELECT object_key,space_id,size_bytes FROM assets WHERE id=$1 AND status='ready' AND sha256 IS NULL`, input.AssetID).Scan(&source, &spaceID, &size)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	destination := "original/" + spaceID.String() + "/" + input.AssetID.String() + "/" + uuid.NewString()
	if _, err := w.db.Exec(ctx, `INSERT INTO object_cleanup(object_key,delete_after) VALUES($1,now()+interval '25 hours')`, destination); err != nil {
		return err
	}
	digest, err := w.store.Seal(ctx, source, destination, size)
	if err != nil {
		return err
	}
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE assets SET object_key=$2,sha256=$3 WHERE id=$1 AND object_key=$4 AND sha256 IS NULL AND status='ready'`, input.AssetID, destination, digest, source)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO object_cleanup(object_key,delete_after) VALUES($1,now()+interval '25 hours') ON CONFLICT(object_key) DO UPDATE SET delete_after=GREATEST(object_cleanup.delete_after,EXCLUDED.delete_after)`, source); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM object_cleanup WHERE object_key=$1`, destination); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
