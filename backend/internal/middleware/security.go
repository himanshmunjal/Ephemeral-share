package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders is the Go equivalent of helmet for a JSON API: it never
// serves HTML, so the CSP forbids everything, and responses can't be framed
// or MIME-sniffed.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cross-Origin-Resource-Policy", "same-site")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Cache-Control", "no-store")
		if gin.Mode() == gin.ReleaseMode {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// RequireHTTPS redirects plain-HTTP requests (as reported by the platform
// proxy in X-Forwarded-Proto) to HTTPS. Health checks are exempt because
// platform probes hit the container over HTTP.
func RequireHTTPS() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/health") {
			c.Next()
			return
		}
		if c.GetHeader("X-Forwarded-Proto") == "http" {
			target := "https://" + c.Request.Host + c.Request.URL.RequestURI()
			c.Redirect(http.StatusPermanentRedirect, target)
			c.Abort()
			return
		}
		c.Next()
	}
}
