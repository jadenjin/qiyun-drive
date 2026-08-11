package main

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCleanupEligibility(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name           string
		state          string
		expires        time.Time
		created        time.Time
		active, failed bool
	}{
		{name: "active expired", state: "uploading", expires: now.Add(-time.Second), created: now, active: true},
		{name: "active current", state: "uploading", expires: now.Add(time.Second), created: now},
		{name: "claimed expired", state: "expired", expires: now, created: now, active: true},
		{name: "recent failure", state: "failed", expires: now, created: now.Add(-30 * time.Minute)},
		{name: "old failure", state: "failed", expires: now, created: now.Add(-2 * time.Hour), failed: true},
		{name: "ready", state: "ready", expires: now.Add(-time.Hour), created: now.Add(-time.Hour)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			active, failed := cleanupEligibility(test.state, test.expires, test.created, now)
			if active != test.active || failed != test.failed {
				t.Fatalf("got active=%v failed=%v", active, failed)
			}
		})
	}
}

func TestTruncateErrorPreservesUTF8(t *testing.T) {
	message := strings.Repeat("上传失败", 1000)
	truncated := truncateError(errors.New(message))
	if !utf8.ValidString(truncated) {
		t.Fatal("truncated error is not valid UTF-8")
	}
	if len([]rune(truncated)) != 2000 {
		t.Fatalf("unexpected rune count: %d", len([]rune(truncated)))
	}
}

func TestJobTimeoutPrecedesStaleRecoveryWindow(t *testing.T) {
	if jobTimeout >= 15*time.Minute {
		t.Fatal("stale recovery must not race a normally running job")
	}
}
