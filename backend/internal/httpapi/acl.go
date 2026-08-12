package httpapi

import (
	"net/http"

	"github.com/google/uuid"
)

func (s *Server) getNodePermissions(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	nodeID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.nodePermission(r.Context(), a, nodeID)
	if err != nil || level < permissionManager {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	var inherit bool
	if err := s.db.QueryRow(r.Context(), `SELECT inherit_permissions FROM nodes WHERE id=$1 AND deleted_at IS NULL`, nodeID).Scan(&inherit); err != nil {
		dbNotFound(w, err)
		return
	}
	s.writePermissions(w, r, "node", nodeID, inherit)
}

func (s *Server) getAlbumPermissions(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	albumID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.albumPermission(r.Context(), a, albumID)
	if err != nil || level < permissionManager {
		writeError(w, http.StatusNotFound, "not_found", "相册不存在")
		return
	}
	var inherit bool
	if err := s.db.QueryRow(r.Context(), `SELECT inherit_permissions FROM albums WHERE id=$1`, albumID).Scan(&inherit); err != nil {
		dbNotFound(w, err)
		return
	}
	s.writePermissions(w, r, "album", albumID, inherit)
}

func (s *Server) writePermissions(w http.ResponseWriter, r *http.Request, resourceType string, resourceID uuid.UUID, inherit bool) {
	rows, err := s.db.Query(r.Context(), `
		SELECT a.principal_user_id,u.username,u.display_name,a.permission
		FROM acl_entries a JOIN users u ON u.id=a.principal_user_id
		WHERE a.resource_type=$1 AND a.resource_id=$2 ORDER BY lower(u.display_name)`, resourceType, resourceID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	entries := make([]map[string]any, 0)
	for rows.Next() {
		var userID uuid.UUID
		var username, displayName, permission string
		if err := rows.Scan(&userID, &username, &displayName, &permission); err != nil {
			internalError(w, err)
			return
		}
		entries = append(entries, map[string]any{"userId": userID, "username": username, "displayName": displayName, "permission": permission})
	}
	writeJSON(w, http.StatusOK, map[string]any{"inherit": inherit, "entries": entries})
}

func (s *Server) setNodePermissions(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	nodeID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.nodePermission(r.Context(), a, nodeID)
	if err != nil || level < permissionManager {
		writeError(w, http.StatusNotFound, "not_found", "资源不存在")
		return
	}
	var input struct {
		Inherit bool `json:"inherit"`
		Entries []struct {
			UserID     uuid.UUID `json:"userId"`
			Permission string    `json:"permission"`
		} `json:"entries"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Entries) > 100 {
		writeError(w, http.StatusBadRequest, "too_many_entries", "权限成员过多")
		return
	}
	seen := make(map[uuid.UUID]struct{}, len(input.Entries))
	for _, entry := range input.Entries {
		if _, exists := seen[entry.UserID]; exists {
			writeError(w, http.StatusBadRequest, "duplicate_member", "同一成员不能重复设置权限")
			return
		}
		seen[entry.UserID] = struct{}{}
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `UPDATE nodes SET inherit_permissions=$1 WHERE id=$2`, input.Inherit, nodeID); err != nil {
		internalError(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM acl_entries WHERE resource_type='node' AND resource_id=$1`, nodeID); err != nil {
		internalError(w, err)
		return
	}
	for _, entry := range input.Entries {
		if permissionLevel(entry.Permission) == permissionNone {
			writeError(w, http.StatusBadRequest, "invalid_permission", "权限值不合法")
			return
		}
		var member bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM household_members WHERE household_id=$1 AND user_id=$2)`, a.HouseholdID, entry.UserID).Scan(&member); err != nil || !member {
			writeError(w, http.StatusBadRequest, "invalid_member", "权限成员不属于当前家庭")
			return
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO acl_entries(id,resource_type,resource_id,principal_user_id,permission,created_by) VALUES($1,'node',$2,$3,$4,$5)`, uuid.New(), nodeID, entry.UserID, entry.Permission, a.UserID); err != nil {
			internalError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "node.permissions_update", "node", &nodeID, map[string]any{"inherit": input.Inherit, "entryCount": len(input.Entries)})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) setAlbumPermissions(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	albumID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	level, err := s.albumPermission(r.Context(), a, albumID)
	if err != nil || level < permissionManager {
		writeError(w, http.StatusNotFound, "not_found", "相册不存在")
		return
	}
	var input struct {
		Inherit bool `json:"inherit"`
		Entries []struct {
			UserID     uuid.UUID `json:"userId"`
			Permission string    `json:"permission"`
		} `json:"entries"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Entries) > 100 {
		writeError(w, http.StatusBadRequest, "too_many_entries", "权限成员过多")
		return
	}
	seen := make(map[uuid.UUID]struct{}, len(input.Entries))
	for _, entry := range input.Entries {
		if _, exists := seen[entry.UserID]; exists {
			writeError(w, http.StatusBadRequest, "duplicate_member", "同一成员不能重复设置权限")
			return
		}
		seen[entry.UserID] = struct{}{}
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `UPDATE albums SET inherit_permissions=$1,updated_at=now() WHERE id=$2`, input.Inherit, albumID); err != nil {
		internalError(w, err)
		return
	}
	if _, err := tx.Exec(r.Context(), `DELETE FROM acl_entries WHERE resource_type='album' AND resource_id=$1`, albumID); err != nil {
		internalError(w, err)
		return
	}
	for _, entry := range input.Entries {
		if permissionLevel(entry.Permission) == permissionNone {
			writeError(w, http.StatusBadRequest, "invalid_permission", "权限值不合法")
			return
		}
		var member bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM household_members WHERE household_id=$1 AND user_id=$2)`, a.HouseholdID, entry.UserID).Scan(&member); err != nil || !member {
			writeError(w, http.StatusBadRequest, "invalid_member", "权限成员不属于当前家庭")
			return
		}
		if _, err := tx.Exec(r.Context(), `INSERT INTO acl_entries(id,resource_type,resource_id,principal_user_id,permission,created_by) VALUES($1,'album',$2,$3,$4,$5)`, uuid.New(), albumID, entry.UserID, entry.Permission, a.UserID); err != nil {
			internalError(w, err)
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "album.permissions_update", "album", &albumID, map[string]any{"inherit": input.Inherit, "entryCount": len(input.Entries)})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
