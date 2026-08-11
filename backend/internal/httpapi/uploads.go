package httpapi

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"pan/backend/internal/storage"
)

const multipartThreshold int64 = 32 << 20

type uploadRecord struct {
	ID           uuid.UUID
	AssetID      uuid.UUID
	NodeID       uuid.UUID
	UserID       uuid.UUID
	SpaceID      uuid.UUID
	ObjectKey    string
	UploadID     *string
	Method       string
	ExpectedSize int64
	State        string
	ExpiresAt    time.Time
}

func (s *Server) createUploadBatch(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var input struct {
		SpaceID    uuid.UUID  `json:"spaceId"`
		ParentID   *uuid.UUID `json:"parentId"`
		Folders    []string   `json:"folders"`
		TotalFiles int        `json:"totalFiles"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	level, err := s.parentPermission(r.Context(), a, input.SpaceID, input.ParentID)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "目标目录不存在")
		return
	}
	if len(input.Folders) > 500 {
		writeError(w, http.StatusBadRequest, "too_many_entries", "每批最多提交 500 个目录")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	for _, folder := range input.Folders {
		parts, err := cleanRelativePath(folder)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_path", "目录路径不合法")
			return
		}
		if _, err := s.ensureFolderPath(r.Context(), tx, input.SpaceID, input.ParentID, parts, a.UserID); err != nil {
			internalError(w, err)
			return
		}
	}
	id := uuid.New()
	expires := time.Now().Add(s.cfg.UploadTTL)
	if _, err := tx.Exec(r.Context(), `INSERT INTO upload_batches(id,space_id,created_by,total_files,expires_at) VALUES($1,$2,$3,$4,$5)`, id, input.SpaceID, a.UserID, input.TotalFiles, expires); err != nil {
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "expiresAt": expires})
}

func cleanRelativePath(value string) ([]string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(value, "/") {
		return nil, fmt.Errorf("absolute paths are not allowed")
	}
	value = strings.Trim(value, "/")
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, "/")
	for i, part := range parts {
		name, err := cleanName(part)
		if err != nil {
			return nil, err
		}
		parts[i] = name
	}
	return parts, nil
}

func (s *Server) ensureFolderPath(ctx context.Context, tx pgx.Tx, spaceID uuid.UUID, parentID *uuid.UUID, parts []string, userID uuid.UUID) (*uuid.UUID, error) {
	current := parentID
	for _, name := range parts {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM nodes WHERE space_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND lower(name)=lower($3) AND kind='folder' AND deleted_at IS NULL`, spaceID, current, name).Scan(&id)
		if err == pgx.ErrNoRows {
			id = uuid.New()
			if _, err := tx.Exec(ctx, `INSERT INTO nodes(id,space_id,parent_id,kind,name,created_by) VALUES($1,$2,$3,'folder',$4,$5)`, id, spaceID, current, name, userID); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		}
		current = &id
	}
	return current, nil
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var input struct {
		SpaceID      uuid.UUID  `json:"spaceId"`
		ParentID     *uuid.UUID `json:"parentId"`
		BatchID      *uuid.UUID `json:"batchId"`
		Name         string     `json:"name"`
		RelativePath string     `json:"relativePath"`
		SizeBytes    int64      `json:"sizeBytes"`
		MimeType     string     `json:"mimeType"`
		Conflict     string     `json:"conflictPolicy"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.SizeBytes < 0 {
		writeError(w, http.StatusBadRequest, "invalid_size", "文件大小不合法")
		return
	}
	level, err := s.parentPermission(r.Context(), a, input.SpaceID, input.ParentID)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "目标目录不存在")
		return
	}
	name, err := cleanName(input.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_name", "文件名称不合法")
		return
	}
	if input.MimeType == "" {
		input.MimeType = "application/octet-stream"
	}
	input.MimeType = inferMimeType(name, input.MimeType)
	var directories []string
	if input.RelativePath != "" {
		parts, err := cleanRelativePath(input.RelativePath)
		if err != nil || len(parts) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_path", "相对路径不合法")
			return
		}
		if len(parts) > 1 {
			directories = parts[:len(parts)-1]
		}
		name = parts[len(parts)-1]
	}
	method := "put"
	if input.SizeBytes > multipartThreshold {
		method = "multipart"
	}
	assetID, nodeID, sessionID := uuid.New(), uuid.New(), uuid.New()
	objectKey := fmt.Sprintf("original/%s/%s", input.SpaceID, assetID)
	expires := time.Now().Add(s.cfg.UploadTTL)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var quota, used, reserved int64
	if err := tx.QueryRow(r.Context(), `SELECT quota_bytes,used_bytes,reserved_bytes FROM spaces WHERE id=$1 FOR UPDATE`, input.SpaceID).Scan(&quota, &used, &reserved); err != nil {
		internalError(w, err)
		return
	}
	if quota > 0 && used+reserved+input.SizeBytes > quota {
		writeError(w, http.StatusConflict, "quota_exceeded", "空间配额不足")
		return
	}
	parentID, err := s.ensureFolderPath(r.Context(), tx, input.SpaceID, input.ParentID, directories, a.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	name, err = s.resolveConflict(r.Context(), tx, a, input.SpaceID, parentID, name, input.Conflict)
	if err != nil {
		if err.Error() == "conflict" {
			writeError(w, http.StatusConflict, "name_conflict", "目标目录存在同名文件")
		} else {
			internalError(w, err)
		}
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE spaces SET reserved_bytes=reserved_bytes+$1 WHERE id=$2`, input.SizeBytes, input.SpaceID); err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO assets(id,space_id,object_key,size_bytes,mime_type,status,created_by) VALUES($1,$2,$3,$4,$5,'pending',$6)`, assetID, input.SpaceID, objectKey, input.SizeBytes, input.MimeType, a.UserID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO nodes(id,space_id,parent_id,asset_id,kind,name,created_by) VALUES($1,$2,$3,$4,'file',$5,$6)`, nodeID, input.SpaceID, parentID, assetID, name, a.UserID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO upload_sessions(id,batch_id,asset_id,node_id,user_id,method,expected_size,state,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,'uploading',$8)`, sessionID, input.BatchID, assetID, nodeID, a.UserID, method, input.SizeBytes, expires)
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	response := map[string]any{"id": sessionID, "nodeId": nodeID, "method": method, "name": name, "expiresAt": expires}
	if method == "put" {
		url, err := s.store.PresignPut(r.Context(), objectKey, input.MimeType)
		if err != nil {
			s.failUpload(r.Context(), sessionID, assetID, input.SpaceID, input.SizeBytes)
			internalError(w, err)
			return
		}
		response["url"] = url
	} else {
		uploadID, err := s.store.CreateMultipart(r.Context(), objectKey, input.MimeType)
		if err != nil {
			s.failUpload(r.Context(), sessionID, assetID, input.SpaceID, input.SizeBytes)
			internalError(w, err)
			return
		}
		if _, err := s.db.Exec(r.Context(), `UPDATE upload_sessions SET upload_id=$1 WHERE id=$2`, uploadID, sessionID); err != nil {
			_ = s.store.AbortMultipart(r.Context(), objectKey, uploadID)
			s.failUpload(r.Context(), sessionID, assetID, input.SpaceID, input.SizeBytes)
			internalError(w, err)
			return
		}
		response["partSize"] = choosePartSize(input.SizeBytes)
	}
	writeJSON(w, http.StatusCreated, response)
}

func choosePartSize(size int64) int64 {
	minimum := int64(16 << 20)
	needed := int64(math.Ceil(float64(size) / 10000.0))
	megabyte := int64(1 << 20)
	needed = ((needed + megabyte - 1) / megabyte) * megabyte
	if needed > minimum {
		return needed
	}
	return minimum
}

func (s *Server) resolveConflict(ctx context.Context, tx pgx.Tx, a actor, spaceID uuid.UUID, parentID *uuid.UUID, name, policy string) (string, error) {
	var existingID uuid.UUID
	var existingKind string
	err := tx.QueryRow(ctx, `SELECT id,kind FROM nodes WHERE space_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND lower(name)=lower($3) AND deleted_at IS NULL`, spaceID, parentID, name).Scan(&existingID, &existingKind)
	if err == pgx.ErrNoRows {
		return name, nil
	}
	if err != nil {
		return "", err
	}
	switch policy {
	case "skip":
		return "", fmt.Errorf("conflict")
	case "replace":
		if existingKind != "file" {
			return "", fmt.Errorf("conflict")
		}
		level, err := s.nodePermission(ctx, a, existingID)
		if err != nil || level < permissionEditor {
			return "", fmt.Errorf("conflict")
		}
		_, err = tx.Exec(ctx, `UPDATE nodes SET original_parent_id=parent_id,deleted_at=now(),updated_at=now() WHERE id=$1`, existingID)
		return name, err
	default:
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		for i := 1; i < 10000; i++ {
			candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE space_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND lower(name)=lower($3) AND deleted_at IS NULL)`, spaceID, parentID, candidate).Scan(&exists); err != nil {
				return "", err
			}
			if !exists {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("too many conflicts")
	}
}

func (s *Server) failUpload(ctx context.Context, sessionID, assetID, spaceID uuid.UUID, reserved int64) {
	_, _ = s.db.Exec(ctx, `UPDATE upload_sessions SET state='failed' WHERE id=$1`, sessionID)
	_, _ = s.db.Exec(ctx, `UPDATE assets SET status='failed' WHERE id=$1`, assetID)
	_, _ = s.db.Exec(ctx, `UPDATE spaces SET reserved_bytes=GREATEST(0,reserved_bytes-$1) WHERE id=$2`, reserved, spaceID)
}

func (s *Server) getUpload(ctx context.Context, id, userID uuid.UUID) (uploadRecord, error) {
	var item uploadRecord
	err := s.db.QueryRow(ctx, `
		SELECT u.id,u.asset_id,u.node_id,u.user_id,a.space_id,a.object_key,u.upload_id,u.method,u.expected_size,u.state,u.expires_at
		FROM upload_sessions u JOIN assets a ON a.id=u.asset_id
		WHERE u.id=$1 AND u.user_id=$2`, id, userID).Scan(&item.ID, &item.AssetID, &item.NodeID, &item.UserID, &item.SpaceID, &item.ObjectKey, &item.UploadID, &item.Method, &item.ExpectedSize, &item.State, &item.ExpiresAt)
	return item, err
}

func (s *Server) presignParts(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	upload, err := s.getUpload(r.Context(), id, a.UserID)
	if err != nil || upload.Method != "multipart" || upload.UploadID == nil || upload.ExpiresAt.Before(time.Now()) {
		writeError(w, http.StatusNotFound, "not_found", "上传会话不存在或已过期")
		return
	}
	var input struct {
		PartNumbers []int32 `json:"partNumbers"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.PartNumbers) == 0 || len(input.PartNumbers) > 50 {
		writeError(w, http.StatusBadRequest, "invalid_parts", "每次可签发 1 至 50 个分片")
		return
	}
	items := make([]map[string]any, 0, len(input.PartNumbers))
	for _, number := range input.PartNumbers {
		if number < 1 || number > 10000 {
			writeError(w, http.StatusBadRequest, "invalid_parts", "分片编号不合法")
			return
		}
		url, err := s.store.PresignPart(r.Context(), upload.ObjectKey, *upload.UploadID, number)
		if err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"partNumber": number, "url": url})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	upload, err := s.getUpload(r.Context(), id, a.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "上传会话不存在")
		return
	}
	if upload.State == "ready" {
		writeJSON(w, http.StatusOK, map[string]any{"id": upload.ID, "nodeId": upload.NodeID, "state": "ready"})
		return
	}
	if upload.ExpiresAt.Before(time.Now()) {
		writeError(w, http.StatusGone, "upload_expired", "上传会话已过期")
		return
	}
	var input struct {
		Parts []storage.CompletedPart `json:"parts"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if upload.Method == "multipart" {
		if upload.UploadID == nil || len(input.Parts) == 0 {
			writeError(w, http.StatusBadRequest, "invalid_parts", "缺少已上传分片")
			return
		}
		sort.Slice(input.Parts, func(i, j int) bool { return input.Parts[i].Number < input.Parts[j].Number })
		if err := s.store.CompleteMultipart(r.Context(), upload.ObjectKey, *upload.UploadID, input.Parts); err != nil {
			writeError(w, http.StatusConflict, "complete_failed", "分片校验失败，请重试")
			return
		}
	}
	actualSize, mime, err := s.store.Head(r.Context(), upload.ObjectKey)
	if err != nil {
		writeError(w, http.StatusConflict, "object_missing", "尚未检测到完整文件")
		return
	}
	if actualSize != upload.ExpectedSize {
		writeError(w, http.StatusConflict, "size_mismatch", "文件大小与上传任务不一致")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	result, err := tx.Exec(r.Context(), `UPDATE upload_sessions SET state='ready' WHERE id=$1 AND state<>'ready'`, upload.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	if result.RowsAffected() > 0 {
		if mime == "" {
			mime = "application/octet-stream"
		}
		if _, err = tx.Exec(r.Context(), `UPDATE assets SET status='ready',size_bytes=$1,mime_type=COALESCE(NULLIF(mime_type,'application/octet-stream'),$2) WHERE id=$3`, actualSize, mime, upload.AssetID); err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE spaces SET reserved_bytes=GREATEST(0,reserved_bytes-$1),used_bytes=used_bytes+$2 WHERE id=$3`, upload.ExpectedSize, actualSize, upload.SpaceID)
		}
		if err == nil && isPhotoMime(mime) {
			_, err = tx.Exec(r.Context(), `INSERT INTO photo_details(asset_id) VALUES($1) ON CONFLICT DO NOTHING`, upload.AssetID)
		}
		if err == nil && isPhotoMime(mime) {
			_, err = tx.Exec(r.Context(), `INSERT INTO jobs(id,kind,payload) VALUES($1,'index_photo',jsonb_build_object('assetId',$2::text))`, uuid.New(), upload.AssetID.String())
		}
		if err == nil {
			_, _ = tx.Exec(r.Context(), `UPDATE upload_batches SET completed_files=completed_files+1,state=CASE WHEN completed_files+1>=total_files THEN 'ready' ELSE state END WHERE id=(SELECT batch_id FROM upload_sessions WHERE id=$1)`, upload.ID)
		}
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	var uploadedName string
	_ = s.db.QueryRow(r.Context(), `SELECT name FROM nodes WHERE id=$1`, upload.NodeID).Scan(&uploadedName)
	s.audit(r.Context(), a, "node.upload", "node", &upload.NodeID, map[string]any{"name": uploadedName, "sizeBytes": actualSize})
	writeJSON(w, http.StatusOK, map[string]any{"id": upload.ID, "nodeId": upload.NodeID, "state": "ready"})
}

func isPhotoMime(mime string) bool {
	mime = strings.ToLower(strings.Split(mime, ";")[0])
	switch mime {
	case "image/jpeg", "image/png", "image/webp", "image/gif", "image/heic", "image/heif":
		return true
	default:
		return false
	}
}

func inferMimeType(name, provided string) string {
	if provided != "" && provided != "application/octet-stream" {
		return provided
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	default:
		return "application/octet-stream"
	}
}

func (s *Server) abortUpload(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	upload, err := s.getUpload(r.Context(), id, a.UserID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "上传会话不存在")
		return
	}
	if upload.UploadID != nil {
		_ = s.store.AbortMultipart(r.Context(), upload.ObjectKey, *upload.UploadID)
	}
	_ = s.store.Delete(r.Context(), upload.ObjectKey)
	tx, err := s.db.Begin(r.Context())
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE spaces SET reserved_bytes=GREATEST(0,reserved_bytes-$1) WHERE id=$2`, upload.ExpectedSize, upload.SpaceID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE upload_sessions SET state='aborted' WHERE id=$1`, upload.ID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM nodes WHERE id=$1`, upload.NodeID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM assets WHERE id=$1`, upload.AssetID)
	}
	if err != nil {
		_ = tx.Rollback(r.Context())
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listUploads(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	rows, err := s.db.Query(r.Context(), `SELECT u.id,u.node_id,n.name,u.method,u.expected_size,u.state,u.expires_at,u.created_at FROM upload_sessions u JOIN nodes n ON n.id=u.node_id WHERE u.user_id=$1 AND u.created_at>now()-interval '7 days' ORDER BY u.created_at DESC LIMIT 100`, a.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, nodeID uuid.UUID
		var name, method, state string
		var size int64
		var expires, created time.Time
		if err := rows.Scan(&id, &nodeID, &name, &method, &size, &state, &expires, &created); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "nodeId": nodeID, "name": name, "method": method, "sizeBytes": size, "state": state, "expiresAt": expires, "createdAt": created})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
