package handlers

import (
	"crypto/subtle"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"ephemeral-share/internal/config"
	"ephemeral-share/internal/redisclient"
)

// Guard implements brute-force protection for the access code: every lookup
// that misses (unknown/expired code) counts against the caller's IP, and once
// the budget is spent that IP is locked out for the lockout window — which
// also covers WebSocket, content and join endpoints, not just one route.
type Guard struct {
	store *redisclient.Store
	cfg   *config.Config
}

func NewGuard(store *redisclient.Store, cfg *config.Config) *Guard {
	return &Guard{store: store, cfg: cfg}
}

// Locked aborts with 429 and returns true if this IP is currently locked out.
func (g *Guard) Locked(c *gin.Context) bool {
	if locked, ttl := g.store.IsLocked(c.Request.Context(), c.ClientIP(), g.cfg.MaxFailedLookups); locked {
		c.Header("Retry-After", strconv.Itoa(int(ttl.Seconds())+1))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error": "too many wrong codes — try again later",
			"code":  "locked_out",
		})
		return true
	}
	return false
}

// Miss records a failed lookup for this IP.
func (g *Guard) Miss(c *gin.Context) {
	g.store.RecordFailure(c.Request.Context(), c.ClientIP(), g.cfg.LockoutDuration)
}

// participantID reads the per-browser id; unknown/oversized values are dropped.
func participantID(c *gin.Context) string {
	id := c.GetHeader("X-Participant-ID")
	if len(id) < 8 || len(id) > 64 {
		return ""
	}
	return id
}

// whoToken validates the client's encrypted display identity. It is opaque to
// the server (AES-GCM ciphertext, base64url); we only check shape and size so
// it can't be abused to stash arbitrary data.
func validWho(v string) string {
	if len(v) < 16 || len(v) > 300 {
		return ""
	}
	for _, r := range v {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if !ok {
			return ""
		}
	}
	return v
}

func whoHeader(c *gin.Context) string { return validWho(c.GetHeader("X-Participant-Who")) }

func tokenEquals(a, b string) bool {
	return a != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
