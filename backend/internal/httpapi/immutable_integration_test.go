package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	panAuth "pan/backend/internal/auth"
)

// Requires the ordinary live fixture plus a running worker. Signed storage
// requests may connect through an internal test address while preserving Host.
func exerciseImmutableUpload(t *testing.T, baseURL, cookie string, spaceID uuid.UUID, pool *pgxpool.Pool) {
	t.Helper()
	t.Run("expired multipart releases reservation", func(t *testing.T) { exerciseExpiredMultipart(t, baseURL, cookie, spaceID, pool) })
	for _, tc := range []struct {
		name      string
		data      []byte
		badDigest bool
	}{
		{"small", []byte("original upload bytes"), false},
		{"multipart", bytes.Repeat([]byte("a"), 33<<20), false},
		{"bad-digest", []byte("checksum must reject"), true},
		{"album-archive", []byte("shared album original bytes"), false},
		{"lease-race", []byte("two workers must publish this once"), false},
		{"revoked-publication", []byte("queued upload loses edit permission"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uploadSpace := spaceID
			if tc.name == "album-archive" || tc.name == "revoked-publication" {
				if err := pool.QueryRow(context.Background(), `SELECT id FROM spaces WHERE kind='family' AND household_id=(SELECT household_id FROM spaces WHERE id=$1)`, spaceID).Scan(&uploadSpace); err != nil {
					t.Fatal(err)
				}
			}
			status, body := liveJSON(t, http.MethodPost, baseURL+"/uploads", cookie, "", map[string]any{"spaceId": uploadSpace, "name": "immutable-" + uuid.NewString(), "sizeBytes": len(tc.data), "mimeType": "application/octet-stream"})
			requireLiveStatus(t, status, http.StatusCreated, body)
			var up struct {
				ID, NodeID, Method, URL string
				PartSize                int
			}
			if err := json.Unmarshal(body, &up); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				status, _ := liveJSON(t, http.MethodDelete, baseURL+"/uploads/"+up.ID, cookie, "", nil)
				if status == http.StatusConflict {
					liveJSON(t, http.MethodDelete, baseURL+"/nodes/"+up.NodeID, cookie, "", nil)
					liveJSON(t, http.MethodDelete, baseURL+"/trash/"+up.NodeID, cookie, "", nil)
					for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
						var exists bool
						if err := pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1)`, up.NodeID).Scan(&exists); err != nil {
							t.Error(err)
							break
						}
						if !exists {
							break
						}
						time.Sleep(100 * time.Millisecond)
					}
				}
			})
			parts := []map[string]any{}
			if up.Method == "put" {
				status, _, _ = signedTestRequest(t, http.MethodPut, up.URL, append(append([]byte{}, tc.data...), byte('!')))
				if status >= 200 && status < 300 {
					t.Fatal("false upload size accepted")
				}
				status, _, body = signedTestRequest(t, http.MethodPut, up.URL, tc.data)
				requireLiveStatus(t, status, http.StatusOK, body)
			} else {
				for offset, number := 0, 1; offset < len(tc.data); offset, number = offset+up.PartSize, number+1 {
					status, body = liveJSON(t, http.MethodPost, baseURL+"/uploads/"+up.ID+"/parts", cookie, "", map[string]any{"partNumbers": []int{number}})
					requireLiveStatus(t, status, http.StatusOK, body)
					var signed struct{ Items []struct{ URL string } }
					if err := json.Unmarshal(body, &signed); err != nil {
						t.Fatal(err)
					}
					var headers http.Header
					status, headers, body = signedTestRequest(t, http.MethodPut, signed.Items[0].URL, tc.data[offset:min(offset+up.PartSize, len(tc.data))])
					requireLiveStatus(t, status, http.StatusOK, body)
					parts = append(parts, map[string]any{"partNumber": number, "etag": headers.Get("ETag")})
				}
			}
			digest := sha256.Sum256(tc.data)
			checksum := hex.EncodeToString(digest[:])
			if tc.name == "revoked-publication" {
				exerciseRevokedPublication(t, baseURL, up.ID, up.NodeID, checksum, pool)
				return
			}
			if tc.badDigest {
				checksum = strings.Repeat("0", 64)
			}
			payload := map[string]any{"parts": parts, "sha256": checksum}
			var releaseRace func()
			if tc.name == "lease-race" {
				releaseRace = holdPublicationForLeaseRace(t, pool, up.ID)
			}
			status, body = liveJSON(t, http.MethodPost, baseURL+"/uploads/"+up.ID+"/complete", cookie, "", payload)
			requireLiveStatus(t, status, http.StatusAccepted, body)
			if releaseRace != nil {
				releaseRace()
			}
			// A repeated complete must reuse the same publication, not charge
			// quota again or create another independently copying worker.
			status, body = liveJSON(t, http.MethodPost, baseURL+"/uploads/"+up.ID+"/complete", cookie, "", payload)
			if tc.name == "lease-race" && status == http.StatusConflict {
				status, body = liveJSON(t, http.MethodPost, baseURL+"/uploads/"+up.ID+"/complete", cookie, "", payload)
			}
			if status != http.StatusAccepted && status != http.StatusOK {
				t.Fatalf("duplicate completion: %d %s", status, body)
			}
			ready := false
			for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
				status, body = liveJSON(t, http.MethodPost, baseURL+"/uploads/"+up.ID+"/resume", cookie, "", nil)
				if tc.badDigest && status == http.StatusGone {
					return
				}
				if status == http.StatusOK && bytes.Contains(body, []byte(`"state":"ready"`)) {
					ready = true
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			if !ready || tc.badDigest {
				t.Fatalf("unexpected publication: ready=%v status=%d body=%s", ready, status, body)
			}
			if up.Method == "put" {
				replay := bytes.Repeat([]byte("z"), len(tc.data))
				status, _, body = signedTestRequest(t, http.MethodPut, up.URL, replay)
				requireLiveStatus(t, status, http.StatusOK, body)
			}
			status, body = liveJSON(t, http.MethodGet, baseURL+"/nodes/"+up.NodeID+"/download", cookie, "", nil)
			requireLiveStatus(t, status, http.StatusOK, body)
			var download struct{ URL string }
			if err := json.Unmarshal(body, &download); err != nil {
				t.Fatal(err)
			}
			status, _, body = signedTestRequest(t, http.MethodGet, download.URL, nil)
			requireLiveStatus(t, status, http.StatusOK, nil)
			if sha256.Sum256(body) != digest {
				t.Fatal("publication was changed by replay or transfer corruption")
			}
			if tc.name == "lease-race" {
				var count int
				var used, readyBytes int64
				if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE resource_id=$1 AND action='node.upload'`, up.NodeID).Scan(&count); err != nil || count != 1 {
					t.Fatalf("duplicate upload publication: %d %v", count, err)
				}
				if err := pool.QueryRow(context.Background(), `SELECT s.used_bytes,COALESCE((SELECT sum(size_bytes) FROM assets WHERE space_id=s.id AND status='ready'),0) FROM spaces s WHERE id=$1`, uploadSpace).Scan(&used, &readyBytes); err != nil || used != readyBytes {
					t.Fatalf("publication quota drift: %d %d %v", used, readyBytes, err)
				}
			}
			if tc.name == "album-archive" {
				exerciseAlbumArchive(t, baseURL, up.NodeID, tc.data, pool)
			}
			if tc.name == "small" {
				ctx := context.Background()
				var assetID uuid.UUID
				var oldKey string
				if err := pool.QueryRow(ctx, `UPDATE assets SET sha256=NULL WHERE id=(SELECT asset_id FROM nodes WHERE id=$1) RETURNING id,object_key`, up.NodeID).Scan(&assetID, &oldKey); err != nil {
					t.Fatal(err)
				}
				jobID := uuid.New()
				if _, err := pool.Exec(ctx, `INSERT INTO jobs(id,kind,payload) VALUES($1,'seal_legacy_asset',jsonb_build_object('assetId',$2::text))`, jobID, assetID.String()); err != nil {
					t.Fatal(err)
				}
				defer pool.Exec(ctx, `DELETE FROM jobs WHERE id=$1`, jobID)
				var sealed bool
				for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
					var key string
					var hash *string
					if err := pool.QueryRow(ctx, `SELECT object_key,sha256 FROM assets WHERE id=$1`, assetID).Scan(&key, &hash); err != nil {
						t.Fatal(err)
					}
					if hash != nil {
						if *hash != checksum || key == oldKey {
							t.Fatal("legacy sealing did not preserve digest and change key")
						}
						sealed = true
						break
					}
					time.Sleep(250 * time.Millisecond)
				}
				if !sealed {
					t.Fatal("legacy integrity job did not complete")
				}
				var cleanup bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM object_cleanup WHERE object_key=$1 AND delete_after>now())`, oldKey).Scan(&cleanup); err != nil || !cleanup {
					t.Fatalf("legacy source not scheduled for safe cleanup: %v", err)
				}
				status, body = liveJSON(t, http.MethodGet, baseURL+"/nodes/"+up.NodeID+"/download", cookie, "", nil)
				requireLiveStatus(t, status, 200, body)
				if err := json.Unmarshal(body, &download); err != nil {
					t.Fatal(err)
				}
				status, _, body = signedTestRequest(t, http.MethodGet, download.URL, nil)
				requireLiveStatus(t, status, 200, nil)
				if sha256.Sum256(body) != digest {
					t.Fatal("legacy sealed download is corrupted")
				}
			}
		})
	}
}

func exerciseExpiredMultipart(t *testing.T, baseURL, cookie string, spaceID uuid.UUID, pool *pgxpool.Pool) {
	ctx := context.Background()
	var beforeUsed, beforeReserved int64
	if err := pool.QueryRow(ctx, `SELECT used_bytes,reserved_bytes FROM spaces WHERE id=$1`, spaceID).Scan(&beforeUsed, &beforeReserved); err != nil {
		t.Fatal(err)
	}
	status, body := liveJSON(t, http.MethodPost, baseURL+"/uploads", cookie, "", map[string]any{"spaceId": spaceID, "name": "expired-" + uuid.NewString(), "sizeBytes": 33 << 20, "mimeType": "application/octet-stream"})
	requireLiveStatus(t, status, 201, body)
	var up struct {
		ID, NodeID, Method string
		PartSize           int
	}
	if err := json.Unmarshal(body, &up); err != nil || up.Method != "multipart" {
		t.Fatalf("expected multipart: %s", body)
	}
	defer liveJSON(t, http.MethodDelete, baseURL+"/uploads/"+up.ID, cookie, "", nil)
	status, body = liveJSON(t, http.MethodPost, baseURL+"/uploads/"+up.ID+"/parts", cookie, "", map[string]any{"partNumbers": []int{1}})
	requireLiveStatus(t, status, 200, body)
	var signed struct{ Items []struct{ URL string } }
	if err := json.Unmarshal(body, &signed); err != nil || len(signed.Items) != 1 {
		t.Fatalf("missing signed part: %s", body)
	}
	part := make([]byte, up.PartSize)
	status, _, body = signedTestRequest(t, http.MethodPut, signed.Items[0].URL, part)
	requireLiveStatus(t, status, 200, body)
	if _, err := pool.Exec(ctx, `UPDATE upload_sessions SET expires_at=now()-interval '1 minute' WHERE id=$1`, up.ID); err != nil {
		t.Fatal(err)
	}
	jobID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO jobs(id,kind,payload) VALUES($1,'cleanup_upload',jsonb_build_object('uploadId',$2::text))`, jobID, up.ID); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `DELETE FROM jobs WHERE id=$1`, jobID)
	removed := false
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM upload_sessions WHERE id=$1)`, up.ID).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			removed = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !removed {
		t.Fatal("expired upload not removed")
	}
	var used, reserved int64
	if err := pool.QueryRow(ctx, `SELECT used_bytes,reserved_bytes FROM spaces WHERE id=$1`, spaceID).Scan(&used, &reserved); err != nil {
		t.Fatal(err)
	}
	if used != beforeUsed || reserved != beforeReserved {
		t.Fatalf("expired upload quota drift: used %d/%d reserved %d/%d", used, beforeUsed, reserved, beforeReserved)
	}
	status, _, body = signedTestRequest(t, http.MethodPut, signed.Items[0].URL, part)
	if status < 400 || !bytes.Contains(body, []byte("NoSuchUpload")) {
		t.Fatalf("expired multipart remains usable: %d %s", status, body)
	}
	// Reproduce the maintenance crash window: state was claimed as expired,
	// but quota has not yet been released when the user presses cancel.
	status, body = liveJSON(t, http.MethodPost, baseURL+"/uploads", cookie, "", map[string]any{"spaceId": spaceID, "name": "cancel-expired-" + uuid.NewString(), "sizeBytes": 42, "mimeType": "application/octet-stream"})
	requireLiveStatus(t, status, 201, body)
	if err := json.Unmarshal(body, &up); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE upload_sessions SET state='expired',expires_at=now()-interval '1 minute' WHERE id=$1`, up.ID); err != nil {
		t.Fatal(err)
	}
	status, body = liveJSON(t, http.MethodDelete, baseURL+"/uploads/"+up.ID, cookie, "", nil)
	requireLiveStatus(t, status, 204, body)
	if err := pool.QueryRow(ctx, `SELECT used_bytes,reserved_bytes FROM spaces WHERE id=$1`, spaceID).Scan(&used, &reserved); err != nil {
		t.Fatal(err)
	}
	if used != beforeUsed || reserved != beforeReserved {
		t.Fatalf("cancelling an expired upload leaked quota: %d %d", used, reserved)
	}
}

func exerciseAlbumArchive(t *testing.T, baseURL, nodeID string, data []byte, pool *pgxpool.Pool) {
	ctx := context.Background()
	var memberID, ownerID, spaceID uuid.UUID
	if err := pool.QueryRow(ctx, `UPDATE nodes SET section='photos' WHERE id=$1 RETURNING created_by,space_id`, nodeID).Scan(&memberID, &spaceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT user_id FROM household_members WHERE role='owner' AND household_id=(SELECT household_id FROM spaces WHERE id=$1)`, spaceID).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	albumID, shareID, aclID := uuid.New(), uuid.New(), uuid.New()
	defer pool.Exec(ctx, `DELETE FROM albums WHERE id=$1`, albumID)
	defer pool.Exec(ctx, `DELETE FROM public_shares WHERE id=$1`, shareID)
	defer pool.Exec(ctx, `DELETE FROM acl_entries WHERE id=$1`, aclID)
	if _, err := pool.Exec(ctx, `INSERT INTO albums(id,space_id,name,created_by) VALUES($1,$2,'archive-fixture',$3)`, albumID, spaceID, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO album_items(album_id,node_id,created_by) VALUES($1,$2,$3)`, albumID, nodeID, memberID); err != nil {
		t.Fatal(err)
	}
	token, hash, _ := panAuth.NewToken(32)
	access, accessHash, _ := panAuth.NewToken(32)
	if _, err := pool.Exec(ctx, `INSERT INTO public_shares(id,resource_type,resource_id,token_hash,created_by) VALUES($1,'album',$2,$3,$4)`, shareID, albumID, hash, ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO share_access_tokens(id,share_id,token_hash,expires_at) VALUES($1,$2,$3,now()+interval '1 hour')`, uuid.New(), shareID, accessHash); err != nil {
		t.Fatal(err)
	}
	check := func(count int) {
		status, body := liveJSON(t, http.MethodPost, baseURL+"/public/shares/"+token+"/archive", "", access, nil)
		requireLiveStatus(t, status, 200, body)
		archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		if len(archive.File) != count {
			t.Fatalf("ZIP has %d entries, expected %d", len(archive.File), count)
		}
		if count > 0 {
			stream, err := archive.File[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			contents, err := io.ReadAll(stream)
			stream.Close()
			if err != nil || !bytes.Equal(contents, data) {
				t.Fatal("ZIP content differs from uploaded original")
			}
		}
	}
	check(1)
	if _, err := pool.Exec(ctx, `INSERT INTO acl_entries(id,resource_type,resource_id,principal_user_id,permission,created_by) VALUES($1,'node',$2,$3,'viewer',$4)`, aclID, nodeID, memberID, ownerID); err != nil {
		t.Fatal(err)
	}
	check(0)
}

func signedTestRequest(t *testing.T, method, raw string, payload []byte) (int, http.Header, []byte) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	originalHost := u.Host
	if endpoint := os.Getenv("PAN_S3_CONNECT_URL"); endpoint != "" {
		target, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		u.Scheme, u.Host = target.Scheme, target.Host
	}
	r, err := http.NewRequest(method, u.String(), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	r.Host = originalHost
	r.Header.Set("Content-Type", "application/octet-stream")
	response, err := (&http.Client{Timeout: time.Minute}).Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 40<<20))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, body
}
