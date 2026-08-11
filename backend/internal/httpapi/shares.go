package httpapi

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	panAuth "pan/backend/internal/auth"
)

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	var input struct {
		ResourceType  string     `json:"resourceType"`
		ResourceID    uuid.UUID  `json:"resourceId"`
		Password      string     `json:"password"`
		ExpiresAt     *time.Time `json:"expiresAt"`
		AllowDownload *bool      `json:"allowDownload"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	dbResourceType := input.ResourceType
	var level int
	var err error
	if input.ResourceType == "album" {
		level, err = s.albumPermission(r.Context(), a, input.ResourceID)
	} else {
		level, err = s.nodePermission(r.Context(), a, input.ResourceID)
		if err == nil {
			var kind string
			err = s.db.QueryRow(r.Context(), `SELECT kind FROM nodes WHERE id=$1 AND deleted_at IS NULL`, input.ResourceID).Scan(&kind)
			if err == nil {
				dbResourceType = kind
			}
		}
	}
	if err != nil || level < permissionManager || (dbResourceType != "file" && dbResourceType != "folder" && dbResourceType != "album") {
		writeError(w, http.StatusNotFound, "not_found", "分享资源不存在")
		return
	}
	if input.ExpiresAt != nil && input.ExpiresAt.Before(time.Now()) {
		writeError(w, http.StatusBadRequest, "invalid_expiry", "有效期必须晚于当前时间")
		return
	}
	plain, tokenHash, err := panAuth.NewToken(32)
	if err != nil {
		internalError(w, err)
		return
	}
	var passwordHash *string
	if strings.TrimSpace(input.Password) != "" {
		hash, err := panAuth.HashPassword(input.Password)
		if err != nil {
			internalError(w, err)
			return
		}
		passwordHash = &hash
	}
	id := uuid.New()
	allowDownload := true
	if input.AllowDownload != nil {
		allowDownload = *input.AllowDownload
	}
	_, err = s.db.Exec(r.Context(), `INSERT INTO public_shares(id,resource_type,resource_id,token_hash,password_hash,allow_download,expires_at,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, dbResourceType, input.ResourceID, tokenHash, passwordHash, allowDownload, input.ExpiresAt, a.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	s.audit(r.Context(), a, "share.create", "share", &id, map[string]any{"resourceType": dbResourceType, "resourceId": input.ResourceID})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "url": s.cfg.PublicBaseURL + "/s/" + plain, "expiresAt": input.ExpiresAt, "hasPassword": passwordHash != nil, "allowDownload": allowDownload})
}

func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	rows, err := s.db.Query(r.Context(), `
		SELECT s.id,s.resource_type,s.resource_id,
		       COALESCE((SELECT n.name FROM nodes n WHERE n.id=s.resource_id),(SELECT a.name FROM albums a WHERE a.id=s.resource_id),'已删除内容'),
		       s.password_hash IS NOT NULL,s.allow_download,s.expires_at,s.revoked_at,s.created_at
		FROM public_shares s WHERE s.created_by=$1 ORDER BY s.created_at DESC`, a.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, resourceID uuid.UUID
		var resourceType, resourceName string
		var hasPassword, allowDownload bool
		var expiresAt, revokedAt *time.Time
		var createdAt time.Time
		if err := rows.Scan(&id, &resourceType, &resourceID, &resourceName, &hasPassword, &allowDownload, &expiresAt, &revokedAt, &createdAt); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "resourceType": resourceType, "resourceId": resourceID, "resourceName": resourceName, "hasPassword": hasPassword, "allowDownload": allowDownload, "expiresAt": expiresAt, "revokedAt": revokedAt, "createdAt": createdAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	result, err := s.db.Exec(r.Context(), `UPDATE public_shares SET revoked_at=now() WHERE id=$1 AND created_by=$2 AND revoked_at IS NULL`, id, a.UserID)
	if err != nil {
		internalError(w, err)
		return
	}
	if result.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "not_found", "分享不存在")
		return
	}
	s.audit(r.Context(), a, "share.revoke", "share", &id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) unlockShare(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chiParam(r, "token"))
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	var shareID uuid.UUID
	var passwordHash *string
	err := s.db.QueryRow(r.Context(), `SELECT id,password_hash FROM public_shares WHERE token_hash=$1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>now())`, panAuth.TokenHash(token)).Scan(&shareID, &passwordHash)
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "分享不存在或已失效")
		return
	}
	if passwordHash != nil && !panAuth.VerifyPassword(*passwordHash, input.Password) {
		writeError(w, http.StatusUnauthorized, "wrong_password", "访问密码错误")
		return
	}
	plain, hash, err := panAuth.NewToken(32)
	if err != nil {
		internalError(w, err)
		return
	}
	expires := time.Now().Add(time.Hour)
	if _, err := s.db.Exec(r.Context(), `INSERT INTO share_access_tokens(id,share_id,token_hash,expires_at) VALUES($1,$2,$3,$4)`, uuid.New(), shareID, hash, expires); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accessToken": plain, "expiresAt": expires})
}

func chiParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

func (s *Server) publicShare(w http.ResponseWriter, r *http.Request) {
	plainToken := chiParam(r, "token")
	shareID, resourceID, resourceType, allowDownload, access, err := s.validateShareAccess(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "share_locked", "请先验证分享密码")
		return
	}
	response := map[string]any{"id": shareID, "resourceType": resourceType, "allowDownload": allowDownload}
	if resourceType == "album" {
		var name, description string
		if err := s.db.QueryRow(r.Context(), `SELECT name,description FROM albums WHERE id=$1`, resourceID).Scan(&name, &description); err != nil {
			writeError(w, http.StatusNotFound, "not_found", "分享内容不存在")
			return
		}
		response["name"], response["description"] = name, description
		rows, err := s.db.Query(r.Context(), `SELECT n.name,a.object_key,p.thumb_small_key,p.remark,COALESCE(p.taken_at,n.created_at) FROM album_items i JOIN nodes n ON n.id=i.node_id JOIN assets a ON a.id=n.asset_id LEFT JOIN photo_details p ON p.asset_id=a.id WHERE i.album_id=$1 AND n.deleted_at IS NULL ORDER BY i.position,i.created_at LIMIT 1000`, resourceID)
		if err != nil {
			internalError(w, err)
			return
		}
		items := make([]map[string]any, 0)
		for rows.Next() {
			var itemName, key, remark string
			var thumb *string
			var takenAt time.Time
			if err := rows.Scan(&itemName, &key, &thumb, &remark, &takenAt); err != nil {
				rows.Close()
				internalError(w, err)
				return
			}
			if thumb != nil {
				key = *thumb
			}
			url, _ := s.store.PresignGet(r.Context(), key, "")
			items = append(items, map[string]any{"name": itemName, "previewUrl": url, "remark": remark, "takenAt": takenAt})
		}
		rows.Close()
		response["items"] = items
	} else {
		var name, key string
		if err := s.db.QueryRow(r.Context(), `SELECT n.name,COALESCE(a.object_key,'') FROM nodes n LEFT JOIN assets a ON a.id=n.asset_id WHERE n.id=$1 AND n.deleted_at IS NULL`, resourceID).Scan(&name, &key); err != nil {
			writeError(w, http.StatusNotFound, "not_found", "分享内容不存在")
			return
		}
		response["name"] = name
		if resourceType == "file" && allowDownload {
			url, _ := s.store.PresignGet(r.Context(), key, name)
			response["downloadUrl"] = url
		}
		if resourceType == "folder" {
			rows, err := s.db.Query(r.Context(), `
				WITH RECURSIVE tree AS (
				 SELECT id,parent_id,name,kind,asset_id,name::text AS relative_path FROM nodes WHERE id=$1 AND deleted_at IS NULL
				 UNION ALL SELECT n.id,n.parent_id,n.name,n.kind,n.asset_id,(t.relative_path || '/' || n.name) FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE n.deleted_at IS NULL
				)
				SELECT t.name,t.kind,t.relative_path,COALESCE(a.size_bytes,0) FROM tree t LEFT JOIN assets a ON a.id=t.asset_id WHERE t.id<>$1 ORDER BY t.relative_path LIMIT 1000`, resourceID)
			if err != nil {
				internalError(w, err)
				return
			}
			items := make([]map[string]any, 0)
			for rows.Next() {
				var itemName, kind, relativePath string
				var size int64
				if err := rows.Scan(&itemName, &kind, &relativePath, &size); err != nil {
					rows.Close()
					internalError(w, err)
					return
				}
				items = append(items, map[string]any{"name": itemName, "kind": kind, "relativePath": relativePath, "sizeBytes": size})
			}
			rows.Close()
			response["items"] = items
		}
	}
	if allowDownload && resourceType != "file" {
		response["archiveUrl"] = fmt.Sprintf("/api/v1/public/shares/%s/archive?access_token=%s", plainToken, access)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) validateShareAccess(r *http.Request) (uuid.UUID, uuid.UUID, string, bool, string, error) {
	plainToken := chiParam(r, "token")
	access := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if access == "" {
		access = r.URL.Query().Get("access_token")
	}
	var shareID, resourceID uuid.UUID
	var resourceType string
	var allowDownload bool
	err := s.db.QueryRow(r.Context(), `
		SELECT s.id,s.resource_type,s.resource_id,s.allow_download
		FROM public_shares s JOIN share_access_tokens a ON a.share_id=s.id
		WHERE s.token_hash=$1 AND a.token_hash=$2 AND a.expires_at>now() AND s.revoked_at IS NULL AND (s.expires_at IS NULL OR s.expires_at>now())`, panAuth.TokenHash(plainToken), panAuth.TokenHash(access)).Scan(&shareID, &resourceType, &resourceID, &allowDownload)
	return shareID, resourceID, resourceType, allowDownload, access, err
}

func (s *Server) publicShareArchive(w http.ResponseWriter, r *http.Request) {
	_, resourceID, resourceType, allowDownload, _, err := s.validateShareAccess(r)
	if err != nil || !allowDownload || resourceType == "file" {
		writeError(w, http.StatusNotFound, "not_found", "下载不可用")
		return
	}
	var archiveName string
	entries := make([]archiveEntry, 0)
	if resourceType == "folder" {
		if err := s.db.QueryRow(r.Context(), `SELECT name FROM nodes WHERE id=$1 AND deleted_at IS NULL`, resourceID).Scan(&archiveName); err != nil {
			writeError(w, http.StatusNotFound, "not_found", "分享内容不存在")
			return
		}
		rows, err := s.db.Query(r.Context(), `
			WITH RECURSIVE tree AS (
			 SELECT id,parent_id,name,kind,asset_id,name::text AS relative_path FROM nodes WHERE id=$1 AND deleted_at IS NULL
			 UNION ALL SELECT n.id,n.parent_id,n.name,n.kind,n.asset_id,(t.relative_path || '/' || n.name) FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE n.deleted_at IS NULL
			)
			SELECT t.relative_path,a.object_key FROM tree t JOIN assets a ON a.id=t.asset_id WHERE t.kind='file' AND a.status='ready' ORDER BY t.relative_path`, resourceID)
		if err != nil {
			internalError(w, err)
			return
		}
		for rows.Next() {
			var entry archiveEntry
			if err := rows.Scan(&entry.Name, &entry.Key); err != nil {
				rows.Close()
				internalError(w, err)
				return
			}
			entries = append(entries, entry)
		}
		rows.Close()
	} else {
		if err := s.db.QueryRow(r.Context(), `SELECT name FROM albums WHERE id=$1`, resourceID).Scan(&archiveName); err != nil {
			writeError(w, http.StatusNotFound, "not_found", "分享内容不存在")
			return
		}
		rows, err := s.db.Query(r.Context(), `SELECT n.name,a.object_key FROM album_items i JOIN nodes n ON n.id=i.node_id JOIN assets a ON a.id=n.asset_id WHERE i.album_id=$1 AND n.deleted_at IS NULL AND a.status='ready' ORDER BY i.position,i.created_at`, resourceID)
		if err != nil {
			internalError(w, err)
			return
		}
		for rows.Next() {
			var entry archiveEntry
			if err := rows.Scan(&entry.Name, &entry.Key); err != nil {
				rows.Close()
				internalError(w, err)
				return
			}
			entries = append(entries, entry)
		}
		rows.Close()
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s.zip", pathEscape(archiveName)))
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

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	a := actorFrom(r)
	if a.Role != "owner" && a.Role != "admin" {
		writeError(w, http.StatusForbidden, "forbidden", "只有家庭管理员可以查看审计记录")
		return
	}
	rows, err := s.db.Query(r.Context(), `SELECT e.id,e.action,e.resource_type,e.resource_id,e.metadata,e.created_at,u.display_name FROM audit_events e LEFT JOIN users u ON u.id=e.actor_user_id WHERE e.household_id=$1 ORDER BY e.created_at DESC LIMIT 200`, a.HouseholdID)
	if err != nil {
		internalError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id uuid.UUID
		var action string
		var resourceType, actorName *string
		var resourceID *uuid.UUID
		var metadata []byte
		var createdAt time.Time
		if err := rows.Scan(&id, &action, &resourceType, &resourceID, &metadata, &createdAt, &actorName); err != nil {
			internalError(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "action": action, "resourceType": resourceType, "resourceId": resourceID, "metadata": string(metadata), "actorName": actorName, "createdAt": createdAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
