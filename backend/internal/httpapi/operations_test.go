package httpapi

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOperationsReportsRejectInvalidOrUnboundedContent(t *testing.T) {
	dir := t.TempDir()
	for _, content := range []string{`{`, `{"status":"success"} {}`, strings.Repeat(" ", 65536) + `{}`} {
		if err := os.WriteFile(filepath.Join(dir, "backup-status.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if got := readOpsReport(dir, "backup-status.json"); got != nil {
			t.Fatalf("accepted malformed or oversized report: %v", got)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "backup-status.json"), []byte(`{"status":"success"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if readOpsReport(dir, "backup-status.json") == nil {
		t.Fatal("rejected valid report")
	}
	if readOpsReport(dir, "../backup-status.json") != nil {
		t.Fatal("accepted non-allowlisted path")
	}
}

func TestOperationFailureNeverRevealsPrivateDetails(t *testing.T) {
	for _, message := range []string{"private-secret.jpg: integrity mismatch", "private-secret.jpg: permission denied", "private-secret.jpg: timeout", "private-secret.jpg: NoSuchKey", "private-secret.jpg: no space left", "private-secret.jpg: dial tcp", "private-secret.jpg: unexpected failure"} {
		result := operationFailure("finalize_upload", message)
		if result == "" || strings.Contains(result, "private-secret") {
			t.Fatalf("unsafe failure category: %q", result)
		}
	}
}
