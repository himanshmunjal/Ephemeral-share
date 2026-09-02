package handlers

import (
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
}

func NewSessionHandler(store *redisclient.Store, s3 *storage.Client, cfg *config.Config, stats *analytics.Store) *SessionHandler {
	return &SessionHandler{store: store, s3: s3, cfg: cfg, stats: stats}
}

// CreateSession — POST /api/v1/sessions
func (h *SessionHandler) CreateSession(c *gin.Context) {
	var req models.CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Type == models.SharedWorkspace && req.MaxParticipants <= 0 {
		req.MaxParticipants = 10 // sane default
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
		OwnerToken:      uuid.NewString(),
	}
	if err := h.store.CreateSession(c.Request.Context(), sess); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist session"})
		return
	}
	if h.stats != nil {
		h.stats.RecordSessionCreated(sess)
	}

	// Owner token returned once, used for early-termination auth. Also set as an
	// HttpOnly cookie so the creator's browser doesn't need to store it manually.
	c.SetCookie("owner_token", sess.OwnerToken, int(h.cfg.SessionTTL.Seconds()), "/", "", true, true)
	c.JSON(http.StatusCreated, models.CreateSessionResponse{
		Code:      sess.Code,
		Type:      sess.Type,
		ExpiresAt: sess.ExpiresAt,
	})
}

// GetSession — GET /api/v1/sessions/:code
func (h *SessionHandler) GetSession(c *gin.Context) {
	code := c.Param("code")
	sess, err := h.store.GetSession(c.Request.Context(), code)
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found or expired"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	ttl, _ := h.store.TTLRemaining(c.Request.Context(), code)
	c.JSON(http.StatusOK, gin.H{
		"code":             sess.Code,
		"type":             sess.Type,
		"max_participants": sess.MaxParticipants,
		"expires_at":       sess.ExpiresAt,
		"ttl_seconds":      int(ttl.Seconds()),
	})
}

// JoinSession — POST /api/v1/sessions/:code/join
func (h *SessionHandler) JoinSession(c *gin.Context) {
	code := c.Param("code")
	sess, err := h.store.GetSession(c.Request.Context(), code)
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found or expired"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	participantID := uuid.NewString()
	ok, err := h.store.JoinParticipant(c.Request.Context(), code, participantID, sess.MaxParticipants)
	if err != nil {
		c.JSON(http.StatusGone, gin.H{"error": "session expired"})
		return
	}
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": "session is at participant capacity"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"participant_id": participantID, "session": sess})
}

// DeleteSession — DELETE /api/v1/sessions/:code (owner-only early termination)
func (h *SessionHandler) DeleteSession(c *gin.Context) {
	code := c.Param("code")
	token, _ := c.Cookie("owner_token")
	sess, err := h.store.GetSession(c.Request.Context(), code)
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	if sess.OwnerToken != token {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the session owner can end it early"})
		return
	}
	_ = h.s3.DeleteSessionObjects(c.Request.Context(), code)
	_ = h.store.DeleteSession(c.Request.Context(), code)
	if h.stats != nil {
		h.stats.MarkSessionEnded(code)
	}
	c.JSON(http.StatusOK, gin.H{"status": "terminated"})
}
