package middleware

import (
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// visitor tracks a per-IP token bucket limiter.
type visitor struct {
	limiter *rate.Limiter
}

var (
	visitors = map[string]*visitor{}
	mu       sync.Mutex
)

// RateLimit returns middleware allowing `perMinute` requests/min/IP with a
// small burst allowance. Applied to code-lookup and join endpoints to blunt
// brute-force code guessing.
func RateLimit(perMinute int) gin.HandlerFunc {
	limit := rate.Limit(float64(perMinute) / 60.0)
	return func(c *gin.Context) {
		ip := c.ClientIP()

		mu.Lock()
		v, ok := visitors[ip]
		if !ok {
			v = &visitor{limiter: rate.NewLimiter(limit, perMinute)}
			visitors[ip] = v
		}
		mu.Unlock()

		if !v.limiter.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "too many requests, slow down"})
			return
		}
		c.Next()
	}
}
