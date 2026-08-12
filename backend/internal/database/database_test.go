package database

import (
	"strings"
	"testing"
)

func TestQueryIndexMigrationIsEmbedded(t *testing.T) {
	body, err := migrations.ReadFile("migrations/002_query_indexes.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, index := range []string{
		"sessions_user_expiry_idx",
		"nodes_space_updated_idx",
		"upload_sessions_cleanup_idx",
		"albums_space_updated_idx",
		"public_shares_creator_created_idx",
		"audit_events_household_created_idx",
	} {
		if !strings.Contains(text, index) {
			t.Fatalf("missing expected query index %s", index)
		}
	}
}
