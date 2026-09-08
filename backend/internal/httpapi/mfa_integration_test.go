package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func fixtureTOTP(secret string) string {
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], uint64(time.Now().Unix()/30))
	h := hmac.New(sha1.New, key)
	_, _ = h.Write(data[:])
	sum := h.Sum(nil)
	offset := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[offset:offset+4])&0x7fffffff)%1000000)
}

func exerciseMFA(t *testing.T, baseURL, cookie, username string, userID uuid.UUID, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	status, body := liveJSON(t, http.MethodPost, baseURL+"/me/totp/setup", cookie, "", map[string]string{"password": "Stage4Disposable123!"})
	requireLiveStatus(t, status, 200, body)
	var setup struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(body, &setup); err != nil || setup.Secret == "" {
		t.Fatal("missing setup secret")
	}
	defer pool.Exec(ctx, `UPDATE users SET totp_secret=NULL,totp_pending_secret=NULL,totp_pending_expires=NULL,totp_last_step=-1 WHERE id=$1`, userID)
	defer pool.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id=$1`, userID)
	code := fixtureTOTP(setup.Secret)
	status, body = liveJSON(t, http.MethodPost, baseURL+"/me/totp/confirm", cookie, "", map[string]string{"code": code})
	requireLiveStatus(t, status, 200, body)
	var result struct {
		Codes []string `json:"recoveryCodes"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Codes) != 8 {
		t.Fatal("expected eight recovery codes")
	}
	var encrypted []byte
	if err := pool.QueryRow(ctx, `SELECT totp_secret FROM users WHERE id=$1`, userID).Scan(&encrypted); err != nil || bytes.Contains(encrypted, []byte(setup.Secret)) {
		t.Fatal("TOTP secret is not encrypted")
	}
	for _, value := range []string{"", code} {
		status, body = liveJSON(t, http.MethodPost, baseURL+"/auth/login", "", "", map[string]string{"username": username, "password": "Stage4Disposable123!", "code": value})
		requireLiveStatus(t, status, 401, body)
	}
	payload, _ := json.Marshal(map[string]string{"username": username, "password": "Stage4Disposable123!", "code": result.Codes[0]})
	request, _ := http.NewRequest(http.MethodPost, baseURL+"/auth/login", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0) Chrome/130.0 Edg/130.0")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("recovery login: %d", response.StatusCode)
	}
	var recoveredCookie string
	for _, c := range response.Cookies() {
		if c.Name == "pan_session" {
			recoveredCookie = c.Name + "=" + c.Value
		}
	}
	if recoveredCookie == "" {
		t.Fatal("recovery did not create session")
	}
	status, body = liveJSON(t, http.MethodGet, baseURL+"/me/sessions", recoveredCookie, "", nil)
	requireLiveStatus(t, status, 200, body)
	if !bytes.Contains(body, []byte("Windows · Edge")) || !bytes.Contains(body, []byte("sourceIp")) {
		t.Fatal("session device/source metadata missing")
	}
	status, body = liveJSON(t, http.MethodPost, baseURL+"/auth/login", "", "", map[string]string{"username": username, "password": "Stage4Disposable123!", "code": result.Codes[0]})
	requireLiveStatus(t, status, 401, body)
	status, body = liveJSON(t, http.MethodGet, baseURL+"/me/security", cookie, "", nil)
	requireLiveStatus(t, status, 200, body)
	if !bytes.Contains(body, []byte("mfa.recovery_used")) || !bytes.Contains(body, []byte("login.mfa_denied")) {
		t.Fatal("security history omitted recovery/failures")
	}
	status, body = liveJSON(t, http.MethodPost, baseURL+"/me/totp/disable", cookie, "", map[string]string{"password": "Stage4Disposable123!", "code": result.Codes[1]})
	requireLiveStatus(t, status, 200, body)
	status, body = liveJSON(t, http.MethodGet, baseURL+"/me", recoveredCookie, "", nil)
	requireLiveStatus(t, status, 401, body)
}
