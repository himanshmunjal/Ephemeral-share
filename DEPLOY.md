# Deploying ephemeral-share

Four pieces: **frontend** (static React app) · **backend** (Go API + WebSockets) · **Redis** (live state) · **object storage** (encrypted files). Pick one of two paths.

| | Path A — $0 | Path B — about $5/month |
|---|---|---|
| Frontend | Vercel Hobby | Vercel Hobby |
| Backend | **Render** free web service | **Railway** |
| Redis | **Upstash** free | Railway Redis (same project) |
| Storage | Cloudflare R2 free tier *(card on file)* — or Backblaze B2 *(no card, see below)* | same |
| Trade-offs | Backend sleeps after ~15 min idle (≈1 min to wake); Redis has a monthly command cap | Always on, no caps to watch; costs money |

> **Verify before signing up.** Free tiers change. Figures here come from vendor docs and third-party summaries; re-check the pricing pages for Render, Upstash, Cloudflare R2 and Railway, and note Vercel Hobby is for non-commercial use. Cloudflare may ask for a payment method even for free R2 usage.

---

## Step 1 — Cloudflare R2 (both paths)

1. Cloudflare dashboard → **R2** → create bucket `ephemeral-share`.
2. **R2 → Manage API tokens** → create a token with *Object Read & Write* scoped to that bucket. Save Access Key ID, Secret and your **Account ID**.
3. Bucket → **Settings → CORS policy** → paste `infra/storage-cors.json`, replacing `YOUR-APP.vercel.app` with your real Vercel URL (browsers upload straight to R2, so this is mandatory; fill it in after Step 4).
4. Bucket → **Settings → Object lifecycle** → delete objects after 1 day (a backstop under the app's own cleanup).

### Step 1 alternative — Backblaze B2 (no card needed, per its sign-up page)

Use this if you don't want to add a card to Cloudflare. Backblaze advertises a free 10 GB tier with no card required; sources disagreed, so confirm on sign-up.

1. Create a **private** bucket. On the bucket page note its **endpoint** (`s3.<region>.backblazeb2.com`) and **region**.
2. **Application Keys** → create a key limited to that bucket (read + write). Save the key ID and secret.
3. Set `S3_ENDPOINT` and `S3_PUBLIC_ENDPOINT` to `https://s3.<region>.backblazeb2.com`, `S3_REGION` to the region, plus the key ID/secret as `S3_ACCESS_KEY`/`S3_SECRET_KEY`.
4. **CORS:** add a rule allowing your Vercel origin for the S3 GET, PUT and HEAD operations with all headers (browsers upload directly). Check Backblaze's CORS docs for the exact form.
5. **Lifecycle:** add a rule **"Keep only the last version"**. B2 keeps old/hidden versions by default and they still count toward your 10 GB, so without this, deleted files may keep using space. I couldn't confirm how a plain S3 delete behaves, so watch your usage after a test.
6. **Untested here:** I couldn't run uploads against B2. If a browser upload fails with a signature/`Content-Length` error, set `SIGN_CONTENT_LENGTH=false` (the server still verifies and deletes mismatched sizes at completion) and retry.

## Step 2A — Redis on Upstash (Path A)

1. Create an Upstash Redis database **in the same region as your backend** (e.g. Singapore for a Singapore Render service). The API makes at least one Redis round trip per request, so distance multiplies directly into latency: measured on a Mumbai database from a Singapore backend, each round trip cost ~0.4 s.
2. Note the **endpoint host**, port (6379) and **password**. Upstash only accepts TLS, so you'll set `REDIS_TLS=true`.
3. The backend runs `CONFIG SET notify-keyspace-events Ex` at boot; Upstash's docs say that parameter is settable. If boot reports it isn't, set `ALLOW_NO_KEYSPACE_EVENTS=true` — files are then removed by the orphan sweep within `ORPHAN_SWEEP_SECONDS` instead of instantly.
4. **Command budget:** the free tier is limited per month (check the current number). Rough estimate: an hour-long session with three people uses on the order of 500–1,000 commands (heartbeats dominate), so budget for a few hundred sessions a month. Watch Upstash's usage graph during your trial.

## Step 3A — Backend on Render (Path A)

1. Render → **New → Web Service** → connect the repo.
2. *Root Directory* `backend` · *Runtime* Docker · *Instance type* **Free** · *Health Check Path* `/health`.
3. Environment variables:
   ```
   GIN_MODE=release
   REDIS_ADDR=<upstash-host>:6379
   REDIS_PASSWORD=<upstash-password>
   REDIS_TLS=true
   S3_ENDPOINT=https://<ACCOUNT_ID>.r2.cloudflarestorage.com
   S3_PUBLIC_ENDPOINT=https://<ACCOUNT_ID>.r2.cloudflarestorage.com
   S3_BUCKET=ephemeral-share
   S3_REGION=auto
   S3_ACCESS_KEY=...   S3_SECRET_KEY=...
   ALLOWED_ORIGIN=https://YOUR-APP.vercel.app
   TRUSTED_PROXIES=private,cloudflare
   ENFORCE_HTTPS=true
   ADMIN_TOKEN=<openssl rand -hex 32>
   ```
   Don't set `PORT` (Render injects it) or `POSTGRES_DSN`.
4. Note the `https://<name>.onrender.com` URL.
5. **Cold starts:** a free service sleeps after ~15 minutes without inbound traffic and takes about a minute to wake, so the first visitor after a quiet period waits. A free uptime monitor hitting `/health` every ~10 minutes avoids this (check Render's free instance-hours allowance first).

## Step 2B/3B — Backend + Redis on Railway (Path B)

1. Push the repo to GitHub → Railway **New Project → Deploy from GitHub repo**. In service **Settings**: *Root Directory* `backend`, builder *Dockerfile*, *Healthcheck Path* `/health`.
2. **New → Database → Redis** in the same project (no TLS needed on the private network).
3. Backend variables — same as Path A except:
   ```
   REDIS_ADDR=${{Redis.REDISHOST}}:${{Redis.REDISPORT}}
   REDIS_PASSWORD=${{Redis.REDISPASSWORD}}
   ```
   (omit `REDIS_TLS`).
4. **Settings → Networking → Generate Domain**. Set a usage/budget alert in Railway.

## Step 4 — Frontend on Vercel (both paths)

1. Vercel → **Add New Project** → import the repo → *Root Directory* `frontend` (Vite is auto-detected; `vercel.json` provides routing and security headers).
2. Environment variables:
   ```
   VITE_API_URL=https://<your-backend-host>/api/v1
   VITE_CONTACT_EMAIL=abuse@yourdomain.com
   ```
3. Deploy, then put the final Vercel URL into `ALLOWED_ORIGIN` (backend) and the R2 CORS policy, and restart the backend.

## Step 5 — Optional error tracking

Create Sentry projects (Go + Browser) and set `SENTRY_DSN` on the backend and `VITE_SENTRY_DSN` on Vercel (redeploy). Session codes are scrubbed from every event; only errors are sent.

## Step 6 — Verify (do not skip)

1. `curl https://<backend>/health` → `{"status":"ok"}`.
2. **Real client IPs:** Render and similar hosts sit behind Cloudflare, so use `TRUSTED_PROXIES=private,cloudflare`. Then confirm with `curl -H "X-Admin-Token: $ADMIN_TOKEN" https://<backend>/admin/diag/ip` — `client_ip` must equal your public IP (compare with `curl ifconfig.me`), not a Cloudflare or internal address. If it doesn't, rate limits and lockouts would apply to many users at once; fix it before going public.
3. Open the site in **two different browsers** (not two windows of one): create a session, join from the second, confirm "2 online" with two different anonymous names, upload a file in one, download it in the other, then delete it from the uploader's side. This also confirms R2 accepts the uploads (CORS + signed size).
4. Enter ~11 wrong codes → "Too many wrong codes" screen.
5. Takedown: `curl -X DELETE -H "X-Admin-Token: $ADMIN_TOKEN" https://<backend>/admin/sessions/<CODE>`.

## If it feels slow

Measure first (a request's time is roughly *Redis round trips × Redis latency*):

```
for i in 1 2 3; do curl -s -o /dev/null -w "%{time_total}s\n" https://<backend>/health; done
```

- **~0.1–0.2 s** is healthy (network + one Redis ping).
- **0.5 s or more** for `/health` means Redis is far from the backend — put them in the same region.
- **A minute on the first request** is the free instance waking from sleep; ping `/health` every ~10 minutes with a free uptime monitor.

The backend is built to need few round trips (see ARCHITECTURE §9), so a nearby Redis makes it feel instant.

## Scaling notes

- The backend is stateless (state lives in Redis); add replicas and WebSocket fan-out already goes through Redis pub/sub. Presence, quotas and lockouts are atomic, so they stay correct across replicas.
- Rate-limit token buckets are per replica (lockouts are shared via Redis).
- First bottlenecks: Redis command volume on Path A, and R2 operations. Files never pass through the API.
