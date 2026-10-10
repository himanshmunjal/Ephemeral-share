# Ephemeral Cross-Device Sharing Platform

Share files and text between devices with a short access code. Every session (**Quick Transfer** or **Shared Workspace**) lives for exactly **5 minutes** and is then deleted automatically.

**Stack:** Go 1.25 (Gin) · React 18 + Vite + Tailwind CSS · Redis 7 · PostgreSQL 16 · S3-compatible storage (MinIO locally, AWS S3 / Cloudflare R2 in production) · Docker · Capacitor (iOS/Android wrapper)

📐 Full system design, data flows, threat model and known limitations: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)
🚀 Production checklist: [`PRODUCTION.md`](PRODUCTION.md)

---

## Features

- **Two session modes**
  - **Quick Transfer:** create, drop files or text, share the code.
  - **Shared Workspace:** several people join with the code and share content live. The participant limit is configurable (default 10).
- **Content types:** any file (documents, images, audio, video, ZIP; up to 200 MB by default), plus text notes, multi-line code snippets, URLs and clipboard content.
- **Short, readable codes:** Crockford base32 (no `I/L/O/U` lookalikes), from `crypto/rand`. Join by typing the code or pasting the link.
- **Hard 5-minute expiry:** one absolute Redis TTL that is never extended. A live countdown turns red in the last 30 seconds.
- **Real-time feed:** WebSockets fanned out through Redis pub/sub, with auto-reconnect and catch-up.
- **Direct-to-storage uploads and downloads:** pre-signed URLs, so file bytes never pass through the API. Upload progress bar included.
- **Client-side encryption:** AES-GCM-256 in the browser for file bytes, filenames and text.
- **Owner-only "End session":** deletes everything immediately for everyone.
- Light/dark theme, responsive UI, toast notifications.

## Architecture at a Glance

```
 Browser (React) ──REST──▶  Go/Gin API ──▶ Redis      (live state, TTL, pub/sub, expiry events)
       │         ◀──WS───       │     └──▶ PostgreSQL (async analytics only)
       └──── PUT/GET encrypted bytes via pre-signed URL ────▶ S3 / R2 / MinIO
```

| Component | Role |
|---|---|
| **Redis** | Source of truth for live sessions, content metadata, participants and pending uploads. Every key shares the session's TTL. Pub/sub carries WebSocket events between API instances. Keyspace `expired` events trigger cleanup. |
| **Object storage** | Encrypted file blobs under `sessions/{code}/{contentId}/…`. |
| **PostgreSQL** | Aggregate traffic analytics only: hashed code, type, timestamps, share counts. Writes are async and best-effort, so the app keeps working if Postgres is down. |
| **Go API** | REST handlers, WebSocket hub, rate limiting, and the cleanup worker. Holds no per-request state, so it scales horizontally. |

## Client-Side Encryption

All file bytes, filenames, MIME types and text are encrypted **in the browser** with AES-GCM-256 before upload. The server, Redis and object storage only ever store ciphertext.

- The key is derived in the browser from the access code: `PBKDF2-SHA256`, 100k iterations. Anyone with the code can decrypt, so there is no separate key to share or lose.
- Every payload uses a fresh random IV. GCM also detects tampering.
- **Honest threat model:** the code is sent to the server on every request. That means this protects against leaked storage, Redis dumps and storage providers, but it is **not** zero-knowledge against the server operator. Strength is also bounded by the code's entropy. This trade-off favors the "type a short code" UX. See [ARCHITECTURE §8.2](docs/ARCHITECTURE.md#82-client-side-encryption-frontendsrclibcryptojs).

## How Expiration Works

1. On creation, `SET session:{code} … EX 300`. This is the only clock.
2. Child keys (content list, items, pending uploads, participants) get the session's **remaining** TTL. Nothing ever extends it.
3. Every content request checks that the session key is live first, so the API returns `410 Gone` at exactly 5:00.
4. Redis emits a keyspace `expired` event (`notify-keyspace-events Ex`). The cleanup worker then:
   - deletes the session's S3 objects,
   - marks the analytics row inactive,
   - pushes `session_expired` to WebSocket clients and closes their connections.
5. A periodic sweep (every 60 s) corrects any analytics rows a missed event left active.
6. At boot, the backend **refuses to start** if Redis keyspace notifications are disabled.

## Quick Start (Docker Compose)

```bash
git clone https://github.com/himanshmunjal/Ephemeral-share.git
cd ephemeral-share
docker compose up --build
```

| Service | URL |
|---|---|
| Frontend | http://localhost:5173 |
| Backend API | http://localhost:8080 (health: `/healthz`) |
| MinIO console | http://localhost:9001 (`minioadmin` / `minioadmin`) |

`minio-init` creates the `ephemeral-share` bucket and its CORS policy on first boot. Postgres migrations run automatically on first volume init.

> Tip: set `SESSION_TTL_SECONDS=30` on the backend to watch expiry and cleanup happen quickly.

## Local Development (without Docker)

**Backend**
```bash
cd backend
go mod tidy
go run ./cmd/server
```
You need local Postgres, Redis started with `--notify-keyspace-events Ex`, and a MinIO/S3 endpoint. See `.env.example`.

**Frontend**
```bash
cd frontend
npm install
npm run dev
```

**Mobile (Capacitor)**
```bash
cd frontend
npm run cap:ios       # or: npm run cap:android
```

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `SESSION_TTL_SECONDS` | `300` | Session lifetime |
| `MAX_UPLOAD_MB` | `200` | Per-file size limit (checked before and after upload) |
| `CODE_LENGTH` | `6` (`8` in compose) | Access code length |
| `RATE_LIMIT_PER_MIN` | `60` | Per-IP limit on create (lookup/join get 2×) |
| `ALLOWED_ORIGIN` | `http://localhost:5173` | Comma-separated CORS + WebSocket origins |
| `REDIS_ADDR` / `REDIS_PASSWORD` | `redis:6379` | Redis |
| `POSTGRES_DSN` | local dev DSN | Analytics DB (optional at runtime) |
| `S3_ENDPOINT` | `http://minio:9000` | Backend → storage (`none` for AWS S3) |
| `S3_PUBLIC_ENDPOINT` | `http://localhost:9000` | Browser-reachable host used to sign URLs |
| `S3_BUCKET`, `S3_REGION`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | MinIO dev values | Storage credentials (leave keys blank to use an IAM role) |
| `VITE_API_URL` | `http://localhost:8080/api/v1` | Frontend build-time API base (`https` gives `wss` automatically) |

## API Overview

Base: `/api/v1`. Full table with status codes in [ARCHITECTURE §7](docs/ARCHITECTURE.md#7-api-reference).

| Method | Path | Description |
|---|---|---|
| POST | `/sessions` | Create a session (`quick_transfer` or `shared_workspace`). Sets the `owner_token` cookie. |
| GET | `/sessions/:code` | Session metadata and remaining TTL |
| POST | `/sessions/:code/join` | Join a Shared Workspace (enforces the participant limit) |
| DELETE | `/sessions/:code` | Owner-only early termination |
| POST | `/sessions/:code/content/upload-url` | Get a pre-signed PUT URL (upload is held as pending) |
| POST | `/sessions/:code/content/:id/complete` | Confirm upload. Server checks real size, then publishes the item. |
| POST | `/sessions/:code/content/text` | Add encrypted text (`note` / `snippet` / `url` / `clipboard`, ≤ 64 KB) |
| GET | `/sessions/:code/content` | List the content feed |
| GET | `/sessions/:code/content/:id/download` | 302 to a pre-signed GET (`?redirect=false` returns JSON) |
| WS | `/ws/sessions/:code` | Events: `content_added`, `participant_joined`, `session_ended`, `session_expired` |

## Security Controls

- Secure random codes, collision-checked, plus per-IP token-bucket rate limiting on create, lookup and join.
- Size limits enforced twice: the declared size before presigning, and the actual size from `HEAD` after upload (oversized objects are deleted).
- Input validation with Gin binding tags; text `content_type` restricted to an allow-list.
- Short-lived, single-object pre-signed URLs (PUT 2 min, GET 60 s).
- Owner token in an `HttpOnly; Secure` cookie; only hashes are stored in Postgres.
- CORS and the WebSocket origin check use the same explicit allow-list.
- In `GIN_MODE=release` the server refuses to boot with dev credentials, localhost or `http://` origins, or a localhost public S3 endpoint.

## Project Layout

```
backend/            Go/Gin API
  cmd/server/         entrypoint + boot-time safety checks
  internal/handlers/  session, content, websocket, cleanup, code generation
  internal/redisclient/  Redis keys, TTL logic, pub/sub
  internal/storage/   S3 client + pre-signing
  internal/ws/        per-session WebSocket hub
  internal/analytics/ async Postgres analytics
  internal/middleware/ rate limiting
  migrations/         SQL schema
frontend/           React + Tailwind SPA (+ Capacitor ios/ and android/)
docs/               Architecture and data-flow docs
infra/              MinIO bucket/CORS bootstrap
docker-compose.yml  Local stack: postgres, redis, minio, backend, frontend
```

## Deploying to Production

`docker-compose.yml` and `.env.example` are for local dev only (plain HTTP, MinIO, dev credentials). Before deploying, follow [`PRODUCTION.md`](PRODUCTION.md) and fill in [`.env.production.example`](.env.production.example). It covers:

- Redis keyspace notifications
- credential rotation
- CORS
- TLS
- the split between the internal and public S3 endpoints

Run the backend with `GIN_MODE=release` so it checks most of this itself at boot.

## Status and Roadmap

**Done:**
- session create and join by code or link
- client-side encrypted file and text sharing via pre-signed URLs, with two-phase upload confirmation
- live Shared Workspace feed
- TTL-driven expiry and cleanup with a boot-time safety check
- owner termination
- light/dark responsive UI
- Docker setup
- Capacitor mobile wrapper

**Next** (details in [ARCHITECTURE §11](docs/ARCHITECTURE.md#11-known-limitations-and-future-work)):
- Redis-backed global rate limiting with lockout
- atomic participant limit (Lua)
- single-uploader enforcement for Quick Transfer
- bucket lifecycle rule plus paginated S3 cleanup
- HMAC instead of SHA-256 for code hashes
- optional fragment-key end-to-end mode
- chunked/multipart encrypted uploads for very large files
- automated tests
