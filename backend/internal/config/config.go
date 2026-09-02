package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all environment-driven settings for the service.
type Config struct {
	Port             string
	PostgresDSN      string
	RedisAddr        string
	RedisPassword    string
	S3Endpoint       string
	S3PublicEndpoint string
	S3Bucket         string
	S3Region         string
	S3AccessKey      string
	S3SecretKey      string
	SessionTTL       time.Duration // default 5 minutes, configurable for tests
	MaxUploadBytes   int64
	AllowedOrigins   []string
	RateLimitPerMin  int
	CodeLength       int
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

// Load reads configuration from environment variables with sane local-dev defaults.
func Load() *Config {
	ttlSeconds := getEnvInt("SESSION_TTL_SECONDS", 300)
	return &Config{
		Port:            getEnv("PORT", "8080"),
		PostgresDSN:     getEnv("POSTGRES_DSN", "postgres://postgres:postgres@postgres:5432/ephemeral?sslmode=disable"),
		RedisAddr:       getEnv("REDIS_ADDR", "redis:6379"),
		RedisPassword:   getEnv("REDIS_PASSWORD", ""),
		// Defaults to local MinIO. For real AWS S3, set S3_ENDPOINT=none —
		// this tells the storage client to skip the custom-endpoint override
		// entirely and let the AWS SDK resolve the regional endpoint itself.
		S3Endpoint:      getEnv("S3_ENDPOINT", "http://minio:9000"),
		// The browser uploads/downloads directly against this host via
		// pre-signed URLs, so it must be reachable from the user's machine —
		// NOT the docker-compose service name. Defaults to the host-mapped
		// MinIO port for local dev.
		S3PublicEndpoint: getEnv("S3_PUBLIC_ENDPOINT", "http://localhost:9000"),
		S3Bucket:        getEnv("S3_BUCKET", "ephemeral-share"),
		S3Region:        getEnv("S3_REGION", "us-east-1"),
		S3AccessKey:     getEnv("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey:     getEnv("S3_SECRET_KEY", "minioadmin"),
		SessionTTL:      time.Duration(ttlSeconds) * time.Second,
		MaxUploadBytes:  int64(getEnvInt("MAX_UPLOAD_MB", 200)) * 1024 * 1024,
		// Comma-separated so a real deployment can allow both the apex and
		// "www" host (or a staging + prod frontend) without code changes.
		AllowedOrigins:  splitOrigins(getEnv("ALLOWED_ORIGIN", "http://localhost:5173")),
		RateLimitPerMin: getEnvInt("RATE_LIMIT_PER_MIN", 60),
		CodeLength:      getEnvInt("CODE_LENGTH", 8),
	}
}

func splitOrigins(raw string) []string {
	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			origins = append(origins, p)
		}
	}
	return origins
}

// knownInsecureDefaults are dev-only credentials baked into this file's
// fallbacks purely for docker-compose/local-dev convenience. Shipping any of
// them to production silently (no ALLOWED_ORIGIN override, default MinIO
// creds, default Postgres password) is a real incident waiting to happen, so
// CheckProductionSafety refuses to boot with them once GIN_MODE=release.
func (c *Config) CheckProductionSafety() error {
	var problems []string

	if c.S3AccessKey == "minioadmin" || c.S3SecretKey == "minioadmin" {
		problems = append(problems, "S3_ACCESS_KEY/S3_SECRET_KEY are still the MinIO dev defaults (minioadmin) — set real credentials or an IAM role")
	}
	if strings.Contains(c.PostgresDSN, "postgres:postgres@") || strings.Contains(c.PostgresDSN, "postgres:1981@") {
		problems = append(problems, "POSTGRES_DSN is still using a dev default password — rotate it")
	}
	for _, o := range c.AllowedOrigins {
		if strings.Contains(o, "localhost") || strings.Contains(o, "127.0.0.1") {
			problems = append(problems, fmt.Sprintf("ALLOWED_ORIGIN %q is a localhost origin — set it to your real frontend domain", o))
		}
		if strings.HasPrefix(o, "http://") {
			problems = append(problems, fmt.Sprintf("ALLOWED_ORIGIN %q is plain http:// — production must serve the frontend over https", o))
		}
	}
	if strings.Contains(c.S3PublicEndpoint, "localhost") || strings.Contains(c.S3PublicEndpoint, "127.0.0.1") {
		problems = append(problems, "S3_PUBLIC_ENDPOINT still points at localhost — it must be the browser-reachable host used to sign pre-signed URLs")
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to start in production mode (GIN_MODE=release) with insecure defaults:\n  - %s", strings.Join(problems, "\n  - "))
}
