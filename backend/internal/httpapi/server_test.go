package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"pan/backend/internal/config"
)

func TestPasswordWorkRejectsConcurrentExcessAndReleasesSlots(t *testing.T) {
	s := &Server{passwordSlots: make(chan struct{}, 2)}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	handler := s.limitPasswordWork(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
		}()
	}
	<-entered
	<-entered
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "" {
		t.Errorf("excess password work was not rejected: %d", recorder.Code)
	}
	close(release)
	workers.Wait()
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("password slot was leaked: %d", recorder.Code)
	}
}

func TestUploadPathResourceBounds(t *testing.T) {
	for _, value := range []string{strings.Repeat("a/", 64) + "file", strings.Repeat("a", 4097)} {
		if _, err := cleanRelativePath(value); err == nil {
			t.Fatal("unbounded upload path accepted")
		}
	}
	if _, err := cleanRelativePath("家庭/旅行/photo.jpg"); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeJSONRejectsTrailingPayload(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", strings.NewReader(`{"name":"ok"} {"extra":true}`))
	recorder := httptest.NewRecorder()
	var input struct {
		Name string `json:"name"`
	}
	if decodeJSON(recorder, req, &input) {
		t.Fatal("multiple JSON values must be rejected")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
}

func TestAttemptLimiterCountsFailuresAndClearsOnSuccess(t *testing.T) {
	limiter := newAttemptLimiter()
	now := time.Now()
	for index := 0; index < limiter.maxFailures; index++ {
		if allowed, _ := limiter.allowed("client:login", now); !allowed {
			t.Fatalf("attempt %d should still be allowed", index+1)
		}
		limiter.recordFailure("client:login", now)
	}
	if allowed, retry := limiter.allowed("client:login", now); allowed || retry <= 0 {
		t.Fatal("client should be blocked after the failure limit")
	}
	limiter.clear("client:login")
	if allowed, _ := limiter.allowed("client:login", now); !allowed {
		t.Fatal("successful authentication should clear failures")
	}
}

func TestAttemptLimiterBoundsClientMap(t *testing.T) {
	limiter := newAttemptLimiter()
	limiter.maxEntries = 8
	now := time.Now()
	for index := 0; index < 100; index++ {
		limiter.recordFailure(string(rune(index+1)), now)
	}
	if len(limiter.windows) > limiter.maxEntries {
		t.Fatalf("limiter map grew past its bound: %d", len(limiter.windows))
	}
}

func TestClientIPOnlyTrustsForwardedHeaderWhenConfigured(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "203.0.113.8")
	server := &Server{}
	if got := server.clientIP(req); got != "127.0.0.1" {
		t.Fatalf("untrusted forwarded address used: %s", got)
	}
	server.cfg = config.Config{TrustProxy: true}
	if got := server.clientIP(req); got != "203.0.113.8" {
		t.Fatalf("trusted forwarded address ignored: %s", got)
	}
	req.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.8")
	if got := server.clientIP(req); got != "203.0.113.8" {
		t.Fatalf("client-controlled forwarded prefix was trusted: %s", got)
	}
}

func TestSecurityHeadersProtectAPIResponses(t *testing.T) {
	server := &Server{}
	handler := server.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/me", nil))
	for header, expected := range map[string]string{
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := recorder.Header().Get(header); got != expected {
			t.Fatalf("%s = %q, want %q", header, got, expected)
		}
	}
}

func TestSafeLogPathRedactsShareCapabilities(t *testing.T) {
	secret := "this-token-must-never-reach-logs"
	for _, path := range []string{
		"/api/v1/public/shares/" + secret,
		"/api/v1/public/shares/" + secret + "/unlock",
		"/api/v1/public/shares/" + secret + "/archive",
	} {
		got := safeLogPath(path)
		if strings.Contains(got, secret) || !strings.Contains(got, "[redacted]") {
			t.Fatalf("share path was not redacted: %q", got)
		}
	}
	if got := safeLogPath("/api/v1/nodes/123"); got != "/api/v1/nodes/123" {
		t.Fatalf("ordinary API path changed: %q", got)
	}
}

func TestPasswordLengthHasUpperBound(t *testing.T) {
	if err := validatePassword(strings.Repeat("a", 129)); err == nil {
		t.Fatal("oversized password should be rejected before hashing")
	}
	if err := validatePassword("Admin123@jit"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
}

func TestPreviewableMIMEAllowlist(t *testing.T) {
	for _, mime := range []string{"image/png", "VIDEO/MP4", "audio/mpeg", "text/plain; charset=utf-8", "application/pdf"} {
		if !isPreviewableMIME(mime) {
			t.Errorf("expected %q to be previewable", mime)
		}
	}
	for _, mime := range []string{"", "application/octet-stream", "application/zip", "application/pdfx", "text/html", "image/svg+xml", "video/x-ms-asf"} {
		if isPreviewableMIME(mime) {
			t.Errorf("expected %q to be rejected", mime)
		}
	}
}

func TestSafeArchivePathCannotEscapeArchiveRoot(t *testing.T) {
	for input, expected := range map[string]string{
		"family/photo.jpg":     "family/photo.jpg",
		"../../outside.txt":    "outside.txt",
		"\\absolute\\file.txt": "absolute/file.txt",
		"/rooted.txt":          "rooted.txt",
	} {
		if got := safeArchivePath(input); got != expected {
			t.Errorf("safeArchivePath(%q) = %q, want %q", input, got, expected)
		}
	}
}

func TestDownloadFilenameUsesRFC5987Encoding(t *testing.T) {
	if got := pathEscape("家庭;照片.zip"); got != "%E5%AE%B6%E5%BA%AD%3B%E7%85%A7%E7%89%87.zip" {
		t.Fatalf("unexpected encoded filename: %q", got)
	}
}

func TestAdministrativeRoleBoundaries(t *testing.T) {
	if !canInviteRole("owner", "admin") || !canInviteRole("admin", "member") {
		t.Fatal("expected legitimate invitations to be allowed")
	}
	if canInviteRole("admin", "admin") || canInviteRole("member", "member") {
		t.Fatal("administrator creation must remain owner-only")
	}
	if !canResetMemberRole("owner", "owner") || !canResetMemberRole("admin", "member") {
		t.Fatal("expected legitimate resets to be allowed")
	}
	if canResetMemberRole("admin", "admin") || canResetMemberRole("admin", "owner") {
		t.Fatal("administrators must not reset peer or owner credentials")
	}
}

func TestUsernameValidation(t *testing.T) {
	for _, username := range []string{"jaden", "family.member", "user_01", "a-b"} {
		if !validUsername(username) {
			t.Errorf("valid username rejected: %q", username)
		}
	}
	for _, username := range []string{"", "ab", "with space", "管理员", "-leading", strings.Repeat("a", 65)} {
		if validUsername(username) {
			t.Errorf("invalid username accepted: %q", username)
		}
	}
}
