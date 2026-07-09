# liveurl

**The tunnel URL that survives your laptop going offline.**

`liveurl` exposes a local development server (`localhost:3000`) to the public internet through a
stable URL — like ngrok, Cloudflare Tunnel, or `localtunnel`. The difference is what happens when
you close your laptop: instead of the URL going dead, it keeps serving a cached snapshot of your
app to browsers and keeps queuing incoming webhooks (Stripe, GitHub, ...) for replay the moment
you reconnect — in the exact order they arrived.

---

## The problem

Your laptop doesn't have a public IP address. The moment you want to show a client a work-in-progress
demo, receive a webhook from Stripe while developing payments locally, or let a teammate poke at
something running on your machine, you need a **tunnel**: a public URL that forwards traffic to your
local port.

Every existing tunnel tool solves *that* problem. None of them solve the next one: **the URL is only
alive while your laptop is awake, plugged in, and running the tunnel client.** Close the lid, lose
Wi-Fi, or just `Ctrl+C` the process, and:

- The demo link you sent a client shows a broken-connection error.
- Any webhook a provider sends in that window is gone (or burns through the provider's own limited
  retry attempts).
- There is no in-between state — it's either fully live or fully dead.

### What was already being done about it

| Tool | What it's good at | Where it stops |
|---|---|---|
| **ngrok** | The incumbent — mature, well-documented, has a request inspector | Free tier: ~2 hour sessions, a new random subdomain every restart, 1 GB/month bandwidth, an interstitial warning page on every visit. Still just a dumb pipe — offline means dead. |
| **Cloudflare Tunnel** | Free, reliable, backed by Cloudflare's network | More setup (cloudflared, a Cloudflare account, Zero Trust config). Same fundamental limit: agent offline → tunnel offline. |
| **localtunnel / localhost.run** | Zero-config, one command, free | Effectively unmaintained / best-effort uptime; no offline behavior at all. |
| **frp / rathole** (self-hosted) | Full control, your own server | You own the pain: TLS, reconnect logic, config format — and still nothing for the offline case. |
| **Hookdeck / webhook.site** | Excellent at *inspecting and replaying webhooks* | Not a tunnel at all — doesn't expose your app to browsers, doesn't proxy live traffic. |

Nobody combines "live tunnel" with "graceful degradation when the tunnel isn't live." That gap is
what `liveurl` fills.

## What liveurl does differently

Three things happen automatically, with nothing extra for you to configure per-request:

1. **Live proxy** (agent connected) — a stable subdomain reverse-proxies straight to your localhost,
   including WebSocket passthrough for things like Vite/Next.js hot reload.
2. **Snapshot fallback** (agent disconnected, browser traffic) — while you were online, every
   cacheable `GET` response was passively written to a snapshot store. Once offline, those same
   pages keep rendering — with a visible "this is a snapshot from [time]" banner — instead of a
   dead connection. Pages you never visited get an honest "not available offline" message, not a
   silent failure.
3. **Webhook buffering + ordered replay** (agent disconnected, webhook-shaped traffic) — requests
   that look like a Stripe/GitHub/Twilio/etc. webhook (by signature header, by an explicit path
   rule you declared, or by a JSON-body heuristic) get a `202 Accepted` and are durably queued
   instead of bounced. The instant you reconnect, they replay against your local app **in the exact
   order they were received**, byte-identical, so signature verification still works.

## Architecture (short version)

```
Browser / Stripe ──► Edge (Go) ──online──► yamux stream ──► Agent (your laptop) ──► localhost:3000
                        │
                        └──offline──► classify request:
                                        webhook-shaped → Postgres queue (replayed on reconnect)
                                        browser GET    → snapshot cache (+ offline banner)
                                        anything else  → honest "offline" page
```

Redis tracks agent presence (a 15s heartbeat TTL) to flip a tunnel between online/offline; Postgres
holds the webhook queue and the snapshot cache. See `internal/edge`, `internal/agent`, and
`internal/proto` for the actual implementation.

---

## Getting started

### How you actually get the CLI today

There's no package manager entry or downloadable binary yet (`brew install liveurl` doesn't exist)
— this is a from-source Go project right now. That's a real, honest limitation, not a design
choice; see [Roadmap](#roadmap--current-limitations).

```powershell
git clone <this repo>
cd "Live URL Project"
go build -o bin/liveurl.exe ./cmd/liveurl      # the CLI you run on your dev machine
```

You need Go 1.23+ installed. That's the only requirement for using an *existing* liveurl server
(like the one below) — you don't need Docker, Postgres, or Redis unless you're running the server
side yourself (see [Self-hosting your own server](#self-hosting-your-own-server)).

### Using an existing liveurl server

Someone running a `liveurld` server (a teammate, or yourself if you've deployed one — see below)
gives you an auth token via `liveurld seed`. Then, on your own machine:

```powershell
# one-time
liveurl login lu_xxxxxxxxxxxx --server yourdomain.com:4443 --tls

# every time you want to expose a port
liveurl http 3000 --subdomain myapp --buffer "/webhooks/*"
```

```
connected — forwarding https://myapp.yourdomain.com to 127.0.0.1:3000
```

That's the entire day-to-day workflow — share the URL, it works like any tunnel. `--buffer` is a
list of path globs (e.g. `/webhooks/*`) you know are webhook endpoints, so they're always queued
instead of guessed at (see [classification rules](#how-requests-are-classified-while-offline)).

### What you'll actually see when you go offline

Close your laptop or `Ctrl+C` the agent. The URL doesn't die:

- Reload a page you'd already visited — it renders from cache with a small "liveurl: live server is
  offline — showing a snapshot from ..." banner instead of an error.
- A page you never loaded shows an honest "not available offline" message.
- A webhook provider hits your URL and gets `202 Accepted` with an `X-Liveurl-Buffered: true`
  header — not a timeout, not a bounce.

Reconnect (`liveurl http 3000 ...` again), and every buffered webhook replays automatically, in
order, against your local app. Check what happened at any point with:

```powershell
liveurl status --tunnel myapp          # online/offline, queue depth, snapshot cache size
liveurl events list --tunnel myapp     # every buffered event and its state
liveurl events replay <event-id>       # manually force-retry one right now
liveurl events clear --tunnel myapp    # drop the queue
```

### Self-hosting your own server

If you want your own deployment rather than using someone else's, you need: a domain with DNS
you control (Cloudflare's free DNS-01 API is what this project uses — see the note on DuckDNS
below if you're tempted to use it), a small VPS (a $12-15/month box is comfortably enough for
`liveurld` + Postgres + Redis), and a wildcard TLS cert (`*.yourdomain.com`) via
[`acme.sh`](https://github.com/acmesh-official/acme.sh) and Let's Encrypt.

```powershell
# on your dev machine
docker compose -f deploy/docker-compose.yml up -d      # Postgres + Redis, for local dev
go build -o bin/liveurld.exe ./cmd/liveurld
go build -o bin/demoapp.exe  ./examples/demoapp         # a tiny app to test against

./bin/liveurld.exe serve       # tunnel listener :4443, public HTTP :8080, control API :8081
./bin/liveurld.exe seed        # creates a user, prints an auth token
```

By default `liveurld` expects Postgres on `127.0.0.1:5433` and Redis on `127.0.0.1:6380` (see
`internal/config/config.go`) — deliberately non-default ports to avoid colliding with any
already-installed Postgres/Redis on your machine. Override via `LIVEURL_POSTGRES_DSN` /
`LIVEURL_REDIS_ADDR`.

For a real deployment (not just local dev against `*.lvh.me`, a wildcard domain that resolves to
`127.0.0.1`), set these before `liveurld serve`:

```
LIVEURL_PUBLIC_HOST=yourdomain.com
LIVEURL_PUBLIC_ADDR=:443
LIVEURL_TUNNEL_ADDR=:4443
LIVEURL_TLS_CERT_FILE=/etc/liveurl/tls/fullchain.pem
LIVEURL_TLS_KEY_FILE=/etc/liveurl/tls/privkey.pem
```

and run it behind systemd with the cert renewed on a timer (`acme.sh --cron`, with a reload hook
that restarts `liveurld`). One real-world note from actually doing this: **DuckDNS's nameservers
were too unreliable for Let's Encrypt's DNS-01 validation** (repeated CAA/TXT lookup timeouts from
Let's Encrypt's own validation servers, a documented recurring complaint) — Cloudflare's free DNS
plan doesn't have that problem and is what this project actually runs on. If you go the Cloudflare
route, remember to set the `A`/wildcard `A` records to **"DNS only"** (gray cloud), not "Proxied" —
Cloudflare's proxy doesn't forward the tunnel's custom port at all.

## CLI reference

```
liveurl login <token> [--server host:port] [--tls]     save credentials
liveurl http <port> [--subdomain X] [--buffer "/path/*" ...]   open a tunnel
liveurl events list --tunnel X [--state queued|replaying|delivered|dead]
liveurl events replay <event-id>         manually retry one buffered event now
liveurl events clear --tunnel X          drop all buffered events for a tunnel
liveurl status --tunnel X                online/offline, queue depth, snapshot size

liveurld serve                           run the server
liveurld seed [--email you@x.com]        create a user + print a token
```

## How requests are classified while offline

In order, first match wins:

1. `--buffer` path globs you declared when opening the tunnel.
2. Known provider signature headers (`Stripe-Signature`, `X-GitHub-Event`, `X-Hub-Signature-256`,
   `X-Twilio-Signature`, `Svix-Signature`, `X-Shopify-Hmac-Sha256`, `X-Slack-Signature`, ...).
3. Heuristic: non-GET with a JSON/form body and no `text/html` in `Accept`.

Anything else that's a `GET`/`HEAD` tries the snapshot cache; everything else gets an honest
offline page. Replayed requests are sent byte-identical (same headers, same body) with two added
headers — `X-Liveurl-Buffered: true` and `X-Liveurl-Original-Timestamp` — so a provider's signature
verification still passes; if your app enforces a tight webhook timestamp-tolerance window, that's
the header to widen against.

## Repo layout

`internal/proto` (wire handshake), `internal/agent` (the CLI's tunnel client), `internal/edge`
(the server's public listeners, + `classify`, `snapshot`, `replay` subpackages), `internal/control`
(the private REST API behind `liveurl events`/`status`), `internal/store` (Postgres + Redis),
`cmd/liveurld`, `cmd/liveurl`, `examples/demoapp`.

## Tests

```powershell
go test ./...
```

Unit tests cover the webhook classifier and snapshot cacheability rules. The replay package's test
is an integration test that talks to a real Postgres (via `docker compose`); it skips itself if
Postgres isn't reachable.

## Roadmap / current limitations

Being upfront about what this is *not*, today:

- **No self-serve signup.** Accounts are created by whoever runs `liveurld seed` — there's no
  website, no "create account" button. Fine for a self-hosted team tool, not yet a product someone
  signs up for.
- **No packaged binary distribution.** `go build` from source is the only install path — no
  `brew`/`npm`/downloadable release yet.
- **CLI only.** Everything above is terminal-driven; no web dashboard.
- **Single edge node, single region.** No multi-region routing yet.
- Snapshot caching is passive-only (only pages you actually visited while online get cached) — no
  active crawler yet.
