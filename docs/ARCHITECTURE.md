# Ephemeral Cross-Device Sharing Platform — Architecture

Stack: **Go 1.25 (Gin)** backend · **React 18 + Vite + Tailwind CSS** frontend (also wrapped for iOS/Android with Capacitor) · **Redis 7** · **PostgreSQL 16** · **S3-compatible object storage** (MinIO locally; AWS S3 or Cloudflare R2 in production)

This document describes the system **as implemented**. Where a design goal is not yet implemented, it is called out explicitly in [§11 Known Limitations](#11-known-limitations-and-future-work).

---

## 1. Goals and Constraints

| Requirement | How it is met |
|---|---|
| Share files + text across devices using a short code | 6–8 char Crockford base32 code, generated with `crypto/rand` |
| Two modes: Quick Transfer, Shared Workspace | `type` field on the session; Shared Workspace adds participant limit + live feed |
| Every session lives **exactly 5 minutes** | One absolute Redis TTL set at creation and **never extended** |
| Content is **deleted** automatically after expiry | Redis TTL removes metadata; a keyspace-notification worker deletes S3 objects |
| Real-time updates for Shared Workspace | WebSocket per client, fanned out through Redis pub/sub |
| Large files without overloading the API | Browser uploads/downloads directly to object storage via pre-signed URLs |
| Secure | Client-side AES-GCM encryption, rate limiting, CORS + WS origin checks, size limits, owner-only termination, production boot checks |

---

## 2. High-Level Architecture

```
          ┌──────────────────────────────────────────────┐
          │  Browser / Capacitor app (React + Tailwind)  │
          │  - Web Crypto: PBKDF2 key derivation,        │
          │    AES-GCM encrypt/decrypt                   │
          └───────┬──────────────────┬───────────────┬───┘
     REST (JSON)  │      WebSocket   │               │  PUT/GET encrypted bytes
                  │                  │               │  (pre-signed URLs, direct)
                  ▼                  ▼               │
          ┌──────────────────────────────────┐       │
          │        Go / Gin API server       │       │
          │  handlers/  session, content, ws │       │
          │  middleware/ per-IP rate limit   │       │
          │  ws/        per-session hub      │       │
          │  CleanupWorker (goroutines)      │       │
          └───┬───────────────┬──────────┬───┘       │
              │               │          │           │
              ▼               ▼          ▼           ▼
      ┌─────────────┐ ┌─────────────┐ ┌──────────────────────┐
      │   Redis     │ │ PostgreSQL  │ │ Object storage        │
      │ live state, │ │ analytics   │ │ (MinIO / S3 / R2)     │
      │ TTL, pub/sub│ │ only        │ │ encrypted file blobs  │
      │ expiry evts │ │ (async)     │ │                       │
      └─────────────┘ └─────────────┘ └──────────────────────┘
```

### Responsibilities of each store

| Store | Role | Why this store |
|---|---|---|
| **Redis** | **Source of truth** for everything live: session record, content list, content items, pending uploads, participants. Also the pub/sub bus for WebSocket fanout and the source of expiry events. | Native per-key TTL is the perfect primitive for "exactly 5 minutes". In-memory speed for the hot path. Pub/sub lets multiple API instances share real-time events. |
| **PostgreSQL** | Durable **analytics only**: one row per session with hashed code, type, timestamps, active flag, and text/file share counters. **Never** the shared content. | Data that must survive past the 5-minute window (traffic stats). Relational and queryable. |
| **Object storage** | Encrypted file bytes, under `sessions/{code}/{contentId}/{encryptedFilename}`. | Cheap, scalable blob storage; pre-signed URLs keep large files off the API servers. |

**Design principle:** Postgres is off the critical path. All Postgres writes run in background goroutines with a 5s timeout (`analytics.Store.run`). If Postgres is down at boot the server logs a warning and runs without analytics — creating, sharing, and expiring sessions still work on Redis alone.

---

## 3. Code Layout

```
backend/
  cmd/server/main.go             Boot: config, Redis ping + keyspace-notification check,
                                 S3 client, optional Postgres, cleanup worker, router
  internal/config/               Env-based config + CheckProductionSafety()
  internal/models/               Session, ContentItem, request/response DTOs
  internal/handlers/
    router.go                    Routes, CORS, rate-limit wiring, /healthz
    session.go                   Create / Get / Join / Delete session
    content.go                   Upload URL, complete upload, text, list, download
    websocket.go                 WS upgrade with origin check
    cleanup.go                   Expiry listener + periodic Postgres sweep
    codegen.go                   Secure random access codes
  internal/redisclient/store.go  All Redis keys, TTL logic, pub/sub publish
  internal/storage/s3.go         S3 client (internal vs public endpoint), presign, delete
  internal/ws/hub.go             Per-session WebSocket hub fed by Redis pub/sub
  internal/analytics/store.go    Async, best-effort Postgres writes
  internal/middleware/ratelimit.go  Token bucket per IP
  migrations/                    001_init.sql, 002_analytics.sql

frontend/src/
  pages/        CreateSessionPage, JoinSessionPage, SessionRoomPage
  components/   UploadWidget, ContentFeedItem, ConfirmDialog, Toast, Theme toggle
  hooks/        useSessionSocket (WS + reconnect), useCountdown, useTheme
  lib/          api.js (REST client), crypto.js (Web Crypto), format.js
```

---

## 4. Session Lifecycle

```
 POST /sessions                                     T = 0
   ├─ generate code (crypto/rand, collision check in Redis)
   ├─ SET session:{code} <json> EX 300             ← the ONE clock that matters
   ├─ Set-Cookie: owner_token (HttpOnly, Secure)
   └─ async INSERT analytics row in Postgres

 ACTIVE  (0 – 300 s)
   ├─ content / participant keys written with TTL = parent's *remaining* TTL
   ├─ nothing ever calls EXPIRE to *extend* the session
   └─ frontend counts down from expires_at; red warning in the last 30 s

 EXPIRY  (T = 300 s)          ─── or ───   EARLY END (owner DELETE)
   ├─ Redis removes session:{code}           ├─ verify owner_token cookie
   ├─ Redis emits __keyevent@0__:expired     ├─ delete S3 objects under prefix
   ├─ CleanupWorker on every instance:       ├─ DEL all Redis keys for session
   │    • delete S3 objects under prefix     ├─ PUBLISH ws:{code} session_ended
   │    • mark Postgres row inactive         └─ mark Postgres row inactive
   │    • broadcast session_expired to WS
   │      clients and close them
   └─ PeriodicSweep (every 1 min) marks any missed Postgres rows inactive
```

**Why the TTL is never extended:** if adding content refreshed the TTL, a session could be kept alive forever. Instead, every child key copies the parent's *remaining* TTL (`Store.AddContent`, `AddPendingUpload`, `JoinParticipant`), so nothing can outlive the session.

**Inaccessibility is instant even if cleanup is late:** every content endpoint first calls `requireLiveSession`, which does a Redis `GET session:{code}`. Redis treats an expired key as missing on access, so the API returns `410 Gone` exactly at expiry, even if the background expiry event is a little late.

---

## 5. Data Model

### 5.1 Redis (all keys TTL-bound to the session)

| Key | Type | Content |
|---|---|---|
| `session:{code}` | STRING (JSON) | `code, type, max_participants, created_at, expires_at, owner_token` |
| `session:{code}:content` | LIST | Ordered content IDs (feed order) |
| `session:{code}:content:{id}` | STRING (JSON) | `id, kind (file/text), content_type, size_bytes, storage_key, text_payload (ciphertext), filename (encrypted envelope), created_at` |
| `session:{code}:pending:{id}` | STRING (JSON) | File item whose bytes are still uploading — hidden from the feed |
| `session:{code}:participants` | SET | Participant UUIDs (Shared Workspace) |
| `ws:{code}` | pub/sub channel | `content_added`, `participant_joined`, `session_ended` |

Writes that touch more than one key use `TxPipeline` (MULTI/EXEC), so they are applied together.

Internal fields (`owner_token`, `storage_key`) are tagged `json:"-"` on the API models so they never appear in responses. Separate `sessionRecord`/`contentRecord` structs keep them when writing to Redis.

### 5.2 PostgreSQL (analytics)

```sql
sessions (
  id                 UUID PK DEFAULT gen_random_uuid(),
  code_hash          TEXT,          -- SHA-256 of the code; raw code is never stored
  type               TEXT CHECK (type IN ('quick_transfer','shared_workspace')),
  max_participants   INT,
  created_at         TIMESTAMPTZ,
  expires_at         TIMESTAMPTZ,
  expired_at         TIMESTAMPTZ NULL,
  session_token_hash TEXT,          -- SHA-256 of owner token
  is_active          BOOLEAN,
  text_shared_count  INT,
  file_shared_count  INT
)
-- partial index on (code_hash) WHERE is_active, plus is_active and created_at indexes
```

Codes are reused after a session expires, so `code_hash` is **not** unique (migration 002 drops the unique constraint). Updates always filter on `is_active = true` so they never touch an old row with the same code.

`content_items` and `rate_limit_events` tables exist in `001_init.sql` but are **not used** by the current code. Content metadata stays in Redis only, so it disappears automatically.

### 5.3 Object storage

- Key: `sessions/{code}/{contentId}/{encryptedFilenameEnvelope}`. Putting the code in the prefix lets cleanup delete a whole session with one prefix list.
- All objects are opaque ciphertext uploaded as `application/octet-stream`.

---

## 6. Data Flows

### 6.1 Create a session
1. User picks a mode (and max participants for Shared Workspace; default 10).
2. `POST /api/v1/sessions` → code generated, stored in Redis with TTL, `owner_token` set as an HttpOnly cookie.
3. Frontend stores an `owner:{code}` flag in `sessionStorage` only to show the "End session" button. The real authorization is the cookie.
4. Navigate to `/session/{code}`.

### 6.2 Join
1. User enters a code or pastes a link. `JoinSessionPage` extracts the code and validates it with `GET /sessions/:code`.
2. `SessionRoomPage` calls `GET /sessions/:code`. For a Shared Workspace it also calls `POST /join`, which checks `max_participants` and returns `409` when full.
3. The browser derives the AES key from the code (PBKDF2), loads the feed, and opens the WebSocket.

### 6.3 Upload a file (two-phase, direct to storage)
```
Browser                                   API                      Redis            S3
  │ encrypt bytes (AES-GCM)                │                         │               │
  │ encrypt {name, mime} → envelope        │                         │               │
  │── POST /content/upload-url ───────────▶│ check live, size ≤ max  │               │
  │                                        │ presign PUT (2 min)     │               │
  │                                        │── SET pending:{id} ────▶│               │
  │◀── {content_id, upload_url} ───────────│                         │               │
  │── PUT ciphertext (XHR, progress bar) ──────────────────────────────────────────▶│
  │── POST /content/{id}/complete ────────▶│── GET pending ─────────▶│               │
  │                                        │── HEAD object ─────────────────────────▶│ real size
  │                                        │   size > max → delete object, 413       │
  │                                        │── GETDEL pending ──────▶│ (only once)   │
  │                                        │── RPUSH + SET item ────▶│               │
  │                                        │── PUBLISH content_added▶│               │
  │◀── 201 item ───────────────────────────│                         │               │
```
Why two phases:
- Other users never see a half-uploaded file.
- The server checks the **actual** stored size with `HEAD`, because a pre-signed PUT URL does not enforce the declared size.
- `GETDEL` is atomic, so a repeated `/complete` call cannot add or broadcast the file twice.

### 6.4 Share text
The browser trims outer blank lines, classifies the text as `note` (single line) or `snippet` (multi-line) **before** encrypting, encrypts it, and sends base64 ciphertext to `POST /content/text` (max 64 KB). The server stores it inline in Redis. No object storage is used.

### 6.5 Download a file
1. `GET /content/:id/download?redirect=false` → JSON `{download_url}` (pre-signed GET, 60 s).
   - Without `redirect=false` the endpoint returns a 302 redirect. JS uses the JSON form because `fetch` following a cross-origin redirect sends `Origin: null`, which the storage CORS policy rejects.
2. `fetch(url)` → ciphertext → `decryptBytes` → `Blob` → download using the decrypted filename and MIME type.

### 6.6 Real-time updates
```
API instance A                 Redis                    API instance B
 AddContent ── PUBLISH ws:{code} ──▶ ── message ──▶ Hub(code).broadcast → its WS clients
            ◀── message ── Hub(code).broadcast → its WS clients
```
- `ws.Hub`: one per session code **per instance**. It is created on the first WS connection, subscribes to `ws:{code}`, and is torn down when its last client disconnects.
- Any instance can publish; every instance with clients in that session relays the event. This is why the WebSocket tier scales horizontally.
- Client (`useSessionSocket`): on `content_added` it refetches the list (the event carries only metadata; the client re-reads the source of truth). On `session_ended`/`session_expired` it redirects home. On a dropped connection it reconnects with exponential backoff (1 s → 10 s max) and refetches the feed to catch up on missed events.
- The WebSocket upgrade checks `Origin` against the same `ALLOWED_ORIGIN` list as CORS.

### 6.7 Expiry and cleanup
- Redis is started with `notify-keyspace-events Ex`. At boot, `verifyKeyspaceNotifications` fails startup if the setting is missing. If the provider blocks `CONFIG GET`, it logs a loud warning instead.
- `CleanupWorker.Run` subscribes to `__keyevent@0__:expired`. It reacts only to the top-level `session:{code}` key (exactly one `:`) and ignores child keys.
- `cleanupSession`: delete S3 objects → mark Postgres row ended → `ws.NotifyExpired` (broadcast + close local sockets).
- Every instance receives the expiry event. S3 deletes are idempotent, so duplicate work is safe.
- `PeriodicSweep` (every 60 s) marks Postgres rows inactive when `expires_at` has passed. This is a backstop for analytics accuracy only; it does **not** delete S3 objects.

---

## 7. API Reference

Base URL: `/api/v1`. Errors are `{"error": "..."}`.

| Method | Path | Rate limit | Success | Notable errors |
|---|---|---|---|---|
| POST | `/sessions` body `{type, max_participants?}` | 60/min/IP | `201 {code, type, expires_at}` + `owner_token` cookie | 400 invalid type |
| GET | `/sessions/:code` | 120/min/IP | `200 {code, type, max_participants, expires_at, ttl_seconds}` | 404 not found/expired |
| POST | `/sessions/:code/join` | 120/min/IP | `200 {participant_id, session}` | 404, 409 at capacity, 410 |
| DELETE | `/sessions/:code` | — | `200 {status:"terminated"}` | 403 not owner, 404 |
| POST | `/sessions/:code/content/upload-url` body `{filename, content_type, size_bytes}` | — | `200 {content_id, upload_url, expires_in_seconds}` | 400, 410, 413 |
| POST | `/sessions/:code/content/:id/complete` | — | `201 item` | 404, 409 not uploaded yet, 410, 413 |
| POST | `/sessions/:code/content/text` body `{content_type: note\|snippet\|url\|clipboard, text ≤ 64KB}` | — | `201 item` | 400, 410 |
| GET | `/sessions/:code/content` | — | `200 {items: [...]}` | 410 |
| GET | `/sessions/:code/content/:id/download[?redirect=false]` | — | `302` → pre-signed URL, or `200 {download_url}`; text items return the item | 404, 410 |
| GET | `/ws/sessions/:code` (WebSocket) | — | events: `content_added`, `participant_joined`, `session_ended`, `session_expired` | 410 |
| GET | `/healthz` | — | `200 {status:"ok"}` | — |

---

## 8. Security

### 8.1 Access codes
- Alphabet: Crockford base32 (`0-9 A-Z` without `I L O U`), so codes are easy to read and type. 256 is a multiple of 32, so `byte % 32` has **no modulo bias**.
- Length: `CODE_LENGTH` (default 6 in `config.go`, 8 in `docker-compose.yml`).
  - 6 chars = 32⁶ ≈ 1.07 billion codes (~30 bits).
  - 8 chars = 32⁸ ≈ 1.1 trillion codes (~40 bits).
- Generated with `crypto/rand`. Up to 5 retries if the code is already live in Redis.
- **Guessing resistance** = code space × 5-minute lifetime × rate limit. One IP at 120 lookups/min makes about 600 guesses in a session's lifetime. Even with 1,000 live sessions, the chance of hitting any 6-char code is about 600 × 1,000 / 1.07 × 10⁹ ≈ 0.06%. With 8 chars it is about 0.00005%.

### 8.2 Client-side encryption (`frontend/src/lib/crypto.js`)
- Key = `PBKDF2-SHA256(code, fixed app salt, 100,000 iterations)` → AES-GCM-256, non-extractable `CryptoKey`.
- Each payload gets a random 12-byte IV; stored as `IV || ciphertext+tag`. GCM gives both confidentiality and integrity: tampered ciphertext fails to decrypt.
- Encrypted: file bytes, filename + MIME type (as one encrypted envelope), and text payloads.
- **Threat model (honest):** the key comes from the code, and the code is in every request URL. So:
  - ✅ Protects against anyone who sees only stored data or storage traffic (a leaked bucket, storage provider, Redis dump, misconfigured public bucket).
  - ❌ **Not** zero-knowledge against the server operator. Anyone with the code can derive the key.
  - Encryption strength is bounded by code entropy (~30–40 bits). It is not a full 256-bit secret.
  - The server can still see metadata: session code, number of items, ciphertext sizes, timing, and the note/snippet label.
- **Trade-off chosen:** an earlier version generated a random key and passed it in the URL fragment (`#k=…`), which browsers never send to the server. That was true end-to-end encryption, but a code typed by hand could not decrypt anything. This broke the core "type a short code on another device" flow. The current design favors that UX. A future option is to offer both modes: share the full link for E2E, or use the code only for convenience.

### 8.3 Authorization
- **The code is the credential** for viewing and adding content. There are no accounts, which matches the "share a code" UX.
- **Owner-only termination:** a random UUID `owner_token` is sent as an `HttpOnly; Secure` cookie (JavaScript cannot read it) and compared with the token stored in Redis on `DELETE`. Postgres stores only its SHA-256 hash.

### 8.4 Abuse controls
- Per-IP token-bucket rate limiting (`golang.org/x/time/rate`) on create (60/min) and on lookup/join (120/min).
- Upload size limit (`MAX_UPLOAD_MB`, default 200 MB). The declared size is checked before presigning, and the **actual stored size** is checked with `HEAD` after upload. Oversized objects are deleted.
- Text limited to 64 KB. `content_type` is restricted to an allow-list (`note|snippet|url|clipboard`).
- Request bodies are validated with Gin binding tags (`required`, `oneof`, `max`, `min`).
- Short-lived pre-signed URLs: PUT 2 min, GET 60 s, scoped to one object key.

### 8.5 Transport and deployment hardening
- CORS and the WebSocket `CheckOrigin` both use an explicit `ALLOWED_ORIGIN` list.
- TLS is terminated at a reverse proxy or platform load balancer. The frontend derives `wss://` from an `https://` `VITE_API_URL` automatically.
- `GIN_MODE=release` runs `CheckProductionSafety()` and **refuses to boot** with:
  - default MinIO credentials,
  - the default Postgres password,
  - localhost or `http://` CORS origins,
  - a localhost `S3_PUBLIC_ENDPOINT`.
- Boot also fails if Redis keyspace notifications are off, so a broken cleanup setup cannot fail silently.

---

## 9. Scalability and Reliability

| Concern | Approach |
|---|---|
| Many API instances | API instances keep no per-request state (session state lives in Redis), so they can sit behind a load balancer. WebSocket events go through Redis pub/sub, so clients of the same session can connect to different instances. |
| Large files | Bytes never pass through Go. The browser talks directly to S3/R2 with pre-signed URLs, so API memory and bandwidth stay flat whatever the file size. |
| Hot path latency | Every request path uses Redis only. Postgres writes are async and best-effort. |
| Postgres outage | The app keeps working; analytics are skipped and the failure is logged. |
| Missed expiry event | `requireLiveSession` still returns 410 at the exact expiry moment. The periodic sweep fixes analytics state. |
| Half-finished uploads | The pending key expires with the session. Unconfirmed objects are deleted with the session prefix. |
| Duplicate completes | `GETDEL` makes completion idempotent. |
| WebSocket drops | Client reconnects with backoff and refetches the feed. Server closes sockets on write error. |
| Late expiry delivery | Redis emits `expired` when it actually removes the key (on access or by its background sampler). This can lag by a short time under load, but the data is already inaccessible. |

**Scaling path:**
1. Run N stateless API containers behind an ALB or Nginx.
2. Use managed Redis with keyspace notifications enabled.
3. Use S3 or R2 for storage (R2 has no egress fees, which suits download-heavy traffic).
4. Use managed Postgres.
5. Move rate-limit counters into Redis (see §11) so limits are global instead of per instance.
6. If Redis becomes the bottleneck, shard by session code. All keys for one session share the `session:{code}` prefix. For Redis Cluster they would need a hash tag like `session:{CODE}` so they land on the same slot.

---

## 10. Deployment

- `docker-compose.yml` (**local dev only**):
  - `postgres` (migrations auto-run from `backend/migrations` on first volume init)
  - `redis` (`--notify-keyspace-events Ex`)
  - `minio` + `minio-init` (creates the bucket and its CORS policy)
  - `backend`, built with a multi-stage Go Dockerfile into a small Alpine image
  - `frontend`, a Vite build served by Nginx with SPA fallback
- Production: see [`PRODUCTION.md`](../PRODUCTION.md) and [`.env.production.example`](../.env.production.example). TLS is handled by the platform load balancer or reverse proxy.
- Mobile: `frontend/capacitor.config.json` plus the `ios/` and `android/` projects wrap the same web build (`npm run cap:ios` / `cap:android`).
- All behaviour is configured through environment variables: TTL, upload limit, rate limit, code length, origins, and S3 endpoints. Setting `SESSION_TTL_SECONDS` to a small value makes expiry quick to test.

---

## 11. Known Limitations and Future Work

These are known gaps between the current code and a hardened production system:

1. **Rate limiter is in-memory and per instance.**
   - With N instances the effective limit is N×.
   - The `visitors` map is never pruned, so memory grows with unique IPs.
   - Content and WebSocket endpoints are not rate limited.
   - *Fix:* Redis-based sliding window, plus a lockout after repeated 404s on code lookups.
2. **Quick Transfer is not enforced as single-uploader.** Anyone with the code can upload in either mode. *Fix:* only allow uploads from the holder of `owner_token` in Quick Transfer mode.
3. **Participant-limit race.** `SCARD` and `SADD` are separate calls, so concurrent joins can exceed the limit. Participants are never removed, and a page refresh joins again with a new ID. *Fix:* a Lua script doing an atomic check-and-add; remove the participant on WS disconnect; reuse a participant ID kept in `sessionStorage`.
4. **No storage lifecycle backstop.**
   - If an expiry event is missed while no instance is listening, the S3 objects stay behind.
   - `DeleteSessionObjects` reads one `ListObjectsV2` page (1,000 keys).
   - *Fix:* a bucket lifecycle rule (expire `sessions/` after 1 day), paginated batch delete, and a sweep that lists S3 prefixes for sessions no longer in Redis.
5. **The local MinIO bucket allows anonymous download.** Object keys contain an unguessable UUID plus ciphertext, so the risk is low, but production buckets should be private. Pre-signed GETs already work without public access.
6. **Code hashes in Postgres are unsalted SHA-256.** In a ~30-bit code space they can be reversed offline. *Fix:* HMAC with a server-side secret.
7. **Encryption is not zero-knowledge** (see §8.2). *Fix:* an optional URL-fragment key mode.
8. **No server-side file-type validation.** Content is encrypted, so the server cannot see it. Validation would need to happen on the client before encryption, which is advisory only.
9. **No `session_expiring` WebSocket event.** The client shows its own 30-second warning from `expires_at` instead.
10. **No automated tests yet.** Priorities:
    - Go unit tests for the Redis store using miniredis (TTL capping, `GETDEL` idempotency), plus handler tests with `httptest`;
    - an integration test with a short `SESSION_TTL_SECONDS` to check cleanup end to end;
    - Vitest for the crypto round trip.
11. **Migrations** run only on first Postgres volume init (`docker-entrypoint-initdb.d`). *Fix:* a migration tool such as golang-migrate or goose.
12. **Large files are encrypted in memory** (`file.arrayBuffer()`), which is a problem near the 200 MB limit on mobile. *Fix:* chunked encryption plus S3 multipart upload, which would also allow resumable uploads.
