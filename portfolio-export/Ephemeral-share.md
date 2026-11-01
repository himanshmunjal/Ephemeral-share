---
intro: A no-login tool for moving files and text between devices with a short code, where sessions expire on a timer and the server stores only ciphertext. The hard part was running it on free tiers, with Redis about 0.3 seconds away.
caption: The React client encrypts with Web Crypto and calls the Gin REST API, which keeps state in Redis and issues pre-signed URLs so file bytes go straight to Object storage, while the WebSocket hub and Cleanup worker follow Redis events.
---

## About

Ephemeral Share moves a file or note between devices. You create a session, get an eight-character code, and anyone with it can see and add content. Everything is encrypted in the browser and deleted when the timer ends.

I built it as a public service on free tiers. **How do you run a no-account file-sharing service where anyone can upload, and keep storage, abuse and latency under control?**

## Architecture

The **React client** uses **Web Crypto** to derive an AES-GCM key from the access code and encrypt files, names and notes before they leave the tab. It sends JSON to the **Gin REST API**, which enforces rate limits, a wrong-code lockout and quotas, and keeps session state in **Redis** under one TTL. For files it returns a pre-signed URL, and the client sends ciphertext straight to **Object storage**. **PostgreSQL (optional)** holds only aggregate stats.

Redis also carries live updates: the **WebSocket hub** pushes presence and content events from a per-session channel. The **Cleanup worker** deletes a session's objects when its key expires, and an orphan sweep catches the rest.

## How it's implemented

### Cleanup that survives restarts

Each session is one Redis key with a TTL. Expiry events trigger cleanup, but Upstash did not deliver them: a one-second test key produced no event in 15 seconds. So an orphan sweep is the real guarantee. It lists the bucket, checks each prefix against Redis, and deletes files whose session is gone.

### Quotas that cannot be raced

File size, file count, total bytes and room size are enforced on the server. Quota claims and room joins are Lua scripts, so concurrent uploads cannot race a limit. Wrong-code lookups lock an IP out after ten misses. Behind Cloudflare I read the right-most untrusted `X-Forwarded-For` address, and a test shows a forged header cannot dodge the lockout.

### Direct uploads with a size check

File bytes never pass through Go. The pre-signed PUT signs the declared `Content-Length`, and completion reads the real size with `HEAD` and deletes mismatches. On Backblaze B2 I confirmed the store itself rejects a wrong-sized upload. A setting disables signing for stores that reject it.

### Identity without accounts

Each browser gets a random ID and a name like Swift Otter, encrypted with the session key before it leaves the tab. Only the uploader's ID can delete an item, so clearing site data forfeits that right.

### Fewer Redis round trips

With Redis in another region, latency was the round-trip count. I merged the lockout check, session lookup, TTL and presence into one pipeline, replaced per-item reads with one `MGET`, and put broadcasts in the write's transaction. A test enforces a round-trip budget per endpoint.

## Tech stack

| Layer | Choice | Why |
|---|---|---|
| Language | Go 1.25, JavaScript | Goroutines for the hub and workers |
| Framework | Gin, React 18, Tailwind, Capacitor | Small API, one web build for mobile |
| Data | Redis 7, Lua scripts | TTL, pub/sub and atomic quotas |
| Storage | Backblaze B2, R2 or MinIO | Blobs bypass the API |
| Infra and CI | Vercel, Render, Upstash, GitHub Actions | Free tiers, race-tested builds |
| Testing | Go with miniredis and a fake S3, Vitest | Real router, no external services |

## Results

| Measure | Baseline | Best |
|---|---|---|
| Redis round trips, fetch a session | 4 | **1** |
| Redis round trips, feed of 6 items | 9 | **2** |
| Redis round trips, add a text note | 7 | **3** |

I counted round trips with a client hook on the commit before the change and on the new code. On the live service the old code took 1.27 to 1.76 seconds to fetch a session and about 3.4 seconds to load a room, roughly 0.3 seconds per round trip. I have not measured the new code live yet.

## What I'd do differently

- Put Redis in the same region as the API, because distance is still the biggest cost.
- Make it zero-knowledge, since the code is also the lookup key and the server could decrypt.
- Add browser end-to-end tests, since the 33 backend tests use no real browser.
