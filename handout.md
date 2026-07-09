# liveurl — Session Handout

Purpose: hand this whole project to a **new Claude session** with zero context loss. Read this top to bottom before touching anything. It covers what the product is, everything built so far in the order it happened, every bug found and fixed, every real infrastructure decision and why, current live production state, and what's still open.

A note on secrets: this file deliberately does **not** embed live credentials (Cloudflare API token, production auth token, etc.) — it says where each one lives / how to regenerate it instead. This is a judgment call made without asking the user first; if you want them inline for convenience, the user can add them (they're already visible in the prior conversation history).

---

## 1. TL;DR — current state in 60 seconds

- **Product**: `liveurl` — a localhost tunnel (ngrok-alternative) whose differentiator is *surviving the tunnel agent going offline*: webhook buffering + ordered replay, and cached page snapshots with an offline banner.
- **Stack**: Go (agent + edge server), Postgres, Redis, vanilla-JS embedded web dashboard. Module path `github.com/Tehman700/liveurl`. MIT licensed.
- **It is deployed for real right now**: `https://tideover.site`, on an AWS EC2 instance (`i-00cf08d9fc565b40a`, t3.small, ap-south-1/Mumbai), Elastic IP `65.2.198.192`, DNS on Cloudflare, real Let's Encrypt wildcard cert, running as a systemd service.
- **Local dev environment** still exists independently at `*.lvh.me:8080` with its own Postgres/Redis (ports 5433/6380 to avoid clashing with the user's native Windows Postgres/Redis services on 5432/6379).
- **There is no git repository yet.** Nothing has been committed or pushed anywhere. This is the single biggest open item.
- Full local test suite passes (`go test ./...`). Full production regression suite (live proxy → offline → snapshot fallback → webhook buffer → reconnect → ordered replay) has been manually verified against the real `tideover.site` deployment multiple times.
- SSH access to the VPS: `ssh -i D:\liveurl.pem ubuntu@65.2.198.192` (key path is on the user's D: drive).

---

## 2. The problem this solves (why this project exists)

Researched at the start of the session: every existing tunnel tool (ngrok, Cloudflare Tunnel, localtunnel, frp/rathole, Pinggy, etc.) shares one fatal flaw — **close your laptop and the URL dies**. Nobody combines:
1. A live reverse-proxy tunnel (the ngrok part, solved by everyone), with
2. Durable webhook buffering + ordered replay for when you're offline (partially solved by Hookdeck/webhook.site, but those aren't tunnels), with
3. A cached "last known state" snapshot fallback for browser traffic while offline (nobody does this).

Secondary pain points identified in existing tools: ngrok's free tier (2-hour sessions, random subdomain each restart, 1GB/month bandwidth cap, interstitial warning page), self-hosted options (frp/rathole) being reliable but painful to configure and not resilient to reconnects.

`liveurl`'s pitch: **the tunnel URL that survives your laptop going to sleep.**

Name origin: considered "Keepalive" as a brandable name (checked for collisions — clean), but the user had already bought `tideover.site` from Namecheap before that suggestion — "tide someone over" (help through a rough patch until things return to normal) turned out to be a very apt name for what the product does. Went with that.

---

## 3. Architecture (as actually built)

Two binaries, one shared module:

```
cmd/liveurld/          — the server binary (agent-facing tunnel listener, public HTTP listener, control API, dashboard)
cmd/liveurl/           — the CLI agent binary developers run on their own machine
internal/proto/        — shared handshake types + wire constants (agent <-> server)
internal/agent/        — agent's dial/reconnect loop + local-port forwarding
internal/edge/         — Router (public HTTP), TunnelServer (agent connections), rate limiter
internal/edge/classify/ — webhook-vs-browser classifier
internal/edge/snapshot/ — passive snapshot cache rules (cacheability, banner injection)
internal/edge/replay/  — ordered webhook replay engine
internal/control/      — private REST API (tunnels/events/status), used by both the CLI and the dashboard
internal/store/        — Postgres repos (users, tunnels, webhook_events, snapshots) + Redis presence
internal/dashboard/    — embedded web dashboard (go:embed, vanilla HTML/CSS/JS)
internal/config/       — env-var config, shared getenv/getenvFloat/getenvInt helpers
internal/cliconfig/    — the CLI's local ~/.liveurl/config.json handling
examples/demoapp/      — a tiny Go HTTP app used for local testing
```

### Tunnel protocol
Agent dials the tunnel port (`:4443`, TLS in production), does a JSON handshake on the first yamux stream (auth token, requested subdomain, buffer-rule globs), then every subsequent HTTP request from the public listener is forwarded over a **fresh yamux stream per request**. WebSocket upgrades are passed through by detecting the 101 response and switching to raw bidirectional byte copying on the same stream. Presence (online/offline) is tracked in Redis with a 15-second TTL, refreshed by an agent heartbeat every 5 seconds.

### The three pillars
1. **Live proxy** — when the agent is connected, `Router` looks up the session by subdomain and proxies the request straight through.
2. **Webhook buffering + ordered replay** — when offline, incoming requests are classified (see below); webhook-shaped ones are persisted verbatim (method/path/query/headers/body) to Postgres and get `202 Accepted`. On reconnect, `internal/edge/replay` drains the queue **oldest-first**, replaying byte-exact requests with `X-Liveurl-Buffered: true` and `X-Liveurl-Original-Timestamp` headers added. A `5xx` response stops the drain and re-queues the failed event for the next reconnect; a manual `liveurl events replay <id>` (or the dashboard's Replay button) can retry one immediately.
3. **Snapshot fallback** — while online, successful cacheable `GET` responses are passively written into a Postgres-backed snapshot cache (per tunnel, capped at 100MB, oldest evicted first). Cacheability rules deliberately exclude anything with a `Cookie`/`Authorization` request header or a `Set-Cookie` response header, or `Cache-Control: private|no-store` — this is a **hard security rule**, not a nice-to-have: it's what stops one visitor's authenticated page from leaking into the shared cache for a different visitor. While offline, cached `GET`s are served with an injected "offline, showing a snapshot from ⟨time⟩" banner; uncached paths get an honest "not available offline" page instead of a dead connection.

### Webhook classifier (`internal/edge/classify`)
Order, first match wins:
1. User-declared `--buffer` path globs (explicit, always wins).
2. Known provider signature headers (`Stripe-Signature`, `X-GitHub-Event`, `X-Hub-Signature-256`, `X-Twilio-Signature`, `Svix-Signature`, `X-Shopify-Hmac-Sha256`, `X-Slack-Signature`, etc.).
3. Heuristic: non-GET + JSON/form content-type + no `text/html` in `Accept`.

**Known, deliberately-not-fixed limitation**: a plain frontend `fetch()` POST (e.g. an app's own "add a note" AJAX call) with `Content-Type: application/json` and no explicit `Accept: text/html` will match rule 3 and get silently buffered as if it were a webhook when offline, rather than failing with a clear "you're offline" error. Demonstrated deliberately in the `liveurl-notes-app` test app (see §6) — the app's own JS surfaces this ("Buffered: the agent is offline...") rather than hiding it. Flagged in the roadmap as something a future "never buffer this path" declaration (the inverse of `--buffer`) could fix.

---

## 4. Build timeline — v1 local build (Stages 0–3)

Built under a formal approved plan (Go stack, local-first, `*.lvh.me` simulating public subdomains since it always resolves to 127.0.0.1).

- **Stage 0**: Installed Go via winget (wasn't installed). Scaffolded the repo, `deploy/docker-compose.yml` for Postgres 17 + Redis 8.
  - **Bug found**: Postgres/Redis containers silently lost the port-binding race to **pre-existing native Windows Postgres/Redis services** already listening on 5432/6379. Fixed by remapping Docker's ports to `5433`/`6380` (recorded in `docker-compose.yml` and `internal/config`).
- **Stage 1**: Core tunnel — proto handshake, `TunnelServer`, `Router`, agent dial/reconnect with exponential backoff+jitter, WebSocket passthrough.
- **Stage 2**: Offline state machine, classifier, webhook buffer + ordered replay engine, `internal/control` REST API, `events`/`status` CLI commands.
- **Stage 3**: Passive snapshot cache + offline banner injection.
- **Testing**: unit tests for classifier and snapshot cacheability; an integration test for replay ordering that talks to the real local Postgres (skips itself if unreachable).
- **Bug found during manual verification**: the CLI's `liveurl events replay <id>` reported "replayed" even when the local app returned `500`. Root cause: `replay.ReplayEvent` didn't surface the actual HTTP outcome to its caller. **Fixed**: changed its signature to return the status code; the CLI now correctly reports `failed ... event re-queued for retry`.
- Full local demo walkthrough validated end-to-end multiple times: live proxy parity with direct localhost, unknown-subdomain 404, snapshot hit/miss/banner, ordered webhook buffering + replay, retry-then-requeue on `5xx`, manual replay, reconnect resuming the same subdomain.

A Node/Express test app was later built at `examples/demoapp`-adjacent for early testing; a more elaborate one lives at `D:\liveurl-notes-app` (see §6).

---

## 5. Build timeline — real deployment

### Domain/DNS saga (this took several attempts — read this before repeating it)

1. First attempt: **DuckDNS** (`liveurl.duckdns.org`, free). DuckDNS's wildcard behavior is genuinely correct for this use case (any subdomain of a registered name auto-resolves to the same IP, no per-tunnel DNS registration needed) and it supports the ACME DNS-01 TXT-record API for free wildcard certs.
   - **Failed 4 times in a row** issuing the wildcard cert via `acme.sh`'s `dns_duckdns` hook, each with a *different* DNS error (SERVFAIL on CAA lookup, timeout on CAA lookup, timeout on TXT lookup, "Incorrect TXT record"). Conclusion: DuckDNS's authoritative nameservers are genuinely unreliable for this specific operation (their DNS-01 challenge propagation), not a transient fluke — confirmed by prior web research surfacing this as a known recurring complaint.
   - Also discovered mid-attempt: DuckDNS only supports **one TXT record at a time**, so requesting an apex+wildcard SAN pair in one `acme.sh` call is fundamentally racy (this is separate from the reliability issue above, but compounds it).
   - **Decision**: abandoned DuckDNS. User bought a real domain instead.
2. **Bought `tideover.site` on Namecheap.** Considered AWS Route 53 as the DNS host (would give zero-static-key IAM-role auth for `acme.sh`) but **the user's AWS account cannot register/transfer domains** (a common free-tier/new-account restriction) — Route 53 domain registration was a dead end. Went with **Cloudflare** instead: free DNS, reliable DNS-01, a scoped API token instead of an IAM role.
3. Cloudflare setup: added the site, switched Namecheap's nameservers to Cloudflare's (`rodney.ns.cloudflare.com`, `ulla.ns.cloudflare.com`). **Two DNS records manually fixed in Cloudflare's dashboard** (not Namecheap's — nameserver delegation disables Namecheap's own DNS editor):
   - `A tideover.site → 65.2.198.192`, proxy status set to **DNS only** (gray cloud, not orange/"Proxied")
   - `A * → 65.2.198.192` (the wildcard), also **DNS only**
   - **Why DNS-only matters**: Cloudflare's proxy only forwards a fixed list of common web ports and does not forward port 4443 (the tunnel port) at all, and would terminate TLS at their edge instead of passing through to the real cert. Getting this wrong would have silently broken the tunnel port entirely. This is a durable rule for this project: **never enable Cloudflare's orange-cloud proxy on these records.**
   - Verified via `nslookup` against both 1.1.1.1 and 8.8.8.8 directly that the wildcard genuinely resolves for arbitrary subdomains before proceeding.
4. Cloudflare API token created (My Profile → API Tokens → "Edit zone DNS" template, scoped to just the `tideover.site` zone).
5. **Cert issuance via `acme.sh`'s `dns_cf` hook succeeded on the first real attempt** (apex + wildcard together in one call — Cloudflare doesn't have DuckDNS's single-TXT-record limitation), taking under a minute. Night-and-day difference from DuckDNS.

### AWS EC2 provisioning
- User launched via Console (AWS CLI credentials in this environment are invalid — `aws sts get-caller-identity` fails — so this had to go through the Console, not `aws ec2 run-instances`).
- Instance: Ubuntu Server (came up as 26.04 LTS "resolute"), **t3.small** (1.9GB RAM), Elastic IP `65.2.198.192` associated.
- Security group (`sg-068c97ed9fe84e503`, "launch-wizard-5"): SSH/22 restricted to the user's IP, HTTP/80 and HTTPS/443 open to `0.0.0.0/0`, Custom TCP/4443 open to `0.0.0.0/0` (the tunnel port). No other ports open — Postgres/Redis/control-API stay unreachable from outside by design.
- Added 1GB swap file (t3.small has no swap by default and 1.9GB RAM is tight for Postgres+Redis+liveurld).
- Installed Docker via the official convenience script; Postgres+Redis run via the **same** `deploy/docker-compose.yml` as local dev, bound to `127.0.0.1` only (hardened this port-binding as part of the rate-limiting/dashboard work — see §7).
- Cross-compiled `liveurld` locally (`GOOS=linux GOARCH=amd64 go build`) and `scp`'d it up — no Go toolchain needed on the tiny VPS.
- systemd unit `liveurld.service`: runs as user `ubuntu` (not root), `AmbientCapabilities=CAP_NET_BIND_SERVICE` + `CapabilityBoundingSet=CAP_NET_BIND_SERVICE` to allow binding port 443 without root, `Restart=on-failure`, enabled (survives reboots).
- `acme.sh`'s renewal reload hook (`--reloadcmd 'sudo systemctl restart liveurld'`) plus a narrowly-scoped `/etc/sudoers.d/liveurl-restart` entry (`ubuntu ALL=(ALL) NOPASSWD: /usr/bin/systemctl restart liveurld` only) so the cron-driven renewal can restart the service without a password, without granting broad sudo.
- Cert installed to a fixed path (`/etc/liveurl/tls/{fullchain.pem,privkey.pem}`) via `acme.sh --install-cert`, read directly by both `liveurld`'s TLS listeners (tunnel port and public HTTPS port share the same cert).
- Ran `liveurld seed --email admin@tideover.site` once on the box to create the real production user/token (see §1 re: not reproducing the literal token value here).

### Bug found and fixed during production verification
The tunnel handshake's `PublicURL` field was **hardcoded** to the local-dev constant `lvh.me:8080` regardless of actual deployment (`proto.DefaultPublicHost`). This was purely a cosmetic/display bug (routing itself was correct — `Router.BaseHost` was right), but it meant every user, on every deployment, would see the wrong URL echoed back by the agent on connect. **Fixed**: added `PublicHost`/`PublicTLS` fields to `TunnelServer`, threaded through from `cmd/liveurld/main.go`'s config, so the handshake reply now reports the real scheme+host. Verified the agent auto-reconnected with the corrected message (`https://demo.tideover.site` instead of `http://demo.lvh.me:8080`).

### A red herring worth remembering
During the later distribution/rate-limiting work (§7), port 4443 appeared to be unreachable again via `bash`'s `/dev/tcp` mechanism (`cat < /dev/tcp/65.2.198.192/4443` timed out repeatedly), even after re-confirming the security group rule was correct and the server was genuinely listening (`ss -tlnp` on the box showed it bound). **Turned out to be a false negative specific to Git Bash's `/dev/tcp` on this machine** — `PowerShell`'s `Test-NetConnection -ComputerName 65.2.198.192 -Port 4443` immediately returned `TcpTestSucceeded: True`. **Lesson for future debugging on this Windows machine: prefer `Test-NetConnection` (PowerShell) over `/dev/tcp` (Git Bash) for TCP reachability checks** — the latter has given at least one confirmed false timeout on this specific box/network combo.

Also during this period: the user's home IP rotated, breaking the SSH security-group rule temporarily (fixed by re-adding the current IP).

### Full production verification (repeated after every deploy)
Real Let's Encrypt cert confirmed via `openssl s_client` (`Verify return code: 0 (ok)`, no `-k` needed anywhere) and via plain `curl` (rejects without `-k` against a self-signed test cert used earlier for local TLS-code smoke-testing, confirming validation is genuinely enforced, not accidentally disabled). Full cycle confirmed working against the real public domain: live proxy → kill agent → wait for 15s presence TTL → snapshot+banner serving → webhook `202` buffering → reconnect → ordered replay → states flip to `delivered`. Confirmed Postgres (5433), Redis (6380), and the control API (8081) all correctly **time out** from the public internet (only 443/4443 are reachable).

---

## 6. The Node.js test app (`D:\liveurl-notes-app`)

Built to exercise liveurl more realistically than the minimal Go `examples/demoapp`, and deliberately in a **different stack** (Node/Express, not Go) to prove liveurl is stack-agnostic — it operates purely at the HTTP/WebSocket protocol level and has no idea what generated the response on the other end.

Contains, on purpose:
- Multi-page snapshot testing (`/`, `/about`)
- A login-gated `/dashboard` page (demo/demo123) — proves that **cookie-bearing requests are never cached**, so a logged-out visitor (or offline browsing without the cookie) can never see a leaked copy of another session's authenticated page
- A live WebSocket clock — visually shows "ticking = live" vs. "frozen + disconnected = viewing a cached snapshot"
- A notes API (`GET`/`POST /api/notes`) — this is the concrete example of the classifier's known JSON-POST-misclassification limitation described in §3
- `/webhooks/test` (succeeds) and `/webhooks/fail` (always 500, for testing retry/dead-letter)

Run it: `cd D:\liveurl-notes-app && npm install && npm start` (port 4000), then tunnel it with `liveurl http 4000 --subdomain notesapp --buffer "/webhooks/*"`. Cross-machine testing recommendation given to the user: the simplest, most convincing test is just opening the public URL from a second device's browser with **zero setup** — that alone proves genuine internet reachability, not a same-network fluke.

---

## 7. Build timeline — distribution, web dashboard, rate limiting

Requested together as one batch of work, with an upfront design-validation pass via a Plan-mode subagent that read the actual code (not just a description) before implementation — its findings are baked into what was actually built, listed below.

### Preliminary decisions
- **Module path renamed** from bare `liveurl` to `github.com/Tehman700/liveurl` (done *before* any git repo existed — the cheap moment to do it, since changing it after a tagged release would break anyone who'd already `go install`ed it). Every internal import updated via a bulk `sed` pass across all `.go` files.
- **License: MIT** (user's explicit choice, offered alongside Apache-2.0 and a source-available/BSL alternative for protecting a future hosted-service business model — MIT won because there's no hosted business yet and it's the category norm for CLI dev tools).
- `design.md` (shared by the user for dashboard styling) turned out to be **Nike's e-commerce design system** (product cards, campaign photography, sale pricing) — an unrelated spec, not a dashboard design. Resolved with the user: adapt only the **abstract style language** (near-monochrome ink/canvas/soft-cloud palette, pill-shaped buttons, flat shadowless cards, hairline dividers, 8px-based spacing, bold condensed display typography) to dashboard concepts — no e-commerce components forced in.

### Rate limiting (`internal/edge/ratelimit.go`)
In-memory token-bucket limiter (`golang.org/x/time/rate`), not Redis-backed (only one edge node exists today; Redis-backed is the natural upgrade path if/when multi-node routing is ever built). Nil-safe (`(*ipLimiter)(nil).Allow()` always returns `true`) so any construction path that skips the constructor degrades to "no limiting" instead of panicking.

**The critical design correction from the validation pass**: a naive rate limit keyed on source IP alone would be a real product bug — Stripe/GitHub/Twilio send webhooks from a small shared pool of IPs used across *all* their customers globally. Limiting by IP alone means once aggregate provider traffic across the whole platform crosses one bucket, **every tenant's webhooks get throttled simultaneously**, directly breaking the "never miss a webhook" promise. Fixed by keying the tunnel-traffic limiter on `(client IP, subdomain)` instead of IP alone.

Three independent limiter instances:
- `Router.tunnelLimiter` — keyed `ip|subdomain`, ~20 req/s burst 40 (env: `LIVEURL_RATE_HTTP_RPS`/`_BURST`), applied inside `Router.ServeHTTP` right after the subdomain is parsed from the Host header.
- `Router.dashboardLimiter` — keyed on IP alone, ~10 req/s burst 30 (env: `LIVEURL_RATE_DASHBOARD_RPS`/`_BURST`), covers the apex host (landing page + dashboard + its API) — generous since it's mostly the tunnel owner's own polling, but not absent, since `/dashboard/api/*` is now internet-reachable.
- `TunnelServer.handshakeLimiter` — keyed on IP alone, ~10 attempts/minute (env: `LIVEURL_RATE_HANDSHAKE_PER_MIN`), checked in the raw TCP `Accept()` loop *before* any yamux/handshake work begins; over-limit connections are just closed immediately (no HTTP semantics apply this deep, so no `429`/`Retry-After` — that framing is HTTP-only).

**IP extraction note**: uses `r.RemoteAddr`/`conn.RemoteAddr()` directly. Deliberately does **not** trust `X-Forwarded-For`/`CF-Connecting-IP` — Cloudflare is DNS-only (not proxied) for this deployment (see §5), so there is no trusted reverse proxy in front of this box and those headers would be fully attacker-forgeable.

Unit tests in `internal/edge/ratelimit_test.go` specifically prove the shared-IP-pool scenario doesn't cause collateral damage (two different `(ip, subdomain)` keys stay fully independent), plus burst/refill/eviction behavior.

### Web dashboard (`internal/dashboard`)
Vanilla HTML/CSS/JS, embedded into the `liveurld` binary via Go's `embed.FS` — no Node build step, keeps `go build` as the only step to produce the server. Mounted on the **existing public HTTPS listener** at the bare apex host (`Router`'s `sub == ""` branch, which previously only served a static landing page, now does a 3-way dispatch: `/dashboard/api/*` → the control API, `/dashboard*` → the embedded SPA, else → landing page). No new port, no security-group change, reuses the existing cert. Live right now at `https://tideover.site/dashboard`.

**Auth**: no username/password login exists anywhere in the system (only opaque Bearer tokens minted via `liveurld seed`) — the dashboard has a "paste your token" screen, stored in the browser's `localStorage`, sent as `Authorization: Bearer` on every fetch. Confirmed sound specifically *because* this is a multi-tenant subdomain platform: `localStorage` is strictly origin-scoped, so a malicious/compromised tunnel's proxied JS at `foo.tideover.site` cannot read the dashboard's token at the `tideover.site` origin — whereas a cookie scoped to `Domain=.tideover.site` *would* leak across every tenant's sibling subdomain. **Durable rule recorded for this codebase: never scope a cookie to the parent domain.**

**Hard security requirement enforced, not just aspirational**: webhook event data (method/path/query/headers/body) comes from arbitrary internet senders — that's the product's entire point. The dashboard renders this data. Every render path uses `textContent` (via small `el()`/`td()` helper functions), never `innerHTML`, for anything that isn't a literal empty-string container-clear — otherwise any internet sender (not just the tunnel owner) could stored-XSS the tunnel owner's dashboard session and steal the bearer token straight out of `localStorage`. This was tested directly: sent a webhook with a `<script>` tag in the body and confirmed it renders as inert literal text.

**Drive-by fix while touching `router.go`**: `brandedPage()` was splicing the raw Host-derived subdomain into HTML unescaped. It was accidentally inert (the 404 path goes through `http.Error`, which stdlib forces to `text/plain` + `nosniff`), but `serveLanding` already sent real `text/html` — fixed with `html.EscapeString` on the interpolated strings.

Views (all backed by the *existing* `internal/control` REST endpoints — no new backend data endpoints were needed): tunnels list with online/offline badges, per-tunnel status (state, queued events, snapshot pages/bytes), an events table (id/method/path/state/attempts/received-at) with a Replay button per row and a Clear-all button, simple 4-second polling refresh (no new push/SSE layer — deemed appropriately scoped since there's no existing pub/sub wiring for control-API consumers).

### Distribution
- `.goreleaser.yaml` — two `builds` entries (`cmd/liveurl`, `cmd/liveurld`), cross-compiling `linux/darwin/windows × amd64/arm64` (`CGO_ENABLED=0` — confirmed safe, all deps are pure Go including pgx v5's driver), ldflags injecting version/commit/date, zip for Windows / tar.gz elsewhere, checksums, auto-changelog. Validated for real via `goreleaser build --snapshot` / `release --snapshot --clean` (works without any git tag or GitHub remote).
- `--version`/richer `--help` added to both Cobra root commands (`var version = "dev"`, overridden via ldflags at release time; `dev`/`none`/`unknown` when built directly with plain `go build`).
- `LICENSE` (MIT) and `NOTICE` (generated from the **full module graph**, not just the 4 direct deps — the indirect deps also compile into the shipped binary and most require attribution).
- Homebrew: one tap formula packaging both binaries. Needs a separate `Tehman700/homebrew-liveurl` repo + a `HOMEBREW_TAP_GITHUB_TOKEN` Actions secret — both are manual one-time steps for the user, not automatable from here.
- `npm-wrapper/`: a minimal npm package (à la `esbuild`/`swc`) with a postinstall script that downloads the matching GitHub Release asset for the current platform. Needs an `NPM_TOKEN` secret to auto-publish.
- `winget/`: manifest YAML scaffolded, but submission realistically needs a manual PR to `microsoft/winget-pkgs` — not reliably one-shot-automatable the way brew/npm are, scoped as "manifest ready, submission is manual" for v1.
- `.github/workflows/release.yml`: tag-triggered CI running goreleaser + the npm publish step.

### Final verification for this batch
Full local `go build/vet/test` pass. Production redeploy (new `liveurld` binary cross-compiled and `scp`'d up, service restarted). Full regression cycle re-run against the live `tideover.site` deployment: live proxy, dashboard reachable at the real public URL showing real tunnel data (`demo` online, `notesapp` offline from earlier testing), snapshot fallback, webhook buffering, ordered replay on reconnect, and a live rate-limit sanity check — all confirmed working in production, not just locally.

---

## 7a. Build timeline — self-serve signup

Closed §9 item 3 from the prior session: accounts previously only existed via an operator running `liveurld seed`. Added, **local-only so far — not yet deployed to `tideover.site`** (see the note at the end of this section):

- **Migration `0002_password_auth.sql`**: adds a nullable `password_hash` column to `users`. Nullable because `liveurld seed`-created accounts still have no password and stay CLI-token-only.
- **`internal/store/users.go`**: `Store.SignUp(ctx, email, password)` — bcrypt-hashes the password, inserts the user and mints its first auth token in one transaction, and fails with `ErrEmailTaken` if the email is already registered **for any reason, including a password-less seeded account** — letting signup attach a password to an existing seeded email would let anyone take over an operator-provisioned account just by knowing its address. `Store.VerifyPassword(ctx, email, password)` returns one generic `ErrInvalidCredentials` for every failure mode (unknown email, no password set, wrong password) so a login form can't be used to enumerate registered emails.
- **`internal/control/server.go`**: two new unauthenticated routes, `POST /api/signup` and `POST /api/login` (reachable publicly at `/dashboard/api/signup`/`/login`). Signup returns `{email, token}` immediately — no email verification step exists anywhere in this deployment (there's no outbound email sending at all yet), so the token is shown once at signup time, matching how `auth_tokens` has always worked (only the SHA-256 hash is ever persisted — this is also why login can't redisplay an old token and instead mints a fresh one on every successful login). Both routes sit behind a dedicated, tightly-budgeted IP rate limiter (~5 attempts/minute) so password guessing is impractical — separate from and much stricter than the existing per-IP dashboard limiter.
- **Reused, not duplicated, the existing rate limiter**: `internal/edge`'s private `ipLimiter`/`newIPLimiter` were exported to `IPLimiter`/`NewIPLimiter` specifically so `internal/control` could reuse the same token-bucket implementation instead of a second copy.
- **Dashboard UI** (`internal/dashboard/web/`): the old single "paste a token" screen is now three tabs — Sign up / Log in / Have a token? — plus a one-time token-reveal screen with a copy button and "Continue to dashboard," which logs straight in using the token still held in memory.
- **Bug found and fixed while adding tests**: `Store.Migrate` had a latent race — two concurrent callers could both see a brand-new migration as "not yet applied" and race to `INSERT` the same `schema_migrations` primary key, one failing with a duplicate-key error. Pre-existing, but invisible before because only one test package (`replay`) ever called `Migrate` against the shared local Postgres; adding `store` and `control` package tests that also call it made it fire reliably. Fixed with a `pg_advisory_lock` held for the duration of `Migrate`, acquired on a single pooled connection (advisory locks are session-scoped, so lock/unlock must happen on the same connection, not through the pool).
- **Verified**: full `go build/vet/test ./...` pass against the real local Postgres/Redis (docker-compose, ports 5433/6380); then manually end-to-end against a locally-running `liveurld` (alternate ports 8090/8091/4444, to avoid disturbing an already-running local dev instance) via direct HTTP calls mirroring exactly what the dashboard's JS sends: signup → 201 with a usable token, duplicate signup → 409, login with right password → 200 with a *different* fresh token, login with wrong password → 401, 6 rapid logins from one IP → 429 on the 6th.
- **Not yet done**: this has **not been deployed to `tideover.site`** — production is still running the pre-signup binary and hasn't had `0002_password_auth.sql` applied. Deploying is the same cross-compile-and-`scp`-and-restart cycle described in §5/§7 (the systemd service runs `liveurld serve`, which calls `st.Migrate` on startup, so the new migration will apply automatically on first restart with the new binary).

---

## 8. Current production access reference

- SSH: `ssh -i D:\liveurl.pem ubuntu@65.2.198.192`
- Systemd service: `sudo systemctl status|restart liveurld`, logs via `sudo journalctl -u liveurld`
- Docker services (Postgres/Redis) on the box: `docker compose -f ~/liveurl/deploy/docker-compose.yml ps`
- Control API is loopback-only (`127.0.0.1:8081` on the box) — reach it from the dev machine via `ssh -i D:\liveurl.pem -L 18081:127.0.0.1:8081 ubuntu@65.2.198.192`, then point the local CLI's `control_url` at `http://127.0.0.1:18081` (or just use the public dashboard at `https://tideover.site/dashboard` instead, which needs no SSH tunnel).
- Local CLI config: `C:\Users\tehma\.liveurl\config.json` — check `server_addr`/`control_url`/`tls` before assuming which environment (local dev vs. production) a `liveurl` command will hit.
- Production auth token: minted via `liveurld seed --email admin@tideover.site` on the box; if lost, SSH in and run `liveurld seed` again for a fresh one (or `liveurld seed --email <new>` for a distinct account).
- Cloudflare API token (used only for `acme.sh` cert renewal on the box): scoped to "Edit zone DNS" on the `tideover.site` zone only; regenerate via Cloudflare dashboard → My Profile → API Tokens if needed.
- Cert auto-renews via `acme.sh`'s systemd timer + the sudoers-scoped restart hook described in §5 — should be fully hands-off, but worth spot-checking `sudo ~/.acme.sh/acme.sh --list` on the box occasionally.

---

## 9. What's NOT done yet / open threads

In rough priority order for "continue the talk":

1. **Git repo exists locally but has never been pushed anywhere.** Initialized with two commits: distribution scaffolding + this handout, then the self-serve signup work (see §7a). No remote configured, no tag cut yet — goreleaser needs a real tag to produce a real release. Cutting one, and creating/pushing to a GitHub remote, is a "visible to others" action this project's own norms say to confirm with the user first (see §10).
2. **Homebrew tap / npm publish / winget PR are all scaffolded but not executed** — each needs a one-time manual account/secret setup step from the user (see §7's Distribution section for exactly what each needs).
3. ~~No self-serve signup~~ **Done** — see §7a. `POST /api/signup`/`/api/login` on the dashboard's API now let users create an account and get a token without an operator running `liveurld seed`. Still open beneath that: no email verification/password-reset (no outbound email sending exists anywhere in this deployment yet), and no billing/plan tiers.
4. **Snapshot cache is passive-only** — a page is only cached if someone actually visited it while the agent was online. An active crawler (e.g. Playwright walking the app through the live tunnel) was discussed as the fast-follow.
5. **Single edge node / single region** — no multi-node routing exists; the in-memory rate limiter and presence tracking assume this.
6. **The classifier's JSON-POST misclassification limitation** (§3) — not fixed, deliberately demonstrated instead. A "never buffer this path" declaration (inverse of `--buffer`) would fix it.
7. **Privacy Policy / Terms of Service** — discussed as needed once real third-party user data flows through a self-serve product (webhook bodies can contain other people's customer PII). Flagged that any AI-drafted version needs real legal review before being treated as binding — not started.
8. Bandwidth/storage quotas beyond the new rate limiter (e.g. S3/R2-backed storage instead of Postgres `bytea` for snapshot/webhook bodies) — not started.

---

## 10. How to pick this up in a new session

Read this file, then:
- `cd "c:\Users\tehma\Desktop\Live URL Project"`, run `git log --oneline` and `git status` — there **is** a local repo now (two commits: distribution scaffolding, then self-serve signup), but still **no remote and no tag**. Don't assume anything has been pushed anywhere.
- Confirm production is still healthy: `ssh -i D:\liveurl.pem ubuntu@65.2.198.192 "sudo systemctl status liveurld"` and/or just visit `https://tideover.site/dashboard`. Remember production is running the **pre-signup** binary (§7a) — the signup/login tabs won't appear there until it's redeployed.
- If asked to "continue," the natural next steps, per the user's own priorities so far, are: (1) deploy the signup work to `tideover.site` (§7a's last bullet has the how), and (2) create a GitHub remote and cut a first tagged release (unlocks goreleaser + Homebrew/npm publishing, which were built but never executed end-to-end). Ask the user to confirm before pushing anything to a real GitHub remote or redeploying production — those are "visible to others / hard to reverse" actions per this project's own operating norms observed throughout this session (AWS provisioning, DNS changes, and similar were all confirmed with the user before executing).
