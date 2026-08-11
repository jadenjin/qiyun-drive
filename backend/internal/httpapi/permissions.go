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
	var kind string
	var ownerID *uuid.UUID
	var householdID uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT kind,owner_user_id,household_id FROM spaces WHERE id=$1`, spaceID).Scan(&kind, &ownerID, &householdID)
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
	var spaceID uuid.UUID
	err := s.db.QueryRow(ctx, `SELECT space_id FROM nodes WHERE id=$1`, nodeID).Scan(&spaceID)
	if err != nil {
		return permissionNone, err
	}
	base, err := s.spacePermission(ctx, a, spaceID)
	if err != nil || base == permissionNone || base == permissionManager {
		return base, err
	}
	rows, err := s.db.Query(ctx, `
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

func (s *Server) albumPermission(ctx context.Context, a actor, albumID uuid.UUID) (int, error) {
	var spaceID uuid.UUID
	var inherit bool
	err := s.db.QueryRow(ctx, `SELECT space_id,inherit_permissions FROM albums WHERE id=$1`, albumID).Scan(&spaceID, &inherit)
	if err != nil {
		return permissionNone, err
	}
	base, err := s.spacePermission(ctx, a, spaceID)
	if err != nil || base == permissionNone || base == permissionManager {
		return base, err
	}
	var explicit string
	err = s.db.QueryRow(ctx, `SELECT permission FROM acl_entries WHERE resource_type='album' AND resource_id=$1 AND principal_user_id=$2`, albumID, a.UserID).Scan(&explicit)
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
