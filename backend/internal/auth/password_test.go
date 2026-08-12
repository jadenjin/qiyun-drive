package auth

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("a-safe-family-password")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "a-safe-family-password") {
		t.Fatal("expected password to verify")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("wrong password must not verify")
	}
}

func TestTokenHashIsStableAndDoesNotExposeToken(t *testing.T) {
	plain, hash, err := NewToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if hash != TokenHash(plain) {
		t.Fatal("token hash should be stable")
	}
	if plain == hash {
		t.Fatal("stored hash must not equal the plain token")
	}
}

func TestValidTokenRequiresExactURLSafeEntropy(t *testing.T) {
	plain, _, err := NewToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidToken(plain, 32) {
		t.Fatal("newly generated token should validate")
	}
	for _, invalid := range []string{"", plain + "x", plain[:len(plain)-1], "not+a/url_safe/token"} {
		if ValidToken(invalid, 32) {
			t.Fatalf("invalid token accepted: %q", invalid)
		}
	}
}
