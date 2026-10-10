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

type ContentHandler struct {
	store *redisclient.Store
	s3    *storage.Client
	cfg   *config.Config
	stats *analytics.Store // nil-safe: analytics is best-effort, never required
}

func NewContentHandler(store *redisclient.Store, s3 *storage.Client, cfg *config.Config, stats *analytics.Store) *ContentHandler {
	return &ContentHandler{store: store, s3: s3, cfg: cfg, stats: stats}
}

func (h *ContentHandler) requireLiveSession(c *gin.Context) bool {
	code := c.Param("code")
	if _, err := h.store.GetSession(c.Request.Context(), code); err != nil {
		if err == redis.Nil {
			c.JSON(http.StatusGone, gin.H{"error": "session not found or expired"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		}
		return false
	}
	return true
}

// RequestUploadURL — POST /api/v1/sessions/:code/content/upload-url
// Issues a pre-signed PUT URL; the client uploads the binary directly to
// object storage so large files never transit the API process.
func (h *ContentHandler) RequestUploadURL(c *gin.Context) {
	if !h.requireLiveSession(c) {
		return
	}
	code := c.Param("code")

	var req models.UploadURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.SizeBytes > h.cfg.MaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file exceeds size limit"})
		return
	}

	contentID := uuid.NewString()
	url, storageKey, err := h.s3.PresignPutURL(c.Request.Context(), code, contentID, req.Filename, req.ContentType, 2*time.Minute)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create upload URL"})
		return
	}

	item := models.ContentItem{
		ID:          contentID,
		SessionCode: code,
		Kind:        models.ContentFile,
		ContentType: req.ContentType,
		SizeBytes:   req.SizeBytes,
		StorageKey:  storageKey,
		Filename:    req.Filename,
		CreatedAt:   time.Now().UTC(),
	}
	// Held as pending — invisible to other participants — until the uploader
	// calls CompleteUpload after its direct-to-storage PUT succeeds.
	if err := h.store.AddPendingUpload(c.Request.Context(), item); err != nil {
		c.JSON(http.StatusGone, gin.H{"error": "session expired mid-upload"})
		return
	}

	c.JSON(http.StatusOK, models.UploadURLResponse{
		ContentID: contentID,
		UploadURL: url,
		ExpiresIn: 120,
	})
}

// CompleteUpload — POST /api/v1/sessions/:code/content/:id/complete
// Called by the uploader once its direct-to-storage PUT succeeds. Only then
// does the file join the session's content list and get broadcast, so nobody
// sees (or tries to download) a half-uploaded file.
func (h *ContentHandler) CompleteUpload(c *gin.Context) {
	if !h.requireLiveSession(c) {
		return
	}
	ctx := c.Request.Context()
	code := c.Param("code")
	id := c.Param("id")

	pending, err := h.store.GetPendingUpload(ctx, code, id)
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "upload not found or already completed"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}

	// Verify against what actually landed in storage rather than trusting the
	// client: the object must exist, and the pre-signed PUT doesn't enforce
	// the size limit, so check it here.
	size, err := h.s3.ObjectSize(ctx, pending.StorageKey)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "file has not finished uploading"})
		return
	}
	if size > h.cfg.MaxUploadBytes {
		_ = h.s3.DeleteObject(ctx, pending.StorageKey)
		_, _ = h.store.TakePendingUpload(ctx, code, id)
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file exceeds size limit"})
		return
	}

	item, err := h.store.TakePendingUpload(ctx, code, id)
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "upload not found or already completed"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	item.SizeBytes = size
	if err := h.store.AddContent(ctx, *item); err != nil {
		c.JSON(http.StatusGone, gin.H{"error": "session expired mid-upload"})
		return
	}
	if h.stats != nil {
		h.stats.IncrementFileShared(code)
	}
	c.JSON(http.StatusCreated, item)
}

// AddTextContent — POST /api/v1/sessions/:code/content/text
func (h *ContentHandler) AddTextContent(c *gin.Context) {
	if !h.requireLiveSession(c) {
		return
	}
	code := c.Param("code")

	var req models.AddTextContentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	item := models.ContentItem{
		ID:          uuid.NewString(),
		SessionCode: code,
		Kind:        models.ContentText,
		ContentType: req.ContentType,
		TextPayload: req.Text,
		CreatedAt:   time.Now().UTC(),
	}
	if err := h.store.AddContent(c.Request.Context(), item); err != nil {
		c.JSON(http.StatusGone, gin.H{"error": "session expired"})
		return
	}
	if h.stats != nil {
		h.stats.IncrementTextShared(code)
	}
	c.JSON(http.StatusCreated, item)
}

// ListContent — GET /api/v1/sessions/:code/content
func (h *ContentHandler) ListContent(c *gin.Context) {
	if !h.requireLiveSession(c) {
		return
	}
	code := c.Param("code")
	items, err := h.store.ListContent(c.Request.Context(), code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list content"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// DownloadContent — GET /api/v1/sessions/:code/content/:id/download
// Redirects to a short-lived pre-signed GET URL rather than proxying the file.
func (h *ContentHandler) DownloadContent(c *gin.Context) {
	if !h.requireLiveSession(c) {
		return
	}
	code := c.Param("code")
	id := c.Param("id")

	items, err := h.store.ListContent(c.Request.Context(), code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	for _, item := range items {
		if item.ID == id {
			if item.Kind == models.ContentText {
				c.JSON(http.StatusOK, item)
				return
			}
			url, err := h.s3.PresignGetURL(c.Request.Context(), item.StorageKey, 60*time.Second)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate download link"})
				return
			}
			// fetch() following a cross-origin redirect sends "Origin: null",
			// which the object store's CORS policy rejects — so JS callers ask
			// for the URL as JSON and fetch it directly instead.
			if c.Query("redirect") == "false" {
				c.JSON(http.StatusOK, gin.H{"download_url": url})
				return
			}
			c.Redirect(http.StatusFound, url)
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "content not found or expired"})
}
