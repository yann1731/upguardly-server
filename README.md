# UpGuardly — Backend

The Go backend for **UpGuardly**, a service‑monitoring and alerting platform (think
UptimeRobot). It periodically probes user‑defined targets over **HTTP**, **TCP port**
and **ICMP ping**, records latency/health, and fans out **alerts** over email, SMS,
Discord and Slack when a monitor changes state.

The codebase ships **two binaries**:

| Binary | Path | Responsibility |
|--------|------|----------------|
| `server`    | `cmd/server`    | REST API (`api.upguardly.com/v1/...`), auth, billing, and an embedded single‑node scheduler |
| `scheduler` | `cmd/scheduler` | Horizontally‑scalable distributed scheduler that partitions monitors across instances via etcd |

---

## Table of contents

- [Tech stack](#tech-stack)
- [Architecture](#architecture)
- [Project layout](#project-layout)
- [Data model](#data-model)
- [HTTP API](#http-api)
- [Authentication & authorization](#authentication--authorization)
- [Monitoring engine](#monitoring-engine)
- [Distributed scheduler](#distributed-scheduler)
- [Alerting](#alerting)
- [Billing (Stripe)](#billing-stripe)
- [Security](#security)
- [Observability](#observability)
- [Configuration](#configuration)
- [Build & run](#build--run)
- [Testing](#testing)
- [Docker & deployment](#docker--deployment)

---

## Tech stack

- **Language:** Go 1.25
- **Web framework:** [Gin](https://github.com/gin-gonic/gin)
- **Database:** PostgreSQL (port 5432) via the [Prisma Client Go](https://github.com/steebchen/prisma-client-go) ORM
- **Auth:** [SuperTokens](https://supertokens.com/) (email/password + sessions)
- **Billing:** [Stripe](https://github.com/stripe/stripe-go) (`stripe-go/v76`)
- **Distributed coordination:** [etcd](https://etcd.io/) v3 (leases + watches)
- **Metrics:** Prometheus (`prometheus/client_golang`)
- **Email:** SMTP via `gomail`
- **Config:** environment variables (`joho/godotenv` loads `.env` in dev)

---

## Architecture

```
                       ┌────────────────────────────┐
   Browser / Client ──▶│  server  (cmd/server)      │
                       │  Gin REST API  :8080        │
                       │  ├─ SuperTokens middleware  │
                       │  ├─ rate limit / sec headers│
                       │  ├─ Stripe webhooks         │
                       │  └─ embedded scheduler*     │
                       └─────────────┬───────────────┘
                                     │ Prisma
                                     ▼
                            ┌──────────────────┐
                            │   PostgreSQL     │◀── shared with SuperTokens core
                            └──────────────────┘
                                     ▲
                                     │ Prisma (read monitors, write results/history)
   ┌────────────────────────────────┴───────────────────────────────┐
   │  scheduler × N  (cmd/scheduler)                                  │
   │  ├─ etcd lease + membership watch  ──▶  partition ownership      │
   │  ├─ owns subset of monitors (md5(monitorID) % partitionCount)  │
   │  └─ runs per‑monitor ticker goroutines                          │
   └─────────────────────────────────────────────────────────────────┘

* The single `server` process also runs an in‑process `scheduler.Scheduler`
  (no etcd/partitioning) so a one‑box deployment works without the distributed
  scheduler. For scale‑out, run dedicated `scheduler` instances instead.
```

There are **two scheduler implementations** that share the same check/alert logic:

- `scheduler.Scheduler` — embedded in `cmd/server`. Runs every enabled monitor on
  this single process.
- `scheduler.DistributedScheduler` — used by `cmd/scheduler`. Only runs monitors
  whose partition this instance owns.

Both derive alerting decisions from the open‑incident row in Postgres, so alert
state survives restarts and partition handoffs with no instance‑local storage.

---

## Project layout

```
cmd/
  server/      main.go     # API server + embedded scheduler
  scheduler/   main.go     # distributed scheduler worker
internal/
  api/
    router.go              # route table, CORS, security headers, metrics endpoint
    handlers/              # Gin handlers: monitors, integrations, orgs, members,
                           #   invitations, subscriptions, health
    middleware/            # auth, org_auth (RBAC), ratelimit, metrics
  auth/                    # SuperTokens init + session middleware
  config/                  # env-var config loader + missing-secret warnings
  alerter/                 # channel implementations: email, sms, discord, slack
  mailer/                  # SMTP mailer (invitation emails)
  monitor/                 # checkers: http.go, port.go, ping.go + SSRF validate.go
  scheduler/               # scheduler.go (embedded) + distributed.go
  coordination/            # etcd coordinator + partition manager
  stripeservice/           # Stripe client wrapper (checkout, portal, webhooks)
  metrics/                 # Prometheus metric definitions
  models/                  # domain types, the Store interface, request DTOs
  database/                # Prisma client wrapper + Store implementation
prisma/
  schema.prisma            # data model (source of truth)
  migrations/              # SQL migrations applied via `migrate deploy`
entrypoint.sh              # runs `prisma migrate deploy` then exec's the binary
Dockerfile                 # multi-stage: dev / builder / production (distroless-ish alpine)
```

The `internal/database/prisma/` directory contains **generated** code
(`go run github.com/steebchen/prisma-client-go generate`) and the query engine
binary; it is gitignored and produced at build time.

---

## Data model

Defined in `prisma/schema.prisma`. PostgreSQL is the store; tables use
`snake_case` via `@map`. All IDs are CUIDs.

| Model | Purpose | Key fields |
|-------|---------|-----------|
| `Organization` | Tenant / team | `name`, `ownerId` |
| `OrganizationMember` | User ↔ org with a role | `userId`, `role` (OWNER/ADMIN/MEMBER/VIEWER), unique `(orgId, userId)` |
| `Invitation` | Pending email invite | `email`, `role`, `token` (stored **hashed**), `status`, `expiresAt` |
| `Subscription` | Stripe billing state per org | `plan` (FREE/PRO/ENTERPRISE), `status`, `stripeCustomerId`, `stripeSubscriptionId`, period dates |
| `Monitor` | A thing being watched | `userId`, optional `orgId`, `type` (HTTP/PORT/PING), `target`, `interval`, `timeout`, `enabled` |
| `MonitorResult` | One check outcome | `status` (UP/DOWN/DEGRADED), `latency` (ms), `statusCode?`, `message?`, `checkedAt` |
| `Alert` | Notification rule on a monitor | `channel` (EMAIL/SMS/DISCORD/SLACK), `target`, `enabled` |
| `AlertHistory` | Log of sent alerts | `status`, `message`, `sentAt` |

> **Note:** the database is **shared with the SuperTokens core**. Migrations are
> applied with `prisma migrate deploy` (never `db push`) precisely so Prisma never
> drops the SuperTokens‑owned tables in the same schema (see `entrypoint.sh`).

All access goes through the `models.Store` interface (`internal/models/store.go`),
implemented by `database.PrismaStore`. This indirection makes handlers unit‑testable
against a mock store (`handlers/mock_store_test.go`).

---

## HTTP API

Base path: **`/v1`**. JSON in/out. Convention: `api.upguardly.com/v1/{route}`.

### Public

| Method | Path | Notes |
|--------|------|-------|
| `GET`  | `/v1/health` | Liveness check |
| `POST` | `/v1/webhooks/stripe` | Stripe webhook; verified by signature, strict rate limit |
| `*`    | `/auth/*` | SuperTokens‑managed auth routes (sign up/in/out, session) |
| `GET`  | `/metrics` | Prometheus metrics (guarded by `METRICS_TOKEN` bearer if set) |

### Authenticated (`AuthRequired` — valid SuperTokens session)

**Monitors**

| Method | Path |
|--------|------|
| `POST` | `/v1/monitors` |
| `GET` | `/v1/monitors` |
| `GET` | `/v1/monitors/:id` |
| `PUT` | `/v1/monitors/:id` |
| `DELETE` | `/v1/monitors/:id` |
| `GET` | `/v1/monitors/:id/results` (`?limit=` 1‑1000, default 100) |
| `GET` | `/v1/monitors/:id/channels` (integrations merged with this monitor's overrides) |
| `PUT` | `/v1/monitors/:id/channels/:channelId` (per‑monitor opt‑in/opt‑out override) |
| `DELETE` | `/v1/monitors/:id/channels/:channelId` (revert to the account‑wide setting) |

**Integrations (notification channels)** — account‑level alert destinations
every monitor inherits; EMAIL targets are pinned server‑side to the account email.

| Method | Path |
|--------|------|
| `POST` | `/v1/notification-channels` |
| `GET` | `/v1/notification-channels` |
| `PUT` | `/v1/notification-channels/:id` |
| `DELETE` | `/v1/notification-channels/:id` |

**Invitations**

| Method | Path |
|--------|------|
| `POST` | `/v1/invitations/:token/accept` |

**Organizations** (role‑gated — see below)

| Method | Path | Min role |
|--------|------|----------|
| `POST` | `/v1/organizations` | (any authed user) |
| `GET` | `/v1/organizations` | (any authed user) |
| `GET` | `/v1/organizations/:id` | VIEWER |
| `PUT` | `/v1/organizations/:id` | ADMIN |
| `DELETE` | `/v1/organizations/:id` | OWNER |
| `GET` | `/v1/organizations/:id/members` | VIEWER |
| `PUT` | `/v1/organizations/:id/members/:memberId` | ADMIN |
| `DELETE` | `/v1/organizations/:id/members/:memberId` | MEMBER |
| `POST` | `/v1/organizations/:id/invitations` | ADMIN |
| `GET` | `/v1/organizations/:id/invitations` | ADMIN |
| `DELETE` | `/v1/organizations/:id/invitations/:invId` | ADMIN |
| `GET` | `/v1/organizations/:id/subscription` | VIEWER |
| `POST` | `/v1/organizations/:id/subscription` | OWNER (creates Stripe checkout) |
| `POST` | `/v1/organizations/:id/subscription/portal` | OWNER (Stripe billing portal) |

Mutating endpoints additionally pass through `StrictRateLimit()` (30 req/min/IP).

---

## Authentication & authorization

- **Authentication** is handled by **SuperTokens** (email/password recipe + session
  recipe), initialised in `internal/auth/supertokens.go`. The Gin app mounts the
  SuperTokens middleware globally; `middleware.AuthRequired()`
  (`internal/api/middleware/auth.go`) verifies the session and stores the user ID
  under the `userId` context key.
- **Authorization** for org‑scoped routes uses `middleware.RequireOrgRole(store, minRole)`
  (`internal/api/middleware/org_auth.go`). It loads the caller's membership and enforces
  a role hierarchy: `OWNER > ADMIN > MEMBER > VIEWER` (`models.RoleAtLeast`). On success
  it sets `orgId` and `orgRole` in context.
- Resource ownership for personal monitors is enforced in the store layer — every
  monitor query is scoped by `userId`.

---

## Monitoring engine

`internal/monitor` defines a `Checker` interface and a factory `NewChecker(type)`:

- **`HTTPChecker`** (`http.go`) — issues a `GET` with a custom User‑Agent. Maps status
  codes to health: 2xx/3xx → `UP`, 4xx/5xx → `DOWN`; `>2000ms` while UP → `DEGRADED`
  ("high latency"). Records the response `statusCode`.
- **`PortChecker`** (`port.go`) — TCP dial to `host:port`.
- **`PingChecker`** (`ping.go`) — ICMP reachability.

Each check returns a `models.CheckResult{ Status, Latency, StatusCode?, Message }`.

The scheduler runs one goroutine per monitor with a `time.Ticker` at the monitor's
`interval`. On each tick it re‑reads the monitor from the DB (so config changes apply
live), runs the check, **persists a `MonitorResult`**, and — when the status
**changes** from the last observed value — triggers alerts. The very first observation
seeds last‑status without alerting (no spurious "recovered/down" on startup).

---

## Distributed scheduler

For scale‑out, run multiple `cmd/scheduler` instances. Coordination lives in
`internal/coordination`:

- **`Coordinator`** (`coordinator.go`) registers the instance in etcd under
  `/upguardly/schedulers/instances/<id>` with a **lease** (TTL keep‑alive). It
  watches that prefix and emits `MembershipEvent`s whenever instances join/leave.
- **`PartitionManager`** (`partition.go`) maps every monitor to a partition with
  `md5(monitorID) % partitionCount` (md5 so the identical expression runs in SQL —
  the sync query filters to owned partitions in the database), and assigns
  partition `p` to the instance at
  sorted index `i` when `p % instanceCount == i`. On membership change it computes a
  `PartitionDelta{Gained, Lost}` and the scheduler starts/stops jobs accordingly.
- Alert state lives in the open‑incident row in Postgres (see `incidents.go`),
  so a restart or partition handoff recovers without re‑alerting.

This gives roughly even monitor distribution and automatic rebalancing on
scale up/down, with no monitor checked by two instances at once.

---

## Alerting

`internal/alerter` defines an `Alerter` interface and a `Manager` that holds one
implementation per channel:

| Channel | Target meaning | Impl |
|---------|----------------|------|
| `EMAIL` | recipient address | `email.go` (SMTP via gomail) |
| `SMS` | phone number | `sms.go` (Twilio) |
| `DISCORD` | webhook URL | `discord.go` |
| `SLACK` | webhook URL | `slack.go` |

`Manager.Send(ctx, channel, target, monitor, result)` selects the alerter, injects the
per‑alert `target`, and dispatches. Every send is logged to `AlertHistory`.

---

## Billing (Stripe)

`internal/stripeservice` wraps the Stripe SDK; handlers live in
`handlers/subscriptions.go`.

- **Checkout** (`POST .../subscription`): client sends only a `plan`
  (`PRO`/`ENTERPRISE`) and optional **relative** success/cancel paths. The server
  builds absolute redirect URLs from the trusted `WEBSITE_DOMAIN` (open‑redirect
  protection) and creates a Stripe Checkout session.
- **Billing portal** (`POST .../subscription/portal`): returns a Stripe portal URL.
- **Webhooks** (`POST /v1/webhooks/stripe`): signature‑verified. Handles
  `customer.subscription.created/updated/deleted` and `invoice.payment_failed`,
  upserting the org's `Subscription`. Unrecognised price IDs are rejected (not silently
  defaulted) to avoid accidental plan escalation.

---

## Security

The backend hardening (see `internal/api/router.go`, `middleware/`, `monitor/validate.go`):

- **Security headers** on every response: `X-Content-Type-Options`, `X-Frame-Options:
  DENY`, `X-XSS-Protection`, `Referrer-Policy`, `Strict-Transport-Security`.
- **CORS** locked to the configured `WEBSITE_DOMAIN` with credentials allowed.
- **Rate limiting** — fixed‑window per‑IP: 300 req/min default, 30 req/min on
  sensitive writes (`StrictRateLimit`). In‑memory; swap for Redis for multi‑instance
  API.
- **SSRF prevention** — `monitor.ValidateTarget` rejects targets that resolve to
  private/reserved ranges (RFC‑1918, loopback, link‑local incl. cloud metadata
  `169.254.0.0/16`, IPv6 ULA/link‑local, etc.). Run at create and on target/type
  update; the scheduler re‑validates at runtime to blunt DNS‑rebinding.
- **Invitation tokens** — a 32‑byte random token is emailed to the invitee, but only
  its **SHA‑256 hash** is stored; tokens are never returned in list responses.
- **Metrics endpoint** can be bearer‑guarded with `METRICS_TOKEN`.
- **Stripe redirect URLs** are always server‑constructed from relative paths.
- Config loader **warns** at startup about missing production secrets
  (`DATABASE_URL` default, `SUPERTOKENS_API_KEY`, Stripe keys, `METRICS_TOKEN`,
  `WEBSITE_DOMAIN`).

> See the project memory note "Security audit completed" for the full audit history.

---

## Observability

Prometheus metrics (`internal/metrics/metrics.go`), exposed at `GET /metrics`:

| Metric | Type | Labels |
|--------|------|--------|
| `upguardly_monitor_checks_total` | counter | monitor_id, monitor_name, monitor_type, status |
| `upguardly_monitor_check_latency_ms` | histogram | monitor_id, monitor_name, monitor_type, status |
| `upguardly_monitor_status` | gauge (1=UP, 0=DEGRADED, ‑1=DOWN) | monitor_id, monitor_name, monitor_type |
| `upguardly_alerts_sent_total` | counter | monitor_id, monitor_name, channel, status |
| `upguardly_http_requests_total` | counter | method, path, status_code |
| `upguardly_http_request_duration_seconds` | histogram | method, path, status_code |

HTTP metrics are collected by `middleware.MetricsMiddleware()`.

---

## Configuration

All config is loaded from environment variables in `internal/config/config.go`
(`.env` is auto‑loaded in development). Defaults shown in parentheses.

| Variable | Default | Purpose |
|----------|---------|---------|
| `PORT` | `8080` | API listen port |
| `DATABASE_URL` | `postgresql://postgres:postgres@localhost:5432/upguardly?sslmode=disable` | PostgreSQL DSN (point at PgBouncer with `?pgbouncer=true` when pooling) |
| `DIRECT_DATABASE_URL` | — (falls back to `DATABASE_URL`) | Direct (non‑pooled) Postgres DSN used for migrations only; set when `DATABASE_URL` goes through PgBouncer |
| `EMBEDDED_SCHEDULER` | `false` | Run the in‑process scheduler inside `server` (single‑box only — keep `false` when a dedicated `scheduler` runs or `server` has >1 replica) |
| `REDIS_URL` | — | Shared Redis for distributed rate limiting (in‑memory fallback when unset; limiter fails open on Redis errors) |
| `TRUSTED_PROXIES` | private ranges (`10/8`, `172.16/12`, `192.168/16`, `127.0.0.1/32`) | Comma‑separated proxy CIDRs gin trusts for client‑IP resolution (rate limiting) |
| `SUPERTOKENS_CONNECTION_URI` | `http://localhost:3567` | SuperTokens core URL |
| `SUPERTOKENS_API_KEY` | — | SuperTokens core API key |
| `API_DOMAIN` | `http://localhost:8080` | Public API origin (for SuperTokens) |
| `WEBSITE_DOMAIN` | `http://localhost:3000` | Frontend origin — CORS + redirect/invite links |
| `SENDGRID_API_KEY` / `SENDGRID_FROM` / `SENDGRID_FROM_NAME` | — / — / `Upguardly` | SendGrid email alerter + invitations (API key, verified sender, display name) |
| `EMAIL_ENABLED` | `true` | Kill switch for ALL outbound email (alerts, invites, reset, verify). Set `false` in dev/load tests; sends become dry-run log lines |
| `TWILIO_SID` / `TWILIO_TOKEN` / `TWILIO_FROM` | — | SMS alerter |
| `STRIPE_SECRET_KEY` / `STRIPE_WEBHOOK_SECRET` | — | Stripe API + webhook verification |
| `STRIPE_PRO_PRICE_ID` / `STRIPE_ENTERPRISE_PRICE_ID` | — | Plan → price mapping |
| `METRICS_TOKEN` | — | Bearer token guarding `/metrics` |
| `ETCD_ENDPOINT` | `http://localhost:2379` | etcd endpoint (distributed scheduler) |
| `ETCD_USERNAME` / `ETCD_PASSWORD` | — | etcd auth |
| `SCHEDULER_INSTANCE_ID` | `scheduler-0` | Unique instance id |
| `SCHEDULER_PARTITION_COUNT` | `64` | Number of partitions |
| `SCHEDULER_LEASE_TTL_SECONDS` | `30` | etcd lease TTL |
| `SCHEDULER_SYNC_INTERVAL_SECONDS` | `10` | Monitor resync cadence |

---

## Build & run

```bash
# Generate the Prisma client (required before building; needs DATABASE_URL set)
go run github.com/steebchen/prisma-client-go generate

# Apply migrations (requires a running PostgreSQL)
go run github.com/steebchen/prisma-client-go migrate dev --name <name>

# Build everything
go build ./...

# Run the API server (also runs the embedded single-node scheduler)
go run ./cmd/server

# Run a distributed scheduler worker (needs etcd)
go run ./cmd/scheduler
```

The full `go build ./...` only succeeds once the Prisma client has been generated.

---

## Testing

```bash
go test ./...            # all tests
go test -v ./...         # verbose
go test -race ./...      # race detector (used in CI)
go vet ./...
```

Handler tests run against a mock `Store` (`internal/api/handlers/mock_store_test.go`);
there are unit tests for monitors, alerts, health, and the domain models.

---

## Docker & deployment

- **`Dockerfile`** is multi‑stage: a `dev` stage (`go run` with live Prisma generate),
  a static (CGO‑free) `builder` producing `server`, `scheduler` and a bundled
  `prisma-cli`, and a slim `alpine` production stage running as a non‑root user.
  `entrypoint.sh` runs `prisma migrate deploy` before exec'ing the binary. The Prisma
  query‑engine cache is baked into the image (`XDG_CACHE_HOME=/tmp`) so the container
  never tries to download it at startup.
- **`docker-compose.yaml`** (repo root) wires up `server`, `scheduler`, `db`
  (Postgres 18), `supertokens`, and `etcd` with health checks.
- **CI/CD** (`.github/workflows/ci-cd.yml`) triggers on `v*` tags: runs `go vet` +
  `go test -race`, builds and pushes a GHCR image, then deploys to **staging** and
  **production** over SSH (`docker compose pull && up -d`), gated on a `/v1/health`
  check.

> Dev networking gotcha: container DNS/egress is gated by ufw rules; a
> "hostname could not be resolved" error or a stale image is usually the cause
> (see project memory "Dev Docker networking").
