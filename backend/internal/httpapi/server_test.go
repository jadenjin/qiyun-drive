package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pan/backend/internal/config"
)

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
	for _, mime := range []string{"", "application/octet-stream", "application/zip", "application/pdfx"} {
		if isPreviewableMIME(mime) {
			t.Errorf("expected %q to be rejected", mime)
		}
	}
}
