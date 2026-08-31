package config

import (
	"fmt"
	"net"
	"net/url"
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
	TrustProxy       bool
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

func Load() (Config, error) {
	cookieSecure, err := envBool("COOKIE_SECURE", false)
	if err != nil {
		return Config{}, err
	}
	trustProxy, err := envBool("TRUST_PROXY", false)
	if err != nil {
		return Config{}, err
	}
	presignTTL, err := envDuration("PRESIGN_TTL", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	uploadTTL, err := envDuration("UPLOAD_TTL", 24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	trashRetention, err := envDuration("TRASH_RETENTION", 30*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Addr:             env("ADDR", ":8080"),
		DatabaseURL:      env("DATABASE_URL", "postgres://pan:pan@localhost:5432/pan?sslmode=disable"),
		PublicBaseURL:    strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:3000"), "/"),
		AllowedOrigin:    strings.TrimRight(env("ALLOWED_ORIGIN", "http://localhost:3000"), "/"),
		CookieSecure:     cookieSecure,
		TrustProxy:       trustProxy,
		S3Endpoint:       strings.TrimRight(env("S3_ENDPOINT", "http://localhost:9000"), "/"),
		S3PublicEndpoint: strings.TrimRight(env("S3_PUBLIC_ENDPOINT", "http://localhost:9000"), "/"),
		S3Region:         env("S3_REGION", "us-east-1"),
		S3AccessKey:      env("S3_ACCESS_KEY", "qiyun"),
		S3SecretKey:      env("S3_SECRET_KEY", "change-me-now"),
		S3Bucket:         env("S3_BUCKET", "pan-objects"),
		PresignTTL:       presignTTL,
		UploadTTL:        uploadTTL,
		TrashRetention:   trashRetention,
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	publicURL, err := validateHTTPURL("PUBLIC_BASE_URL", c.PublicBaseURL)
	if err != nil {
		return err
	}
	if publicURL.Path != "" || publicURL.RawQuery != "" || publicURL.Fragment != "" {
		return fmt.Errorf("PUBLIC_BASE_URL must contain only scheme and host")
	}
	allowedOrigin, err := validateHTTPURL("ALLOWED_ORIGIN", c.AllowedOrigin)
	if err != nil {
		return err
	}
	if allowedOrigin.Path != "" || allowedOrigin.RawQuery != "" || allowedOrigin.Fragment != "" {
		return fmt.Errorf("ALLOWED_ORIGIN must contain only scheme and host")
	}
	if publicURL.Scheme+"://"+publicURL.Host != c.AllowedOrigin {
		return fmt.Errorf("ALLOWED_ORIGIN must match the public application origin")
	}
	if publicURL.Scheme == "https" && !c.CookieSecure {
		return fmt.Errorf("COOKIE_SECURE must be true when PUBLIC_BASE_URL uses HTTPS")
	}
	if publicURL.Scheme == "http" && c.CookieSecure {
		return fmt.Errorf("COOKIE_SECURE must be false when PUBLIC_BASE_URL uses HTTP")
	}
	if publicURL.Scheme == "http" && !isPrivateOrLoopbackHost(publicURL.Hostname()) {
		return fmt.Errorf("PUBLIC_BASE_URL must use HTTPS outside localhost or a private network")
	}
	if _, err := validateHTTPURL("S3_ENDPOINT", c.S3Endpoint); err != nil {
		return err
	}
	if _, err := validateHTTPURL("S3_PUBLIC_ENDPOINT", c.S3PublicEndpoint); err != nil {
		return err
	}
	databaseURL, err := url.Parse(c.DatabaseURL)
	if err != nil || (databaseURL.Scheme != "postgres" && databaseURL.Scheme != "postgresql") || databaseURL.Host == "" {
		return fmt.Errorf("DATABASE_URL must be a valid PostgreSQL URL")
	}
	if len(c.S3AccessKey) < 3 {
		return fmt.Errorf("S3_ACCESS_KEY must contain at least 3 characters")
	}
	if len(c.S3SecretKey) < 8 || c.S3SecretKey == "change-me-now" {
		return fmt.Errorf("S3_SECRET_KEY must be replaced with a secret of at least 8 characters")
	}
	if c.PresignTTL < time.Minute || c.PresignTTL > time.Hour {
		return fmt.Errorf("PRESIGN_TTL must be between 1m and 1h")
	}
	if c.UploadTTL < c.PresignTTL || c.UploadTTL > 7*24*time.Hour {
		return fmt.Errorf("UPLOAD_TTL must be at least PRESIGN_TTL and no more than 168h")
	}
	if c.TrashRetention < 24*time.Hour {
		return fmt.Errorf("TRASH_RETENTION must be at least 24h")
	}
	return nil
}

func isPrivateOrLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func validateHTTPURL(name, value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("%s must be a valid HTTP(S) URL without credentials", name)
	}
	return parsed, nil
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration", key)
	}
	return parsed, nil
}
