package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"ephemeral-share/internal/analytics"
	"ephemeral-share/internal/config"
	"ephemeral-share/internal/models"
	"ephemeral-share/internal/redisclient"
	"ephemeral-share/internal/storage"
)

type SessionHandler struct {
	store *redisclient.Store
	s3    *storage.Client
	cfg   *config.Config
	stats *analytics.Store // nil-safe: analytics is best-effort, never required
	guard *Guard
}

func NewSessionHandler(store *redisclient.Store, s3 *storage.Client, cfg *config.Config, stats *analytics.Store, guard *Guard) *SessionHandler {
	return &SessionHandler{store: store, s3: s3, cfg: cfg, stats: stats, guard: guard}
}

func (h *SessionHandler) limits() models.Limits {
	return models.Limits{
		MaxFileBytes:    h.cfg.MaxUploadBytes,
		MaxFiles:        h.cfg.MaxFilesPerSession,
		MaxSessionBytes: h.cfg.MaxSessionBytes,
	}
}

// CreateSession — POST /api/v1/sessions
func (h *SessionHandler) CreateSession(c *gin.Context) {
	var req models.CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	// The participant cap is always enforced and never exceeds the server-wide
	// hard limit, whatever the client asks for.
	if req.MaxParticipants <= 0 || req.MaxParticipants > h.cfg.MaxParticipants {
		req.MaxParticipants = h.cfg.MaxParticipants
	}

	code, err := GenerateUniqueCode(c.Request.Context(), h.store, h.cfg.CodeLength)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not create session, try again"})
		return
	}

	now := time.Now().UTC()
	sess := models.Session{
		Code:            code,
		Type:            req.Type,
		MaxParticipants: req.MaxParticipants,
		CreatedAt:       now,
		ExpiresAt:       now.Add(h.cfg.SessionTTL),
		OwnerToken:      uuid.NewString() + uuid.NewString(),
	}
	if err := h.store.CreateSession(c.Request.Context(), sess); err != nil {
		slog.Error("create session failed", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist session"})
		return
	}
	if h.stats != nil {
		h.stats.RecordSessionCreated(sess)
	}

	// The owner token is returned once in the body and sent back in the
	// X-Owner-Token header. (A cookie would be third-party when the frontend
	// and API live on different domains, which Safari/Firefox block.)
	c.JSON(http.StatusCreated, models.CreateSessionResponse{
		Code:       sess.Code,
		Type:       sess.Type,
		ExpiresAt:  sess.ExpiresAt,
		OwnerToken: sess.OwnerToken,
	})
}

// GetSession — GET /api/v1/sessions/:code
func (h *SessionHandler) GetSession(c *gin.Context) {
	if h.guard.Locked(c) {
		return
	}
	ctx := c.Request.Context()
	code := c.Param("code")
	sess, err := h.store.GetSession(ctx, code)
	if err == redis.Nil {
		h.guard.Miss(c)
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found or expired", "code": "not_found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	ttl, _ := h.store.TTLRemaining(ctx, code)
	online, _ := h.store.PresenceCount(ctx, code)
	c.JSON(http.StatusOK, gin.H{
		"code":              sess.Code,
		"type":              sess.Type,
		"max_participants":  sess.MaxParticipants,
		"online":            online,
		"expires_at":        sess.ExpiresAt,
		"ttl_seconds":       int(ttl.Seconds()),
		"ttl_total_seconds": int(h.cfg.SessionTTL.Seconds()),
		"limits":            h.limits(),
	})
}

// JoinSession — POST /api/v1/sessions/:code/join
// A pre-flight capacity check so the UI can show "room is full" before it
// opens a socket. The authoritative, race-free cap is enforced atomically
// when the WebSocket registers its presence.
func (h *SessionHandler) JoinSession(c *gin.Context) {
	if h.guard.Locked(c) {
		return
	}
	ctx := c.Request.Context()
	code := c.Param("code")
	sess, err := h.store.GetSession(ctx, code)
	if err == redis.Nil {
		h.guard.Miss(c)
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found or expired", "code": "not_found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	online, _ := h.store.PresenceCount(ctx, code)
	if online >= sess.MaxParticipants {
		c.JSON(http.StatusConflict, gin.H{"error": "this session is full", "code": "full"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"online": online, "max_participants": sess.MaxParticipants})
}

// DeleteSession — DELETE /api/v1/sessions/:code (owner-only early termination)
func (h *SessionHandler) DeleteSession(c *gin.Context) {
	if h.guard.Locked(c) {
		return
	}
	code := c.Param("code")
	sess, err := h.store.GetSession(c.Request.Context(), code)
	if err == redis.Nil {
		h.guard.Miss(c)
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found or expired", "code": "not_found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	if !tokenEquals(c.GetHeader("X-Owner-Token"), sess.OwnerToken) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the session owner can end it early"})
		return
	}
	h.terminate(c.Request.Context(), code)
	c.JSON(http.StatusOK, gin.H{"status": "terminated"})
}

// AdminDeleteSession — DELETE /admin/sessions/:code
// Takedown path for abuse reports: the operator terminates a session by code.
// Registered only when ADMIN_TOKEN is configured.
func (h *SessionHandler) AdminDeleteSession(c *gin.Context) {
	if !tokenEquals(c.GetHeader("X-Admin-Token"), h.cfg.AdminToken) {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	code := c.Param("code")
	if ok, _ := h.store.CodeExists(c.Request.Context(), code); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found or expired"})
		return
	}
	h.terminate(c.Request.Context(), code)
	slog.Warn("session terminated by admin takedown")
	c.JSON(http.StatusOK, gin.H{"status": "terminated"})
}

func (h *SessionHandler) terminate(ctx context.Context, code string) {
	if err := h.s3.DeleteSessionObjects(ctx, code); err != nil {
		slog.Error("terminate: s3 cleanup failed (orphan sweep will retry)", "error", err)
	}
	_ = h.store.DeleteSession(ctx, code)
	if h.stats != nil {
		h.stats.MarkSessionEnded(code)
	}
}
