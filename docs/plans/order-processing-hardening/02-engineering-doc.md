# Engineering Design: Order Processing Hardening

- Status: Approved
- Approved by user: 2026-10-03
- Slug: `order-processing-hardening`
- Author/date: Codex / 2026-10-03
- Basis: [approved overview](01-overview.md), [review](00-review-20261003.md), [project guidance](../../../AGENTS.md).
- Skills consulted: `plan-workflow` (phase gates/serial ownership), `plan-review` (nine lenses), `backend-go` (inward dependencies, transaction repositories, Echo v5, context/errors/assertions/testing).
- Adaptations retained: top-level packages, environment config, manual OpenAPI, standard-library fakes; no event outbox because no events are published.

## 1. Outcome and boundaries

Close B1/B2 and R0–R6 while preserving order ownership, state transitions, indexed bounded processing and the five-minute default. Add approved region-selected quotes using managed rates, keeping one immutable base catalog currency. Existing orders retain their recorded money. Payments, taxes, shipping charges, product editing, live FX feeds and base-currency migration remain excluded.

One application process owns HTTP, worker observations and local auth limits; PostgreSQL owns catalog settings, rates, quotes and orders. No additional runtime service or Go decimal dependency is required: use exact scaled-decimal/rational arithmetic via `math/big`, with checked `int64` results.

```mermaid
sequenceDiagram
    participant C as Customer
    participant A as API / services
    participant D as PostgreSQL
    C->>A: POST /order-quotes (region, items)
    A->>D: Read base prices + applicable rate; save snapshots
    A-->>C: Quote ID, target prices, rate, expiry
    C->>A: POST /orders (quote_id)
    A->>D: Lock owned quote; validate expiry; insert order + mark consumed
    A-->>C: Order with immutable monetary snapshots
```

## 2. Domain, ports and application contracts

| Area | Files and contract |
| --- | --- |
| Exact money | New `domain/money/money.go`, `markets.go`: currency metadata, decimal rate parser, conversion/rounding; no HTTP/ORM types. |
| Catalog settings/rates | New `domain/pricing/pricing.go`, `repository.go`: settings, immutable rate versions; repository `Settings(ctx)`, `ActiveRate(ctx, base, target, now)`, `Initialize(ctx, input)`, `ImportRate(ctx, input)`. |
| Quotes | New `domain/quote/quote.go`, `repository.go`: customer, region/mapping version, expiry, pricing snapshot and item snapshots; `Insert`, `GetForUpdate(ctx,id,customer)`, `MarkConsumed`, `DeleteExpiredBatch`. |
| Quote use case | New `service/quote/{service,commands,command_handler}.go`: `HandleCreate(ctx, CreateCommand{CustomerID,Region,Items})`; all monetary calculations are server-side. |
| Order use case | Extend `service/order` with `CreateFromQuoteCommand{CustomerID,QuoteID}` and `HandleCreateFromQuote`; add `FindByQuote(ctx,id,customer)` to `domain/order.Repository`. Retain existing direct-create command. |
| Transaction repositories | Extend `service/uow.Repositories` with `Pricing()`, `Quotes()` and `Now(ctx) (time.Time,error)`; implement with the same GORM transaction handle as `Orders()` and `Products()`. `Now` selects `clock_timestamp()`. |
| Time | New small `ports.Clock` with `Now() time.Time` for in-process timing/tests; adapter in `internal/clock`. Expiry enforcement uses database time as specified below. |
| Worker observations | New `service/processing/status.go`: mutex-protected `Snapshot(now)` with lifecycle, last attempt/success/failure and running status; bootstrap passes read-only access to readiness. |

No service imports GORM/Echo. Repositories receive context first. Add compile assertions to all existing/new repository, JWT, denylist, UoW, clock and pending-processor adapters. Preserve sentinels through operation-boundary `%w` wrapping; HTTP maps sentinels without exposing infrastructure error strings.

## 3. Currency, region and exact pricing policy — Q1

- Maintain a deliberately bounded `markets-v1` mapping: US→USD, IN→INR, GB→GBP, JP→JPY, KW→KWD, DE/FR→EUR. Metadata uses 2 minor digits except JPY=0 and KWD=3. This is the supported application catalog, not all global territories. Updating it requires a reviewed code release and mapping-version change.
- `STORE_REGION` defaults to US and selects the target currency when quote input omits region. Explicit region is an uppercase supported country/market code; unsupported values return 422. Currency cannot be supplied independently or inferred from IP/language.
- Fresh catalog: base defaults to the configured region's currency; an explicitly supplied supported `CURRENCY` overrides that initialization default. Persist the winner exactly once. On subsequent starts, explicit `CURRENCY` must match stored base; changing `STORE_REGION` never changes base prices.
- Fresh initialization must lock `products`/`orders` against inserts while checking emptiness and inserting singleton settings; handle concurrent starters atomically. Seed/product writes require initialized settings. The first initialization wins; conflicting initializers fail clearly.
- Existing products or orders with no settings: startup fails with an actionable initialization message. Use `orders catalog init --currency CODE --confirm-existing` during maintenance. Require existing order currencies, if any, to agree with the supplied base; contradictions stop adoption for explicit data investigation. Never rewrite historical money or guess from amounts. No automatic repair of already mislabelled data is promised.
- `orders rates add --target CODE --rate DECIMAL --valid-from RFC3339 --valid-until RFC3339 --source LABEL` appends a rate under the stored base currency. CLI/operator access only; no new admin HTTP endpoint. Accept positive plain decimals with at most 12 fractional digits and 12 integral digits; reject exponents, NaN, zero, negative, equal base/target, unsupported currency or invalid interval. Source label is bounded and non-secret.
- Rates are immutable UUID versions. Serialize import on the settings row; reject overlapping intervals for the same pair. Validity is `[valid_from, valid_until)` in UTC. Future consecutive intervals are supported; no inversion/cross-rate calculation. Rate means target major units per one base major unit. Base-to-base uses exact 1 with no stored FX row.
- Parse rate as integer `R / 10^s`. Compute target minor units from base minor units `b` as `b × R × 10^targetDigits / (10^s × 10^baseDigits)` with arbitrary-precision intermediates, round half-even once per unit, then check positive `int64` fit. Reject a positive source rounding to zero, multiplication overflow or sum overflow; never clamp.
- Multiply the rounded target unit amount by quantity and sum lines. Displayed unit × quantity equals displayed line, and lines equal total. Compute source totals with the existing checked arithmetic too. All order items share one target currency/rate. Re-selecting a region always starts from base prices.
- Do not install pretend live rates on startup. Tests/smoke explicitly seed rates tagged `test-fixture`; operator-managed rates are business configuration with stated validity, not market accuracy guarantees.

## 4. Persistence and transaction behavior

Reserve migrations **000002_pricing** and **000003_order_quotes**, each with `.up.sql`/`.down.sql`; retain 000001 unchanged. Extend `internal/testutil/postgres.go` to apply all versions and initialize fresh test settings explicitly.

| Migration | Schema, constraints and indexes |
| --- | --- |
| 000002 | `catalog_settings(singleton_id SMALLINT PRIMARY KEY CHECK=1, base_currency CHAR(3), initialized_at TIMESTAMPTZ)`; only singleton initialization is exposed, no base update operation. `fx_rates(id UUID PK, base_currency, target_currency, rate NUMERIC(24,12) CHECK>0, valid_from, valid_until CHECK>valid_from, source VARCHAR(128), created_at)`; check currency codes against supported metadata at the service boundary; index `(base_currency,target_currency,valid_from,valid_until)`. |
| 000003 | `order_quotes(id UUID PK, customer_id FK users, region CHAR(2), mapping_version VARCHAR(32), base_currency CHAR(3), currency CHAR(3), base_digits SMALLINT, target_digits SMALLINT, rate NUMERIC(24,12), rate_id nullable FK fx_rates, rate_source VARCHAR(128), rate_valid_from/ rate_valid_until nullable TIMESTAMPTZ, source_total_minor BIGINT CHECK>0, total_minor BIGINT CHECK>0, created_at, expires_at CHECK>created_at, consumed_order_id nullable UNIQUE FK orders)`; index `expires_at`. |
| 000003 | `quote_items(quote_id FK order_quotes ON DELETE CASCADE, position, product_id FK products, sku, name, quantity CHECK>0, source_unit_price_minor CHECK>0, source_line_total_minor CHECK>0, unit_price_minor CHECK>0, line_total_minor CHECK>0)`; PK `(quote_id,position)`, unique `(quote_id,product_id)`, checked line=unit×quantity for both currencies. |
| 000003 | Extend `orders` with nullable unique `quote_id` (intentionally no FK so quote cleanup cannot erase replay lookup), `pricing_mode` (`legacy`,`base`,`quote`), region, mapping version, source currency/digits, target digits, rate/version/source/validity and source total snapshots. Extend `order_items` with source unit/line amounts. |

Backfill existing orders as `pricing_mode=legacy`; leave unknown source/rate metadata NULL rather than inventing an exchange rate. Existing currency, unit amounts and totals are untouched. Add conditional SQL checks requiring complete positive pricing metadata for new `base`/`quote` rows, base-mode rate=1 and matching currencies, quote-mode non-NULL quote ID, and valid digit ranges. Migration sets the legacy value only for existing rows; do not leave a default that permits old binaries to write after upgrade. New item inserts always include source values; legacy item rows may remain NULL.

Quote creation reads products/settings/rate through transaction-scoped repositories and inserts quote/items atomically. Use DB `clock_timestamp()` as the pricing instant and require `valid_from <= now < valid_until`. Quotes expire at `min(now + QUOTE_TTL, rate.valid_until)`; base quotes use `now + QUOTE_TTL`. Stored snapshots include rate metadata and precision, so later mapping changes cannot alter them. Already-issued valid quotes remain usable after a mapping update; selecting another region/items means requesting a new quote.

Create-from-quote uses READ COMMITTED and first searches an existing order by `(quote_id, customer)` for a safe retry. Otherwise lock the owned quote with `FOR UPDATE`, then recheck consumption and current DB time after lock acquisition. Expiry is checked at acceptance under the lock, not against a client clock. Insert order/items from snapshots and mark quote consumed in one UoW transaction. Concurrent submissions create exactly one order. Retry returns that order even after quote expiry/cleanup. Ownership failures return 404. Cancellation never makes a quote reusable. This provides idempotency only for quoted orders, not a new global idempotency API.

Clean quotes at least 24h past expiry in batches of 500, using the expiry index and `SKIP LOCKED`; one cleanup batch per processing tick, without historical-order scans. Order snapshots and unique quote IDs preserve provenance/retry behavior after quote deletion. Cleanup locks coordinate with consumption; consumed references point from quotes to orders, so deleting a quote does not delete an order. Retention is a cleanup eligibility threshold, not a deletion-time guarantee under backlog.

## 5. HTTP contracts and compatibility

Wire new `api/rest/v1/quote_handler.go` through `api/rest/routes/routes.go` and bootstrap; reuse the existing customer-only role middleware and strict JSON binder.

| Endpoint | Request/response and status |
| --- | --- |
| `POST /api/v1/order-quotes` | Customer request `{ "region":"IN", "items":[{"product_id":"UUID","quantity":2}] }`; region optional. 201 with `id`, `region`, `mapping_version`, `base_currency`, `currency`, decimal-string `rate`, `rate_id` (nullable), rate validity/source, `expires_at`, source/target totals and snapshotted items. 1–100 distinct items as for orders. |
| `POST /api/v1/orders` quoted form | `{ "quote_id":"UUID" }` exclusively. 201 new order and Location; 200 same existing order on retry. Reject mixing quote ID with items/region/currency. |
| `POST /api/v1/orders` existing form | `{ "items":[...] }` remains a direct base-currency purchase, calculated from DB prices in the existing transaction. It never implicitly converts or follows the target region. Clients wanting regional pricing must use quotes; document this compatibility distinction explicitly. |
| Product get/list/create | Existing `currency` comes from persisted base settings. `price_minor` is always base currency; product endpoints do not present unaccepted converted prices. |
| Order get/list/create | Existing `currency`, amounts, items and statuses remain. Add nullable `pricing` object for source/rate/region/quote provenance; `pricing.mode=legacy` has unknown metadata NULL. No recalculation on reads. |
| `GET /api/v1/ready` | 200/503 with `status` plus `checks.database`, `checks.worker`, `worker.last_attempt_at`, `last_success_at`, `last_failure_at`, `running`. Safe states/timestamps only, no internal error messages. Liveness endpoints remain separate. |

Preserve `{error,details}`. Use 400 malformed JSON, 422 invalid shape/region/products/arithmetic, 401 authentication, 403 role, 404 inaccessible quote/order, 409 quote expiry or unavailable/expired conversion rate, 429 throttle, 503 readiness. Add stable `details.reason` for pricing errors (`quote_expired`, `rate_unavailable`, `quote_required_fields`, `amount_out_of_range`) and tests; database outages remain generic 500. Rate and quote times are UTC RFC3339; rates are decimal strings, not JSON floating-point numbers. Update `docs/openapi.yaml` and README examples together.

## 6. Worker observations and readiness — Q2

Let `I=PROCESSING_INTERVAL` (default 5m). First processing attempt remains at I. Each drain has a child context deadline of I; preserve cutoff, 500-row batches, partial index and conditional transitions. Runs remain serial. A failed/timed-out batch leaves prior committed batches intact for next-tick recovery.

Record process start, last attempt start, last success, last failure, running and a safe failure classification under a mutex. A successful empty run counts as success. Readiness checks DB/schema within 2s (migration version 3, not dirty, plus the initialized catalog singleton), then snapshot: worker is healthy during initial grace `now-start < 2I` if no failure; afterward require a success newer than 2I, no failure newer than success, and no running attempt lasting I or longer. At threshold equality mark unhealthy. Failed attempt marks unhealthy immediately; the next successful attempt clears failure condition. A running attempt does not clear a previous failure. Exit before shutdown is unhealthy; shutdown cancels in-flight DB work.

Readiness describes this process's worker, not a global queue-latency guarantee. Token/quote cleanup failures log separately and retry next tick; they do not pretend order draining failed. Bound cleanup by a separate `min(I,30s)` child timeout and one quote batch, so maintenance cannot indefinitely delay the next order attempt. Use a fake clock/processor to verify all threshold boundaries, no-overlap behavior, empty success and recovery under the race detector.

## 7. Auth safeguards, logging and errors — Q3

- Login performs one bcrypt comparison for every syntactically valid credential attempt: real stored hash for a known account, precomputed valid dummy hash at the same configured/default cost for a missing account; evaluate active status after comparison. Generate the dummy once during client construction, never per missing-user request. Inject a private verifier seam for deterministic tests; keep JWT/DB role/denylist behavior. DB errors remain errors, not fake bad-password responses. No claim of identical end-to-end timing or registration-enumeration protection.
- Fixed-window per-route/IP limiter runs before binding/password work: login 10 requests/minute, register 5/minute, each window starting on first admitted request. Count successes and failures. Key normalized socket peer IP from `RemoteAddr` (IPv4-mapped addresses unmap); ignore Forwarded/X-Forwarded-For. Invalid peer identity uses one shared fallback bucket. Reverse proxies share their peer quota; trusted-proxy support is deferred and documented.
- Bound each route's map to 10,000 entries. Periodically sweep expired entries under a lock using the injected clock, scanning at most the configured cap; no goroutine per key. At capacity, reject unseen keys until entries expire rather than evicting active entries to bypass limits. Synchronize concurrent admissions. Return 429 `{error:"too many requests"}` and integer-second `Retry-After`; no credentials or IPs in response/logs.
- Shared nonblocking auth in-flight semaphore defaults to 4 to bound simultaneous expensive work across IPs; excess returns 429/Retry-After=1, always releases with defer. No Redis; replicas have independent limits. Fixed windows permit boundary bursts; deployment-wide quotas are explicitly not promised.
- Generate server-side UUID request IDs, echo `X-Request-ID`, propagate request/actor IDs in context through a small `internal/logging` slog handler; ignore client request IDs. Log route template, method, status and duration, never raw URL/query/body/headers. Log successful product/order creation, transitions/cancellation, quote creation and CLI pricing changes after commit; quote retry is `outcome=replayed`, not another creation event.
- Mutation fields: request ID, actor ID or `actor_type=operator/system`, resource ID, action, committed status/outcome. Worker logs have run ID, committed count, duration and outcome, including partial completion on error; no per-order batch logs required. Use `InfoContext`/`ErrorContext`. Error logs use safe operation/classification fields; do not serialize raw DB/DSN/credential-bearing errors. Preserve wrapped causes for programmatic inspection.

## 8. Configuration and rollout — Q4

| Key | Default / validation |
| --- | --- |
| `STORE_REGION` | US; supported mapping only. |
| `CURRENCY` | Optional explicit base initialization/assertion; unset means derive only on fresh initialization, otherwise use persisted base. Preserve explicit legacy deployments. |
| `QUOTE_TTL` | 5m; greater than zero and no more than 1h. |
| `AUTH_LOGIN_LIMIT` / `AUTH_REGISTER_LIMIT` | 10 / 5; integers 1–10000. |
| `AUTH_RATE_WINDOW` | 1m; 1s–1h. |
| `AUTH_RATE_MAX_KEYS` / `AUTH_MAX_IN_FLIGHT` | 10000 / 4; 1–100000 per route / 1–64. |

Validate before startup. Update `.env.example`, Compose, README and config tests. Compose must stop forcing `CURRENCY=USD` when unset, allowing STORE_REGION initialization. Config parsing must distinguish absent/empty optional currency from a configured assertion. Existing interval/batch/JWT settings remain. No feature flag or live external dependency is added.

Deploy in maintenance: stop old writers/workers, back up data, apply 000002 then 000003, explicitly initialize legacy catalog if needed, add valid rates for desired target markets, deploy new binary, run readiness and smoke. Fresh server/seed can initialize automatically. Existing-catalog CLI initialization must be repeatable for the same value and fail on conflict. Mixed-history adoption requires investigation before deployment. Future rate imports are operator commands and do not require an API restart.

Rollback: before new quote/multi-currency writes, restore the previous binary only with its matching schema/config after backing up. The 000003 down migration refuses while quotes or non-legacy orders exist; 000002 down refuses while rate rows or a differing order currency remain. Test reversibility on an empty disposable database. After new monetary writes, prefer forward repair; restoring a pre-upgrade backup requires explicit acceptance of losing later writes. Never claim an old binary safely preserves new pricing provenance. No automated destructive data cleanup is part of rollback.

## 9. Verification and evidence

| Layer | Required cases / evidence |
| --- | --- |
| Pure unit | Decimal parsing; 0/2/3-digit currencies; half-even ties; base identity; repeated region changes; zero-rounded/overflow rejection; totals. Quote expiry capped by rate validity; immutable source/target snapshots. |
| Auth/HTTP unit | Known/missing/inactive bcrypt path, generic errors, limiter boundaries/reset/concurrent admission/map exhaustion/in-flight release/spoofed forwarded headers; quote DTO exclusive forms and error mapping; safe request/log fields and committed/replayed outcomes. |
| Worker/config unit | Initial grace, first tick, exact stale/timeout boundary, failed/empty/recovered attempts, cleanup independence, concurrent snapshot access; all config bounds and absent-vs-explicit currency. |
| Live PostgreSQL | Fresh/concurrent/legacy/conflicting initialization; rates overlap rejection under concurrent imports; quote ownership/expiry-after-lock, transactional rollback, same-quote concurrent orders and retry after cleanup; legacy snapshots unchanged; restart/region/rate changes; all existing cancellation/worker races and pagination tests. |
| Migrations/performance | Disposable 000001→000003, down→up without data, legacy fixture upgrade, destructive-down guards; retain `EXPLAIN (ANALYZE, BUFFERS)` for bounded pending SQL with substantial historical data and the quote-expiry cleanup index. No invented latency target. |
| End-to-end | Docker smoke with explicit test rate and quoted-order flow; automatic processing observed before shipping/delivery. Keep PENDING-only cancellation/skipped-stage assertions in controlled integration tests without a live ticker; do not merely lengthen sleeps. |

At implementation start, recheck Go/dependency download, Docker and test DB capabilities. Run `go mod tidy` once to resolve manifests, then `go build -mod=readonly ./cmd/orders`, `go vet -mod=readonly ./...`, `go test -mod=readonly -race ./...`, and `go test -mod=readonly -race -tags=integration ./...` with a dedicated TEST_DATABASE_URL. Run `make fmt-check`, disposable migration checks and `make smoke-docker`. Tighten Make/Docker commands to readonly resolution after tidy; remove image-build `go mod tidy` so missing manifests fail reproducibly.

Write commands, tool versions, exit statuses and sanitized output/EXPLAIN paths under `docs/reviews/order-processing-hardening/verification/` during execution; never include credentials. No Git metadata currently exists, so use evidence/file links and mark commit links unavailable. External access failures leave B1 blocked; do useful unit/code work where feasible but never declare full acceptance. No live FX/payment tests apply because these integrations are excluded.

## 10. Serial execution and task list

One owner, **Codex**, performs every task serially. No delegated agents. Expected effort after access is available: roughly 6–9 engineering days (pricing/quotes 3–4, hardening 1–2, integrated validation/docs 2–3); planning estimate only, revised upward from Phase 1 for approved conversion scope.

| Shared files | Strategy |
| --- | --- |
| `domain/*/repository.go`, `service/uow/uow.go`, `ports/clock.go` | Define interfaces in T2 before adapters; adjust fakes serially. |
| `db/migrations/000002_pricing.*.sql`, `000003_order_quotes.*.sql`, `internal/testutil/postgres.go` | Reserved numbers; one writer, no edits to 000001. |
| `api/rest/routes/routes.go`, `internal/bootstrap/server.go`, `cmd/orders/main.go` | Serial CLI/wiring after service contracts exist. |
| `configs/config.go`, `.env.example`, `docker-compose.yml`, `docs/openapi.yaml` | Single owner; update defaults and public contracts with wiring. |
| `go.mod`, `go.sum`, `Dockerfile`, `Makefile` | T1 resolves manifests once; T8 makes builds readonly; resync only for an actual dependency change. |
| Generated files | None; maintain manual OpenAPI and standard-library fakes. Do not add code generation solely for skill conformity. |

| ID | Task / completion condition | Owner | Write scope | Depends on |
| --- | --- | --- | --- | --- |
| T1 | Capability check and baseline; tidy manifests; record unavailable checks | Codex | `go.mod`, `go.sum`, verification directory | Engineering approval |
| T2 | Exact money/types/interfaces with meaningful arithmetic tests | Codex | `domain/money`, `domain/pricing`, `domain/quote`, `domain/order`, `service/uow`, `ports/clock.go`, `internal/clock` | T1 baseline attempted |
| T3 | Settings/rates/quotes persistence, migrations and operator CLI; live regression cases | Codex | `db/migrations/000002*`, `000003*`, `db/gorm`, `internal/testutil`, `cmd/orders` | T2 |
| T4 | Quote/order use cases, DTOs and pricing contract tests | Codex | `service/quote`, `service/order`, `api/rest/v1`, `api/rest/server.go` | T3 |
| T5 | Worker observation/deadlines/cleanup and readiness tests | Codex | `service/processing`, `ports/processing.go`, `api/rest/routes` | T3, T4 |
| T6 | Dummy verification, bounded limiter and auth regression tests | Codex | `service/auth/jwt`, `domain/user`, `api/rest/middleware` | T5 |
| T7 | Safe contextual logs and adapter/error conformance across changed layers | Codex | `internal/logging`, `api/rest`, `service`, `db/gorm`, `cmd/orders` | T6 |
| T8 | Serial config/CLI/HTTP/bootstrap wiring, docs and reproducible build config | Codex | `configs`, `.env.example`, `docker-compose.yml`, `Dockerfile`, `Makefile`, `cmd/orders`, `internal/bootstrap`, `api/rest/routes`, `docs/openapi.yaml`, `README.md`, `AGENTS.md` | T4–T7 |
| T9 | Deterministic smoke + full acceptance/EXPLAIN evidence; update original finding status | Codex | `scripts`, integration tests, verification directory, plan/review docs | T8 |

T3 live tests may remain blocked while T4–T8 progress; blockers must carry into T9. Each task includes relevant tests, not just implementation. T9 closes B1/B2/R1–R6 only from evidence. After engineering approval, derive `03-todo.md` directly from T1–T9 and maintain actual status/links; this completes R0's process artifacts without pretending implementation is done.

## 11. Review

Q1–Q4 have proposed concrete answers in sections 3–9; no unresolved product question is required for execution beyond this design's approval. [Persona review](00-review-20261003.md) records remaining delivery risks and their mitigations. Engineering and overview approvals are recorded; implementation is authorized. Source implementation is now present; final acceptance is tracked in `03-todo.md` and remains blocked by dependency/runtime availability.

Primary references: [PostgreSQL READ COMMITTED locking](https://www.postgresql.org/docs/18/transaction-iso.html#XACT-READ-COMMITTED) explains visibility after a competing row update; [PostgreSQL current date/time functions](https://www.postgresql.org/docs/18/functions-datetime.html#FUNCTIONS-DATETIME-CURRENT) distinguishes transaction timestamps from actual clock time; [CLDR currency metadata](https://unicode.org/reports/tr35/tr35-numbers.html#Supplemental_Currency_Data) describes territory associations and currency precision. Market support, half-even pricing, rate validity and quote behavior above are application design decisions.
