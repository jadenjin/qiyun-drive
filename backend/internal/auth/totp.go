package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6238 interoperable HMAC-SHA1, not a signature or password digest.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"
)

func NewTOTPSecret() (string, error) {
	key := make([]byte, 20)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(key), nil
}

func totpCode(secret string, step int64, digits int) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(key) < 20 || step < 0 {
		return "", fmt.Errorf("invalid TOTP secret or step")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, key) // #nosec G401 -- Required HMAC-SHA1 for RFC 6238 authenticator interoperability.
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := int(sum[len(sum)-1] & 15)
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1000000)
	if digits == 8 {
		mod = 100000000
	}
	return fmt.Sprintf("%0*d", digits, value%mod), nil
}

// MatchTOTP permits one neighboring 30-second step for clock skew and returns
// the consumed step so the database can reject reuse atomically.
func MatchTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	for _, offset := range []int64{0, -1, 1} {
		step := now.Unix()/30 + offset
		if step <= lastStep {
			continue
		}
		expected, err := totpCode(secret, step, 6)
		if err == nil && subtle.ConstantTimeCompare([]byte(code), []byte(expected)) == 1 {
			return step, true
		}
	}
	return 0, false
}

func secretCipher(keyHex string) (cipher.AEAD, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("MFA_ENCRYPTION_KEY must contain 32 random bytes encoded as hex")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func EncryptTOTP(keyHex, userID, secret string) ([]byte, error) {
	aead, err := secretCipher(keyHex)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, []byte(secret), []byte(userID)), nil
}

func DecryptTOTP(keyHex, userID string, encrypted []byte) (string, error) {
	aead, err := secretCipher(keyHex)
	if err != nil {
		return "", err
	}
	if len(encrypted) < aead.NonceSize() {
		return "", fmt.Errorf("invalid encrypted TOTP secret")
	}
	plain, err := aead.Open(nil, encrypted[:aead.NonceSize()], encrypted[aead.NonceSize():], []byte(userID))
	return string(plain), err
}
