# Production Deployment Checklist

The `docker-compose.yml` / `.env.example` defaults are for **local dev only**
(plain HTTP, MinIO, `minioadmin`, `postgres:postgres`). Do not deploy them
as-is. Everything below is now enforced or at least checked by the backend
where practical (see `backend/internal/config/config.go`'s
`CheckProductionSafety`, run automatically at boot when `GIN_MODE=release`).

## 1. Redis: keyspace notifications are a hard requirement

The auto-expiry/cleanup worker (`backend/internal/handlers/cleanup.go`)
subscribes to `__keyevent@0__:expired` and does nothing else to detect
expiry. If `notify-keyspace-events` doesn't include `Ex` (or `A`), sessions'
S3 objects and WebSocket connections are **never cleaned up** — with no
error, just silent leakage.

- Self-hosted: `redis-server --notify-keyspace-events Ex` (already set in
  `docker-compose.yml`), or `CONFIG SET notify-keyspace-events Ex`.
- Managed: verify support **before** committing to a provider.
  - AWS ElastiCache (non-serverless): supported via a parameter group
    (`notify-keyspace-events = Ex`). ElastiCache **Serverless** does not
    allow `CONFIG SET` — check current docs before relying on it.
  - Redis Cloud / Redis Enterprise: supported, set via the console or
    `CONFIG SET` depending on plan.
  - Upstash: historically restricted or unsupported on some plans — verify
    for your exact plan before choosing it for this project.
- The backend first tries `CONFIG SET notify-keyspace-events Ex` itself, then checks `CONFIG GET notify-keyspace-events` at boot
  (`cmd/server/main.go: verifyKeyspaceNotifications`) and refuses to start
  if it can read the config and it's missing `Ex`/`A`. If the provider
  blocks `CONFIG GET` itself, the backend only logs a warning — verify
  manually in that case.
- Two periodic backstops run regardless: `PeriodicSweep` (every minute) fixes
  analytics rows, and the **orphan sweep** (every `ORPHAN_SWEEP_SECONDS`, and once
  at boot) deletes storage objects whose session no longer exists — so a restart
  or a missed event can't leak files. If your Redis forbids `CONFIG SET`, set
  `ALLOW_NO_KEYSPACE_EVENTS=true` and rely on the orphan sweep (cleanup is then
  delayed by up to one sweep interval).

## 2. Secrets: rotate everything

Never deploy with the dev defaults baked into `config.go`'s fallbacks:

| Variable | Dev default | Action |
|---|---|---|
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | `minioadmin` | Use a real IAM user's keys, or better, leave both blank and run on an IAM role (ECS task role / EC2 instance profile) so the AWS SDK's default credential chain picks it up. |
| `POSTGRES_DSN` password | `postgres` (or any local override like `1981`) | Rotate to a strong, generated password stored in your secrets manager (not in `.env` committed to git — see `.gitignore`). |
| MinIO root creds | `minioadmin` / `minioadmin` | Only relevant if you're still using self-hosted MinIO in prod — rotate `MINIO_ROOT_USER`/`MINIO_ROOT_PASSWORD`, or move to AWS S3 / Cloudflare R2 with IAM-scoped credentials instead. |

The backend will refuse to boot with `GIN_MODE=release` if it detects the
`minioadmin` or default-Postgres-password patterns still in `S3_ACCESS_KEY`/
`S3_SECRET_KEY`/`POSTGRES_DSN`.

## 3. CORS: `ALLOWED_ORIGIN`

Set to your real frontend origin(s), e.g.:

```
ALLOWED_ORIGIN=https://share.example.com
```

Comma-separate multiple origins if you serve both an apex and `www` domain,
or staging + prod:

```
ALLOWED_ORIGIN=https://share.example.com,https://www.share.example.com
```

This also gates the WebSocket upgrade's `Origin` check
(`backend/internal/handlers/websocket.go`) — both use the same list. The
boot check rejects any `localhost`/`127.0.0.1` origin or plain `http://`
origin once `GIN_MODE=release`.

## 4. TLS (frontend + WebSocket)

Neither the Go backend (`router.Run`, plain HTTP) nor the frontend's nginx
(`frontend/nginx.conf`, port 80) terminate TLS themselves — terminate it at
a reverse proxy or platform load balancer in front of both (e.g. an ALB/NLB
+ ACM cert, Caddy, nginx with certbot, or your PaaS's built-in HTTPS —
Fly.io/Render/Railway all do this automatically).

- Frontend must be served over `https://`.
- Backend API + WebSocket endpoint must be served over `https://`/`wss://`.
- The frontend derives its WebSocket URL from `VITE_API_URL`
  (`frontend/src/lib/api.js: wsUrl`) by swapping the `http`/`https` prefix —
  so once `VITE_API_URL=https://api.example.com/api/v1` is set at build
  time, `wss://` is automatic. No separate WS URL env var to misconfigure.
- Don't forget to update `ALLOWED_ORIGIN` to the `https://` frontend origin
  too (see §3) — an `http://` origin is rejected at boot in release mode.

## 5. S3 endpoint split

`S3_ENDPOINT` (backend → storage, internal/private network path) and
`S3_PUBLIC_ENDPOINT` (browser → storage, used only to sign pre-signed
upload/download URLs) are deliberately separate — see
`backend/internal/storage/s3.go`. Set both correctly for whichever backing
store you use:

| Storage | `S3_ENDPOINT` | `S3_PUBLIC_ENDPOINT` |
|---|---|---|
| Self-hosted MinIO | internal/private hostname (e.g. `http://minio.internal:9000`) | public hostname/CDN in front of it, over `https://` |
| AWS S3 | `none` (lets the SDK resolve the regional endpoint) | leave blank — it falls back to the same AWS-resolved endpoint, which is already publicly reachable |
| Cloudflare R2 | your R2 account endpoint | your R2 public bucket URL / custom domain |

Boot check: the backend refuses to start in release mode if
`S3_PUBLIC_ENDPOINT` still contains `localhost`/`127.0.0.1`.

## 6. Postgres is optional

Leave `POSTGRES_DSN` unset to run Redis-only (no traffic stats). The production
safety check only inspects the DSN if one is set.

## 7. Error tracking (optional)

Set `SENTRY_DSN` on the backend and `VITE_SENTRY_DSN` on the frontend build. Both
sides scrub session codes (which double as encryption keys) from every event and
disable request data, PII, tracing, replay and browser breadcrumbs.

## Quick verification after deploy

```bash
GIN_MODE=release ./server   # will log.Fatal on any of the above if misconfigured
```
