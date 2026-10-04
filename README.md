# Order Processing System

A standalone Go backend for customer orders, an admin-managed product catalog,
and automatic processing. It uses Echo v5, domain/application/persistence layers,
JWT Bearer sessions, bcrypt passwords, and a token denylist.

## Requirements mapping

All endpoints below use the `/api/v1` prefix and a Bearer token.

| Assignment requirement | Endpoint / behavior | Access |
| --- | --- | --- |
| Create an order with multiple items | `POST /orders` with `items` and `Idempotency-Key`; returns 201 and PENDING. | Customer |
| Retrieve order details | `GET /orders/{id}` | Owning customer or admin |
| Update order status | `PATCH /orders/{id}/status`; PENDING → PROCESSING → SHIPPED → DELIVERED. | Admin only |
| Automatically process PENDING every five minutes | Embedded worker; first run five minutes after process startup, then every five minutes. | Automatic |
| List all orders, optionally by status | `GET /orders?status=PENDING`; omit status for all accessible orders, follow `next_cursor` for further pages. | Customer's own orders; admin sees all |
| Cancel only a PENDING order | `POST /orders/{id}/cancel`; 200 if PENDING or already CANCELLED; otherwise 409. | Owning customer only |

`CANCELLED` is an additional terminal status used to record cancellation. Admins
advance order status; customers place and cancel their own orders. Authentication,
catalog pricing and regional quotes are additional features beyond the assignment.
The core items-only order flow uses base currency and needs no FX-rate setup.
It still requires authentication and catalog products; there is no unauthenticated
or catalog-free order mode.

For the quickest executable review, run `go mod tidy` then `make smoke-docker`
on a host with Go, Python 3 and Docker access. It provisions the admin, customers,
products and isolated database automatically, exercises the APIs, and cleans up.
It uses a **five-second** worker interval; the
[live-demo runbook](docs/plans/order-processing-hardening/05-live-demo.md) covers
the actual five-minute timing. Run `make acceptance` for the complete local test suite.

Start with this README and the
[requirements/test matrix](docs/plans/order-processing-hardening/04-test-plan.md).
Use the [local test setup](TESTING.md) for complete paths with Docker, native
PostgreSQL, or unit tests without a database.
The [architecture discussion](architecture.md) provides design details.

## Known limitations and next steps

The implementation covers the assignment's order operations and automatic
processing. The following boundaries describe optional extensions and production
work beyond that scope.

| Area | Implemented behavior | Remaining boundary / optional next step |
| --- | --- | --- |
| Create retries | Customer-scoped `Idempotency-Key` support for items and quote requests, payload conflict detection, atomic persistence and bounded lock/operation waits. A key is required for both request forms. | Changing the key on an items-only retry can create another order. Keys are retained indefinitely; `created_at` supports future cleanup, but the proposed 30-day retention policy is not enforced. |
| Status history | Orders store their current status and update time; structured logs describe committed mutations. | There is no durable history of every transition. If required, record previous/new status, actor, reason and time in the same transaction as each change. |
| Scheduling | Each API instance runs a worker at a configurable interval, defaulting to five minutes. Conditional updates and `SKIP LOCKED` batches coordinate concurrent processing. | Replicas have independent tick schedules. A single deployment-wide cadence would require a dedicated scheduler or leader coordination. |
| Commerce workflows | Orders validate catalog products and snapshot quantities and prices. | Stock reservation, payments, refunds and external fulfillment are not implemented. Adding them requires separate business rules and reliable integration workflows. |
| Capacity and operations | Indexed queries, bounded batches, cursor pagination, request admission/deadlines, separate API/worker/probe connection pools, graceful shutdown, worker-aware readiness, structured logs and per-process auth limits. | Production throughput is unmeasured. Add load tests, metrics and tracing; coordinate auth limits across replicas when needed. Use measurements to guide caching or database scaling. |
| Availability | Order data is persisted in PostgreSQL; transactions protect atomic writes and readiness checks database health. The supplied deployment uses one database instance. | Automated failover and backup/restore procedures are not provided or tested. Add them before making production availability commitments. |

Cancellation is retry-safe: repeating it returns 200 with the owned CANCELLED
order, without changing its timestamp. Cancellation of PROCESSING, SHIPPED or
DELIVERED returns 409.

## Retry-safe order creation

Send a required `Idempotency-Key` with every items or quote order request.
Missing, empty, malformed or multiple values return 422 before order persistence. First creation
returns 201; the same customer, key and payload return the original order in its
current state with 200. Reusing the key with a different payload returns 409.
Persist the key before sending and reuse it with the same payload after a timeout
or process restart. A new key on an items-only request identifies a new purchase.
Quote identity additionally prevents duplicate consumption, but the header is still required. See the [idempotency design](architecture.md#decision-durable-idempotency-records)
for the table choice, key format, transaction handling, timeouts and retention.

This tightens the previous optional-header contract: existing clients must now
supply a key. Transactional recovery and endpoint-specific replay limits are
described in [crash recovery](architecture.md#crash-recovery-by-workflow-step).

## Run with Docker

Requirements: Docker Compose and Go 1.26 for dependency preparation. Run `go mod tidy` once with network access before the first build; builds then require readonly manifests. Local development also requires Go 1.26. Python 3
is used only by the smoke test.

```sh
cp .env.example .env
# Set JWT_SECRET in .env to a random secret (at least 32 bytes).
# Generate one with: openssl rand -hex 32
docker compose up --build -d --wait

# Explicitly provision an admin; registration always creates a customer.
ADMIN_PASSWORD='choose-a-password' docker compose run --rm -e ADMIN_PASSWORD api \
  admin --email admin@example.com --name Admin

# Optional sample products (safe to repeat).
docker compose run --rm api seed
```

API: `http://localhost:8080`. Swagger UI:
`http://localhost:8080/api/swagger/index.html`. The UI loads Swagger assets from
a CDN; the specification itself is served locally at
`/api/swagger/openapi.yaml` and can also be imported into an API client.

Compose runs SQL migrations after PostgreSQL becomes healthy and starts the API
only after migration succeeds. Data persists in a named volume. Use
`docker compose down` to stop the stack while retaining data.

## API example

Register a customer and log in as the provisioned admin. Copy the returned tokens
into `CUSTOMER_TOKEN` and `ADMIN_TOKEN` in your shell.

```sh
curl -s http://localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"name":"Customer","email":"customer@example.com","password":"customer-password"}'

curl -s http://localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"choose-a-password"}'

# Admin adds a catalog product. 1299 means USD 12.99 with the default currency.
curl -s http://localhost:8080/api/v1/products \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"sku":"BOOK-002","name":"Notebook","price_minor":1299}'

# Customers browse the catalog to obtain product IDs.
curl -s http://localhost:8080/api/v1/products \
  -H "Authorization: Bearer $CUSTOMER_TOKEN"

# Replace the product IDs with catalog IDs. Prices are always read from the DB.
curl -s http://localhost:8080/api/v1/orders \
  -H 'Idempotency-Key: checkout-example-1' \
  -H "Authorization: Bearer $CUSTOMER_TOKEN" -H 'Content-Type: application/json' \
  -d '{"items":[{"product_id":"PRODUCT_UUID_1","quantity":2},{"product_id":"PRODUCT_UUID_2","quantity":1}]}'

curl -s "http://localhost:8080/api/v1/orders/$ORDER_ID" \
  -H "Authorization: Bearer $CUSTOMER_TOKEN"
curl -s 'http://localhost:8080/api/v1/orders?status=PENDING&limit=20' \
  -H "Authorization: Bearer $CUSTOMER_TOKEN"

# Cancel a PENDING order; retrying an already CANCELLED order also returns 200.
curl -s -X POST "http://localhost:8080/api/v1/orders/$ORDER_ID/cancel" \
  -H "Authorization: Bearer $CUSTOMER_TOKEN"

# For a different order already in PROCESSING, an admin can advance to SHIPPED.
curl -s -X PATCH "http://localhost:8080/api/v1/orders/$ORDER_ID/status" \
  -H "Authorization: Bearer $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"status":"SHIPPED"}'

curl -s -X POST http://localhost:8080/api/v1/auth/logout \
  -H "Authorization: Bearer $CUSTOMER_TOKEN"
```

| Operation | Customer | Admin |
| --- | --- | --- |
| Create order | Own identity, derived from token | Forbidden |
| Retrieve/list orders | Own orders | All orders |
| Cancel order | Own PENDING orders; already CANCELLED retries return 200 | Forbidden |
| Advance status | Forbidden | Any order, one stage at a time |
| Browse products | Allowed | Allowed |
| Add product | Forbidden | Allowed |

Statuses advance `PENDING → PROCESSING → SHIPPED → DELIVERED`.
Cancellation moves `PENDING → CANCELLED`; DELIVERED and CANCELLED are terminal.
The admin status endpoint can also advance PENDING to PROCESSING manually.

Order creation accepts 1–100 distinct product IDs with positive quantities.
It calculates totals using checked `int64` minor-unit arithmetic and saves the
order and all item snapshots in a transaction. Unknown/duplicate products and
overflow are rejected. Changing a catalog price later does not change a purchase.
No client-supplied customer IDs, roles, or prices are accepted.

Lists return `{ "items": [...], "next_cursor": "UUID or null" }`, ordered by
UUIDv7 descending. Pass `next_cursor` as `cursor`, retaining the same filters.
The default page size is 20; the maximum is 100. Order filtering includes
CANCELLED. Order lists fetch all page items in one additional query, avoiding
one database query per order.

Errors use `{ "error": "message", "details": { "field": "reason" } }`.
Malformed JSON returns 400, an unsupported content type 415, validation 422,
authentication 401, forbidden actions 403, inaccessible/missing orders 404,
and duplicate resources or invalid current states 409. Server errors expose a
generic message. Passwords must be 8–72 bytes (registration also requires at
least eight characters); emails are trimmed and lowercased.

## Background processing and scale

The embedded worker first runs five minutes after startup, then every five
minutes. It processes PENDING orders created by the run's start time, regardless
of their age. Orders created during the drain wait for the next run.
With a healthy idle worker, an order normally waits between approximately zero
and five minutes for the next tick. This is a periodic schedule, not a five-minute
delay measured from each order's creation; there is no immediate startup run.
Backlog, locks, failures or restarts can make the wait longer.

The queue is a PostgreSQL partial index on `(created_at, id)` with predicate
`status = 'PENDING'`. The worker uses that literal predicate, oldest-first
ordering, a configurable batch limit (500 by default), and
`FOR UPDATE SKIP LOCKED`. Each batch selects IDs and updates them to PROCESSING
inside one SQL statement/transaction. It reads neither historical order data
nor item data, and successful updates remove rows from the partial index.

Separate processes can share the queue safely. Cancellation and admin updates
use conditional updates; row locking ensures exactly one competing transition
succeeds. A short batch does not stop the drain; an empty batch does. Locked
rows skipped by a run are eligible again next tick. An index does not eliminate
the work of updating each pending row: processing cost grows with the pending
backlog, while transactions and application memory remain bounded.

Runs do not overlap within a process. A failed batch rolls back; committed prior
batches remain processed and outstanding rows are retried at the next tick.
Shutdown cancels active worker database operations. The worker also purges
expired logout tokens using an expiry index. JSON logs include processed count,
duration, and failures. `/health` and `/api/v1/health` are liveness probes;
`/api/v1/ready` checks schema version 5, initialized catalog settings and local worker health. It exposes safe last-attempt/success/failure timestamps. Startup grace is two intervals; failed runs are unhealthy immediately, stale success after two intervals is unhealthy, and a successful run (including an empty queue) restores readiness. Each drain has a one-interval deadline. Quote cleanup is bounded to 500 rows per tick, eligible 24 hours after expiry; cleanup failures are logged separately.

Business requests have a configurable 10-second context budget starting before
authentication and a 16-request admission limit per process. Excess work receives
503 with a `Retry-After` delay of 1–3 seconds; health/readiness probes bypass admission.
API, worker/maintenance and readiness use separate pools (16/3/1 connections by
default). All application connections have 5-second statement, 2-second lock and
10-second idle-transaction timeouts. Order creation also retains its transaction-local
limits. These controls bound work; production capacity remains unmeasured.

On SIGTERM, readiness reports draining and new business requests receive 503.
Accepted requests keep their contexts and may finish within the 30-second shutdown
budget. The worker stops independently. An optional deregistration delay precedes
HTTP shutdown; configure the deployment termination grace period to exceed that
delay plus the shutdown budget, with a margin. Compose defaults to 65 seconds.
A worker panic logs sanitized stack frames and initiates controlled shutdown.

## Local development and tests

Follow [TESTING.md](TESTING.md) for step-by-step local setup **with or without
Docker**, including migrations, integration tests, API smoke, expected results
and cleanup.

For requirement-by-requirement coverage, see the
[test plan](docs/plans/order-processing-hardening/04-test-plan.md). The
[live-demo runbook](docs/plans/order-processing-hardening/05-live-demo.md)
includes executable requests, expected results, a real five-minute scheduler
check, and a results checklist.

Export the `.env` settings before running local commands; the Go binary reads
environment variables directly:

```sh
set -a
. ./.env
set +a
docker compose up -d postgres
go mod tidy
make migrate-up
make seed
make run
```

```sh
make fmt-check
make vet
# Unit/HTTP tests only: no PostgreSQL required; pinned Go dependencies are required.
make test

# The integration suite creates/drops only unique test schemas in this DB.
# Choose a dedicated local database whose role can create schemas.
TEST_DATABASE_URL='postgres://orders:orders_local@127.0.0.1:5432/orders?sslmode=disable' \
  make integration

# Complete Docker/API flow, including automatic processing on a 5-second tick.
# Uses an isolated Compose project and removes only that project's test volume.
make smoke-docker
```

PostgreSQL tests are guarded by `//go:build integration`; plain `go test ./...`
and `make test` exclude them. `make integration` enables the tag and requires
`TEST_DATABASE_URL`. The worker suite includes a real ticker test with a short
interval, while the Docker smoke exercises that scheduler against PostgreSQL.
Neither substitutes for the two real five-minute ticks in the live demo.
A completed local five-minute scheduler run is recorded in the
[scheduler demo note](docs/reviews/order-processing-hardening/verification/native-five-minute-demo/README.md).

Unit tests cover snapshot totals, invalid orders, arithmetic overflow, password
limits, auth/token validation and revocation, JSON validation/error envelopes,
worker batching/cancellation, and configuration. PostgreSQL integration tests
cover API role/ownership rules, filtering/cursors, transaction rollback,
concurrent processing, cancellation races, batch limits/cutoffs, and
`EXPLAIN ANALYZE` index use against a large historical dataset.

To smoke-test an already running API instead, provision an admin and run:

```sh
ADMIN_EMAIL=admin@example.com ADMIN_PASSWORD='choose-a-password' make smoke
```

The smoke test adds sample users/products/orders. The standalone smoke test uses
the admin status API; the Docker smoke test verifies the scheduled worker too.
Its default ports are 18080/15432, overridable with
`SMOKE_HTTP_PORT`/`SMOKE_POSTGRES_PORT`.

## Structure and configuration

```text
api/rest/              Echo server, auth/role middleware, v1 handlers, routes
api/validator/         JSON-field validation errors
domain/                Pure user, product, order types and repository interfaces
service/               Auth adapter, commands/queries, unit of work, worker
db/gorm/               PostgreSQL adapters and bounded worker SQL
db/migrations/         Embedded golang-migrate up/down SQL pairs
ports/                 Authentication and pending-processing interfaces
internal/bootstrap/    API/worker composition and shutdown
cmd/orders/            server, migrate, admin, seed commands
configs/               Environment configuration and defaults
docs/                  Embedded OpenAPI specification and Swagger UI
scripts/               API and Docker smoke tests
```

| Variable | Default/requirement |
| --- | --- |
| `DATABASE_URL` | Required PostgreSQL URL |
| `JWT_SECRET` | Required for server; at least 32 bytes |
| `JWT_ISSUER` | `order-management` |
| `JWT_TOKEN_TTL` | `24h` |
| `HTTP_ADDRESS` | `:8080` |
| `HTTP_MAX_IN_FLIGHT` / `HTTP_REQUEST_TIMEOUT` | 16 / `10s`; admission before authentication, probes excluded |
| `HTTP_SHUTDOWN_TIMEOUT` / `HTTP_DRAIN_DELAY` | `30s` / `0s`; shutdown timeout must cover request timeout |
| `DB_API_MAX_OPEN` / `DB_WORKER_MAX_OPEN` / `DB_READINESS_MAX_OPEN` | 16 / 3 / 1; budget their sum across replicas |
| `DB_CONNECT_TIMEOUT` / `DB_STATEMENT_TIMEOUT` / `DB_LOCK_TIMEOUT` | `5s` / `5s` / `2s`; lock timeout must not exceed statement timeout |
| `DB_IDLE_TRANSACTION_TIMEOUT` / `DB_CONN_MAX_IDLE_TIME` | `10s` / `5m` |
| `COMPOSE_STOP_GRACE_PERIOD` | `65s`; increase when configured drain/shutdown budgets require it |
| `CURRENCY` | Optional supported base-currency initialization/assertion; must match persisted settings thereafter |
| `STORE_REGION` | `US`; supported US, IN, GB, JP, KW, DE, FR |
| `QUOTE_TTL` | `5m`, positive and at most 1h |
| `AUTH_LOGIN_LIMIT` / `AUTH_REGISTER_LIMIT` | 10 / 5 attempts per window, each 1–10000 |
| `AUTH_RATE_WINDOW` | `1m`, allowed 1s–1h |
| `AUTH_RATE_MAX_KEYS` / `AUTH_MAX_IN_FLIGHT` | 10000 per route / 4 simultaneous auth requests; bounds 1–100000 / 1–64 |
| `PROCESSING_INTERVAL` | `5m`, minimum 1s; shorten only for development/tests |
| `PROCESSING_BATCH_SIZE` | `500`, allowed 1–10000 |
| `ADMIN_PASSWORD` | Required only for explicit admin creation |
| `HTTP_PORT` / `POSTGRES_PORT` | Compose host ports 8080 / 5432 |

Catalog currency is persisted and cannot be changed by restarting with another region. An explicit mismatched `CURRENCY` fails startup. Orders preserve their accepted monetary snapshots. JWT validation
requires HS256, the configured issuer, an expiry and matching subject/user ID.
Each token has a unique ID, so logout revokes that token only. The database's
current role and active status gate every authenticated request.

`migrate up` applies pending migrations; `migrate down` reverts one version. New down migrations refuse to discard quote/pricing provenance; the initial migration removes application tables. Migrations are
explicit, rather than GORM auto-migration. Admin creation rejects existing
emails instead of silently promoting or replacing a customer.

This implementation intentionally omits inventory, payments, product editing/deletion,
a frontend, refresh tokens, and password recovery. The application runs as a
standalone service with PostgreSQL.


## Regional pricing

Items-only orders use the catalog's persisted base currency. To purchase in a
supported regional currency, obtain a quote and submit its `quote_id`. Conversion
uses manually imported rates; changing region never changes existing prices or
orders. See [regional pricing and quote handling](architecture.md#regional-pricing-exact-money-and-quote-consumption)
and the [operator commands](architecture.md#regional-pricing-setup-and-api-examples).

## Authentication limits and operational logs

Login defaults to 10 attempts/minute/IP; registration to 5, with at most four auth
requests in flight. Limits run before password work and count failed attempts.
A 429 response includes Retry-After seconds. Maps are bounded; a full map rejects
new identities until entries expire. Fixed windows can burst at a boundary.
Limits are process-local, so replicas multiply the allowance. Socket peer IP is
used; forwarded headers are ignored. Requests behind a proxy share its peer quota;
configure ingress limits for that deployment rather than trusting arbitrary headers.
Missing-user login performs dummy bcrypt verification; inactive users also undergo
verification. This reduces the obvious shortcut but does not guarantee equal latency.

Responses include a generated X-Request-ID. Structured logs correlate actor,
resource and committed/replayed action outcomes without passwords, tokens, bodies,
query strings or raw infrastructure error messages. Worker logs include run ID,
committed count, duration and success/failure, including partial progress.

## Upgrade, rollback and local testing

On a host with Go 1.26, Python 3, Make, network access and Docker Compose, run
`make acceptance`. It resolves dependencies, checks the build/vet/race suite,
provisions an isolated PostgreSQL 18 container on a dynamic loopback port, checks
migration up/down/up, runs verbose integration tests (including EXPLAIN), then
runs Docker/API smoke. It removes its own test containers/volumes and stores
redacted logs, manifest snapshots and a JSON result under
`.cache/acceptance/acceptance-<run-id>/`.
It does not use an existing application database. A failed prerequisite returns
nonzero and records a blocked run, never a successful acceptance result.

Apply all migrations before starting the API. Existing catalogs require explicit
currency adoption; rollback guards protect order and retry data. See
[migration and upgrade procedures](architecture.md#migration-and-upgrade-procedures)
before upgrading an existing database.

Run the local test commands in [TESTING.md](TESTING.md). Generated results stay
in ignored `.cache/` storage and are not part of the submission.
