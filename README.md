# ephemeral.share

Share files and text between devices with a short access code. Every session (**Quick Transfer** or **Shared Workspace**) lives for a fixed time — **1 hour by default**, configurable — and is then deleted automatically.

**Stack:** Go 1.25 (Gin) · React 18 + Vite + Tailwind CSS · Redis 7 · S3-compatible storage (MinIO locally; Cloudflare R2 / AWS S3 in production) · optional PostgreSQL for aggregate stats · optional Sentry · Docker · Capacitor (iOS/Android wrapper)

📐 System design, data flows, threat model: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
🚀 Step-by-step deploy (Vercel + Railway + Cloudflare R2): [`DEPLOY.md`](DEPLOY.md)
✅ Production checklist and rationale: [`PRODUCTION.md`](PRODUCTION.md)

---

## Features

- **Two session modes:** Quick Transfer, and Shared Workspace where several people share live.
- **Content:** files (25 MB each by default), plus notes, multi-line snippets and URLs.
- **Multi-file uploads** with progress, **cancel and retry**, drag-and-drop and paste-an-image.
- **Anonymous display names:** each browser gets a random name and avatar colour ("Swift Otter") that the user can rename. No accounts, emails or logins are involved, and the name is encrypted before it reaches the server.
- **Live presence:** who is in the room, enforced participant cap (default 10), reload-safe.
- **Delete your own uploads:** only the browser that shared an item can delete it.
- **Host controls:** the creator can end the session for everyone at any time.
- **Live countdown** with a burning-fuse indicator; friendly screens for expired / ended / full / not-found / locked-out.
- **Privacy, Terms and Report-abuse pages**, plus an operator takedown endpoint.
- Light/dark theme, responsive UI.

## How it protects itself (and its host)

| Concern | Control |
|---|---|
| Storage cost / abuse | Per-file, per-session file-count and total-bytes caps, enforced atomically on the server (Lua), not just in the UI. Only encrypted `application/octet-stream` uploads are accepted. |
| Code guessing | 8-character codes (~40 bits), per-IP rate limits, and a lockout after repeated wrong-code lookups across *every* endpoint. |
| Flooding | Separate per-IP token buckets for session creation, lookups, uploads, room traffic and WebSocket connects (idle entries evicted). |
| Leftover files | TTL expiry + keyspace-event cleanup + a periodic **orphan sweep** that removes files whose session is gone (covers restarts and missed events). |
| Browser hardening | Strict CSP and security headers on both the API and the Vercel frontend; downloads are forced to `attachment`; HTTPS redirect and HSTS in production. |
| Operability | `/health` (checks Redis), structured JSON logs that never contain access codes, optional Sentry with codes scrubbed, graceful reconnects. |
| Misconfiguration | In `GIN_MODE=release` the server refuses to boot with dev credentials, localhost/`http://` origins, or a weak admin token. |

## Encryption — what it does and doesn't do

Files, filenames, notes and display names are encrypted **in the browser** with AES-GCM-256 before upload; storage, Redis and the database only ever hold ciphertext. The key is derived from the access code (PBKDF2-SHA256, 100k iterations), so anyone with the code can decrypt, and there's no separate key to share.

**This is not zero-knowledge against the operator.** The access code is also the session lookup key, so the server receives it on each request and could in principle use it to decrypt. It protects against leaked storage buckets, database dumps and storage providers — not against whoever runs the server. We don't store or log the code (logs record route patterns, and Sentry events are scrubbed), but that is practice, not a technical guarantee. The in-app Privacy page says the same. See [ARCHITECTURE §8.2](docs/ARCHITECTURE.md#82-client-side-encryption-frontendsrclibcryptojs).

## Architecture at a glance

```
 Browser (React) ──REST──▶  Go/Gin API ──▶ Redis      (live state, quotas, presence, TTL, pub/sub)
       │         ◀──WS───       │     └──▶ PostgreSQL (optional, async aggregate stats)
       └──── PUT/GET encrypted bytes via pre-signed URL ────▶ S3 / R2 / MinIO
```

## Quick start (Docker Compose)

```bash
git clone https://github.com/himanshmunjal/Ephemeral-share.git
cd ephemeral-share
docker compose up --build
```

| Service | URL |
|---|---|
| Frontend | http://localhost:5173 |
| Backend API | http://localhost:8080 (health: `/health`) |
| MinIO console | http://localhost:9001 (`minioadmin` / `minioadmin`) |

> Tip: set `SESSION_TTL_SECONDS=60` to watch expiry and cleanup happen quickly.

## Local development (without Docker)

```bash
# Redis with expiry notifications (the backend also tries to enable this itself)
redis-server --notify-keyspace-events Ex

# Backend — needs Redis and an S3-compatible endpoint (see .env.example); Postgres is optional
cd backend && go run ./cmd/server

# Frontend
cd frontend && npm install && npm run dev
```

**Mobile (Capacitor):** `cd frontend && npm run cap:ios` or `npm run cap:android` (add `capacitor://localhost,https://localhost` to `ALLOWED_ORIGIN`).

## Testing and CI

```bash
cd backend  && go test -race ./...     # integration tests: real router + in-memory Redis + fake S3
cd frontend && npm test                # Vitest: crypto, identity, API client, scrubbing
```

The backend suite exercises the HTTP API end to end — quotas, uploader-only delete, lockout, rate limits, presence cap over real WebSockets, admin takedown, CORS/security headers, health, and the orphan sweep — with no external services. GitHub Actions (`.github/workflows/ci.yml`) runs both suites plus `gofmt`, `go vet` and a production frontend build on every push and pull request.

## Configuration

Backend (environment variables; full annotated list in [`.env.production.example`](.env.production.example)):

| Variable | Default | Purpose |
|---|---|---|
| `SESSION_TTL_SECONDS` | `3600` | Session lifetime |
| `MAX_UPLOAD_MB` | `25` | Per-file size limit |
| `MAX_FILES_PER_SESSION` / `MAX_SESSION_MB` | `20` / `100` | Per-session file count / total size |
| `MAX_TEXT_ITEMS_PER_SESSION` | `200` | Notes per session |
| `MAX_PARTICIPANTS` | `10` | Hard cap on people in a room |
| `CODE_LENGTH` | `8` | Access code length |
| `RATE_LIMIT_PER_MIN` | `60` | Base per-IP rate (lookups ×3, creation ÷6, with floors) |
| `UPLOAD_RATE_LIMIT_PER_MIN` / `ROOM_RATE_LIMIT_PER_MIN` | `30` / `240` | Upload and in-room traffic limits |
| `MAX_FAILED_LOOKUPS` / `LOCKOUT_SECONDS` | `10` / `900` | Wrong-code lockout |
| `SIGN_CONTENT_LENGTH` | `true` | Sign the declared size into upload URLs; set `false` for S3-compatible stores that reject it |
| `ORPHAN_SWEEP_SECONDS` / `ORPHAN_GRACE_SECONDS` | `600` / `600` | Orphaned-file sweep interval and minimum age |
| `ALLOWED_ORIGIN` | `http://localhost:5173` | Comma-separated CORS + WebSocket origins |
| `TRUSTED_PROXIES` | *(unset)* | `private`, `cloudflare` and/or CIDRs: trust platform proxies and read the real client IP from `X-Forwarded-For` |
| `CLIENT_IP_HEADER` | *(unset)* | Alternative: a single-value header the platform overwrites (e.g. `CF-Connecting-IP`) |
| `ENFORCE_HTTPS` | *(unset)* | `true` redirects plain-HTTP requests |
| `ADMIN_TOKEN` | *(unset)* | Enables `DELETE /admin/sessions/:code` for takedowns |
| `SENTRY_DSN` / `SENTRY_ENVIRONMENT` | *(unset)* / `production` | Optional error tracking |
| `REDIS_ADDR` / `REDIS_PASSWORD` / `REDIS_TLS` | `redis:6379` / — / off | Redis (`REDIS_TLS=true` for managed, TLS-only Redis such as Upstash) |
| `POSTGRES_DSN` | *(unset)* | Optional aggregate stats; unset runs Redis-only |
| `S3_ENDPOINT`, `S3_PUBLIC_ENDPOINT`, `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | MinIO dev values | Object storage |

Frontend (build-time): `VITE_API_URL` (API base; `https` gives `wss` automatically), `VITE_CONTACT_EMAIL` (shown on the abuse page), `VITE_SENTRY_DSN` (optional).

## API overview

Base `/api/v1`; every request may carry `X-Participant-ID` (random per-browser id), `X-Owner-Token` (host) and `X-Participant-Who` (encrypted display identity). Full table in [ARCHITECTURE §7](docs/ARCHITECTURE.md#7-api-reference).

| Method | Path | Description |
|---|---|---|
| POST | `/sessions` | Create a session; returns the code and the owner token (once) |
| GET | `/sessions/:code` | Metadata, remaining TTL, limits, online count |
| POST | `/sessions/:code/join` | Capacity pre-check (returns `full` when at the cap) |
| DELETE | `/sessions/:code` | Host ends the session |
| POST | `/sessions/:code/content/upload-url` | Reserve quota + get a pre-signed PUT URL |
| POST | `/sessions/:code/content/:id/complete` | Verify real size, publish the item |
| POST | `/sessions/:code/content/text` | Add an encrypted note/snippet/URL |
| DELETE | `/sessions/:code/content/:id` | Uploader deletes their item (or cancels a pending upload) |
| GET | `/sessions/:code/content` | List the feed (`mine` marks your items) |
| GET | `/sessions/:code/content/:id/download` | Pre-signed GET (`?redirect=false` for JSON) |
| WS | `/ws/sessions/:code?who=…` | Events: `presence`, `content_added`, `content_removed`, `session_ended`, `session_expired` |
| GET | `/health` | Liveness + Redis check |
| DELETE | `/admin/sessions/:code` | Operator takedown (needs `X-Admin-Token`; disabled unless configured) |

## Project layout

```
backend/
  cmd/server/            entrypoint, boot checks, Sentry/log wiring
  internal/config/       env config + production safety checks
  internal/handlers/     session, content, websocket, cleanup (+ orphan sweep), guard (lockout), router
  internal/middleware/   rate limiting, security headers, HTTPS, structured logging
  internal/redisclient/  keys, TTL, quotas (Lua), presence, lockout counters, pub/sub
  internal/storage/      S3 client, pre-signing, paginated list/delete
  internal/ws/           per-session WebSocket hub (+ heartbeat)
  internal/monitoring/   optional Sentry with code scrubbing
  internal/analytics/    optional async Postgres stats
frontend/
  src/pages/             Create, Join, Room, Privacy/Terms/Abuse
  src/components/        UploadWidget (queue), ContentFeedItem, Avatar, SessionGone, ErrorBoundary …
  src/lib/               api, crypto, identity, monitoring
  vercel.json            routing + security headers for Vercel
.github/workflows/       CI
infra/                   MinIO bootstrap, bucket CORS template
```

## Deploying

Follow [`DEPLOY.md`](DEPLOY.md). `docker-compose.yml` and `.env.example` are local-dev only.

## Status

**Done:** everything above, plus automated tests and CI.

**Known limitations / next** (details in [ARCHITECTURE §11](docs/ARCHITECTURE.md#11-known-limitations-and-future-work)): true zero-knowledge mode (client-generated codes with a hash-only server lookup, or a URL-fragment key); chunked/multipart encrypted uploads for very large files; Redis-backed *global* rate-limit counters across replicas; schema-migration tooling; end-to-end browser tests.
