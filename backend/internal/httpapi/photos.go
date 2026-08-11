package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

func (s *Server) listPhotos(w http.ResponseWriter, r *http.Request) {
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
	rows, err := s.db.Query(r.Context(), `
		SELECT n.id,n.name,a.id,a.object_key,a.mime_type,a.size_bytes,p.taken_at,p.width,p.height,p.camera,p.remark,p.thumb_small_key,p.thumb_large_key,n.created_at
		FROM nodes n JOIN assets a ON a.id=n.asset_id LEFT JOIN photo_details p ON p.asset_id=a.id
		WHERE n.space_id=$1 AND n.deleted_at IS NULL AND a.status='ready' AND lower(split_part(a.mime_type,';',1)) IN ('image/jpeg','image/png','image/webp','image/gif','image/heic','image/heif')
		ORDER BY COALESCE(p.taken_at,n.created_at) DESC,n.id DESC LIMIT 500`, spaceID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var nodeID, assetID uuid.UUID
		var name, objectKey, mime, remark string
		var size int64
		var takenAt *time.Time
		var width, height *int
		var camera, smallKey, largeKey *string
		var createdAt time.Time
		if err := rows.Scan(&nodeID, &name, &assetID, &objectKey, &mime, &size, &takenAt, &width, &height, &camera, &remark, &smallKey, &largeKey, &createdAt); err != nil {
			internalError(w, err)
			return
		}
		permission, err := s.nodePermission(r.Context(), a, nodeID)
		if err != nil || permission < permissionViewer {
			continue
		}
		thumbKey := objectKey
		if smallKey != nil && *smallKey != "" {
			thumbKey = *smallKey
		}
		previewKey := objectKey
		if largeKey != nil && *largeKey != "" {
			previewKey = *largeKey
		}
		thumbURL, _ := s.store.PresignGet(r.Context(), thumbKey, "")
		previewURL, _ := s.store.PresignGet(r.Context(), previewKey, "")
		items = append(items, map[string]any{
			"nodeId": nodeID, "assetId": assetID, "name": name, "mimeType": mime, "sizeBytes": size,
			"takenAt": takenAt, "createdAt": createdAt, "width": width, "height": height, "camera": camera,
			"remark": remark, "thumbUrl": thumbURL, "previewUrl": previewURL, "permission": permissionName(permission),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) updatePhoto(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	nodeID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.nodePermission(r.Context(), a, nodeID)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "照片不存在")
		return
	}
	var input struct {
		Remark string `json:"remark"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Remark = strings.TrimSpace(input.Remark)
	if len([]rune(input.Remark)) > 2000 {
		writeError(w, http.StatusBadRequest, "remark_too_long", "备注最多 2000 个字符")
		return
	}
	result, err := s.db.Exec(r.Context(), `UPDATE photo_details p SET remark=$1 FROM nodes n WHERE n.asset_id=p.asset_id AND n.id=$2`, input.Remark, nodeID)
	if err != nil {
		internalError(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "照片尚未完成索引")
		return
	}
	s.audit(r.Context(), a, "photo.remark_update", "node", &nodeID, map[string]any{"hasRemark": input.Remark != ""})
	writeJSON(w, http.StatusOK, map[string]any{"nodeId": nodeID, "remark": input.Remark})
}

func (s *Server) listAlbums(w http.ResponseWriter, r *http.Request) {
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
	rows, err := s.db.Query(r.Context(), `
		SELECT a.id,a.name,a.description,a.created_at,count(i.node_id),
		       (SELECT COALESCE(NULLIF(p.thumb_small_key,''),asset.object_key)
		        FROM album_items cover_item
		        JOIN nodes n ON n.id=cover_item.node_id
		        JOIN assets asset ON asset.id=n.asset_id
		        LEFT JOIN photo_details p ON p.asset_id=asset.id
		        WHERE cover_item.album_id=a.id AND n.deleted_at IS NULL AND asset.status='ready'
		        ORDER BY COALESCE(p.taken_at,n.created_at) DESC,cover_item.created_at DESC LIMIT 1)
		FROM albums a LEFT JOIN album_items i ON i.album_id=a.id
		WHERE a.space_id=$1 GROUP BY a.id ORDER BY a.updated_at DESC`, spaceID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id uuid.UUID
		var name, description string
		var created time.Time
		var count int64
		var coverKey *string
		if err := rows.Scan(&id, &name, &description, &created, &count, &coverKey); err != nil {
			internalError(w, err)
			return
		}
		permission, _ := s.albumPermission(r.Context(), a, id)
		if permission >= permissionViewer {
			item := map[string]any{"id": id, "name": name, "description": description, "itemCount": count, "permission": permissionName(permission), "createdAt": created}
			if coverKey != nil {
				item["coverUrl"], _ = s.store.PresignGet(r.Context(), *coverKey, "")
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createAlbum(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var input struct {
		SpaceID     uuid.UUID `json:"spaceId"`
		Name        string    `json:"name"`
		Description string    `json:"description"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	name, err := cleanName(input.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_name", "相册名称不合法")
		return
	}
	level, err := s.spacePermission(r.Context(), a, input.SpaceID)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "空间不存在")
		return
	}
	id := uuid.New()
	if _, err := s.db.Exec(r.Context(), `INSERT INTO albums(id,space_id,name,description,created_by) VALUES($1,$2,$3,$4,$5)`, id, input.SpaceID, name, strings.TrimSpace(input.Description), a.UserID); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "album.create", "album", &id, map[string]any{"name": name})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "spaceId": input.SpaceID, "name": name, "description": strings.TrimSpace(input.Description)})
}

func (s *Server) updateAlbum(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	albumID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.albumPermission(r.Context(), a, albumID)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "相册不存在")
		return
	}
	var input struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var name, description string
	if err := s.db.QueryRow(r.Context(), `SELECT name,description FROM albums WHERE id=$1`, albumID).Scan(&name, &description); err != nil {
		dbNotFound(w, err)
		return
	}
	if input.Name != nil {
		name, err = cleanName(*input.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_name", "相册名称不合法")
			return
		}
	}
	if input.Description != nil {
		description = strings.TrimSpace(*input.Description)
		if len([]rune(description)) > 2000 {
			writeError(w, http.StatusBadRequest, "description_too_long", "相册描述最多 2000 个字符")
			return
		}
	}
	if _, err := s.db.Exec(r.Context(), `UPDATE albums SET name=$1,description=$2,updated_at=now() WHERE id=$3`, name, description, albumID); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "album.update", "album", &albumID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": albumID, "name": name, "description": description})
}

func (s *Server) deleteAlbum(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	albumID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.albumPermission(r.Context(), a, albumID)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "相册不存在")
		return
	}
	var creator uuid.UUID
	if err := s.db.QueryRow(r.Context(), `SELECT created_by FROM albums WHERE id=$1`, albumID).Scan(&creator); err != nil {
		dbNotFound(w, err)
		return
	}
	if level < permissionManager && creator != a.UserID {
		writeError(w, http.StatusNotFound, "not_found", "相册不存在")
		return
	}
	if _, err := s.db.Exec(r.Context(), `DELETE FROM albums WHERE id=$1`, albumID); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "album.delete", "album", &albumID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addAlbumItems(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	albumID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.albumPermission(r.Context(), a, albumID)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "相册不存在")
		return
	}
	var input struct {
		NodeIDs []uuid.UUID `json:"nodeIds"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.NodeIDs) == 0 || len(input.NodeIDs) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_items", "一次可添加 1 至 500 张照片")
		return
	}
	var albumSpace uuid.UUID
	_ = s.db.QueryRow(r.Context(), `SELECT space_id FROM albums WHERE id=$1`, albumID).Scan(&albumSpace)
	added := 0
	for _, nodeID := range input.NodeIDs {
		permission, err := s.nodePermission(r.Context(), a, nodeID)
		if err != nil || permission < permissionViewer {
			continue
		}
		result, err := s.db.Exec(r.Context(), `INSERT INTO album_items(album_id,node_id,created_by) SELECT $1,n.id,$2 FROM nodes n JOIN assets a ON a.id=n.asset_id WHERE n.id=$3 AND n.space_id=$4 AND n.deleted_at IS NULL AND a.status='ready' AND a.mime_type LIKE 'image/%' ON CONFLICT DO NOTHING`, albumID, a.UserID, nodeID, albumSpace)
		if err == nil {
			added += int(result.RowsAffected())
		}
	}
	s.audit(r.Context(), a, "album.items_add", "album", &albumID, map[string]any{"count": added})
	writeJSON(w, http.StatusOK, map[string]any{"added": added})
}

func (s *Server) removeAlbumItem(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	albumID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	nodeID, ok := parseUUIDParam(w, r, "nodeID")
	if !ok {
		return
	}
	level, err := s.albumPermission(r.Context(), a, albumID)
	if err != nil || level < permissionEditor {
		writeError(w, http.StatusNotFound, "not_found", "相册不存在")
		return
	}
	result, err := s.db.Exec(r.Context(), `DELETE FROM album_items WHERE album_id=$1 AND node_id=$2`, albumID, nodeID)
	if err != nil {
		internalError(w, err)
		return
	}
	if result.RowsAffected() > 0 {
		s.audit(r.Context(), a, "album.item_remove", "album", &albumID, map[string]any{"nodeId": nodeID})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listAlbumItems(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	albumID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.albumPermission(r.Context(), a, albumID)
	if err != nil || level < permissionViewer {
		writeError(w, http.StatusNotFound, "not_found", "相册不存在")
		return
	}
	rows, err := s.db.Query(r.Context(), `
		SELECT n.id,n.name,a.object_key,p.thumb_small_key,p.thumb_large_key,p.remark,p.taken_at,n.created_at
		FROM album_items i JOIN nodes n ON n.id=i.node_id JOIN assets a ON a.id=n.asset_id
		LEFT JOIN photo_details p ON p.asset_id=a.id
		WHERE i.album_id=$1 AND n.deleted_at IS NULL AND a.status='ready'
		ORDER BY COALESCE(p.taken_at,n.created_at) DESC,i.created_at DESC`, albumID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var nodeID uuid.UUID
		var name, key, remark string
		var thumb, preview *string
		var takenAt *time.Time
		var createdAt time.Time
		if err := rows.Scan(&nodeID, &name, &key, &thumb, &preview, &remark, &takenAt, &createdAt); err != nil {
			internalError(w, err)
			return
		}
		thumbKey, previewKey := key, key
		if thumb != nil && *thumb != "" {
			thumbKey = *thumb
		}
		if preview != nil && *preview != "" {
			previewKey = *preview
		}
		thumbURL, _ := s.store.PresignGet(r.Context(), thumbKey, "")
		previewURL, _ := s.store.PresignGet(r.Context(), previewKey, "")
		effectiveTakenAt := createdAt
		if takenAt != nil {
			effectiveTakenAt = *takenAt
		}
		items = append(items, map[string]any{"nodeId": nodeID, "name": name, "thumbUrl": thumbURL, "previewUrl": previewURL, "remark": remark, "takenAt": effectiveTakenAt, "createdAt": createdAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
