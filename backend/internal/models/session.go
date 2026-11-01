package models

import "time"

type SessionType string

const (
	QuickTransfer   SessionType = "quick_transfer"
	SharedWorkspace SessionType = "shared_workspace"
)

type ContentKind string

const (
	ContentFile ContentKind = "file"
	ContentText ContentKind = "text"
)

// Session is the live, Redis-backed representation of an active session.
type Session struct {
	Code            string      `json:"code"`
	Type            SessionType `json:"type"`
	MaxParticipants int         `json:"max_participants"`
	CreatedAt       time.Time   `json:"created_at"`
	ExpiresAt       time.Time   `json:"expires_at"`
	OwnerToken      string      `json:"-"`
}

// Limits are the per-session quotas exposed to clients so the UI can
// pre-validate. The server enforces them regardless.
type Limits struct {
	MaxFileBytes    int64 `json:"max_file_bytes"`
	MaxFiles        int   `json:"max_files"`
	MaxSessionBytes int64 `json:"max_session_bytes"`
}

// ContentItem represents one piece of shared content (file or text) in a session.
type ContentItem struct {
	ID          string      `json:"id"`
	SessionCode string      `json:"session_code"`
	Kind        ContentKind `json:"kind"`
	ContentType string      `json:"content_type"` // mime type or subtype: note, url, snippet
	SizeBytes   int64       `json:"size_bytes,omitempty"`
	StorageKey  string      `json:"-"`
	TextPayload string      `json:"text_payload,omitempty"`
	// Filename carries the client-side-encrypted {name, mime} envelope for
	// file items (base64url AES-GCM ciphertext) — opaque to the server, which
	// never sees the real filename or MIME type. Doubles as the S3 key suffix.
	Filename    string    `json:"filename,omitempty"`
	DownloadURL string    `json:"download_url,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// UploaderID is the random per-browser id sent in X-Participant-ID. It is
	// never serialised to other clients (that would let anyone spoof it to
	// delete someone else's item); they only get the computed Mine flag.
	UploaderID string `json:"-"`
	Mine       bool   `json:"mine,omitempty"`
	// Who is the uploader's display identity, encrypted in the browser with
	// the session key (opaque to the server, like filenames and text).
	Who string `json:"who,omitempty"`
}

// CreateSessionRequest is the payload for POST /api/v1/sessions
type CreateSessionRequest struct {
	Type            SessionType `json:"type" binding:"required,oneof=quick_transfer shared_workspace"`
	MaxParticipants int         `json:"max_participants"`
}

// CreateSessionResponse returned after successful creation.
type CreateSessionResponse struct {
	Code       string      `json:"code"`
	Type       SessionType `json:"type"`
	ExpiresAt  time.Time   `json:"expires_at"`
	OwnerToken string      `json:"owner_token"`
}

// AddTextContentRequest for text/note/url/snippet uploads.
type AddTextContentRequest struct {
	ContentType string `json:"content_type" binding:"required,oneof=note snippet url clipboard"`
	Text        string `json:"text" binding:"required,max=65536"`
}

// UploadURLRequest asks the backend for a pre-signed PUT URL.
type UploadURLRequest struct {
	Filename    string `json:"filename" binding:"required"`
	ContentType string `json:"content_type" binding:"required"`
	SizeBytes   int64  `json:"size_bytes" binding:"required,min=1"`
}

type UploadURLResponse struct {
	ContentID string `json:"content_id"`
	UploadURL string `json:"upload_url"`
	ExpiresIn int    `json:"expires_in_seconds"`
}
