package config

import (
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Addr: ":8080", DatabaseURL: "postgres://pan:secret@postgres:5432/pan?sslmode=disable",
		PublicBaseURL: "https://cloud.example.com", AllowedOrigin: "https://cloud.example.com",
		CookieSecure: true, S3Endpoint: "http://minio:9000", S3PublicEndpoint: "https://objects.example.com",
		S3AccessKey: "panminio", S3SecretKey: "a-long-storage-secret", S3Bucket: "pan-objects",
		PresignTTL: 15 * time.Minute, UploadTTL: 24 * time.Hour, TrashRetention: 30 * 24 * time.Hour,
	}
}

func TestValidateAcceptsProductionConfiguration(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsSecurityMisconfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"insecure HTTPS cookie", func(c *Config) { c.CookieSecure = false }},
		{"secure cookie over HTTP", func(c *Config) {
			c.PublicBaseURL = "http://localhost:3000"
			c.AllowedOrigin = "http://localhost:3000"
		}},
		{"public HTTP deployment", func(c *Config) {
			c.PublicBaseURL = "http://cloud.example.com"
			c.AllowedOrigin = "http://cloud.example.com"
			c.CookieSecure = false
		}},
		{"public URL path", func(c *Config) {
			c.PublicBaseURL = "https://cloud.example.com/drive"
		}},
		{"mismatched CORS origin", func(c *Config) { c.AllowedOrigin = "https://evil.example" }},
		{"placeholder storage secret", func(c *Config) { c.S3SecretKey = "change-me-now" }},
		{"oversized presign TTL", func(c *Config) { c.PresignTTL = 2 * time.Hour }},
		{"short trash retention", func(c *Config) { c.TrashRetention = time.Hour }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected configuration validation error")
			}
		})
	}
}

func TestValidateAcceptsLocalHTTPConfiguration(t *testing.T) {
	cfg := validConfig()
	cfg.PublicBaseURL = "http://127.0.0.1:3000"
	cfg.AllowedOrigin = "http://127.0.0.1:3000"
	cfg.CookieSecure = false
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentParsersRejectInvalidValues(t *testing.T) {
	t.Setenv("COOKIE_SECURE", "sometimes")
	if _, err := envBool("COOKIE_SECURE", false); err == nil {
		t.Fatal("expected invalid boolean to fail")
	}
	t.Setenv("UPLOAD_TTL", "tomorrow")
	if _, err := envDuration("UPLOAD_TTL", time.Hour); err == nil {
		t.Fatal("expected invalid duration to fail")
	}
}
