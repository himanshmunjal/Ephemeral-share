package config

import (
	"strings"
	"testing"
)

func prodReady() *Config {
	return &Config{
		S3AccessKey:      "AKIAREAL",
		S3SecretKey:      "realsecret",
		PostgresDSN:      "",
		AllowedOrigins:   []string{"https://app.example.com"},
		S3PublicEndpoint: "https://storage.example.com",
	}
}

func TestProductionSafetyAcceptsASaneConfig(t *testing.T) {
	if err := prodReady().CheckProductionSafety(); err != nil {
		t.Fatalf("a valid production config was rejected: %v", err)
	}
}

func TestProductionSafetyRejectsDevDefaults(t *testing.T) {
	cases := map[string]func(*Config){
		"minio creds":      func(c *Config) { c.S3AccessKey = "minioadmin" },
		"dev postgres":     func(c *Config) { c.PostgresDSN = "postgres://postgres:postgres@db/x" },
		"localhost origin": func(c *Config) { c.AllowedOrigins = []string{"http://localhost:5173"} },
		"http origin":      func(c *Config) { c.AllowedOrigins = []string{"http://app.example.com"} },
		"local s3 public":  func(c *Config) { c.S3PublicEndpoint = "http://localhost:9000" },
		"weak admin token": func(c *Config) { c.AdminToken = "short" },
	}
	for name, mutate := range cases {
		c := prodReady()
		mutate(c)
		if err := c.CheckProductionSafety(); err == nil {
			t.Errorf("%s: expected the config to be rejected", name)
		}
	}
}

func TestPostgresIsOptionalInProduction(t *testing.T) {
	c := prodReady()
	c.PostgresDSN = ""
	if err := c.CheckProductionSafety(); err != nil {
		t.Errorf("an empty POSTGRES_DSN (Redis-only mode) must be allowed: %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("SESSION_TTL_SECONDS", "")
	t.Setenv("CODE_LENGTH", "")
	t.Setenv("MAX_PARTICIPANTS", "")
	c := Load()
	if c.SessionTTL.Seconds() != 3600 || c.CodeLength != 8 || c.MaxParticipants != 10 {
		t.Errorf("unexpected defaults: ttl=%v code=%d participants=%d", c.SessionTTL, c.CodeLength, c.MaxParticipants)
	}
	if !strings.HasPrefix(c.AllowedOrigins[0], "http") {
		t.Error("allowed origins not parsed")
	}
}

func TestProxyAndTLSSettingsAreParsed(t *testing.T) {
	t.Setenv("REDIS_TLS", "true")
	t.Setenv("TRUSTED_PROXIES", "private, 203.0.113.0/24")
	c := Load()
	if !c.RedisTLS {
		t.Error("REDIS_TLS=true not honoured")
	}
	if len(c.TrustedProxies) != 2 || c.TrustedProxies[0] != "private" {
		t.Errorf("TRUSTED_PROXIES parsed as %v", c.TrustedProxies)
	}
}

func TestSignContentLengthDefaultsOn(t *testing.T) {
	t.Setenv("SIGN_CONTENT_LENGTH", "")
	if !Load().SignContentLength {
		t.Error("must default to on")
	}
	t.Setenv("SIGN_CONTENT_LENGTH", "false")
	if Load().SignContentLength {
		t.Error("SIGN_CONTENT_LENGTH=false not honoured")
	}
}
