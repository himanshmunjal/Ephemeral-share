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
	RedisTLS         bool // required by most managed Redis (Upstash, etc.)
	S3Endpoint       string
	S3PublicEndpoint string
	S3Bucket         string
	S3Region         string
	S3AccessKey      string
	S3SecretKey      string
	SessionTTL       time.Duration // default 1 hour (SESSION_TTL_SECONDS)
	MaxUploadBytes   int64
	AllowedOrigins   []string
	RateLimitPerMin  int
	CodeLength       int

	// Abuse / cost protection.
	MaxFilesPerSession    int
	MaxSessionBytes       int64
	MaxTextItems          int
	MaxParticipants       int // hard cap, clamps whatever the creator asks for
	UploadRateLimitPerMin int
	RoomRateLimitPerMin   int // list/delete/download/WebSocket traffic from people already inside a room
	// SignContentLength binds the declared upload size into the pre-signed PUT
	// URL so storage rejects a different-sized body. Some S3-compatible stores
	// mishandle that signed header; set SIGN_CONTENT_LENGTH=false for them. The
	// server still verifies the real size when the upload is completed.
	SignContentLength   bool
	MaxFailedLookups    int           // wrong-code attempts per IP before lockout
	LockoutDuration     time.Duration // lockout window for the above
	OrphanSweepInterval time.Duration
	OrphanGrace         time.Duration // never delete objects younger than this

	// Ops.
	// TrustedProxies lists proxies whose X-Forwarded-For / X-Real-IP we believe,
	// as CIDRs or the shorthand "private" (loopback + private ranges, which
	// covers PaaS edge proxies). The real client is the right-most address that
	// is NOT a trusted proxy, so client-supplied values can't be spoofed.
	TrustedProxies []string
	ClientIPHeader string // e.g. "X-Real-IP" behind Railway/Render; empty = socket address
	AdminToken     string // enables DELETE /admin/sessions/:code for takedowns; empty = disabled
	EnforceHTTPS   bool
	SentryDSN      string // empty = error tracking off
	SentryEnv      string
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return time.Duration(i) * time.Second
		}
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
	ttlSeconds := getEnvInt("SESSION_TTL_SECONDS", 3600)
	return &Config{
		Port:           getEnv("PORT", "8080"),
		PostgresDSN:    getEnv("POSTGRES_DSN", ""), // empty = analytics disabled (Redis-only deployment),
		RedisAddr:      getEnv("REDIS_ADDR", "redis:6379"),
		RedisPassword:  getEnv("REDIS_PASSWORD", ""),
		RedisTLS:       getEnv("REDIS_TLS", "") == "true",
		TrustedProxies: splitOrigins(getEnv("TRUSTED_PROXIES", "")),
		// Defaults to local MinIO. For real AWS S3, set S3_ENDPOINT=none —
		// this tells the storage client to skip the custom-endpoint override
		// entirely and let the AWS SDK resolve the regional endpoint itself.
		S3Endpoint: getEnv("S3_ENDPOINT", "http://minio:9000"),
		// The browser uploads/downloads directly against this host via
		// pre-signed URLs, so it must be reachable from the user's machine —
		// NOT the docker-compose service name. Defaults to the host-mapped
		// MinIO port for local dev.
		S3PublicEndpoint: getEnv("S3_PUBLIC_ENDPOINT", "http://localhost:9000"),
		S3Bucket:         getEnv("S3_BUCKET", "ephemeral-share"),
		S3Region:         getEnv("S3_REGION", "us-east-1"),
		S3AccessKey:      getEnv("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey:      getEnv("S3_SECRET_KEY", "minioadmin"),
		SessionTTL:       time.Duration(ttlSeconds) * time.Second,
		MaxUploadBytes:   int64(getEnvInt("MAX_UPLOAD_MB", 25)) * 1024 * 1024,
		// Comma-separated so a real deployment can allow both the apex and
		// "www" host (or a staging + prod frontend) without code changes.
		AllowedOrigins:  splitOrigins(getEnv("ALLOWED_ORIGIN", "http://localhost:5173")),
		RateLimitPerMin: getEnvInt("RATE_LIMIT_PER_MIN", 60),
		CodeLength:      getEnvInt("CODE_LENGTH", 8),

		MaxFilesPerSession:    getEnvInt("MAX_FILES_PER_SESSION", 20),
		MaxSessionBytes:       int64(getEnvInt("MAX_SESSION_MB", 100)) * 1024 * 1024,
		MaxTextItems:          getEnvInt("MAX_TEXT_ITEMS_PER_SESSION", 200),
		MaxParticipants:       getEnvInt("MAX_PARTICIPANTS", 10),
		UploadRateLimitPerMin: getEnvInt("UPLOAD_RATE_LIMIT_PER_MIN", 30),
		RoomRateLimitPerMin:   getEnvInt("ROOM_RATE_LIMIT_PER_MIN", 240),
		SignContentLength:     getEnv("SIGN_CONTENT_LENGTH", "true") != "false",
		MaxFailedLookups:      getEnvInt("MAX_FAILED_LOOKUPS", 10),
		LockoutDuration:       getEnvDuration("LOCKOUT_SECONDS", 15*time.Minute),
		OrphanSweepInterval:   getEnvDuration("ORPHAN_SWEEP_SECONDS", 10*time.Minute),
		OrphanGrace:           getEnvDuration("ORPHAN_GRACE_SECONDS", 10*time.Minute),

		ClientIPHeader: getEnv("CLIENT_IP_HEADER", ""),
		AdminToken:     getEnv("ADMIN_TOKEN", ""),
		EnforceHTTPS:   getEnv("ENFORCE_HTTPS", "") == "true",
		SentryDSN:      getEnv("SENTRY_DSN", ""),
		SentryEnv:      getEnv("SENTRY_ENVIRONMENT", "production"),
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
	if c.PostgresDSN != "" && (strings.Contains(c.PostgresDSN, "postgres:postgres@") || strings.Contains(c.PostgresDSN, "postgres:1981@")) {
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

	if c.AdminToken != "" && len(c.AdminToken) < 24 {
		problems = append(problems, "ADMIN_TOKEN is shorter than 24 characters — use a long random value or leave it unset")
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("refusing to start in production mode (GIN_MODE=release) with insecure defaults:\n  - %s", strings.Join(problems, "\n  - "))
}
