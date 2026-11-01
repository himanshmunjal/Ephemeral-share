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

// Check enforces the wrong-code lockout AND loads the session in ONE Redis
// round trip (these used to be several sequential calls on every request, which
// is what made the app feel slow against a remote Redis). On failure it writes
// the response (429 locked out, 500, or `missStatus` for an unknown/expired
// code) and returns ok=false.
func (g *Guard) Check(c *gin.Context, code string, opts redisclient.LookupOpts, missStatus int) (*redisclient.Lookup, bool) {
	lk, err := g.store.Lookup(c.Request.Context(), c.ClientIP(), code, opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return nil, false
	}
	if lk.Failures >= g.cfg.MaxFailedLookups {
		c.Header("Retry-After", strconv.Itoa(int(lk.LockTTL.Seconds())+1))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
			"error": "too many wrong codes — try again later",
			"code":  "locked_out",
		})
		return nil, false
	}
	if lk.Session == nil || lk.TTL <= 0 {
		g.Miss(c)
		c.JSON(missStatus, gin.H{"error": "session not found or expired", "code": "not_found"})
		return nil, false
	}
	return lk, true
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
