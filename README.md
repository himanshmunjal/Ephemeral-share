# Ephemeral Cross-Device Sharing Platform

Share files and text between devices with a short code. Every session — **Quick Transfer** or **Shared Workspace** — lives for exactly 5 minutes and is then automatically and completely deleted.

Stack: **Go (Gin)** · **React + Tailwind CSS** · **PostgreSQL** · **Redis** · **S3-compatible storage (MinIO by default, swappable for AWS S3 / Cloudflare R2)**

See [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the full system design, data flow, and security rationale.

## End-to-End Encryption

Every file and text payload is encrypted **in the browser** with AES-GCM 256 before it ever leaves the tab — the server, Redis, and object storage only ever hold ciphertext.

- On session creation, the browser generates a random AES key with the Web Crypto API. The key is never sent in any request; it lives only in the URL fragment (`/session/CODE#k=...`), which browsers never transmit to the server or log.
- Recipients get the key by receiving the full link (fragment included). A bare access code alone lets someone view session metadata but not decrypt content — the room UI offers a manual "paste decryption key" fallback for that case.
- File names and MIME types are encrypted too (bundled into an envelope alongside the file bytes), so the server never learns what was shared, only ciphertext sizes and timestamps.
- The fragment is stripped from the address bar immediately after the key is captured into memory, so it doesn't linger in browser history or address-bar screenshots.

This is a deliberate trade-off, not a full zero-knowledge system: metadata like session codes, content counts, and timing are still visible to the server (see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the full threat model).

## Quick Start (Docker Compose)

```bash
git clone <repo>
cd ephemeral-share
docker compose up --build
```

- Frontend: http://localhost:5173
- Backend API: http://localhost:8080
- MinIO console: http://localhost:9001 (minioadmin / minioadmin)

The `minio-init` service auto-creates the `ephemeral-share` bucket on first boot.

## Local Development (without Docker)

**Backend**
```bash
cd backend
go mod tidy
go run ./cmd/server
```
Requires local Postgres, Redis (with `notify-keyspace-events Ex` enabled), and a MinIO/S3 endpoint — see `.env.example` for the variables to export.

**Frontend**
```bash
cd frontend
npm install
npm run dev
```

## API Overview

| Method | Path | Description |
|---|---|---|
| POST | `/api/v1/sessions` | Create a session (`quick_transfer` or `shared_workspace`) |
| GET | `/api/v1/sessions/:code` | Fetch session metadata + remaining TTL |
| POST | `/api/v1/sessions/:code/join` | Join a Shared Workspace |
| POST | `/api/v1/sessions/:code/content/upload-url` | Get a pre-signed URL, then PUT the file directly to it |
| POST | `/api/v1/sessions/:code/content/text` | Add a text note/snippet/URL |
| GET | `/api/v1/sessions/:code/content` | List current content feed |
| GET | `/api/v1/sessions/:code/content/:id/download` | Redirects to a pre-signed download URL |
| DELETE | `/api/v1/sessions/:code` | Owner-only early termination |
| WS | `/ws/sessions/:code` | Real-time feed for Shared Workspace participants |

## How Expiration Works

1. On creation, the session's Redis hash is written with `EXPIRE 300`.
2. Every dependent Redis key (content list, content items, participant set) is capped to the *same remaining* TTL — never extended — so nothing can outlive the original 5 minutes.
3. Redis keyspace notifications (`notify-keyspace-events Ex`) fire when the session key expires; a backend worker reacts by deleting the session's S3 objects and force-closing any open WebSocket connections.
4. A bucket lifecycle rule and periodic sweep act as a backstop in case a notification is ever missed.

## Project Layout

```
backend/    Go/Gin API, Redis session store, S3 client, WebSocket hub, cleanup worker
frontend/   React + Tailwind SPA (create/join/session-room pages)
docs/       Architecture and data-flow documentation
docker-compose.yml   Full local stack: postgres, redis, minio, backend, frontend
```

## Deploying to Production

`docker-compose.yml` and `.env.example` are local-dev only (plain HTTP,
MinIO, dev credentials). See [`PRODUCTION.md`](PRODUCTION.md) for the
pre-deploy checklist (Redis keyspace notifications, credential rotation,
CORS, TLS, S3 endpoint split) and [`.env.production.example`](.env.production.example)
for a filled-in-the-blanks template. Run the backend with `GIN_MODE=release`
so it self-checks most of this at boot and refuses to start with leftover
dev defaults.

## Status

This implements the core flows end-to-end: session creation, code/link-based join, client-side end-to-end encrypted file/text sharing via pre-signed URLs, live updates for Shared Workspace, TTL-driven expiration and cleanup (with a Postgres periodic-sweep backstop), and a light/dark themed UI. Areas intentionally left as follow-up work: file-type/MIME allow-list enforcement at the API layer (currently opaque to the server since content is encrypted client-side) and resumable/chunked uploads for very large files.
