# ephemeral.share — Architecture

Stack: **Go 1.25 (Gin)** backend · **React 18 + Vite + Tailwind CSS** frontend (also wrapped for iOS/Android with Capacitor) · **Redis 7** · **S3-compatible object storage** (MinIO locally; Cloudflare R2 or AWS S3 in production) · optional **PostgreSQL** (aggregate stats) · optional **Sentry**

This document describes the system **as implemented**. Gaps are listed in [§11](#11-known-limitations-and-future-work).

---

## 1. Goals and constraints

| Requirement | How it is met |
|---|---|
| Share files + text across devices with a short code | 8-character Crockford base32 code from `crypto/rand` (`CODE_LENGTH`) |
| Two modes: Quick Transfer, Shared Workspace | `type` on the session; both share the same room, cap and quota machinery |
| Sessions are temporary | One absolute Redis TTL (`SESSION_TTL_SECONDS`, default **1 hour**) set at creation and **never extended** |
| Content is deleted afterwards | Redis TTL removes metadata; a keyspace-event worker deletes objects; a periodic **orphan sweep** deletes anything left behind |
| Real-time updates and presence | WebSocket per client, fanned out through Redis pub/sub; presence is a heartbeat-backed Redis sorted set |
| Large files without loading the API | Browser uploads/downloads directly to object storage via pre-signed URLs |
| Runs inside free-tier limits | Server-enforced per-file / per-session file-count / per-session byte quotas, participant cap, rate limits, auto-cleanup |
| Hard to abuse | Wrong-code lockout, rate limits, opaque-bytes-only uploads, attachment-only downloads, strict CSP/headers, owner/admin controls |
| No accounts | Anonymous per-browser identifiers and display names (see §8.3) |

---

## 2. High-level architecture

```
          ┌─────────────────────────────────────────────────┐
          │  Browser / Capacitor app (React + Tailwind)     │
          │  Web Crypto: PBKDF2 key derivation, AES-GCM     │
          └───────┬───────────────────┬─────────────────┬───┘
     REST (JSON)  │      WebSocket    │                 │ PUT/GET encrypted bytes
   X-Participant-*│      (?who=…)     │                 │ (pre-signed URLs, direct)
                  ▼                   ▼                 │
          ┌────────────────────────────────────┐        │
          │         Go / Gin API server        │        │
          │ handlers/   session, content, ws   │        │
          │ middleware/ rate limit, headers,   │        │
          │             HTTPS, JSON logging    │        │
          │ ws/         per-session hub        │        │
          │ CleanupWorker (expiry + orphans)   │        │
          │ monitoring/ optional Sentry        │        │
          └───┬──────────────┬───────────┬─────┘        │
              ▼              ▼           ▼              ▼
      ┌─────────────┐ ┌────────────┐ ┌──────────────────────┐
      │   Redis     │ │ PostgreSQL │ │ Object storage       │
      │ live state, │ │ (optional) │ │ (MinIO / S3 / R2)    │
      │ quotas,     │ │ aggregate  │ │ encrypted file blobs │
      │ presence,   │ │ stats only │ │                      │
      │ TTL, pubsub │ │            │ │                      │
      └─────────────┘ └────────────┘ └──────────────────────┘
```

### Responsibilities

| Store | Role |
|---|---|
| **Redis** | Source of truth for everything live: session record, content list/items, pending uploads, per-session usage counters (quota), presence + encrypted display identities, wrong-code lockout counters. Pub/sub bus for WebSocket fan-out; source of expiry events. |
| **Object storage** | Encrypted file bytes under `sessions/{code}/{contentId}/{encryptedFilename}`. |
| **PostgreSQL** | **Optional.** One row per session (hashed code, type, timestamps, share counts) for traffic stats. All writes are async and best-effort; leave `POSTGRES_DSN` unset to run Redis-only. Never holds shared content. |

---

## 3. Code layout

```
backend/
  cmd/server/main.go           Boot: config, safety checks, logging/Sentry, Redis + keyspace check,
                               S3, optional Postgres, cleanup workers, router
  internal/config/             Env config + CheckProductionSafety()
  internal/models/             Session, ContentItem, Limits, request/response DTOs
  internal/handlers/
    router.go                  Routes, CORS, per-route rate limiters, /health, admin route
    session.go                 Create / Get / Join pre-check / Delete / admin takedown
    content.go                 Upload URL (quota reserve), complete, text, list, download, delete/cancel
    websocket.go               Upgrade, atomic presence join, heartbeat
    guard.go                   Wrong-code lockout, participant-id / identity header parsing
    cleanup.go                 Expiry listener, analytics sweep, orphan sweep
    codegen.go                 Secure random access codes
  internal/redisclient/        Keys, TTL, quota Lua, presence, lockout, pub/sub
  internal/storage/s3.go       S3 client (internal vs public endpoint), presign, paginated list/delete
  internal/ws/hub.go           Per-session hub fed by Redis pub/sub; ping/write helpers
  internal/middleware/         ratelimit.go, security.go (headers, HTTPS), logging.go (slog, recovery)
  internal/monitoring/         Sentry with access-code scrubbing
  internal/analytics/          Optional async Postgres writes
  **/*_test.go                 Integration + unit tests (miniredis + fake S3)

frontend/src/
  pages/        CreateSession, JoinSession, SessionRoom, LegalPages (Privacy/Terms/Abuse)
  components/   UploadWidget (queue/cancel/retry/paste), ContentFeedItem, Avatar, SessionGone, ErrorBoundary, …
  hooks/        useSessionSocket (reconnect), useCountdown
  lib/          api.js, crypto.js, identity.js, monitoring.js, format.js
  __tests__/    Vitest suites
```

---

## 4. Session lifecycle

```
 POST /sessions                                         T = 0
   ├─ generate code (crypto/rand, collision check)
   ├─ SET session:{code} <json> EX <ttl>               ← the ONE clock that matters
   ├─ return {code, expires_at, owner_token}            (token returned once, kept in the browser)
   └─ async analytics row (if Postgres is configured)

 ACTIVE
   ├─ child keys (content, pending, usage, presence, identities) carry the parent's *remaining* TTL
   ├─ nothing ever extends the session
   └─ the UI counts down from the server-reported ttl_seconds (anchored to the device clock)

 EXPIRY (TTL elapses)       ─── or ───   EARLY END (host DELETE / admin takedown)
   ├─ Redis drops session:{code}           ├─ verify X-Owner-Token (constant-time) / X-Admin-Token
   ├─ keyspace "expired" event             ├─ delete objects under sessions/{code}/
   ├─ CleanupWorker (each instance):       ├─ DEL all keys, PUBLISH session_ended
   │    delete objects, mark stats ended,  └─ mark stats ended
   │    broadcast session_expired, close sockets
   └─ ORPHAN SWEEP (every ORPHAN_SWEEP_SECONDS, and once at boot) deletes objects whose session no longer
      exists — covers restarts and missed events. Objects younger than ORPHAN_GRACE_SECONDS are skipped.
```

**The TTL is never extended** — every child key copies the parent's remaining TTL, so nothing can outlive the session. **Inaccessibility is instant** even if cleanup lags: every content endpoint first checks `session:{code}`, so the API answers `410` the moment the key is gone. A bucket lifecycle rule (expire after 1 day) is a final backstop.

---

## 5. Data model

### 5.1 Redis (all keys TTL-bound to the session)

| Key | Type | Content |
|---|---|---|
| `session:{code}` | STRING (JSON) | `code, type, max_participants, created_at, expires_at, owner_token` |
| `session:{code}:content` | LIST | Ordered content ids |
| `session:{code}:content:{id}` | STRING (JSON) | `kind, content_type, size_bytes, storage_key, text_payload (ciphertext), filename (encrypted envelope), uploader_id, who (encrypted identity), created_at` |
| `session:{code}:pending:{id}` | STRING (JSON) | File whose bytes are still uploading — hidden from the feed |
| `session:{code}:usage` | HASH | `files`, `texts`, `bytes` counters for quotas (mutated only by Lua) |
| `session:{code}:presence` | ZSET | connection id → last heartbeat (unix seconds) |
| `session:{code}:who` | HASH | connection id → encrypted display identity (opaque ciphertext) |
| `bf:{ip}` | STRING | Wrong-code miss counter; its TTL is the lockout window |
| `ws:{code}` | pub/sub | `presence`, `content_added`, `content_removed`, `session_ended` |

Multi-key writes use `TxPipeline`; check-and-update operations (quota reserve, presence join) are Lua scripts so they are atomic across API replicas. Internal fields (`owner_token`, `storage_key`, `uploader_id`) are `json:"-"` on API models; separate record structs persist them.

### 5.2 PostgreSQL (optional analytics)

One `sessions` row per session: hashed code, type, timestamps, `is_active`, text/file share counters. `content_items` and `rate_limit_events` tables in `001_init.sql` are unused. Writes are fire-and-forget with a 5 s timeout, so Postgres can be down or absent without affecting users.

### 5.3 Object storage

Key: `sessions/{code}/{contentId}/{encryptedFilenameEnvelope}`. Everything is opaque ciphertext stored as `application/octet-stream`. The code in the prefix lets cleanup remove a session with one prefix list; the orphan sweep lists `sessions/` and compares each prefix with Redis.

---

## 6. Data flows

### 6.1 Create
`POST /sessions` → code + `owner_token` in the response body. The browser stores the token in `localStorage` and sends it as `X-Owner-Token`. (A cookie would be a *third-party* cookie when frontend and API are on different domains, which Safari/Firefox block, so a header is used instead.) The participant cap requested by the client is clamped to `MAX_PARTICIPANTS`.

### 6.2 Join and presence
1. `GET /sessions/:code` (metadata, remaining TTL, limits, online count) then `POST /sessions/:code/join` — a **pre-check** that returns `409 full` so the UI can show a friendly screen.
2. The browser derives the AES key, loads the feed, then opens `WS /ws/sessions/:code?who=<encrypted identity>`.
3. On connect the server runs an **atomic presence-join script**: stale heartbeats are swept, and the connection is admitted only if the room has space. Full → `409` before the upgrade. This is the authoritative cap — reloads don't leak slots because slots are tied to live connections, not to join calls.
4. The server pings every 25 s (read deadline 60 s); each pong refreshes the heartbeat. Entries older than 75 s are considered dead, so a crashed instance can't hold slots forever. This also keeps idle-timeout proxies from dropping the socket.
5. Every join/leave publishes a `presence` snapshot `{count, users:[{id, who}]}`; clients decrypt each `who` themselves.

### 6.3 Upload (quota-reserved, two-phase, direct to storage)
```
Browser                                  API                         Redis            Storage
  │ encrypt bytes + {name,mime} + identity │                           │                 │
  │── POST /content/upload-url ───────────▶│ reject if not octet-stream │                 │
  │                                        │ per-file limit             │                 │
  │                                        │── Lua: reserve file slot ─▶│ (count + bytes) │
  │                                        │ presign PUT, Content-Length signed           │
  │                                        │── SET pending:{id} ───────▶│                 │
  │◀── {content_id, upload_url} ───────────│                           │                 │
  │── PUT ciphertext (XHR; abortable) ───────────────────────────────────────────────────▶│
  │── POST /content/{id}/complete ────────▶│── HEAD object ───────────────────────────────▶│ real size
  │                                        │ size ≠ declared → delete, release quota, 413  │
  │                                        │── GETDEL pending, RPUSH + SET item ─▶│        │
  │                                        │── PUBLISH content_added ─▶│                    │
```
- **Quota** is claimed *before* a URL is issued, so concurrent uploads can't both slip under a limit. A failed/cancelled upload calls `DELETE /content/:id`, which removes the pending record and object and releases the quota.
- The presigned PUT signs the declared `Content-Length`, and `complete` re-checks the stored size, so a client can't declare small and upload large.
- `GETDEL` makes completion single-shot; other users never see a half-uploaded file.

### 6.4 Text
The browser classifies (`note`/`snippet`), encrypts, and sends base64 ciphertext (≤ 64 KB) to `POST /content/text`; stored inline in Redis. Counts against `MAX_TEXT_ITEMS_PER_SESSION`.

### 6.5 Download
`GET /content/:id/download?redirect=false` → JSON `{download_url}`: a 60 s presigned GET with `response-content-disposition=attachment` and `application/octet-stream`, so storage never renders a file inline. The browser fetches the ciphertext, decrypts it, and saves the file under its decrypted name.

### 6.6 Deleting
Only the **uploader** — the browser whose `X-Participant-ID` matches the item's recorded uploader — can delete a finished item (`403` for everyone else, including the host). The same rule applies to cancelling a pending upload. The host's power is to end the whole session. Deletion removes the object, the Redis record, releases quota and publishes `content_removed`.

### 6.7 Real-time
Any instance publishes to `ws:{code}`; every instance with clients in that session relays. The client refetches the feed on `content_added`, drops an item on `content_removed`, updates the avatar row on `presence`, and shows a friendly end screen on `session_ended`/`session_expired`. Reconnects use exponential backoff and refetch to catch up; the WebSocket handshake enforces the same origin allow-list as CORS.

---

## 7. API reference

Base `/api/v1`. Errors are `{"error": "...", "code": "<machine-readable>"}`. Optional request headers: `X-Participant-ID` (random per-browser id), `X-Owner-Token` (host), `X-Participant-Who` (encrypted display identity; shape-validated, ≤ 300 chars).

| Method | Path | Limiter | Success | Notable errors |
|---|---|---|---|---|
| POST | `/sessions` `{type, max_participants?}` | creation | `201 {code, type, expires_at, owner_token}` | 400 |
| GET | `/sessions/:code` | lookup | `200 {…, online, ttl_seconds, ttl_total_seconds, limits}` | 404 `not_found`, 429 `locked_out` |
| POST | `/sessions/:code/join` | lookup | `200 {online, max_participants}` | 404, 409 `full` |
| DELETE | `/sessions/:code` | lookup | `200` | 403 not owner, 404 |
| POST | `/sessions/:code/content/upload-url` | uploads | `200 {content_id, upload_url}` | 413 `file_too_large`/`session_full`, 409 `too_many_files`, 415 |
| POST | `/sessions/:code/content/:id/complete` | uploads | `201 item` | 404, 409, 413 |
| POST | `/sessions/:code/content/text` | uploads | `201 item` | 400, 409 |
| DELETE | `/sessions/:code/content/:id` | room | `200` | 403 not uploader, 404 |
| GET | `/sessions/:code/content` | room | `200 {items}` (each with `mine`) | 410 |
| GET | `/sessions/:code/content/:id/download[?redirect=false]` | room | `200 {download_url}` / `302` | 404 |
| WS | `/ws/sessions/:code?who=…` | socket | events: `presence`, `content_added`, `content_removed`, `session_ended`, `session_expired` | 409 `full`, 403 origin, 410 |
| GET | `/health`, `/healthz` | none | `200`; `503` if Redis is down | — |
| DELETE | `/admin/sessions/:code` | 30/min | `200` | 403 (route exists only if `ADMIN_TOKEN` is set) |

Limiter sizes derive from `RATE_LIMIT_PER_MIN`, `UPLOAD_RATE_LIMIT_PER_MIN` and `ROOM_RATE_LIMIT_PER_MIN` (with floors, so a small dev value can't starve a normal page load). Each limiter is an independent per-IP bucket; idle entries are evicted.

---

## 8. Security

### 8.1 Access codes
- Crockford base32 without `I L O U`; `byte % 32` has no modulo bias. Default length **8** (≈ 40 bits).
- Generated with `crypto/rand`, collision-checked against live sessions.
- **Guessing resistance:** every lookup that misses (unknown/expired code) on *any* endpoint — including content and WebSocket — counts against the caller's IP. After `MAX_FAILED_LOOKUPS` (default 10) the IP is locked out for `LOCKOUT_SECONDS` (default 15 min) and gets `429 locked_out` even for valid codes. That caps a single IP at roughly 40 guesses per hour against a space of ~1.1×10¹²; with 1,000 live sessions the odds per IP-hour are on the order of 10⁻⁸. Distributed attackers scale linearly, which is why the per-IP limits sit on top.

### 8.2 Client-side encryption (`frontend/src/lib/crypto.js`)
- Key = `PBKDF2-SHA256(code, fixed app salt, 100,000 iterations)` → non-extractable AES-GCM-256 `CryptoKey`. Fresh 12-byte IV per payload; GCM authenticates, so tampering fails to decrypt.
- Encrypted: file bytes, filename + MIME envelope, note text, and display identities.
- **Threat model (honest):**
  - ✅ Protects against anyone who only obtains stored data or storage traffic: a leaked bucket, a Redis/database dump, a storage provider.
  - ❌ **Not zero-knowledge against the operator.** The code is also the lookup key, so the server receives it on each request (and it appears in the frontend host's request path). The operator could in principle decrypt. We don't store or log the code — logs record route *patterns*, Sentry events are scrubbed — but that is practice, not a technical guarantee. User-facing copy and the Privacy page say so.
  - Strength is bounded by the code's entropy (~40 bits), not a full 256-bit secret.
  - The server sees metadata: session code, item count, ciphertext sizes, timing, note/snippet label, who is online (as opaque ciphertext).
- **Why this trade-off:** an earlier version put a random key in the URL fragment (`#k=…`), which browsers never send to the server — true end-to-end — but a code typed by hand couldn't decrypt anything, breaking the core "type a short code on another device" flow. See §11 for the path to a stronger mode.

### 8.3 Authorization and identity (no accounts)
- **The code is the credential** for viewing and adding content.
- **Host:** the `owner_token` (UUID pair, returned once, compared in constant time) authorises ending the session. It cannot delete other people's items.
- **Uploader:** a random per-browser `X-Participant-ID` (kept in `localStorage`) is recorded on each item the browser shares and is what authorises deletion. It is never sent to other clients — they only receive a computed `mine` flag — so it can't be spoofed from the feed. Consequences: clearing site data forfeits the ability to delete earlier uploads, and two windows of one browser profile count as one uploader.
- **Display names:** each browser gets a random "Swift Otter"-style name and avatar colour, editable by the user. It is encrypted with the session key before leaving the browser and travels as opaque ciphertext (`X-Participant-Who` on uploads, `?who=` on the WebSocket); the server validates only its shape and length. Other participants decrypt it locally; malformed or out-of-range values fall back to safe defaults. There is no real identity to look up — by design.
- **Operator takedown:** `DELETE /admin/sessions/:code` with `X-Admin-Token`, enabled only when `ADMIN_TOKEN` (≥ 24 chars in production) is set.

### 8.4 Abuse and cost controls
- **Quotas** (atomic, server-side): per-file size, files per session, total bytes per session, notes per session; participant cap clamped to `MAX_PARTICIPANTS`.
- **Uploads must be `application/octet-stream`** (everything is ciphertext), and downloads are forced to `attachment`, so the bucket can't be used to serve browsable HTML/SVG.
- Presigned PUT signs the declared length (`SIGN_CONTENT_LENGTH`, default on, so storage itself rejects a different-sized body); `complete` verifies the real stored size either way. With signing off (needed by some S3-compatible stores) a client can briefly upload more than declared, but the object is deleted at completion — or by the orphan sweep if never completed.
- **Rate limits:** independent per-IP token buckets for creation, lookups, uploads, room traffic and socket connects; Lua-backed lockout across replicas.
- **Cleanup:** TTL + keyspace events + orphan sweep + bucket lifecycle rule.

### 8.5 Transport, headers and deployment hardening
- API: `X-Content-Type-Options`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`, a deny-everything `Content-Security-Policy` (it serves JSON only), `Cache-Control: no-store`, HSTS in release, optional HTTPS redirect (`ENFORCE_HTTPS`, health checks exempt).
- Frontend (`vercel.json`): strict CSP (no inline scripts — the theme bootstrap is a static file), HSTS, framing/sniffing/referrer/permissions headers, SPA rewrites.
- CORS and the WebSocket `CheckOrigin` share one explicit `ALLOWED_ORIGIN` list; no credentials are used.
- Client IP for limits/lockouts: by default the socket address (forwarded headers are ignored, since clients control them). Behind a platform proxy set `TRUSTED_PROXIES=private` (add `cloudflare` on hosts fronted by Cloudflare, such as Render, or visitors look like Cloudflare edge addresses; `GET /admin/diag/ip` with the admin token shows what the server resolved): the real client is the right-most `X-Forwarded-For` address that is not a trusted proxy, so a forged prefix can't evade limits (covered by tests). `CLIENT_IP_HEADER` supports platforms that overwrite a single-value header.
- Redis connections can use TLS (`REDIS_TLS=true`) for managed providers that require it.
- `GIN_MODE=release` runs `CheckProductionSafety()` and refuses to boot with dev S3 credentials, a dev Postgres password, localhost/`http://` origins, a localhost public S3 endpoint, or a weak admin token. The server tries to enable Redis keyspace events itself and fails boot if they stay off (override with `ALLOW_NO_KEYSPACE_EVENTS=true`, relying on the orphan sweep).

### 8.6 Logging and error tracking
- Structured JSON logs (`log/slog`) with one line per request. They record the **route pattern** (`/api/v1/sessions/:code`), never the concrete path or query, because the code is the encryption key. Panics are recovered without dumping request headers.
- Optional **Sentry** (`SENTRY_DSN`, `VITE_SENTRY_DSN`): errors only (no tracing/replay), no PII, request data dropped, no breadcrumbs in the browser, and every string scrubbed of session codes (`/sessions/…`, `session:…`, `?who=…`) before sending. Scrubbing is covered by tests on both sides.

---

## 9. Scalability and reliability

| Concern | Approach |
|---|---|
| Many API instances | No per-request state; all shared state is in Redis. Presence, quotas and lockouts are atomic Lua/Redis operations, and events fan out via pub/sub, so replicas can sit behind a load balancer. |
| Large files | Bytes never pass through Go; API memory/bandwidth stay flat regardless of file size. |
| Restarts (free tiers sleep) | Sessions live in Redis, not process memory; sockets auto-reconnect and refetch; the boot-time orphan sweep cleans up anything missed while down. |
| Hot-path latency | Redis only; Postgres (if any) is async and optional. |
| Dead connections | Ping/pong heartbeat; presence entries expire after 75 s without one. |
| Missed expiry event | `410` still fires at the exact expiry; orphan sweep removes files; bucket lifecycle is the last backstop. |
| Duplicate completes | `GETDEL` makes completion idempotent. |

**Scaling path:** run N stateless replicas → managed Redis with keyspace events → R2/S3 (R2 has no egress fees) → move the per-replica token buckets into Redis for global limits (§11) → if Redis becomes the bottleneck, shard by session code (hash-tag `session:{CODE}` for Redis Cluster).

---

## 10. Testing and CI

- **Backend (`go test -race ./...`):** integration tests boot the *real router* against in-memory Redis (`miniredis`) and a fake S3 server — quotas and size verification, uploader-only delete, identity validation, lockout (and its expiry), upload rate limiting, CORS/headers/health (including `503` with Redis down), admin takedown, orphan sweep, presence cap/leave/reuse over real WebSockets, end-of-session broadcast — plus unit tests for config safety, code generation and Sentry scrubbing.
- **Frontend (`npm test`, Vitest):** crypto round-trips and tamper detection, identity generation/sanitisation/encryption, API client headers and error mapping, error-report scrubbing, formatting.
- **CI:** `.github/workflows/ci.yml` runs `gofmt`, `go vet`, the Go tests with the race detector, the frontend tests and a production build on every push and PR.
- Not covered yet: browser end-to-end tests, and tests against real R2/S3 (the fake S3 doesn't verify signatures).

---

## 11. Known limitations and future work

1. **Not zero-knowledge against the operator** (§8.2). *Fix:* have the browser generate the code and send only a hash for lookup (server never sees the key material), or offer a URL-fragment key mode for shared links.
2. **Per-replica rate-limit buckets.** Lockouts and quotas are global (Redis), but the token buckets are in memory, so N replicas allow N× the burst. *Fix:* Redis-backed sliding window.
3. **Identity is per browser, not per person.** Clearing site data loses delete rights; two windows in one profile are the same uploader. Inherent to having no accounts.
4. **Lockout is per IP.** Users behind one NAT share a budget; botnets bypass it. Mitigated by 8-char codes and rate limits.
5. **No server-side file-type validation.** Content is encrypted, so the server can't inspect it; policy is enforced by Terms, takedowns and quotas.
6. **Large files are encrypted in memory** (`file.arrayBuffer()`); the default 25 MB limit keeps this safe on mobile. *Fix:* chunked encryption + multipart upload (also enables resumable uploads).
7. **Migrations** run only on first Postgres volume init. *Fix:* golang-migrate/goose.
8. **Code hashes in Postgres are unsalted SHA-256.** Reversible offline in a ~40-bit space. *Fix:* HMAC with a server secret. (Irrelevant when Postgres is unused.)
9. **Local MinIO bucket allows anonymous download** (dev only); production buckets should be private — presigned URLs already work without public access.
10. **Presigned `Content-Length` signing is untested against real R2/S3**; verify with a staging deploy (see `DEPLOY.md`).
