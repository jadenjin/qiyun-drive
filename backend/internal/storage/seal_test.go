package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"pan/backend/internal/config"
)

func TestUploadSignaturesBindContentLength(t *testing.T) {
	s := New(config.Config{S3Endpoint: "http://localhost:9000", S3Bucket: "pan-objects", S3Region: "us-east-1", S3AccessKey: "test-key", S3SecretKey: "test-secret-not-a-real-credential", PresignTTL: time.Minute})
	put, err := s.PresignPut(context.Background(), "staging/test", "text/plain", 123)
	if err != nil {
		t.Fatal(err)
	}
	part, err := s.PresignPart(context.Background(), "staging/test", "multipart", 1, 123)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{put, part} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(u.Query().Get("X-Amz-SignedHeaders"), "content-length") {
			t.Fatal("upload size is not signed")
		}
	}
}

func TestSealPublishesStableCopyAndHashesBytes(t *testing.T) {
	const original = "stable original bytes"
	staging := original
	final := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "HEAD":
			w.Header().Set("ETag", `"source-v1"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(staging)))
		case r.Method == "PUT":
			if r.Header.Get("X-Amz-Copy-Source-If-Match") != `"source-v1"` {
				t.Error("copy is not conditional")
			}
			if !strings.HasSuffix(r.URL.Path, "/original/test") {
				t.Error("wrong destination")
			}
			final = staging
			staging = "replayed upload bytes"
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<CopyObjectResult><ETag>"copy-v1"</ETag></CopyObjectResult>`)
		case r.Method == "GET":
			fmt.Fprint(w, final)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	s := New(config.Config{S3Endpoint: server.URL, S3Bucket: "pan-objects", S3Region: "us-east-1", S3AccessKey: "test", S3SecretKey: "test", PresignTTL: time.Minute})
	digest, err := s.Seal(context.Background(), "staging/test", "original/test", int64(len(original)))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256([]byte(original))
	if digest != hex.EncodeToString(want[:]) || final != original {
		t.Fatal("replayed staging bytes changed published object")
	}
	if _, err := s.Seal(context.Background(), "same", "same", 1); err == nil {
		t.Fatal("same-key publication allowed")
	}
	if _, err := s.Seal(context.Background(), "staging/test", "original/test", 999); err == nil {
		t.Fatal("false size accepted")
	}
}
