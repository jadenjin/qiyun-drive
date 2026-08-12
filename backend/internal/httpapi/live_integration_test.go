package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	panAuth "pan/backend/internal/auth"
)

// TestLiveAccountAndAuthorizationBoundaries exercises the running API against a
// disposable household member. It is opt-in so normal unit tests stay hermetic.
func TestLiveAccountAndAuthorizationBoundaries(t *testing.T) {
	if os.Getenv("PAN_LIVE_INTEGRATION") != "1" {
		t.Skip("set PAN_LIVE_INTEGRATION=1 to exercise the running API")
	}
	ctx := context.Background()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgres://pan:pan-local-test-password@localhost:5432/pan?sslmode=disable"
	}
	baseURL := strings.TrimRight(os.Getenv("PAN_API_URL"), "/")
	if baseURL == "" {
		baseURL = "http://localhost:8080/api/v1"
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}

	memberID, adminID := uuid.New(), uuid.New()
	personalSpaceID := uuid.New()
	parentID, childID, shareFolderID, privateFolderID, albumID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	invitationID := uuid.New()
	createdAuditResources := make([]uuid.UUID, 0, 8)
	createdSessionIDs := make([]uuid.UUID, 0, 5)
	createdShareIDs := make([]uuid.UUID, 0, 2)
	t.Cleanup(func() {
		for _, id := range createdAuditResources {
			_, _ = pool.Exec(ctx, `DELETE FROM audit_events WHERE resource_id=$1`, id)
		}
		for _, id := range createdShareIDs {
			_, _ = pool.Exec(ctx, `DELETE FROM public_shares WHERE id=$1`, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM invitations WHERE id=$1`, invitationID)
		_, _ = pool.Exec(ctx, `DELETE FROM nodes WHERE id IN ($1,$2)`, parentID, shareFolderID)
		_, _ = pool.Exec(ctx, `DELETE FROM albums WHERE id=$1`, albumID)
		for _, id := range createdSessionIDs {
			_, _ = pool.Exec(ctx, `DELETE FROM sessions WHERE id=$1`, id)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM spaces WHERE id=$1`, personalSpaceID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id IN ($1,$2)`, memberID, adminID)
		pool.Close()
	})

	var ownerID, householdID, familySpaceID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT hm.user_id,hm.household_id FROM household_members hm WHERE hm.role='owner' ORDER BY hm.created_at LIMIT 1`).Scan(&ownerID, &householdID); err != nil {
		t.Fatalf("load household owner: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM spaces WHERE household_id=$1 AND kind='family'`, householdID).Scan(&familySpaceID); err != nil {
		t.Fatalf("load family space: %v", err)
	}
	passwordHash, err := panAuth.HashPassword("Stage4Disposable123!")
	if err != nil {
		t.Fatal(err)
	}
	suffix := strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,username,display_name,password_hash) VALUES($1,$2,$3,$4),($5,$6,$7,$4)`,
		memberID, "stage4-member-"+suffix, "阶段四临时成员", passwordHash, adminID, "stage4-admin-"+suffix, "阶段四临时管理员"); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO household_members(household_id,user_id,role) VALUES($1,$2,'member'),($1,$3,'admin')`, householdID, memberID, adminID); err != nil {
		t.Fatalf("seed memberships: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO spaces(id,household_id,owner_user_id,kind,name) VALUES($1,$2,$3,'personal','阶段四临时空间')`, personalSpaceID, householdID, memberID); err != nil {
		t.Fatalf("seed personal space: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO nodes(id,space_id,parent_id,kind,name,inherit_permissions,created_by) VALUES
		($1,$2,NULL,'folder','stage4-parent',true,$3),
		($4,$2,$1,'folder','stage4-restricted-child',false,$3),
		($5,$2,NULL,'folder','stage4-family-share',false,$3),
		($6,$7,NULL,'folder','stage4-private-share',true,$3)`, parentID, familySpaceID, memberID, childID, shareFolderID, privateFolderID, personalSpaceID); err != nil {
		t.Fatalf("seed folders: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO albums(id,space_id,name,description,inherit_permissions,created_by) VALUES($1,$2,'stage4-revoked-album','',false,$3)`, albumID, familySpaceID, memberID); err != nil {
		t.Fatalf("seed album: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO acl_entries(id,resource_type,resource_id,principal_user_id,permission,created_by) VALUES($1,'node',$2,$3,'manager',$4)`, uuid.New(), shareFolderID, memberID, ownerID); err != nil {
		t.Fatalf("seed share permission: %v", err)
	}

	ownerToken, ownerSessionID := seedLiveSession(t, ctx, pool, ownerID)
	adminToken, adminSessionID := seedLiveSession(t, ctx, pool, adminID)
	memberToken, memberSessionID := seedLiveSession(t, ctx, pool, memberID)
	_, otherMemberSessionID := seedLiveSession(t, ctx, pool, memberID)
	createdSessionIDs = append(createdSessionIDs, ownerSessionID, adminSessionID, memberSessionID, otherMemberSessionID)
	ownerCookie, adminCookie, memberCookie := "pan_session="+ownerToken, "pan_session="+adminToken, "pan_session="+memberToken

	t.Run("session inventory and revocation", func(t *testing.T) {
		status, body := liveJSON(t, http.MethodGet, baseURL+"/me/sessions", memberCookie, "", nil)
		requireLiveStatus(t, status, http.StatusOK, body)
		var response struct {
			Items []struct {
				ID      uuid.UUID `json:"id"`
				Current bool      `json:"current"`
			} `json:"items"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Items) != 2 {
			t.Fatalf("expected two sessions, got %d: %s", len(response.Items), body)
		}
		currentCount := 0
		for _, item := range response.Items {
			if item.Current {
				currentCount++
			}
		}
		if currentCount != 1 {
			t.Fatalf("expected exactly one current session, got %d", currentCount)
		}
		status, body = liveJSON(t, http.MethodDelete, baseURL+"/me/sessions/"+otherMemberSessionID.String(), memberCookie, "", nil)
		requireLiveStatus(t, status, http.StatusNoContent, body)
		createdAuditResources = append(createdAuditResources, otherMemberSessionID)
	})

	t.Run("administrative role boundaries", func(t *testing.T) {
		status, body := liveJSON(t, http.MethodPost, baseURL+"/invitations", adminCookie, "", map[string]any{"role": "admin"})
		requireLiveStatus(t, status, http.StatusForbidden, body)
		status, body = liveJSON(t, http.MethodPatch, baseURL+"/members/"+memberID.String(), adminCookie, "", map[string]any{"role": "admin"})
		requireLiveStatus(t, status, http.StatusForbidden, body)
		status, body = liveJSON(t, http.MethodPost, baseURL+"/members/"+ownerID.String()+"/password-reset", adminCookie, "", map[string]any{})
		requireLiveStatus(t, status, http.StatusForbidden, body)
	})

	t.Run("duplicate invitation username is a conflict", func(t *testing.T) {
		plain, tokenHash, err := panAuth.NewToken(32)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO invitations(id,household_id,created_by,token_hash,role,expires_at) VALUES($1,$2,$3,$4,'member',now()+interval '1 hour')`, invitationID, householdID, ownerID, tokenHash); err != nil {
			t.Fatal(err)
		}
		status, body := liveJSON(t, http.MethodPost, baseURL+"/invitations/accept", "", "", map[string]any{
			"token": plain, "username": "stage4-member-" + suffix, "displayName": "重复用户名", "password": "AnotherStrong123!",
		})
		requireLiveStatus(t, status, http.StatusConflict, body)
	})

	t.Run("restricted descendants block recursive deletion", func(t *testing.T) {
		status, body := liveJSON(t, http.MethodDelete, baseURL+"/nodes/"+parentID.String(), memberCookie, "", nil)
		requireLiveStatus(t, status, http.StatusForbidden, body)
		var deletedAt *time.Time
		if err := pool.QueryRow(ctx, `SELECT deleted_at FROM nodes WHERE id=$1`, parentID).Scan(&deletedAt); err != nil || deletedAt != nil {
			t.Fatalf("parent was modified after denied deletion: deleted=%v err=%v", deletedAt, err)
		}
	})

	t.Run("album creator cannot bypass revoked permission", func(t *testing.T) {
		status, body := liveJSON(t, http.MethodDelete, baseURL+"/albums/"+albumID.String(), memberCookie, "", nil)
		requireLiveStatus(t, status, http.StatusNotFound, body)
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM albums WHERE id=$1)`, albumID).Scan(&exists); err != nil || !exists {
			t.Fatalf("album disappeared after denied deletion: exists=%v err=%v", exists, err)
		}
	})

	t.Run("share password and permission boundaries", func(t *testing.T) {
		status, body := liveJSON(t, http.MethodPost, baseURL+"/shares", memberCookie, "", map[string]any{
			"resourceType": "folder", "resourceId": privateFolderID, "password": "123", "allowDownload": true,
		})
		requireLiveStatus(t, status, http.StatusBadRequest, body)

		status, body = liveJSON(t, http.MethodPost, baseURL+"/shares", memberCookie, "", map[string]any{
			"resourceType": "folder", "resourceId": shareFolderID, "password": "", "allowDownload": true,
		})
		requireLiveStatus(t, status, http.StatusCreated, body)
		var familyShare struct {
			ID  uuid.UUID `json:"id"`
			URL string    `json:"url"`
		}
		if err := json.Unmarshal(body, &familyShare); err != nil {
			t.Fatal(err)
		}
		createdShareIDs = append(createdShareIDs, familyShare.ID)
		createdAuditResources = append(createdAuditResources, familyShare.ID)
		shareURL, err := url.Parse(familyShare.URL)
		if err != nil {
			t.Fatal(err)
		}
		publicToken := path.Base(shareURL.Path)
		status, body = liveJSON(t, http.MethodPost, baseURL+"/public/shares/"+publicToken+"/unlock", "", "", map[string]any{"password": ""})
		requireLiveStatus(t, status, http.StatusOK, body)
		var unlocked struct {
			AccessToken string `json:"accessToken"`
		}
		if err := json.Unmarshal(body, &unlocked); err != nil {
			t.Fatal(err)
		}
		status, body = liveJSON(t, http.MethodGet, baseURL+"/public/shares/"+publicToken, "", "Bearer "+unlocked.AccessToken, nil)
		requireLiveStatus(t, status, http.StatusOK, body)

		status, body = liveJSON(t, http.MethodPost, baseURL+"/shares", memberCookie, "", map[string]any{
			"resourceType": "folder", "resourceId": privateFolderID, "password": "", "allowDownload": true,
		})
		requireLiveStatus(t, status, http.StatusCreated, body)
		var privateShare struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal(body, &privateShare); err != nil {
			t.Fatal(err)
		}
		createdShareIDs = append(createdShareIDs, privateShare.ID)
		createdAuditResources = append(createdAuditResources, privateShare.ID)

		status, body = liveJSON(t, http.MethodGet, baseURL+"/shares", ownerCookie, "", nil)
		requireLiveStatus(t, status, http.StatusOK, body)
		var listed struct {
			Items []struct {
				ID          uuid.UUID `json:"id"`
				CreatorName string    `json:"creatorName"`
				Own         bool      `json:"own"`
			} `json:"items"`
		}
		if err := json.Unmarshal(body, &listed); err != nil {
			t.Fatal(err)
		}
		foundFamily, foundPrivate := false, false
		for _, item := range listed.Items {
			if item.ID == familyShare.ID {
				foundFamily = !item.Own && item.CreatorName == "阶段四临时成员"
			}
			if item.ID == privateShare.ID {
				foundPrivate = true
			}
		}
		if !foundFamily || foundPrivate {
			t.Fatalf("family share governance leaked private data: family=%v private=%v body=%s", foundFamily, foundPrivate, body)
		}

		if _, err := pool.Exec(ctx, `DELETE FROM acl_entries WHERE resource_type='node' AND resource_id=$1 AND principal_user_id=$2`, shareFolderID, memberID); err != nil {
			t.Fatal(err)
		}
		status, body = liveJSON(t, http.MethodGet, baseURL+"/public/shares/"+publicToken, "", "Bearer "+unlocked.AccessToken, nil)
		requireLiveStatus(t, status, http.StatusUnauthorized, body)
		status, body = liveJSON(t, http.MethodDelete, baseURL+"/shares/"+familyShare.ID.String(), ownerCookie, "", nil)
		requireLiveStatus(t, status, http.StatusNoContent, body)
	})

	t.Run("new password reset invalidates old links and sessions", func(t *testing.T) {
		firstToken := createLiveReset(t, baseURL, ownerCookie, memberID)
		secondToken := createLiveReset(t, baseURL, ownerCookie, memberID)
		createdAuditResources = append(createdAuditResources, memberID)
		status, body := liveJSON(t, http.MethodPost, baseURL+"/password-resets/complete", "", "", map[string]any{"token": firstToken, "password": "FreshStage4Password123!"})
		requireLiveStatus(t, status, http.StatusGone, body)
		status, body = liveJSON(t, http.MethodPost, baseURL+"/password-resets/complete", "", "", map[string]any{"token": secondToken, "password": "FreshStage4Password123!"})
		requireLiveStatus(t, status, http.StatusOK, body)
		var sessionCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1`, memberID).Scan(&sessionCount); err != nil || sessionCount != 0 {
			t.Fatalf("password reset left %d sessions: %v", sessionCount, err)
		}
		status, body = liveJSON(t, http.MethodGet, baseURL+"/me", memberCookie, "", nil)
		requireLiveStatus(t, status, http.StatusUnauthorized, body)
	})
}

func seedLiveSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, userID uuid.UUID) (string, uuid.UUID) {
	t.Helper()
	plain, hash, err := panAuth.NewToken(32)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO sessions(id,user_id,token_hash,expires_at) VALUES($1,$2,$3,now()+interval '1 day')`, id, userID, hash); err != nil {
		t.Fatal(err)
	}
	return plain, id
}

func createLiveReset(t *testing.T, baseURL, cookie string, userID uuid.UUID) string {
	t.Helper()
	status, body := liveJSON(t, http.MethodPost, baseURL+"/members/"+userID.String()+"/password-reset", cookie, "", map[string]any{})
	requireLiveStatus(t, status, http.StatusCreated, body)
	var response struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(response.URL)
	if err != nil {
		t.Fatal(err)
	}
	return path.Base(parsed.Path)
}

func liveJSON(t *testing.T, method, endpoint, cookie, authorization string, payload any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		request.Header.Set("Cookie", cookie)
	}
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, result
}

func requireLiveStatus(t *testing.T, got, want int, body []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status=%d want=%d body=%s", got, want, fmt.Sprintf("%q", body))
	}
}
