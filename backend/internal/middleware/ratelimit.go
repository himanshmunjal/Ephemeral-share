package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// limiterSet is one independent per-IP token-bucket table. Each route group
// gets its own so, say, upload traffic can't exhaust the join budget.
type limiterSet struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	limit    rate.Limit
	burst    int
}

// RateLimit returns middleware allowing `perMinute` requests/min/IP (with a
// burst of the same size). Idle entries are evicted so the table can't grow
// without bound under a spray of source addresses.
func RateLimit(perMinute int) gin.HandlerFunc {
	if perMinute <= 0 {
		perMinute = 60
	}
	ls := &limiterSet{
		visitors: map[string]*visitor{},
		limit:    rate.Limit(float64(perMinute) / 60.0),
		burst:    perMinute,
	}
	go ls.evictLoop()

	return func(c *gin.Context) {
		ip := c.ClientIP()
		ls.mu.Lock()
		v, ok := ls.visitors[ip]
		if !ok {
			v = &visitor{limiter: rate.NewLimiter(ls.limit, ls.burst)}
			ls.visitors[ip] = v
		}
		v.lastSeen = time.Now()
		allowed := v.limiter.Allow()
		ls.mu.Unlock()

		if !allowed {
			c.Header("Retry-After", "10")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests, slow down"})
			return
		}
		c.Next()
	}
}

func (ls *limiterSet) evictLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-10 * time.Minute)
		ls.mu.Lock()
		for ip, v := range ls.visitors {
			if v.lastSeen.Before(cutoff) {
				delete(ls.visitors, ip)
			}
		}
		ls.mu.Unlock()
	}
}
