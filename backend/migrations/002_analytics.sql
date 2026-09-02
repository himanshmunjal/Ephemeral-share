-- Analytics-only columns on `sessions`. Redis remains the source of truth for
-- live session state and content; Postgres here only tracks aggregate
-- traffic counters that must survive past a session's TTL — never the
-- shared content itself.
ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS session_token_hash TEXT,
    ADD COLUMN IF NOT EXISTS is_active BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS text_shared_count INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS file_shared_count INT NOT NULL DEFAULT 0;

-- Short codes get recycled once a session expires (see GenerateUniqueCode,
-- which only checks Redis for *currently live* codes), so the same code text
-- legitimately appears in multiple historical rows here. A UNIQUE constraint
-- on code_hash would silently drop every session that reused an old code.
ALTER TABLE sessions DROP CONSTRAINT IF EXISTS sessions_code_hash_key;

CREATE INDEX IF NOT EXISTS idx_sessions_code_hash_active ON sessions (code_hash) WHERE is_active = true;
CREATE INDEX IF NOT EXISTS idx_sessions_is_active ON sessions (is_active);
CREATE INDEX IF NOT EXISTS idx_sessions_created_at ON sessions (created_at);
