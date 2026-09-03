package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 2
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	digest := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(digest)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory uint32
	var iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false
	}
	// Only accept the parameters emitted by HashPassword. Besides making hash
	// upgrades explicit, this prevents corrupted database values from turning a
	// login request into an attacker-controlled memory or CPU allocation.
	if memory != argonMemory || iterations != argonTime || threads != argonThreads {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != argonKeyLen {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, threads, argonKeyLen)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func NewToken(bytes int) (plain, hash string, err error) {
	value := make([]byte, bytes)
	if _, err = rand.Read(value); err != nil {
		return "", "", err
	}
	plain = base64.RawURLEncoding.EncodeToString(value)
	return plain, TokenHash(plain), nil
}

func TokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func ValidToken(token string, expectedBytes int) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	return err == nil && len(decoded) == expectedBytes
}
