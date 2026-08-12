package httpapi

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var invalidNameChars = regexp.MustCompile(`[\x00-\x1f\\/:*?"<>|]`)

type nodeRecord struct {
	ID        uuid.UUID
	SpaceID   uuid.UUID
	ParentID  *uuid.UUID
	AssetID   *uuid.UUID
	Kind      string
	Name      string
	Size      int64
	Mime      string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func cleanName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." || invalidNameChars.MatchString(value) || utf8.RuneCountInString(value) > 255 {
		return "", fmt.Errorf("invalid name")
	}
	return value, nil
}

func (s *Server) listSpaces(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	rows, err := s.db.Query(r.Context(), `
		SELECT id,kind,name,quota_bytes,used_bytes,reserved_bytes,owner_user_id
		FROM spaces WHERE household_id=$1 AND (kind='family' OR owner_user_id=$2)
		ORDER BY CASE kind WHEN 'personal' THEN 0 ELSE 1 END`, a.HouseholdID, a.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id uuid.UUID
		var kind, name string
		var quota, used, reserved int64
		var owner *uuid.UUID
		if err := rows.Scan(&id, &kind, &name, &quota, &used, &reserved, &owner); err != nil {
			internalError(w, err)
			return
		}
		permission, _ := s.spacePermission(r.Context(), a, id)
		items = append(items, map[string]any{"id": id, "kind": kind, "name": name, "quotaBytes": quota, "usedBytes": used, "reservedBytes": reserved, "permission": permissionName(permission)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	spaceID, err := uuid.Parse(r.URL.Query().Get("spaceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_space", "请选择空间")
		return
	}
	var parentID *uuid.UUID
	if raw := r.URL.Query().Get("parentId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_parent", "目录不存在")
			return
		}
		parentID = &id
		level, err := s.nodePermission(r.Context(), a, id)
		if err != nil || level < permissionViewer {
			writeError(w, http.StatusNotFound, "not_found", "目录不存在")
			return
		}
	} else {
		level, err := s.spacePermission(r.Context(), a, spaceID)
		if err != nil || level < permissionViewer {
			writeError(w, http.StatusNotFound, "not_found", "空间不存在")
			return
		}
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT n.id,n.space_id,n.parent_id,n.asset_id,n.kind,n.name,COALESCE(a.size_bytes,0),COALESCE(a.mime_type,''),COALESCE(a.status,'ready'),n.created_at,n.updated_at
		FROM nodes n LEFT JOIN assets a ON a.id=n.asset_id
		WHERE n.space_id=$1 AND n.parent_id IS NOT DISTINCT FROM $2 AND n.deleted_at IS NULL
		AND (n.kind='folder' OR a.status IN ('pending','ready'))
		ORDER BY CASE n.kind WHEN 'folder' THEN 0 ELSE 1 END, lower(n.name)`, spaceID, parentID)
	if err != nil {
		internalError(w, err)
		return
	}
	records := make([]nodeRecord, 0)
	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var n nodeRecord
		if err := rows.Scan(&n.ID, &n.SpaceID, &n.ParentID, &n.AssetID, &n.Kind, &n.Name, &n.Size, &n.Mime, &n.Status, &n.CreatedAt, &n.UpdatedAt); err != nil {
			rows.Close()
			internalError(w, err)
			return
		}
		records = append(records, n)
		ids = append(ids, n.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		internalError(w, err)
		return
	}
	rows.Close()
	permissions, err := s.nodePermissions(r.Context(), a, spaceID, ids)
	if err != nil {
		internalError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(records))
	for _, n := range records {
		if level := permissions[n.ID]; level != permissionNone {
			items = append(items, nodeJSON(n, level))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listFolderTree(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	spaceID, err := uuid.Parse(r.URL.Query().Get("spaceId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_space", "请选择空间")
		return
	}
	level, err := s.spacePermission(r.Context(), a, spaceID)
	if err != nil || level < permissionViewer {
		writeError(w, http.StatusNotFound, "not_found", "空间不存在")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT id,parent_id,name,updated_at FROM nodes WHERE space_id=$1 AND kind='folder' AND deleted_at IS NULL ORDER BY lower(name)`, spaceID)
	if err != nil {
		internalError(w, err)
		return
	}
	type folderRecord struct {
		id        uuid.UUID
		parentID  *uuid.UUID
		name      string
		updatedAt time.Time
	}
	records := make([]folderRecord, 0)
	ids := make([]uuid.UUID, 0)
	for rows.Next() {
		var item folderRecord
		if err := rows.Scan(&item.id, &item.parentID, &item.name, &item.updatedAt); err != nil {
			rows.Close()
			internalError(w, err)
			return
		}
		records = append(records, item)
		ids = append(ids, item.id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		internalError(w, err)
		return
	}
	rows.Close()
	permissions, err := s.nodePermissions(r.Context(), a, spaceID, ids)
	if err != nil {
		internalError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(records))
	for _, item := range records {
		permission := permissions[item.id]
		if permission >= permissionEditor {
			items = append(items, map[string]any{"id": item.id, "parentId": item.parentID, "name": item.name, "permission": permissionName(permission), "updatedAt": item.updatedAt})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func nodeJSON(n nodeRecord, permission int) map[string]any {
	return map[string]any{"id": n.ID, "spaceId": n.SpaceID, "parentId": n.ParentID, "kind": n.Kind, "name": n.Name, "sizeBytes": n.Size, "mimeType": n.Mime, "status": n.Status, "permission": permissionName(permission), "createdAt": n.CreatedAt, "updatedAt": n.UpdatedAt}
}

func (s *Server) createFolder(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var input struct {
		SpaceID  uuid.UUID  `json:"spaceId"`
		ParentID *uuid.UUID `json:"parentId"`
		Name     string     `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	name, err := cleanName(input.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_name", "文件夹名称不合法")
		return
	}
	level, err := s.parentPermission(r.Context(), a, input.SpaceID, input.ParentID)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "目标目录不存在")
		return
	}
	id := uuid.New()
	_, err = s.db.Exec(r.Context(), `INSERT INTO nodes(id,space_id,parent_id,kind,name,created_by) VALUES($1,$2,$3,'folder',$4,$5)`, id, input.SpaceID, input.ParentID, name, a.UserID)
	if err != nil {
		if strings.Contains(err.Error(), "nodes_active_name_idx") {
			writeError(w, http.StatusConflict, "name_conflict", "此目录已存在同名内容")
			return
		}
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "node.folder_create", "node", &id, map[string]any{"name": name, "parentId": input.ParentID})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "spaceId": input.SpaceID, "parentId": input.ParentID, "kind": "folder", "name": name})
}

func (s *Server) parentPermission(ctx context.Context, a actor, spaceID uuid.UUID, parentID *uuid.UUID) (int, error) {
	if parentID == nil {
		return s.spacePermission(ctx, a, spaceID)
	}
	var actualSpace uuid.UUID
	var kind string
	if err := s.db.QueryRow(ctx, `SELECT space_id,kind FROM nodes WHERE id=$1 AND deleted_at IS NULL`, *parentID).Scan(&actualSpace, &kind); err != nil {
		return permissionNone, err
	}
	if actualSpace != spaceID || kind != "folder" {
		return permissionNone, pgx.ErrNoRows
	}
	return s.nodePermission(ctx, a, *parentID)
}

func (s *Server) renameNode(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.nodePermission(r.Context(), a, id)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	name, err := cleanName(input.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_name", "名称不合法")
		return
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE nodes SET name=$1,updated_at=now() WHERE id=$2`, name, id); err != nil {
		if strings.Contains(err.Error(), "nodes_active_name_idx") {
			writeError(w, http.StatusConflict, "name_conflict", "此目录已存在同名内容")
			return
		}
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "node.rename", "node", &id, map[string]any{"name": name})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name})
}

func (s *Server) moveNode(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.nodePermission(r.Context(), a, id)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	var input struct {
		ParentID *uuid.UUID `json:"parentId"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var spaceID uuid.UUID
	var currentParent *uuid.UUID
	var kind, name string
	if err := s.db.QueryRow(r.Context(), `SELECT space_id,parent_id,kind,name FROM nodes WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&spaceID, &currentParent, &kind, &name); err != nil {
		dbNotFound(w, err)
		return
	}
	if input.ParentID != nil && *input.ParentID == id {
		writeError(w, http.StatusBadRequest, "invalid_parent", "不能把文件夹移动到自身")
		return
	}
	targetLevel, err := s.parentPermission(r.Context(), a, spaceID, input.ParentID)
	if err != nil || targetLevel < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "目标目录不存在")
		return
	}
	if kind == "folder" && input.ParentID != nil {
		var descendant bool
		err = s.db.QueryRow(r.Context(), `
			WITH RECURSIVE tree AS (
			  SELECT id FROM nodes WHERE id=$1
			  UNION ALL SELECT n.id FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE n.deleted_at IS NULL
			)
			SELECT EXISTS(SELECT 1 FROM tree WHERE id=$2)`, id, *input.ParentID).Scan(&descendant)
		if err != nil {
			internalError(w, err)
			return
		}
		if descendant {
			writeError(w, http.StatusBadRequest, "invalid_parent", "不能移动到自己的子目录")
			return
		}
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE nodes SET parent_id=$1,updated_at=now() WHERE id=$2`, input.ParentID, id); err != nil {
		if strings.Contains(err.Error(), "nodes_active_name_idx") {
			writeError(w, http.StatusConflict, "name_conflict", "目标目录已存在同名内容")
			return
		}
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "node.move", "node", &id, map[string]any{"name": name, "fromParentId": currentParent, "toParentId": input.ParentID})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "parentId": input.ParentID})
}

func (s *Server) trashNode(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.nodePermission(r.Context(), a, id)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	allowed, err := s.subtreePermissionAtLeast(r.Context(), a, id, permissionEditor)
	if err != nil {
		internalError(w, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "subtree_forbidden", "目录中包含你无权删除的内容")
		return
	}
	_, err = s.db.Exec(r.Context(), `
		WITH RECURSIVE tree AS (
		  SELECT id FROM nodes WHERE id=$1 AND deleted_at IS NULL
		  UNION ALL SELECT n.id FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE n.deleted_at IS NULL
		)
		UPDATE nodes SET original_parent_id=parent_id,deleted_at=now(),updated_at=now() WHERE id IN (SELECT id FROM tree)`, id)
	if err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "node.trash", "node", &id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	rows, err := s.db.Query(r.Context(), `
		SELECT n.id,n.space_id,n.parent_id,n.asset_id,n.kind,n.name,COALESCE(a.size_bytes,0),COALESCE(a.mime_type,''),COALESCE(a.status,'ready'),n.created_at,n.updated_at,n.deleted_at
		FROM nodes n JOIN spaces s ON s.id=n.space_id LEFT JOIN assets a ON a.id=n.asset_id
		WHERE n.deleted_at IS NOT NULL AND s.household_id=$1 AND (s.kind='family' OR s.owner_user_id=$2)
		AND NOT EXISTS(SELECT 1 FROM nodes p WHERE p.id=n.parent_id AND p.deleted_at IS NOT NULL)
		ORDER BY n.deleted_at DESC`, a.HouseholdID, a.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var n nodeRecord
		var deletedAt time.Time
		if err := rows.Scan(&n.ID, &n.SpaceID, &n.ParentID, &n.AssetID, &n.Kind, &n.Name, &n.Size, &n.Mime, &n.Status, &n.CreatedAt, &n.UpdatedAt, &deletedAt); err != nil {
			internalError(w, err)
			return
		}
		permission, err := s.nodePermission(r.Context(), a, n.ID)
		if err != nil || permission < permissionViewer {
			continue
		}
		item := nodeJSON(n, permission)
		item["deletedAt"] = deletedAt
		item["purgeAt"] = deletedAt.Add(s.cfg.TrashRetention)
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) restoreNode(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var spaceID uuid.UUID
	var name string
	var originalParent *uuid.UUID
	if err := s.db.QueryRow(r.Context(), `SELECT space_id,name,original_parent_id FROM nodes WHERE id=$1 AND deleted_at IS NOT NULL`, id).Scan(&spaceID, &name, &originalParent); err != nil {
		dbNotFound(w, err)
		return
	}
	nodeLevel, err := s.nodePermission(r.Context(), a, id)
	if err != nil || nodeLevel < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	level, err := s.parentPermission(r.Context(), a, spaceID, originalParent)
	if err != nil || level < permissionEditor {
		originalParent = nil
		level, err = s.spacePermission(r.Context(), a, spaceID)
	}
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	name, err = s.availableName(r.Context(), spaceID, originalParent, name)
	if err != nil {
		internalError(w, err)
		return
	}
	_, err = s.db.Exec(r.Context(), `
		WITH RECURSIVE tree AS (
		  SELECT id FROM nodes WHERE id=$1
		  UNION ALL SELECT n.id FROM nodes n JOIN tree t ON n.parent_id=t.id
		)
		UPDATE nodes SET deleted_at=NULL,updated_at=now(),parent_id=CASE WHEN id=$1 THEN $2 ELSE parent_id END,name=CASE WHEN id=$1 THEN $3 ELSE name END WHERE id IN (SELECT id FROM tree)`, id, originalParent, name)
	if err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "node.restore", "node", &id, map[string]any{"name": name, "parentId": originalParent})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name, "parentId": originalParent})
}

func (s *Server) purgeNode(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var spaceID uuid.UUID
	if err := s.db.QueryRow(r.Context(), `SELECT space_id FROM nodes WHERE id=$1 AND deleted_at IS NOT NULL`, id).Scan(&spaceID); err != nil {
		dbNotFound(w, err)
		return
	}
	level, err := s.nodePermission(r.Context(), a, id)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	allowed, err := s.subtreePermissionAtLeast(r.Context(), a, id, permissionEditor)
	if err != nil {
		internalError(w, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "subtree_forbidden", "目录中包含你无权永久删除的内容")
		return
	}
	if _, err := s.db.Exec(r.Context(), `INSERT INTO jobs(id,kind,payload) VALUES($1,'purge_node',jsonb_build_object('nodeId',$2::text))`, uuid.New(), id.String()); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "node.purge_requested", "node", &id, nil)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) availableName(ctx context.Context, spaceID uuid.UUID, parentID *uuid.UUID, wanted string) (string, error) {
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE space_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND lower(name)=lower($3) AND deleted_at IS NULL)`, spaceID, parentID, wanted).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		return wanted, nil
	}
	ext := path.Ext(wanted)
	base := strings.TrimSuffix(wanted, ext)
	for i := 1; i < 10000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE space_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND lower(name)=lower($3) AND deleted_at IS NULL)`, spaceID, parentID, candidate).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("too many conflicts")
}

func (s *Server) downloadFile(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.nodePermission(r.Context(), a, id)
	if err != nil || level < permissionViewer {
		writeError(w, http.StatusNotFound, "not_found", "文件不存在")
		return
	}
	var name, key string
	if err := s.db.QueryRow(r.Context(), `SELECT n.name,a.object_key FROM nodes n JOIN assets a ON a.id=n.asset_id WHERE n.id=$1 AND n.kind='file' AND n.deleted_at IS NULL AND a.status='ready'`, id).Scan(&name, &key); err != nil {
		dbNotFound(w, err)
		return
	}
	url, err := s.store.PresignGet(r.Context(), key, name)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url, "expiresIn": int(s.cfg.PresignTTL.Seconds())})
}

func (s *Server) previewFile(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.nodePermission(r.Context(), a, id)
	if err != nil || level < permissionViewer {
		writeError(w, http.StatusNotFound, "not_found", "文件不存在")
		return
	}
	var name, key, mime string
	if err := s.db.QueryRow(r.Context(), `SELECT n.name,a.object_key,a.mime_type FROM nodes n JOIN assets a ON a.id=n.asset_id WHERE n.id=$1 AND n.kind='file' AND n.deleted_at IS NULL AND a.status='ready'`, id).Scan(&name, &key, &mime); err != nil {
		if !dbNotFound(w, err) {
			internalError(w, err)
		}
		return
	}
	if !isPreviewableMIME(mime) {
		writeError(w, http.StatusUnsupportedMediaType, "preview_unsupported", "此文件类型暂不支持在线预览")
		return
	}
	url, err := s.store.PresignGet(r.Context(), key, "")
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name, "mimeType": mime, "url": url, "expiresIn": int(s.cfg.PresignTTL.Seconds())})
}

func isPreviewableMIME(mime string) bool {
	base := strings.ToLower(strings.TrimSpace(strings.Split(mime, ";")[0]))
	return strings.HasPrefix(base, "image/") || strings.HasPrefix(base, "video/") || strings.HasPrefix(base, "audio/") || strings.HasPrefix(base, "text/") || base == "application/pdf"
}

type archiveEntry struct{ Name, Key string }

func (s *Server) downloadArchive(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.nodePermission(r.Context(), a, id)
	if err != nil || level < permissionViewer {
		writeError(w, http.StatusNotFound, "not_found", "目录不存在")
		return
	}
	var rootName, kind string
	if err := s.db.QueryRow(r.Context(), `SELECT name,kind FROM nodes WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&rootName, &kind); err != nil || kind != "folder" {
		writeError(w, http.StatusNotFound, "not_found", "目录不存在")
		return
	}
	rows, err := s.db.Query(r.Context(), `
		WITH RECURSIVE tree AS (
		  SELECT id,parent_id,name,kind,asset_id,name::text AS relative_path FROM nodes WHERE id=$1 AND deleted_at IS NULL
		  UNION ALL
		  SELECT n.id,n.parent_id,n.name,n.kind,n.asset_id,(t.relative_path || '/' || n.name) FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE n.deleted_at IS NULL
		)
		SELECT t.id,t.relative_path,a.object_key FROM tree t JOIN assets a ON a.id=t.asset_id WHERE t.kind='file' AND a.status='ready' ORDER BY t.relative_path`, id)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	entries := make([]archiveEntry, 0)
	for rows.Next() {
		var nodeID uuid.UUID
		var entry archiveEntry
		if err := rows.Scan(&nodeID, &entry.Name, &entry.Key); err != nil {
			internalError(w, err)
			return
		}
		allowed, err := s.nodePermission(r.Context(), a, nodeID)
		if err == nil && allowed >= permissionViewer {
			entries = append(entries, entry)
		}
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s.zip", pathEscape(rootName)))
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, entry := range entries {
		body, err := s.store.Get(r.Context(), entry.Key)
		if err != nil {
			return
		}
		part, err := zw.Create(strings.ReplaceAll(entry.Name, "\\", "/"))
		if err == nil {
			_, err = io.Copy(part, body)
		}
		body.Close()
		if err != nil {
			return
		}
	}
}

func pathEscape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, " ", "%20"), "\"", "")
}

func (s *Server) updateQuota(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	if a.Role != "owner" {
		writeError(w, http.StatusForbidden, "forbidden", "只有家庭所有者可以修改配额")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var input struct {
		QuotaBytes int64 `json:"quotaBytes"`
	}
	if !decodeJSON(w, r, &input) || input.QuotaBytes < 0 {
		if input.QuotaBytes < 0 {
			writeError(w, http.StatusBadRequest, "invalid_quota", "配额不能小于零")
		}
		return
	}
	result, err := s.db.Exec(r.Context(), `UPDATE spaces SET quota_bytes=$1 WHERE id=$2 AND household_id=$3 AND ($1=0 OR $1>=used_bytes+reserved_bytes)`, input.QuotaBytes, id, a.HouseholdID)
	if err != nil {
		internalError(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusConflict, "quota_too_small", "配额不能低于当前已用容量")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "quotaBytes": input.QuotaBytes})
}
