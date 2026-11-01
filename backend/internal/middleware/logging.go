package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"ephemeral-share/internal/monitoring"

	"github.com/gin-gonic/gin"
)

// RequestLogger emits one structured line per request. It logs the route
// *pattern* (/api/v1/sessions/:code), never the concrete path or query: the
// access code doubles as the encryption secret and must not land in logs.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		status := c.Writer.Status()
		lvl := slog.LevelInfo
		if status >= 500 {
			lvl = slog.LevelError
		} else if status >= 400 {
			lvl = slog.LevelWarn
		}
		slog.Log(c.Request.Context(), lvl, "request",
			"method", c.Request.Method,
			"route", route,
			"status", status,
			"ms", time.Since(start).Milliseconds(),
			"ip", c.ClientIP(),
		)
	}
}

// Recovery turns panics into a logged 500 (with stack) without echoing
// request details — gin's default recovery dumps the request, headers included.
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic", "route", c.FullPath(), "error", r, "stack", string(debug.Stack()))
				monitoring.CapturePanic(c.FullPath(), r)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
			}
		}()
		c.Next()
	}
}
