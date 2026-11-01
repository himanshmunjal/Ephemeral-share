package handlers

import (
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

// Every stored file is client-side-encrypted ciphertext, so the only valid
// content type is opaque bytes. Anything else is rejected, which also stops
// the bucket from being used to host browsable HTML/SVG/executables.
const opaqueContentType = "application/octet-stream"

type ContentHandler struct {
	store *redisclient.Store
	s3    *storage.Client
	cfg   *config.Config
	stats *analytics.Store // nil-safe: analytics is best-effort, never required
	guard *Guard
}

func NewContentHandler(store *redisclient.Store, s3 *storage.Client, cfg *config.Config, stats *analytics.Store, guard *Guard) *ContentHandler {
	return &ContentHandler{store: store, s3: s3, cfg: cfg, stats: stats, guard: guard}
}

// requireLiveSession does the lockout check and session load in one round trip.
func (h *ContentHandler) requireLiveSession(c *gin.Context, opts redisclient.LookupOpts) (*redisclient.Lookup, bool) {
	return h.guard.Check(c, c.Param("code"), opts, http.StatusGone)
}

// RequestUploadURL — POST /api/v1/sessions/:code/content/upload-url
// Issues a pre-signed PUT URL; the client uploads the binary directly to
// object storage so large files never transit the API process.
func (h *ContentHandler) RequestUploadURL(c *gin.Context) {
	lk, ok := h.requireLiveSession(c, redisclient.LookupOpts{})
	if !ok {
		return
	}
	ctx := c.Request.Context()
	code := c.Param("code")

	var req models.UploadURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if req.ContentType != opaqueContentType {
		c.JSON(http.StatusUnsupportedMediaType, gin.H{"error": "only encrypted uploads are accepted"})
		return
	}
	if len(req.Filename) > 1024 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "filename too long"})
		return
	}
	if req.SizeBytes > h.cfg.MaxUploadBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file exceeds the per-file size limit", "code": "file_too_large"})
		return
	}

	// Atomically claim a file slot + bytes against the session quotas.
	res, err := h.store.ReserveQuota(ctx, code, "files", h.cfg.MaxFilesPerSession, req.SizeBytes, h.cfg.MaxSessionBytes, lk.TTL)
	if err != nil {
		c.JSON(http.StatusGone, gin.H{"error": "session expired", "code": "not_found"})
		return
	}
	switch res {
	case redisclient.ReserveTooManyFiles:
		c.JSON(http.StatusConflict, gin.H{"error": "this session has reached its file limit", "code": "too_many_files"})
		return
	case redisclient.ReserveTooBig:
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "this session has reached its total storage limit", "code": "session_full"})
		return
	}

	contentID := uuid.NewString()
	url, storageKey, err := h.s3.PresignPutURL(ctx, code, contentID, req.Filename, opaqueContentType, req.SizeBytes, h.cfg.SignContentLength, 5*time.Minute)
	if err != nil {
		h.store.ReleaseQuota(ctx, code, "files", req.SizeBytes)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create upload URL"})
		return
	}

	item := models.ContentItem{
		ID:          contentID,
		SessionCode: code,
		Kind:        models.ContentFile,
		ContentType: opaqueContentType,
		SizeBytes:   req.SizeBytes,
		StorageKey:  storageKey,
		Filename:    req.Filename,
		CreatedAt:   time.Now().UTC(),
		UploaderID:  participantID(c),
		Who:         whoHeader(c),
	}
	// Held as pending — invisible to other participants — until the uploader
	// calls CompleteUpload after its direct-to-storage PUT succeeds.
	if err := h.store.AddPendingUpload(ctx, item, lk.TTL); err != nil {
		h.store.ReleaseQuota(ctx, code, "files", req.SizeBytes)
		c.JSON(http.StatusGone, gin.H{"error": "session expired mid-upload"})
		return
	}

	c.JSON(http.StatusOK, models.UploadURLResponse{
		ContentID: contentID,
		UploadURL: url,
		ExpiresIn: 300,
	})
}

// CompleteUpload — POST /api/v1/sessions/:code/content/:id/complete
// Called by the uploader once its direct-to-storage PUT succeeds. Only then
// does the file join the session's content list and get broadcast, so nobody
// sees (or tries to download) a half-uploaded file.
func (h *ContentHandler) CompleteUpload(c *gin.Context) {
	lk, ok := h.requireLiveSession(c, redisclient.LookupOpts{})
	if !ok {
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
	// client: it must exist and match the size the quota was reserved for.
	size, err := h.s3.ObjectSize(ctx, pending.StorageKey)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "file has not finished uploading"})
		return
	}
	if size != pending.SizeBytes || size > h.cfg.MaxUploadBytes {
		_ = h.s3.DeleteObject(ctx, pending.StorageKey)
		if _, err := h.store.TakePendingUpload(ctx, code, id); err == nil {
			h.store.ReleaseQuota(ctx, code, "files", pending.SizeBytes)
		}
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "uploaded file does not match its declared size"})
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
	if err := h.store.AddContent(ctx, *item, lk.TTL); err != nil {
		c.JSON(http.StatusGone, gin.H{"error": "session expired mid-upload"})
		return
	}
	if h.stats != nil {
		h.stats.IncrementFileShared(code)
	}
	item.Mine = true
	c.JSON(http.StatusCreated, item)
}

// AddTextContent — POST /api/v1/sessions/:code/content/text
func (h *ContentHandler) AddTextContent(c *gin.Context) {
	lk, ok := h.requireLiveSession(c, redisclient.LookupOpts{})
	if !ok {
		return
	}
	ctx := c.Request.Context()
	code := c.Param("code")

	var req models.AddTextContentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	res, err := h.store.ReserveQuota(ctx, code, "texts", h.cfg.MaxTextItems, 0, h.cfg.MaxSessionBytes, lk.TTL)
	if err != nil {
		c.JSON(http.StatusGone, gin.H{"error": "session expired"})
		return
	}
	if res != redisclient.ReserveOK {
		c.JSON(http.StatusConflict, gin.H{"error": "this session has reached its note limit", "code": "too_many_files"})
		return
	}

	item := models.ContentItem{
		ID:          uuid.NewString(),
		SessionCode: code,
		Kind:        models.ContentText,
		ContentType: req.ContentType,
		TextPayload: req.Text,
		CreatedAt:   time.Now().UTC(),
		UploaderID:  participantID(c),
		Who:         whoHeader(c),
	}
	if err := h.store.AddContent(ctx, item, lk.TTL); err != nil {
		h.store.ReleaseQuota(ctx, code, "texts", 0)
		c.JSON(http.StatusGone, gin.H{"error": "session expired"})
		return
	}
	if h.stats != nil {
		h.stats.IncrementTextShared(code)
	}
	item.Mine = true
	c.JSON(http.StatusCreated, item)
}

// CancelOrDeleteContent — DELETE /api/v1/sessions/:code/content/:id
// Only the uploader (the browser that shared the item) can remove it, whether
// it is finished or still pending (cancel / retry). Removing frees its quota.
func (h *ContentHandler) CancelOrDeleteContent(c *gin.Context) {
	if _, ok := h.requireLiveSession(c, redisclient.LookupOpts{}); !ok {
		return
	}
	ctx := c.Request.Context()
	code, id := c.Param("code"), c.Param("id")
	pid := participantID(c)
	isUploader := func(it *models.ContentItem) bool { return pid != "" && it.UploaderID == pid }

	// Pending upload (cancelled / failed before completion).
	if pending, err := h.store.GetPendingUpload(ctx, code, id); err == nil {
		if !isUploader(pending) {
			c.JSON(http.StatusForbidden, gin.H{"error": "only the uploader can remove this"})
			return
		}
		if _, err := h.store.TakePendingUpload(ctx, code, id); err == nil {
			_ = h.s3.DeleteObject(ctx, pending.StorageKey)
			h.store.ReleaseQuota(ctx, code, "files", pending.SizeBytes)
		}
		c.JSON(http.StatusOK, gin.H{"status": "removed"})
		return
	}

	item, err := h.store.GetContentItem(ctx, code, id)
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "content not found or expired"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
	if !isUploader(item) {
		c.JSON(http.StatusForbidden, gin.H{"error": "only the uploader can delete this"})
		return
	}
	if item.Kind == models.ContentFile {
		if err := h.s3.DeleteObject(ctx, item.StorageKey); err != nil {
			slog.Error("delete object failed (orphan sweep will retry)", "error", err)
		}
	}
	if err := h.store.RemoveContent(ctx, code, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to remove item"})
		return
	}
	if item.Kind == models.ContentFile {
		h.store.ReleaseQuota(ctx, code, "files", item.SizeBytes)
	} else {
		h.store.ReleaseQuota(ctx, code, "texts", 0)
	}
	c.JSON(http.StatusOK, gin.H{"status": "removed"})
}

// ListContent — GET /api/v1/sessions/:code/content
// Two round trips total: (lockout + session + ids), then one MGET for all items.
func (h *ContentHandler) ListContent(c *gin.Context) {
	lk, ok := h.requireLiveSession(c, redisclient.LookupOpts{ContentIDs: true})
	if !ok {
		return
	}
	items, err := h.store.GetContentItems(c.Request.Context(), c.Param("code"), lk.ContentIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list content"})
		return
	}
	pid := participantID(c)
	for i := range items {
		items[i].Mine = pid != "" && items[i].UploaderID == pid
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// DownloadContent — GET /api/v1/sessions/:code/content/:id/download
// Redirects to a short-lived pre-signed GET URL rather than proxying the file.
func (h *ContentHandler) DownloadContent(c *gin.Context) {
	if _, ok := h.requireLiveSession(c, redisclient.LookupOpts{}); !ok {
		return
	}
	code := c.Param("code")
	id := c.Param("id")

	item, err := h.store.GetContentItem(c.Request.Context(), code, id)
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "content not found or expired"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup failed"})
		return
	}
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
}
