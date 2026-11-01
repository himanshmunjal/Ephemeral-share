package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"

	"ephemeral-share/internal/analytics"
	"ephemeral-share/internal/config"
	"ephemeral-share/internal/middleware"
	"ephemeral-share/internal/redisclient"
	"ephemeral-share/internal/storage"
)

func NewRouter(store *redisclient.Store, s3 *storage.Client, cfg *config.Config, stats *analytics.Store) *gin.Engine {
	r := gin.New()
	r.Use(middleware.Recovery(), middleware.RequestLogger())

	// Behind a platform proxy the socket address is the proxy's, so per-IP
	// rate limits and lockouts need the real client IP. Only trust a header
	// the platform overwrites (set CLIENT_IP_HEADER); otherwise use the socket.
	switch {
	case cfg.ClientIPHeader != "":
		// A single-value header the platform overwrites (e.g. Cloudflare's
		// CF-Connecting-IP). Not for X-Forwarded-For, which is a list.
		r.TrustedPlatform = cfg.ClientIPHeader
	case len(cfg.TrustedProxies) > 0:
		// Walk X-Forwarded-For from the right, skipping trusted proxies: the
		// first untrusted address is what the platform's edge saw.
		if err := r.SetTrustedProxies(expandProxies(cfg.TrustedProxies)); err != nil {
			slog.Error("invalid TRUSTED_PROXIES; falling back to socket address", "error", err)
			_ = r.SetTrustedProxies(nil)
		}
		r.RemoteIPHeaders = []string{"X-Forwarded-For", "X-Real-IP"}
	default:
		_ = r.SetTrustedProxies(nil)
	}

	// Health first, so probes bypass CORS / HTTPS redirect / rate limits.
	health := func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := store.Ping(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "degraded", "redis": "down"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
	r.GET("/health", health)
	r.GET("/healthz", health)

	if cfg.EnforceHTTPS {
		r.Use(middleware.RequireHTTPS())
	}
	r.Use(middleware.SecurityHeaders())
	r.Use(cors.New(cors.Config{
		AllowOrigins: cfg.AllowedOrigins,
		AllowMethods: []string{"GET", "POST", "DELETE", "OPTIONS"},
		AllowHeaders: []string{"Origin", "Content-Type", "Accept", "X-Owner-Token", "X-Participant-ID", "X-Participant-Who"},
		MaxAge:       12 * time.Hour,
	}))

	guard := NewGuard(store, cfg)
	sessionH := NewSessionHandler(store, s3, cfg, stats, guard)
	contentH := NewContentHandler(store, s3, cfg, stats, guard)
	wsH := NewWebSocketHandler(store, cfg, guard)

	// Each limiter below is its own independent per-IP bucket. Code-guessing
	// is already stopped by the wrong-code lockout, so the lookup limit only
	// needs to blunt floods and is floored so a small dev value of
	// RATE_LIMIT_PER_MIN can't starve a normal room page load.
	lookup := middleware.RateLimit(max(cfg.RateLimitPerMin*3, 60))
	creation := middleware.RateLimit(max(cfg.RateLimitPerMin/6, 10)) // session creation is the expensive one
	uploads := middleware.RateLimit(cfg.UploadRateLimitPerMin)
	room := middleware.RateLimit(cfg.RoomRateLimitPerMin)              // list / delete / download
	socket := middleware.RateLimit(max(cfg.RoomRateLimitPerMin/4, 30)) // connects + reconnects

	api := r.Group("/api/v1")
	{
		api.POST("/sessions", creation, sessionH.CreateSession)
		api.GET("/sessions/:code", lookup, sessionH.GetSession)
		api.POST("/sessions/:code/join", lookup, sessionH.JoinSession)
		api.DELETE("/sessions/:code", lookup, sessionH.DeleteSession)

		api.POST("/sessions/:code/content/upload-url", uploads, contentH.RequestUploadURL)
		api.POST("/sessions/:code/content/:id/complete", uploads, contentH.CompleteUpload)
		api.POST("/sessions/:code/content/text", uploads, contentH.AddTextContent)
		api.DELETE("/sessions/:code/content/:id", room, contentH.CancelOrDeleteContent)
		api.GET("/sessions/:code/content", room, contentH.ListContent)
		api.GET("/sessions/:code/content/:id/download", room, contentH.DownloadContent)
	}

	if cfg.AdminToken != "" {
		r.DELETE("/admin/sessions/:code", middleware.RateLimit(30), sessionH.AdminDeleteSession)
		r.GET("/admin/diag/ip", middleware.RateLimit(30), sessionH.AdminDiagIP)
	}

	r.GET("/ws/sessions/:code", socket, wsH.HandleConnection)

	return r
}

// privateRanges is what "private" expands to: loopback plus the address ranges
// PaaS edge proxies and sidecars connect from.
var privateRanges = []string{
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16",
	"fc00::/7", "fe80::/10",
}

// cloudflareRanges are Cloudflare's published edge ranges
// (https://www.cloudflare.com/ips/). Platforms such as Render sit behind
// Cloudflare, which appears in X-Forwarded-For just before the real visitor;
// without trusting it, every visitor would look like a Cloudflare edge address
// and share one rate limit and lockout. Refresh from that page if it changes.
var cloudflareRanges = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
	"2400:cb00::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2405:b500::/32",
	"2405:8100::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
}

func expandProxies(in []string) []string {
	var out []string
	for _, p := range in {
		switch p {
		case "private":
			out = append(out, privateRanges...)
		case "cloudflare":
			out = append(out, cloudflareRanges...)
		default:
			out = append(out, p)
		}
	}
	return out
}
