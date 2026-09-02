# Ephemeral Cross-Device Sharing Platform — Architecture

Stack: **Go (Gin)** backend · **React + Tailwind CSS** frontend · **PostgreSQL** · **Redis** · **S3-compatible object storage (MinIO/S3/R2)**

## 1. High-Level Architecture

```
                         ┌─────────────────────┐
                         │   React + Tailwind   │
                         │   (SPA, Vite build)  │
                         └──────────┬───────────┘
                       HTTPS REST   │   WSS (Shared Workspace)
                                    ▼
                         ┌─────────────────────┐
                         │   Go / Gin API       │
                         │  - REST handlers     │
                         │  - WebSocket hub     │
                         │  - Auth/code checks  │
                         │  - Expiry scheduler  │
                         └──┬───────┬────────┬──┘
                            │       │        │
              ┌─────────────┘   ┌───┘        └───────────┐
              ▼                 ▼                        ▼
      ┌───────────────┐ ┌───────────────┐      ┌──────────────────┐
      │  PostgreSQL   │ │     Redis     │      │  Object Storage  │
      │ (session &    │ │ (live session │      │  (S3/R2/MinIO)   │
      │  content meta,│ │  state, TTL,  │      │  binary file     │
      │  audit trail) │ │  participants,│      │  blobs           │
      │               │ │  pub/sub for  │      │                  │
      │               │ │  WS fanout)   │      │                  │
      └───────────────┘ └───────────────┘      └──────────────────┘
```

**Why both Postgres and Redis?** Redis is the source of truth for *live* session state (TTL-bound, high write/read frequency, participant presence, pub/sub fanout for WebSocket scaling). Postgres holds durable metadata (audit log of sessions created, content records, rate-limit/abuse tracking) that outlives the 5-minute window for analytics/compliance — content blobs and live keys are still purged from Redis/S3 on expiry regardless.

Since every session lives exactly 5 minutes, Redis TTL keys are the natural expiration primitive; Postgres rows are marked `expired_at` by the same cleanup job rather than relying on cron-only deletes.

## 2. Session Lifecycle

```
CREATE ──▶ ACTIVE (5:00 countdown) ──▶ EXPIRING (T-10s warning) ──▶ EXPIRED ──▶ PURGED
   │             │                                                     │
   │             ├─ Quick Transfer: single uploader, N viewers         │
   │             └─ Shared Workspace: N participants, live content feed│
   │                                                                    │
   └── generates access code + Redis key with TTL=300s ─────────────────┘
```

- On creation: generate code → write `session:{code}` hash to Redis with `EXPIRE 300` → insert row in Postgres `sessions` table.
- On every content add / participant join: refresh Redis hash (do **not** extend TTL — expiration must remain a hard, absolute 5 minutes from creation to avoid indefinite renewal abuse).
- On Redis key expiry: use **Redis keyspace notifications** (`notify-keyspace-events Ex`) subscribed by a backend worker to trigger cleanup (delete S3 objects, mark Postgres row expired, close WebSocket connections for that session).
- A secondary sweep job (every 30s) reconciles anything the keyspace-notification path missed (at-least-once cleanup guarantee).

## 3. Data Model

### Redis (ephemeral, TTL = 300s)
```
session:{code}            HASH   { type, max_participants, created_at, owner_token }
session:{code}:content    LIST   [ contentId, contentId, ... ]   (ordered feed)
session:{code}:content:{id} HASH { kind, meta, storage_key, uploaded_at }
session:{code}:participants SET  { participant_id, ... }
```
All keys share the same TTL set at creation; content/participant keys are set with the *remaining* TTL of the parent so nothing outlives the session.

### PostgreSQL (durable, audit-oriented)
```sql
sessions (
  id UUID PK,
  code_hash TEXT,          -- hashed, never store raw code at rest
  type TEXT CHECK (type IN ('quick_transfer','shared_workspace')),
  max_participants INT,
  created_at TIMESTAMPTZ,
  expires_at TIMESTAMPTZ,
  expired_at TIMESTAMPTZ NULL,
  creator_ip_hash TEXT
)

content_items (
  id UUID PK,
  session_id UUID FK,
  kind TEXT CHECK (kind IN ('file','text')),
  content_type TEXT,        -- mime type / subtype (note, url, snippet, etc.)
  size_bytes BIGINT,
  storage_key TEXT NULL,    -- S3 object key, null for text
  text_payload TEXT NULL,   -- only for small text content
  created_at TIMESTAMPTZ
)

rate_limit_events (
  id UUID PK,
  ip_hash TEXT,
  action TEXT,
  created_at TIMESTAMPTZ
)
```
Access codes are never stored in plaintext — only a salted hash — so a DB read alone can't be used to join a live session.

### Object Storage
Key layout: `sessions/{sessionId}/{contentId}/{filename}`. Bucket lifecycle policy also auto-expires objects older than, e.g., 15 minutes as a defense-in-depth backstop in case app-level cleanup fails.

## 4. API Design (REST)

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/v1/sessions` | Create Quick Transfer or Shared Workspace session |
| GET | `/api/v1/sessions/:code` | Resolve code → session metadata + remaining TTL (requires valid code) |
| POST | `/api/v1/sessions/:code/join` | Join a Shared Workspace (checks participant limit) |
| POST | `/api/v1/sessions/:code/content` | Upload file or text content |
| GET | `/api/v1/sessions/:code/content` | List current content feed |
| GET | `/api/v1/sessions/:code/content/:id/download` | Stream/redirect (pre-signed URL) to download a file |
| DELETE | `/api/v1/sessions/:code` | Owner-initiated early termination |
| WS | `/ws/sessions/:code` | Real-time feed for Shared Workspace |

All endpoints except session creation require the access code; file uploads go through a pre-signed S3 PUT URL flow issued by the backend rather than proxying large binaries through Gin.

## 5. Real-Time (Shared Workspace)

- Gin server holds a WebSocket **hub per session code**, backed by Redis **pub/sub channel `ws:{code}`** so the design scales horizontally across multiple backend instances — any instance publishes new-content events, all instances' hubs relay to their locally connected clients.
- Events: `content_added`, `participant_joined`, `participant_left`, `session_expiring` (T-10s), `session_expired` (force-disconnect).
- Heartbeat/ping-pong every 20s to detect dead connections and free hub memory promptly.

## 6. Security

- **End-to-end encryption**: file bytes, text payloads, and filenames/MIME types are AES-GCM-256 encrypted client-side (Web Crypto API) before any request leaves the browser. The AES key is generated in the browser, never transmitted to the backend, and travels between devices only in the URL fragment (`#k=...`) — a location browsers exclude from requests, server logs, and `Referer` headers. Server, Redis, and S3/MinIO see only ciphertext. Trade-off: session codes, content counts, sizes, and timing remain visible to the server; only payload confidentiality is end-to-end.
- **Codes**: 8-char base32 (Crockford), generated via `crypto/rand`, collision-checked against active Redis keys before assignment. Stored hashed (bcrypt/HMAC) in Postgres.
- **Brute-force protection**: rate limit code lookups per IP (e.g., 10/min) + exponential lockout; failed-attempt counter in Redis.
- **Authorization**: joining a session requires the code as a bearer-style header/token; the code itself is the credential — no separate accounts needed, matching the "share a code" UX.
- **Uploads**: size caps (configurable, e.g., 200MB/file), MIME allow-list, virus/heuristic scanning hook, server-side validation of pre-signed request scope (session-locked prefix).
- **Transport**: HTTPS/WSS only, HSTS, CORS locked to frontend origin.
- **Data-at-rest**: S3 encryption enabled; text payloads in Postgres/Redis encrypted at the disk/volume level.
- **No indefinite persistence**: nothing related to a session's content should exist in any store beyond a short grace window past expiry.

## 7. Scalability & Reliability

- Stateless Gin instances behind a load balancer; Redis pub/sub makes WebSocket fanout horizontal-scale-safe.
- Redis Cluster / managed Redis for session state at higher scale; Postgres read replica for reporting if needed.
- Object storage handles large binaries directly (client uploads via pre-signed URL), keeping API instances lightweight.
- Idempotent cleanup worker + backstop bucket lifecycle rule = cleanup is guaranteed even under partial failure.

## 8. Deployment

- Docker Compose services: `frontend`, `backend`, `postgres`, `redis`, `minio` (local dev stand-in for S3), `nginx` (TLS termination + reverse proxy).
- Environment-based config (`.env`) for storage endpoint, DB DSN, Redis URL, TTL seconds (configurable for testing), rate-limit thresholds.
- Health-check endpoints (`/healthz`, `/readyz`) for orchestration.

## 9. Frontend (React + Tailwind)

- Pages: **Create Session** (choose Quick Transfer / Shared Workspace + config), **Join by Code**, **Session Room** (countdown timer, content feed, upload widget, download list).
- Countdown implemented client-side from `expires_at`, corrected against server clock skew on load; WebSocket `session_expiring`/`session_expired` events drive redirect/lock UI.
- Upload progress via `XMLHttpRequest`/`fetch` progress events against the pre-signed URL.
