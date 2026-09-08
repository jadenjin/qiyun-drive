package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"pan/backend/internal/storage"
)

const (
	multipartThreshold int64 = 32 << 20
	maxObjectSize      int64 = 5 << 40
)

var (
	errUploadPathForbidden = errors.New("upload path is not editable")
	errUploadPathConflict  = errors.New("upload path conflicts with a file")
)

type uploadRecord struct {
	ID           uuid.UUID
	AssetID      uuid.UUID
	NodeID       uuid.UUID
	UserID       uuid.UUID
	SpaceID      uuid.UUID
	ObjectKey    string
	StagingKey   string
	MimeType     string
	UploadID     *string
	Method       string
	ExpectedSize int64
	State        string
	Section      string
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
	if input.TotalFiles < 1 || input.TotalFiles > 100000 {
		writeError(w, http.StatusBadRequest, "invalid_file_count", "每批文件数量需为 1 至 100000")
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
		if _, err := s.ensureFolderPath(r.Context(), tx, a, input.SpaceID, input.ParentID, parts); err != nil {
			if errors.Is(err, errUploadPathForbidden) {
				writeError(w, http.StatusNotFound, "not_found", "目标目录不存在")
			} else if errors.Is(err, errUploadPathConflict) {
				writeError(w, http.StatusConflict, "path_conflict", "目录路径与已有文件冲突")
			} else {
				internalError(w, err)
			}
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
	if len(value) > 4096 {
		return nil, fmt.Errorf("path is too long")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(value, "/") {
		return nil, fmt.Errorf("absolute paths are not allowed")
	}
	value = strings.Trim(value, "/")
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, "/")
	if len(parts) > 64 {
		return nil, fmt.Errorf("path is too deep")
	}
	for i, part := range parts {
		name, err := cleanName(part)
		if err != nil {
			return nil, err
		}
		parts[i] = name
	}
	return parts, nil
}

func (s *Server) ensureFolderPath(ctx context.Context, tx pgx.Tx, a actor, spaceID uuid.UUID, parentID *uuid.UUID, parts []string) (*uuid.UUID, error) {
	current := parentID
	for _, name := range parts {
		var id uuid.UUID
		var kind string
		err := tx.QueryRow(ctx, `SELECT id,kind FROM nodes WHERE space_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND section='files' AND lower(name)=lower($3) AND deleted_at IS NULL`, spaceID, current, name).Scan(&id, &kind)
		if err == pgx.ErrNoRows {
			id = uuid.New()
			if _, err := tx.Exec(ctx, `INSERT INTO nodes(id,space_id,parent_id,kind,name,created_by) VALUES($1,$2,$3,'folder',$4,$5)`, id, spaceID, current, name, a.UserID); err != nil {
				return nil, err
			}
		} else if err != nil {
			return nil, err
		} else if kind != "folder" {
			return nil, errUploadPathConflict
		} else {
			level, permissionErr := nodePermissionWith(ctx, tx, a, id)
			if permissionErr != nil || level < permissionEditor {
				return nil, errUploadPathForbidden
			}
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
		Section      string     `json:"section"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.SizeBytes < 0 || input.SizeBytes > maxObjectSize {
		writeError(w, http.StatusBadRequest, "invalid_size", "文件大小不合法，单个文件最大为 5 TB")
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
	if input.Section == "" {
		input.Section = "files"
	}
	if input.Section != "files" && input.Section != "photos" {
		writeError(w, http.StatusBadRequest, "invalid_section", "上传区域不合法")
		return
	}
	if input.Section == "photos" {
		if input.ParentID != nil || input.RelativePath != "" && strings.ContainsAny(input.RelativePath, `/\\`) {
			writeError(w, http.StatusBadRequest, "invalid_photo_path", "照片不能上传到文件目录")
			return
		}
		if !isPhotoMime(input.MimeType) {
			writeError(w, http.StatusBadRequest, "invalid_photo_type", "照片区域只支持图片文件")
			return
		}
	}
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
	stagingKey := "staging/" + sessionID.String()
	expires := time.Now().Add(s.cfg.UploadTTL)
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if input.BatchID != nil {
		var validBatch bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM upload_batches WHERE id=$1 AND space_id=$2 AND created_by=$3 AND expires_at>now())`, *input.BatchID, input.SpaceID, a.UserID).Scan(&validBatch); err != nil {
			internalError(w, err)
			return
		}
		if !validBatch {
			writeError(w, http.StatusNotFound, "invalid_batch", "上传批次不存在或已过期")
			return
		}
	}
	var quota, used, reserved int64
	if err := tx.QueryRow(r.Context(), `SELECT quota_bytes,used_bytes,reserved_bytes FROM spaces WHERE id=$1 FOR UPDATE`, input.SpaceID).Scan(&quota, &used, &reserved); err != nil {
		internalError(w, err)
		return
	}
	if used > math.MaxInt64-reserved || used+reserved > math.MaxInt64-input.SizeBytes {
		writeError(w, http.StatusConflict, "quota_exceeded", "空间容量记录已达到上限")
		return
	}
	if quota > 0 && input.SizeBytes > quota-used-reserved {
		writeError(w, http.StatusConflict, "quota_exceeded", "空间配额不足")
		return
	}
	parentID, err := s.ensureFolderPath(r.Context(), tx, a, input.SpaceID, input.ParentID, directories)
	if err != nil {
		if errors.Is(err, errUploadPathForbidden) {
			writeError(w, http.StatusNotFound, "not_found", "目标目录不存在")
		} else if errors.Is(err, errUploadPathConflict) {
			writeError(w, http.StatusConflict, "path_conflict", "目录路径与已有文件冲突")
		} else {
			internalError(w, err)
		}
		return
	}
	name, err = s.resolveConflict(r.Context(), tx, a, input.SpaceID, parentID, input.Section, name, input.Conflict)
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
		_, err = tx.Exec(r.Context(), `INSERT INTO nodes(id,space_id,parent_id,asset_id,kind,name,section,created_by) VALUES($1,$2,$3,$4,'file',$5,$6,$7)`, nodeID, input.SpaceID, parentID, assetID, name, input.Section, a.UserID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO upload_sessions(id,batch_id,asset_id,node_id,user_id,method,expected_size,state,expires_at,staging_key) VALUES($1,$2,$3,$4,$5,$6,$7,'uploading',$8,$9)`, sessionID, input.BatchID, assetID, nodeID, a.UserID, method, input.SizeBytes, expires, stagingKey)
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO object_cleanup(object_key,delete_after) VALUES($1,$3),($2,$3)`, stagingKey, objectKey, expires.Add(s.cfg.PresignTTL+time.Hour)); err != nil {
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	response := map[string]any{"id": sessionID, "nodeId": nodeID, "method": method, "name": name, "expiresAt": expires}
	if method == "put" {
		url, err := s.store.PresignPut(r.Context(), stagingKey, input.MimeType, input.SizeBytes)
		if err != nil {
			s.failUpload(r.Context(), sessionID, assetID, input.SpaceID, input.SizeBytes)
			internalError(w, err)
			return
		}
		response["url"] = url
	} else {
		uploadID, err := s.store.CreateMultipart(r.Context(), stagingKey, input.MimeType)
		if err != nil {
			s.failUpload(r.Context(), sessionID, assetID, input.SpaceID, input.SizeBytes)
			internalError(w, err)
			return
		}
		if s.onRollback != nil {
			*s.onRollback = append(*s.onRollback, func(ctx context.Context) { _ = s.store.AbortMultipart(ctx, stagingKey, uploadID) })
		}
		if _, err := s.db.Exec(r.Context(), `UPDATE upload_sessions SET upload_id=$1 WHERE id=$2`, uploadID, sessionID); err != nil {
			_ = s.store.AbortMultipart(r.Context(), stagingKey, uploadID)
			s.failUpload(r.Context(), sessionID, assetID, input.SpaceID, input.SizeBytes)
			internalError(w, err)
			return
		}
		if _, err := s.db.Exec(r.Context(), `UPDATE object_cleanup SET multipart_id=$2 WHERE object_key=$1`, stagingKey, uploadID); err != nil {
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

func (s *Server) resolveConflict(ctx context.Context, tx pgx.Tx, a actor, spaceID uuid.UUID, parentID *uuid.UUID, section, name, policy string) (string, error) {
	var existingID uuid.UUID
	var existingKind string
	err := tx.QueryRow(ctx, `SELECT id,kind FROM nodes WHERE space_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND section=$3 AND lower(name)=lower($4) AND deleted_at IS NULL`, spaceID, parentID, section, name).Scan(&existingID, &existingKind)
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
		level, err := nodePermissionWith(ctx, tx, a, existingID)
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
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE space_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND section=$3 AND lower(name)=lower($4) AND deleted_at IS NULL)`, spaceID, parentID, section, candidate).Scan(&exists); err != nil {
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
	tx, err := s.db.Begin(ctx)
	if err != nil {
		slog.Error("mark upload failed", "upload_id", sessionID, "error", err)
		return
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE upload_sessions SET state='failed' WHERE id=$1 AND state IN ('pending','uploading','completing')`, sessionID)
	if err == nil && result.RowsAffected() > 0 {
		_, err = tx.Exec(ctx, `UPDATE assets SET status='failed' WHERE id=$1`, assetID)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE spaces SET reserved_bytes=GREATEST(0,reserved_bytes-$1) WHERE id=$2`, reserved, spaceID)
		}
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		slog.Error("mark upload failed", "upload_id", sessionID, "error", err)
	}
}

func (s *Server) getUpload(ctx context.Context, id, userID uuid.UUID) (uploadRecord, error) {
	var item uploadRecord
	err := s.db.QueryRow(ctx, `
		SELECT u.id,u.asset_id,u.node_id,u.user_id,a.space_id,a.object_key,COALESCE(u.staging_key,a.object_key),a.mime_type,u.upload_id,u.method,u.expected_size,u.state,n.section,u.expires_at
		FROM upload_sessions u JOIN assets a ON a.id=u.asset_id JOIN nodes n ON n.id=u.node_id
		WHERE u.id=$1 AND u.user_id=$2`, id, userID).Scan(&item.ID, &item.AssetID, &item.NodeID, &item.UserID, &item.SpaceID, &item.ObjectKey, &item.StagingKey, &item.MimeType, &item.UploadID, &item.Method, &item.ExpectedSize, &item.State, &item.Section, &item.ExpiresAt)
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
	if upload.State == "ready" {
		writeError(w, http.StatusConflict, "upload_finalized", "已完成的上传不能继续签发分片")
		return
	}
	if upload.State != "uploading" {
		writeError(w, http.StatusGone, "upload_expired", "上传会话已失效，请重新选择文件")
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
	seen := make(map[int32]struct{}, len(input.PartNumbers))
	for _, number := range input.PartNumbers {
		if number < 1 || number > 10000 || int64(number-1)*choosePartSize(upload.ExpectedSize) >= upload.ExpectedSize {
			writeError(w, http.StatusBadRequest, "invalid_parts", "分片编号不合法")
			return
		}
		if _, exists := seen[number]; exists {
			writeError(w, http.StatusBadRequest, "invalid_parts", "分片编号不能重复")
			return
		}
		seen[number] = struct{}{}
		url, err := s.store.PresignPart(r.Context(), upload.StagingKey, *upload.UploadID, number, min(choosePartSize(upload.ExpectedSize), upload.ExpectedSize-int64(number-1)*choosePartSize(upload.ExpectedSize)))
		if err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"partNumber": number, "url": url})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) resumeUpload(w http.ResponseWriter, r *http.Request) {
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
	if upload.State == "completing" {
		writeJSON(w, http.StatusAccepted, map[string]any{"id": upload.ID, "nodeId": upload.NodeID, "state": "completing"})
		return
	}
	if upload.State == "ready" {
		writeJSON(w, http.StatusOK, map[string]any{"id": upload.ID, "nodeId": upload.NodeID, "state": "ready"})
		return
	}
	if upload.State != "uploading" || upload.ExpiresAt.Before(time.Now()) {
		writeError(w, http.StatusGone, "upload_expired", "上传会话已失效，请重新选择文件")
		return
	}
	response := map[string]any{"id": upload.ID, "nodeId": upload.NodeID, "state": "uploading", "method": upload.Method, "expiresAt": upload.ExpiresAt}
	if upload.Method == "put" {
		var mime string
		if err := s.db.QueryRow(r.Context(), `SELECT mime_type FROM assets WHERE id=$1 AND status='pending'`, upload.AssetID).Scan(&mime); err != nil {
			writeError(w, http.StatusGone, "upload_expired", "上传会话已失效，请重新选择文件")
			return
		}
		url, err := s.store.PresignPut(r.Context(), upload.StagingKey, mime, upload.ExpectedSize)
		if err != nil {
			internalError(w, err)
			return
		}
		response["url"] = url
	} else {
		if upload.UploadID == nil {
			writeError(w, http.StatusGone, "upload_expired", "上传会话已失效，请重新选择文件")
			return
		}
		response["partSize"] = choosePartSize(upload.ExpectedSize)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		Parts  []storage.CompletedPart `json:"parts"`
		SHA256 string                  `json:"sha256"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.SHA256 != "" {
		decoded, err := hex.DecodeString(input.SHA256)
		if err != nil || len(decoded) != 32 {
			writeError(w, http.StatusBadRequest, "invalid_checksum", "文件校验值不合法")
			return
		}
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var nodeID uuid.UUID
	var method, state string
	var expires time.Time
	if err := tx.QueryRow(r.Context(), `SELECT node_id,method,state,expires_at FROM upload_sessions WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, a.UserID).Scan(&nodeID, &method, &state, &expires); err != nil {
		if !dbNotFound(w, err) {
			internalError(w, err)
		}
		return
	}
	if state == "ready" || state == "completing" {
		status := http.StatusOK
		if state == "completing" {
			status = http.StatusAccepted
		}
		writeJSON(w, status, map[string]any{"id": id, "nodeId": nodeID, "state": state})
		return
	}
	if state != "uploading" || !expires.After(time.Now()) {
		writeError(w, http.StatusGone, "upload_expired", "上传会话已失效，请重新选择文件")
		return
	}
	if err := ValidateUploadPermission(r.Context(), tx, a.UserID, nodeID); err != nil {
		writeError(w, http.StatusForbidden, "upload_forbidden", "上传目录的编辑权限已失效")
		return
	}
	if method == "multipart" && !validCompletedParts(input.Parts) {
		writeError(w, http.StatusBadRequest, "invalid_parts", "缺少合法的已上传分片")
		return
	}
	parts, err := json.Marshal(input.Parts)
	if err != nil {
		internalError(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `UPDATE upload_sessions SET state='completing',completion_parts=$2,expected_sha256=NULLIF($3,'') WHERE id=$1`, id, parts, strings.ToLower(input.SHA256)); err != nil {
		internalError(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `INSERT INTO jobs(id,kind,payload) VALUES($1,'finalize_upload',jsonb_build_object('uploadId',$2::text))`, uuid.New(), id); err != nil {
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "nodeId": nodeID, "state": "completing"})
}

// Shared with the finalizer so queueing an upload cannot preserve permissions
// that have since been revoked or publish a node already in the trash.
var ErrUploadPermission = errors.New("upload permission revoked")

func ValidateUploadPermission(ctx context.Context, q permissionQuerier, userID, nodeID uuid.UUID) error {
	var a actor
	if err := q.QueryRow(ctx, `SELECT u.id,hm.household_id,hm.role FROM users u JOIN household_members hm ON hm.user_id=u.id JOIN nodes n ON n.id=$2 JOIN spaces sp ON sp.id=n.space_id AND sp.household_id=hm.household_id WHERE u.id=$1 AND NOT u.disabled AND n.deleted_at IS NULL AND n.purge_job_id IS NULL`, userID, nodeID).Scan(&a.UserID, &a.HouseholdID, &a.Role); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUploadPermission
		}
		return err
	}
	level, err := nodePermissionWith(ctx, q, a, nodeID)
	if err != nil {
		return err
	}
	if level < permissionEditor {
		return ErrUploadPermission
	}
	return nil
}

func validCompletedParts(parts []storage.CompletedPart) bool {
	if len(parts) == 0 || len(parts) > 10000 {
		return false
	}
	seen := make(map[int32]struct{}, len(parts))
	for _, part := range parts {
		if part.Number < 1 || part.Number > 10000 || len(part.ETag) == 0 || len(part.ETag) > 256 || strings.ContainsAny(part.ETag, "\r\n") {
			return false
		}
		if _, exists := seen[part.Number]; exists {
			return false
		}
		seen[part.Number] = struct{}{}
	}
	return true
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

func shouldIndexPhoto(section, mime string) bool {
	return section == "photos" && isPhotoMime(mime)
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
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var lockedState string
	if err := tx.QueryRow(r.Context(), `SELECT state FROM upload_sessions WHERE id=$1 AND user_id=$2 FOR UPDATE`, upload.ID, a.UserID).Scan(&lockedState); err != nil {
		if !dbNotFound(w, err) {
			internalError(w, err)
		}
		return
	}
	if lockedState == "ready" {
		writeError(w, http.StatusConflict, "upload_finalized", "已完成的上传不能取消，请从文件列表删除")
		return
	}
	if lockedState == "pending" || lockedState == "uploading" || lockedState == "completing" || lockedState == "expired" {
		_, err = tx.Exec(r.Context(), `UPDATE spaces SET reserved_bytes=GREATEST(0,reserved_bytes-$1) WHERE id=$2`, upload.ExpectedSize, upload.SpaceID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM nodes WHERE id=$1`, upload.NodeID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM assets WHERE id=$1`, upload.AssetID)
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	if upload.UploadID != nil {
		if err := s.store.AbortMultipart(r.Context(), upload.StagingKey, *upload.UploadID); err != nil {
			slog.Warn("multipart cleanup failed", "upload_id", upload.ID, "error", err)
		}
	}
	if err := s.store.Delete(r.Context(), upload.StagingKey); err != nil {
		slog.Warn("object cleanup failed", "upload_id", upload.ID, "error", err)
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
