package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func exercisePhotoPagination(t *testing.T, baseURL, cookie string, spaceID, userID uuid.UUID, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	nodes, assets := make([]uuid.UUID, 721), make([]uuid.UUID, 721)
	expected := make(map[uuid.UUID]bool)
	for i := range nodes {
		nodes[i], assets[i] = uuid.New(), uuid.New()
		if i >= 121 {
			expected[nodes[i]] = true
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM nodes WHERE id=ANY($1::uuid[])`, nodes)
		_, _ = pool.Exec(ctx, `DELETE FROM assets WHERE id=ANY($1::uuid[])`, assets)
	})
	if _, err := pool.Exec(ctx, `INSERT INTO assets(id,space_id,object_key,mime_type,status,created_by) SELECT id,$2,'pagination-fixture/'||id::text,'image/jpeg','ready',$3 FROM unnest($1::uuid[]) id`, assets, spaceID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO photo_details(asset_id,taken_at) SELECT id,'2020-01-01T00:00:00Z'::timestamptz FROM unnest($1::uuid[]) id`, assets); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO nodes(id,asset_id,space_id,kind,section,name,created_by,inherit_permissions) SELECT n,a,$3,'file','photos','pagination-'||n::text,$4,NOT(n=ANY($5::uuid[])) FROM unnest($1::uuid[],$2::uuid[]) AS pair(n,a)`, nodes, assets, spaceID, userID, nodes[:121]); err != nil {
		t.Fatal(err)
	}
	seen := make(map[uuid.UUID]bool)
	cursor := ""
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatal("pagination failed to terminate")
		}
		status, body := liveJSON(t, http.MethodGet, baseURL+"/photos?spaceId="+spaceID.String()+"&cursor="+url.QueryEscape(cursor), cookie, "", nil)
		requireLiveStatus(t, status, http.StatusOK, body)
		var response struct {
			Items []struct {
				NodeID uuid.UUID `json:"nodeId"`
			} `json:"items"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Items) > 120 {
			t.Fatal("page exceeded bound")
		}
		for _, item := range response.Items {
			if seen[item.NodeID] {
				t.Fatal("duplicate photo across pages")
			}
			seen[item.NodeID] = true
		}
		if response.NextCursor == "" {
			break
		}
		if response.NextCursor == cursor {
			t.Fatal("cursor did not advance")
		}
		cursor = response.NextCursor
	}
	for _, id := range nodes {
		if seen[id] != expected[id] {
			t.Fatalf("pagination permission/completeness mismatch for %s", id)
		}
	}
}
