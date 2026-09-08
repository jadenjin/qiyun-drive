package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func exerciseRevokedPublication(t *testing.T, baseURL, uploadID, nodeID, digest string, pool *pgxpool.Pool) {
	ctx := context.Background()
	var memberID, ownerID, spaceID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT n.created_by,n.space_id,hm.user_id FROM nodes n JOIN spaces s ON s.id=n.space_id JOIN household_members hm ON hm.household_id=s.household_id AND hm.role='owner' WHERE n.id=$1`, nodeID).Scan(&memberID, &spaceID, &ownerID); err != nil {
		t.Fatal(err)
	}
	token, sessionID := seedLiveSession(t, ctx, pool, ownerID)
	defer pool.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, sessionID)
	defer pool.Exec(ctx, `DELETE FROM acl_entries WHERE resource_type='node' AND resource_id=$1`, nodeID)
	// Hold a valid queued publication in the future so revocation commits before
	// either Worker may claim it. Other cases exercise the HTTP queueing path.
	if _, err := pool.Exec(ctx, `UPDATE upload_sessions SET state='completing',expected_sha256=$2,completion_parts='[]' WHERE id=$1`, uploadID, digest); err != nil {
		t.Fatal(err)
	}
	jobID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO jobs(id,kind,payload,run_after) VALUES($1,'finalize_upload',jsonb_build_object('uploadId',$2::text),now()+interval '1 hour')`, jobID, uploadID); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM jobs WHERE id=$1`, jobID)
	status, body := liveJSON(t, http.MethodPut, baseURL+"/nodes/"+nodeID+"/permissions", "pan_session="+token, "", map[string]any{"inherit": false, "entries": []map[string]any{{"userId": memberID, "permission": "viewer"}}})
	if status != 200 && status != 204 {
		t.Fatalf("revoke upload permission: %d %s", status, body)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	failed := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		var state string
		if err := pool.QueryRow(ctx, `SELECT state FROM upload_sessions WHERE id=$1`, uploadID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "ready" {
			t.Fatal("revoked queued upload was published")
		}
		if state == "failed" {
			failed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !failed {
		t.Fatal("revoked upload did not fail")
	}
	var reserved, expected int64
	if err := pool.QueryRow(ctx, `SELECT s.reserved_bytes,COALESCE((SELECT sum(u.expected_size) FROM upload_sessions u JOIN assets a ON a.id=u.asset_id WHERE a.space_id=s.id AND u.state IN ('pending','uploading','completing','expired')),0) FROM spaces s WHERE s.id=$1`, spaceID).Scan(&reserved, &expected); err != nil || reserved != expected {
		t.Fatalf("revoked upload quota drift: %d %d %v", reserved, expected, err)
	}
}

// Two real Workers contend for the same upload after an injected lost lease.
// A row lock holds the first copy immediately before its publication write.
func holdPublicationForLeaseRace(t *testing.T, pool *pgxpool.Pool, uploadID string) func() {
	ctx := context.Background()
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { blocker.Rollback(ctx) })
	if _, err := blocker.Exec(ctx, `SELECT id FROM assets WHERE id=(SELECT asset_id FROM upload_sessions WHERE id=$1) FOR UPDATE`, uploadID); err != nil {
		t.Fatal(err)
	}
	return func() {
		waiting := false
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'UPDATE assets SET status=%'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count > 0 {
				waiting = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !waiting {
			t.Fatal("first Worker did not reach publication barrier")
		}
		if _, err := pool.Exec(ctx, `UPDATE jobs SET state='failed',locked_at=NULL,run_after=now() WHERE kind='finalize_upload' AND payload->>'uploadId'=$1 AND state='running'`, uploadID); err != nil {
			t.Fatal(err)
		}
		reclaimed := false
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
			var attempts int
			if err := pool.QueryRow(ctx, `SELECT attempts FROM jobs WHERE kind='finalize_upload' AND payload->>'uploadId'=$1`, uploadID).Scan(&attempts); err != nil {
				t.Fatal(err)
			}
			var blocked int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND (query LIKE 'UPDATE assets SET status=%' OR query LIKE 'SELECT state FROM upload_sessions WHERE id=%')`).Scan(&blocked); err != nil {
				t.Fatal(err)
			}
			if attempts >= 2 && blocked >= 2 {
				reclaimed = true
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if !reclaimed {
			t.Fatal("second Worker did not reclaim the injected stale lease")
		}
	}
}

func exerciseDeleteAndCreate(t *testing.T, baseURL, cookie string, spaceID, userID uuid.UUID, pool *pgxpool.Pool) {
	ctx := context.Background()
	parent := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO nodes(id,space_id,kind,name,created_by) VALUES($1,$2,'folder','delete-create-race',$3)`, parent, spaceID, userID); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM audit_events WHERE resource_id IN (SELECT id FROM nodes WHERE id=$1 OR parent_id=$1)`, parent)
	defer pool.Exec(ctx, `DELETE FROM nodes WHERE id=$1`, parent)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM nodes WHERE id=$1 FOR UPDATE`, parent); err != nil {
		t.Fatal(err)
	}
	statuses := make(chan int, 2)
	go func() {
		status, _ := liveJSON(t, http.MethodDelete, baseURL+"/nodes/"+parent.String(), cookie, "", nil)
		statuses <- status
	}()
	go func() {
		status, _ := liveJSON(t, http.MethodPost, baseURL+"/folders", cookie, "", map[string]any{"spaceId": spaceID, "parentId": parent, "name": "new-child"})
		statuses <- status
	}()
	waiting := 0
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND (query LIKE '%INSERT INTO nodes%' OR query LIKE '%UPDATE nodes SET original_parent_id%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	results := []int{<-statuses, <-statuses}
	if waiting < 2 {
		t.Fatalf("requests did not reach the write barrier: %v", results)
	}
	conflicts, success := 0, 0
	for _, status := range results {
		if status == 409 {
			conflicts++
		}
		if status == 201 || status == 204 {
			success++
		}
	}
	if conflicts != 1 || success != 1 {
		t.Fatalf("expected one committed mutation and one retryable conflict: %v", results)
	}
	var orphan bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes c JOIN nodes p ON p.id=c.parent_id WHERE p.id=$1 AND p.deleted_at IS NOT NULL AND c.deleted_at IS NULL)`, parent).Scan(&orphan); err != nil || orphan {
		t.Fatalf("live child beneath deleted parent: %v", err)
	}
}
