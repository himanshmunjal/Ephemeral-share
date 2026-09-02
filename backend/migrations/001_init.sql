CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL CHECK (type IN ('quick_transfer', 'shared_workspace')),
    max_participants INT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    expired_at TIMESTAMPTZ,
    creator_ip_hash TEXT
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions (expires_at) WHERE expired_at IS NULL;

CREATE TABLE IF NOT EXISTS content_items (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES sessions (id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('file', 'text')),
    content_type TEXT NOT NULL,
    size_bytes BIGINT,
    storage_key TEXT,
    text_payload TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_content_items_session_id ON content_items (session_id);

CREATE TABLE IF NOT EXISTS rate_limit_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    ip_hash TEXT NOT NULL,
    action TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_rate_limit_ip_action ON rate_limit_events (ip_hash, action, created_at);
