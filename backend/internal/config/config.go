package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr             string
	DatabaseURL      string
	PublicBaseURL    string
	AllowedOrigin    string
	CookieSecure     bool
	S3Endpoint       string
	S3PublicEndpoint string
	S3Region         string
	S3AccessKey      string
	S3SecretKey      string
	S3Bucket         string
	PresignTTL       time.Duration
	UploadTTL        time.Duration
	TrashRetention   time.Duration
}

func Load() Config {
	return Config{
		Addr:             env("ADDR", ":8080"),
		DatabaseURL:      env("DATABASE_URL", "postgres://pan:pan@localhost:5432/pan?sslmode=disable"),
		PublicBaseURL:    strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:3000"), "/"),
		AllowedOrigin:    strings.TrimRight(env("ALLOWED_ORIGIN", "http://localhost:3000"), "/"),
		CookieSecure:     envBool("COOKIE_SECURE", false),
		S3Endpoint:       strings.TrimRight(env("S3_ENDPOINT", "http://localhost:9000"), "/"),
		S3PublicEndpoint: strings.TrimRight(env("S3_PUBLIC_ENDPOINT", "http://localhost:9000"), "/"),
		S3Region:         env("S3_REGION", "us-east-1"),
		S3AccessKey:      env("S3_ACCESS_KEY", "panminio"),
		S3SecretKey:      env("S3_SECRET_KEY", "change-me-now"),
		S3Bucket:         env("S3_BUCKET", "pan-objects"),
		PresignTTL:       envDuration("PRESIGN_TTL", 15*time.Minute),
		UploadTTL:        envDuration("UPLOAD_TTL", 24*time.Hour),
		TrashRetention:   envDuration("TRASH_RETENTION", 30*24*time.Hour),
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
