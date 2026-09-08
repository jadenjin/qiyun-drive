package auth

import (
	"encoding/base32"
	"strings"
	"testing"
	"time"
)

func TestTOTPReferenceVectors(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	for _, tc := range []struct {
		seconds int64
		code    string
	}{{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"}, {1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"}} {
		got, err := totpCode(secret, tc.seconds/30, 8)
		if err != nil || got != tc.code {
			t.Fatalf("RFC 6238 at %d: %s %v", tc.seconds, got, err)
		}
	}
	code, _ := totpCode(secret, 1234567890/30, 6)
	step, ok := MatchTOTP(secret, code, time.Unix(1234567890, 0), -1)
	if !ok {
		t.Fatal("valid code rejected")
	}
	if _, ok := MatchTOTP(secret, code, time.Unix(1234567890, 0), step); ok {
		t.Fatal("replayed step accepted")
	}
	if _, ok := MatchTOTP(secret, code, time.Unix(1234567890+90, 0), -1); ok {
		t.Fatal("expired code accepted")
	}
}

func TestTOTPSecretsBoundToUser(t *testing.T) {
	key := strings.Repeat("ab", 32)
	encrypted, err := EncryptTOTP(key, "user-a", "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := DecryptTOTP(key, "user-a", encrypted)
	if err != nil || plain != "fixture-secret" {
		t.Fatal("secret failed round trip")
	}
	if _, err := DecryptTOTP(key, "user-b", encrypted); err == nil {
		t.Fatal("cross-user secret substitution accepted")
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err := DecryptTOTP(key, "user-a", encrypted); err == nil {
		t.Fatal("tampered secret accepted")
	}
}
