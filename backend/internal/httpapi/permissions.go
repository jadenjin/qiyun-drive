package httpapi

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	permissionNone    = 0
	permissionViewer  = 1
	permissionEditor  = 2
	permissionManager = 3
)

func permissionLevel(value string) int {
	switch value {
	case "manager":
		return permissionManager
	case "editor":
		return permissionEditor
	case "viewer":
		return permissionViewer
	default:
		return permissionNone
	}
}

func permissionName(level int) string {
	switch level {
	case permissionManager:
		return "manager"
	case permissionEditor:
		return "editor"
	case permissionViewer:
		return "viewer"
	default:
		return "none"
	}
}

func (s *Server) spacePermission(ctx context.Context, a actor, spaceID uuid.UUID) (int, error) {
	return spacePermissionWith(ctx, s.db, a, spaceID)
}

type permissionQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func spacePermissionWith(ctx context.Context, db permissionQuerier, a actor, spaceID uuid.UUID) (int, error) {
	var kind string
	var ownerID *uuid.UUID
	var householdID uuid.UUID
	err := db.QueryRow(ctx, `SELECT kind,owner_user_id,household_id FROM spaces WHERE id=$1`, spaceID).Scan(&kind, &ownerID, &householdID)
	if err != nil {
		return permissionNone, err
	}
	if householdID != a.HouseholdID {
		return permissionNone, nil
	}
	if kind == "personal" {
		if ownerID != nil && *ownerID == a.UserID {
			return permissionManager, nil
		}
		return permissionNone, nil
	}
	if a.Role == "owner" || a.Role == "admin" {
		return permissionManager, nil
	}
	return permissionEditor, nil
}

func (s *Server) nodePermission(ctx context.Context, a actor, nodeID uuid.UUID) (int, error) {
	return nodePermissionWith(ctx, s.db, a, nodeID)
}

func nodePermissionWith(ctx context.Context, db permissionQuerier, a actor, nodeID uuid.UUID) (int, error) {
	var spaceID uuid.UUID
	err := db.QueryRow(ctx, `SELECT space_id FROM nodes WHERE id=$1`, nodeID).Scan(&spaceID)
	if err != nil {
		return permissionNone, err
	}
	base, err := spacePermissionWith(ctx, db, a, spaceID)
	if err != nil || base == permissionNone || base == permissionManager {
		return base, err
	}
	rows, err := db.Query(ctx, `
		WITH RECURSIVE chain AS (
		  SELECT id,parent_id,inherit_permissions,0 AS depth FROM nodes WHERE id=$1
		  UNION ALL
		  SELECT n.id,n.parent_id,n.inherit_permissions,c.depth+1 FROM nodes n JOIN chain c ON c.parent_id=n.id
		)
		SELECT c.inherit_permissions,a.permission
		FROM chain c LEFT JOIN acl_entries a ON a.resource_type='node' AND a.resource_id=c.id AND a.principal_user_id=$2
		ORDER BY c.depth`, nodeID, a.UserID)
	if err != nil {
		return permissionNone, err
	}
	defer rows.Close()
	for rows.Next() {
		var inherit bool
		var explicit *string
		if err := rows.Scan(&inherit, &explicit); err != nil {
			return permissionNone, err
		}
		if explicit != nil {
			return permissionLevel(*explicit), nil
		}
		if !inherit {
			return permissionNone, nil
		}
	}
	return base, rows.Err()
}

// nodePermissions resolves many nodes in one recursive query. Listing a large
// directory or photo timeline must not issue one permission query per item.
func (s *Server) nodePermissions(ctx context.Context, a actor, spaceID uuid.UUID, nodeIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	return nodePermissionsWith(ctx, s.db, a, spaceID, nodeIDs)
}

func nodePermissionsWith(ctx context.Context, db permissionQuerier, a actor, spaceID uuid.UUID, nodeIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	result := make(map[uuid.UUID]int, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return result, nil
	}
	base, err := spacePermissionWith(ctx, db, a, spaceID)
	if err != nil {
		return nil, err
	}
	if base == permissionNone || base == permissionManager {
		for _, id := range nodeIDs {
			result[id] = base
		}
		return result, nil
	}
	rows, err := db.Query(ctx, `
		WITH RECURSIVE chain(target_id,id,parent_id,inherit_permissions,depth) AS (
			SELECT n.id,n.id,n.parent_id,n.inherit_permissions,0
			FROM nodes n WHERE n.id=ANY($1::uuid[]) AND n.space_id=$2
			UNION ALL
			SELECT c.target_id,p.id,p.parent_id,p.inherit_permissions,c.depth+1
			FROM chain c JOIN nodes p ON p.id=c.parent_id WHERE p.space_id=$2
		), boundaries AS (
			SELECT c.target_id,c.depth,
				CASE acl.permission WHEN 'manager' THEN 3 WHEN 'editor' THEN 2 WHEN 'viewer' THEN 1 ELSE 0 END AS level
			FROM chain c
			LEFT JOIN acl_entries acl ON acl.resource_type='node' AND acl.resource_id=c.id AND acl.principal_user_id=$3
			WHERE acl.permission IS NOT NULL OR NOT c.inherit_permissions
		), nearest AS (
			SELECT DISTINCT ON (target_id) target_id,level FROM boundaries ORDER BY target_id,depth
		), targets AS (
			SELECT DISTINCT target_id FROM chain WHERE depth=0
		)
		SELECT t.target_id,COALESCE(n.level,$4) FROM targets t LEFT JOIN nearest n ON n.target_id=t.target_id`, nodeIDs, spaceID, a.UserID, base)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var level int
		if err := rows.Scan(&id, &level); err != nil {
			return nil, err
		}
		result[id] = level
	}
	return result, rows.Err()
}

func (s *Server) subtreePermissionAtLeast(ctx context.Context, a actor, nodeID uuid.UUID, required int) (bool, error) {
	return subtreePermissionAtLeastWith(ctx, s.db, a, nodeID, required, false)
}

func (s *Server) activeSubtreePermissionAtLeast(ctx context.Context, a actor, nodeID uuid.UUID, required int) (bool, error) {
	return subtreePermissionAtLeastWith(ctx, s.db, a, nodeID, required, true)
}

func subtreePermissionAtLeastWith(ctx context.Context, db permissionQuerier, a actor, nodeID uuid.UUID, required int, activeOnly bool) (bool, error) {
	rows, err := db.Query(ctx, `
		WITH RECURSIVE tree AS (
		  SELECT id,space_id FROM nodes WHERE id=$1 AND (NOT $2 OR deleted_at IS NULL)
		  UNION SELECT n.id,n.space_id FROM nodes n JOIN tree t ON n.parent_id=t.id WHERE NOT $2 OR n.deleted_at IS NULL
		)
		SELECT id,space_id FROM tree`, nodeID, activeOnly)
	if err != nil {
		return false, err
	}
	ids := make([]uuid.UUID, 0)
	var spaceID uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id, &spaceID); err != nil {
			rows.Close()
			return false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	permissions, err := nodePermissionsWith(ctx, db, a, spaceID, ids)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		if permissions[id] < required {
			return false, nil
		}
	}
	return len(ids) > 0, nil
}

func (s *Server) albumPermission(ctx context.Context, a actor, albumID uuid.UUID) (int, error) {
	return albumPermissionWith(ctx, s.db, a, albumID)
}

func albumPermissionWith(ctx context.Context, db permissionQuerier, a actor, albumID uuid.UUID) (int, error) {
	var spaceID uuid.UUID
	var inherit bool
	err := db.QueryRow(ctx, `SELECT space_id,inherit_permissions FROM albums WHERE id=$1`, albumID).Scan(&spaceID, &inherit)
	if err != nil {
		return permissionNone, err
	}
	base, err := spacePermissionWith(ctx, db, a, spaceID)
	if err != nil || base == permissionNone || base == permissionManager {
		return base, err
	}
	var explicit string
	err = db.QueryRow(ctx, `SELECT permission FROM acl_entries WHERE resource_type='album' AND resource_id=$1 AND principal_user_id=$2`, albumID, a.UserID).Scan(&explicit)
	if err == nil {
		return permissionLevel(explicit), nil
	}
	if err != pgx.ErrNoRows {
		return permissionNone, err
	}
	if !inherit {
		return permissionNone, nil
	}
	return base, nil
}

func (s *Server) albumPermissions(ctx context.Context, a actor, spaceID uuid.UUID, albumIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	result := make(map[uuid.UUID]int, len(albumIDs))
	if len(albumIDs) == 0 {
		return result, nil
	}
	base, err := s.spacePermission(ctx, a, spaceID)
	if err != nil {
		return nil, err
	}
	if base == permissionNone || base == permissionManager {
		for _, id := range albumIDs {
			result[id] = base
		}
		return result, nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT al.id,
			CASE acl.permission WHEN 'manager' THEN 3 WHEN 'editor' THEN 2 WHEN 'viewer' THEN 1
			ELSE CASE WHEN al.inherit_permissions THEN $4 ELSE 0 END END
		FROM albums al
		LEFT JOIN acl_entries acl ON acl.resource_type='album' AND acl.resource_id=al.id AND acl.principal_user_id=$3
		WHERE al.id=ANY($1::uuid[]) AND al.space_id=$2`, albumIDs, spaceID, a.UserID, base)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var level int
		if err := rows.Scan(&id, &level); err != nil {
			return nil, err
		}
		result[id] = level
	}
	return result, rows.Err()
}
