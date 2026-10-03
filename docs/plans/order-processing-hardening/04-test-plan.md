# Order processing test and acceptance plan

This plan maps the five assignment requirements to existing automated tests and
the [live-demo runbook](05-live-demo.md). Coverage listed here describes test
intent; it does not mean the tests have passed.

**Latest execution status, 2026-10-03:** the user's native PostgreSQL 18.4 run
passed build/vet and the complete unit/HTTP/auth/database integration suite with
`-count=1 -race -tags=integration`. The worker test-fixture UUID scan was corrected;
`TestPendingSkipsLockedRowsAndRollsBackFailedBatch` and every other test in the full
rerun passed. This includes idempotency contention, timeouts, rollback, version-4
upgrades and the nonempty 000004 downgrade guard. The 2026-10-04 native deployed smoke passed with automatic processing
([evidence](../../reviews/order-processing-hardening/verification/native-smoke-20261004/README.md)).
On 2026-10-04 IST, full Docker acceptance passed all 22 stages, including migration
up/down/up, integration, smoke and cleanup ([evidence](../../reviews/order-processing-hardening/verification/acceptance-20261003T183917Z-2cf6c389/summary.json)).
Only the actual five-minute demo remains pending. See the [native evidence](../../reviews/order-processing-hardening/verification/native-postgres-20261003/README.md).

Older execution notes below retain historical blockers; this latest native result
supersedes them for the cases explicitly listed as passed.

## Requirement coverage

| ID / requirement | Acceptance criteria, including rejection cases | Existing automated coverage | Live proof |
| --- | --- | --- | --- |
| R1 — Create a multi-item order | Customer submits two products, quantities 2 and 3; returns 201, PENDING, two snapshots and USD 3495 minor units. Reject empty items, nonpositive quantities, duplicate/unknown products, client prices and overflow. A failed write leaves no partial order. | `TestNewSnapshotsCatalogPricesAndTotals`, `TestNewRejectsInvalidOrders` in [domain tests](../../../domain/order/order_test.go); `TestCustomerAndAdminOrderFlow` in [HTTP/DB tests](../../../api/rest/routes/routes_integration_test.go); `TestOrderTransactionSnapshotsAndStatus` in [repository tests](../../../db/gorm/order_repository_integration_test.go). | D1; invalid payloads in D2. |
| R2 — Retrieve by order ID | GET returns the same ID, items, totals and current status. Unknown ID and another customer's order return 404. | `TestCustomerAndAdminOrderFlow`; `TestOrderTransactionSnapshotsAndStatus`. | D2 and reads after D4/D6. |
| R3a — Update order status | PENDING → PROCESSING → SHIPPED → DELIVERED; only one forward stage at a time. Skip, reverse and repeated transitions return 409; customer status changes return 403. | `TestStatusTransitions` in domain tests; `TestCustomerAndAdminOrderFlow`; `TestOrderTransactionSnapshotsAndStatus`. | D1 rejects a skipped stage; D4 completes lifecycle; D7 exercises manual PENDING → PROCESSING. |
| R3b — Process PENDING every five minutes | First worker run occurs five minutes after process startup, then every five minutes. It advances eligible PENDING orders without a status API call; cancelled/delivered orders remain unchanged. | `TestLoadDefaultsAndValidation` in [configuration tests](../../../configs/config_test.go); `TestRunWaitsForFirstTick`, `TestDrainProcessesFullAndShortBatches`, `TestDrainStopsOnFailureAndCancellation` in [worker tests](../../../service/processing/worker_test.go); `TestPendingBatchesConcurrencyCutoffAndQueryPlan` and `TestPendingSkipsLockedRowsAndRollsBackFailedBatch` in repository tests; [Docker smoke](../../../scripts/smoke-docker.sh) exercises a **5-second** interval. | D0 resets the timer; D4 proves first tick; D6 proves recurrence with two timestamped attempts about 300 seconds apart. Unit tests/short smoke do not measure real five-minute cadence. |
| R4 — List orders, optionally by status | Unfiltered list contains all accessible orders across pages; status filter returns only that status. Customer sees own orders, admin sees all. Invalid filter/page size returns 422. | `TestCustomerAndAdminOrderFlow` covers customer isolation, admin visibility, filtering, cursor pages and invalid filters. [API smoke](../../../scripts/smoke.py) also covers delivered filtering and pagination. | D3 unfiltered/CANCELLED lists and cursor page; D4 DELIVERED list; D5 cross-customer admin visibility. |
| R5 — Cancel only PENDING | PENDING → CANCELLED returns 200 and stays cancelled after worker ticks. Repeated cancellation returns 200 without changing the timestamp. PROCESSING, SHIPPED and DELIVERED return 409. A processing/cancellation race has exactly one winner. | `TestStatusTransitions`; `TestCustomerAndAdminOrderFlow`; `TestCancellationAndProcessingRace` in repository tests. | D1 successful/repeated cancellation; D4 rejection at each later stage; D6 cancelled state persists. |

The HTTP/DB scenario controls processing directly, so its successful cancellation
assertion is deterministic. The accelerated Docker smoke accepts either a
successful cancellation or a 409 when the worker wins. That smoke outcome alone
does not prove successful cancellation; retain the controlled integration and
live-demo evidence too.

Review follow-up adds `TestCreateRejectsInvalidUUIDs` in the
[handler tests](../../../api/rest/v1/order_handler_test.go), including a binder
that skips validation, and `TestOrderTransitionUsesDatabaseTime` in the repository
tests. `TestRunProcessesRecurringTicksAndStops` observes two actual 25ms ticker
events and cancellation using a recording processor. It verifies scheduling
without PostgreSQL and does not replace the five-minute live timing check.

## Approved supporting behavior

| Area | Automated evidence to run | Live coverage |
| --- | --- | --- |
| Authentication, roles and logout | [JWT tests](../../../service/auth/jwt/client_test.go): `TestRegisterLoginSessionLogout`, `TestRejectsInvalidJWTAndChecksCurrentRole`, `TestLoginVerifiesMissingAndInactiveAccounts`; HTTP/DB scenario. | Setup provisions admin and registers two customers; D2 checks 401/403/404; D5 proves customer isolation and admin visibility; D7 logs out. |
| Catalog and immutable prices | Domain snapshot test, repository transaction/snapshot test, HTTP/DB product authorization and duplicate SKU checks. | Setup creates USD 1299/299 products; D1 verifies server totals; D2 rejects a supplied price. Product editing is outside scope; price-change snapshot regression is a DB test. |
| Regional currency and managed rates | [Money tests](../../../domain/money/money_test.go) cover minor-unit precision, half-even rounding and overflow. [Quote service tests](../../../service/quote/service_test.go) cover expiry, ownership, rate snapshots and replay. [Pricing integration tests](../../../db/gorm/pricing_integration_test.go) cover concurrent single consumption, expiry after lock waits, rollback, serialized initialization/import and legacy adoption. | D5 compares default US/USD and explicit IN/INR quotes, consumes and replays a quote, verifies original USD order, rejects missing rates/unknown regions/foreign quote use. Fixture rate 2 is a demonstration value, not a market rate. |
| Auth throttling and input handling | [Limiter tests](../../../api/rest/middleware/rate_limit_test.go), [password tests](../../../domain/user/user_test.go), [HTTP binder/error tests](../../../api/rest/server_test.go). | D2 includes invalid input and denied actions. Use automated evidence for limiter capacity/concurrency and forwarded-header behavior. |
| Worker health, logging and concurrency | [Worker status tests](../../../service/processing/status_test.go), [readiness route test](../../../api/rest/routes/readiness_test.go), request log test in server tests and [logging context test](../../../internal/logging/logging_test.go). Repository tests exercise multiple workers, locks and cancellation races. | D0/D4/D6 show readiness, run IDs, counts and timestamps. Failure recovery and concurrency are shown from automated logs, not induced in the timed demo. |
| Migrations and bounded queue/cleanup | [Migration tests](../../../db/gorm/migrations_integration_test.go), pricing tests, pending-batch query-plan test. Retain verbose EXPLAIN output. | Rehearsal artifacts. The small live dataset is not evidence of throughput or index scaling. |

## Execution order

For complete local prerequisites and setup, use [the local test guide](../../../TESTING.md).
It includes full Docker acceptance, native PostgreSQL without Docker, and a
database-free unit-test path. The commands below are the shorter reference for
an already prepared environment.

Run from the repository root on a host with Go 1.26, Python 3, Make, dependency
downloads, Docker Compose and permission to run PostgreSQL 18 containers. Reserve
the Docker smoke ports (18080/15432 by default); the live demo uses 18081/15433.

```sh
# Preferred complete rehearsal: provisions its own disposable database.
make acceptance
```

The runner records prerequisite results, dependency resolution, manifest snapshots,
module verification, format/build/vet/race results, migration up/down/up,
PostgreSQL integration/EXPLAIN and Docker smoke in
`docs/reviews/order-processing-hardening/verification/acceptance-<run-id>/`.
Review `summary.json` and logs; a blocked or failed run is not acceptance.
Dependency resolution can update go.mod/go.sum; retain and review those changes.

For diagnosis or a focused rerun after a fix:

```sh
go mod tidy
make fmt-check
make build
make vet
make test

# Set this to a dedicated disposable PostgreSQL database; schema creation required.
export TEST_DATABASE_URL='postgres://orders:orders_local@127.0.0.1:15433/orders?sslmode=disable'
make integration

# Focused original-requirements API/DB acceptance.
go test -mod=readonly -count=1 -race -v -tags=integration \
  ./api/rest/routes -run '^TestCustomerAndAdminOrderFlow$'

# Concurrency, transaction and query-plan checks.
go test -mod=readonly -count=1 -race -v -tags=integration ./db/gorm

# Isolated API/worker rehearsal; interval is deliberately shortened to five seconds.
make smoke-docker
```

These focused commands assume a database is already running; they do not provision
one. Integration tests create/drop unique test schemas. Migration rollback must
only target disposable data. Do not run integration or smoke against the timed
live-demo API while presenting; it adds load and can change authentication quotas.

## Demo sequence and evidence

Allow preparation/build time separately, then **15–20 minutes** to present the
[runbook](05-live-demo.md). Reserve the first ten minutes after D0 for two real
worker ticks. Use the first interval for order creation, reads, cancellation and
lists; use the second interval for regional pricing and access controls.

Record the source version (commit if available; otherwise a saved source snapshot),
Go/Docker/PostgreSQL versions, acceptance artifact directory, demo date and operator,
and the dedicated Compose project. Record HTTP statuses, relevant order/quote IDs,
expected versus actual amounts, worker attempt timestamps and run logs. The
runbook writes redacted HTTP bodies and worker evidence into a per-run directory.
Do not attach raw auth responses, passwords or tokens.

| Sign-off item | Result to enter | Required evidence |
| --- | --- | --- |
| Full automated acceptance | PASS — 2026-10-04 IST | All 22 stages passed; see the acceptance evidence above. |
| R1 create | NOT RUN / PASS / FAIL | 201, two items, USD 3495, PENDING. |
| R2 retrieve | NOT RUN / PASS / FAIL | Same ID/data; unknown/foreign 404. |
| R3a transitions | NOT RUN / PASS / FAIL | Ordered lifecycle, manual processing, forbidden/invalid transitions. |
| R3b five-minute worker | NOT RUN / PASS / FAIL | Interval 5m, two distinct successful ticks about 300s apart, two automatically advanced orders. |
| R4 list/filter | NOT RUN / PASS / FAIL | Unfiltered pages, filtered statuses, customer/admin scope. |
| R5 cancel | NOT RUN / PASS / FAIL | Pending/repeat success, later-state rejection, still cancelled after worker. |
| Supporting behavior | NOT RUN / PASS / FAIL | D5 pricing/ownership and D7 logout, plus supporting automated logs. |

Mark each row independently. Core demo completion requires every R1–R5 assertion
to pass, including both R3 rows. Full acceptance additionally requires successful
automated stages. A fast smoke or presentation using saved evidence can support a
walkthrough, but must be labelled as such and cannot close the live timing check.

## Retry safety extension

| Case | Test / expected result |
| --- | --- |
| Lost response, same customer/key/payload | `TestPlaceReplayConflictOwnershipAndUnkeyed`: original ID/prices and current status; HTTP 200. |
| Same key, different payload | Service and `TestCustomerAndAdminOrderFlow`: 409 `idempotency_key_conflict`. |
| Same key, another customer | Service/HTTP tests: independent order, no data leak. |
| Two concurrent payloads sharing a key | `TestConcurrentIdempotencyPayloadConflict`: one creation, one conflict, one durable key. |
| Eight concurrent retries | `TestIdempotentCreateConcurrentRequests`: one order/key, one creation and seven replays. |
| Key insertion fails after order/items/quote changes | `TestIdempotencyFailureRollsBackOrderAndQuote`: all writes roll back; same key can retry successfully. |
| Same quote, multiple keys | Rollback/retry and HTTP tests: every accepted key maps to the original order. |
| Changed JSON formatting or UUID case | HTTP flow: 200 replay. Invalid/empty/duplicate headers: 422. |
| Cancel already CANCELLED | Repository/HTTP tests: 200; unchanged updated_at; foreign customer 404. |
| Migration downgrade with keys | Repository test: refuses to erase retry protection. Empty up/down/up includes 000004. |

Service tests passed locally. New PostgreSQL/HTTP cases remain pending dependency
and database access; written test cases are not evidence of a passing run.

### Retention metadata and timeout follow-up

- `created_at` uses the DB clock and remains unchanged across replays; the
  concurrent-create test checks persistence and stable age.
- `TestKeyedCreateDeadline` checks the 10s operation budget, propagation of an
  earlier caller deadline, timeout errors and child-context cleanup.
- `TestIdempotencyLockTimeoutAndRecovery` holds a competing advisory lock,
  checks transaction-local 2s/5s settings, expects a retryable failure without
  writes, then verifies the same key succeeds after release.
- `TestIdempotencyStatementTimeoutRollsBack` stalls the key insert, verifies
  statement timeout rolls back the order/items/key, then retries successfully.
- `TestRetryableTimeoutResponse` checks safe HTTP 503 and `Retry-After: 1`.

The proposed 30-day retention window has no purge implementation or expiry tests;
keys currently remain indefinitely. Database/HTTP execution of this follow-up
remains pending environment access; no live timeout result is claimed.

### Version-4 compatibility and execution follow-up

`TestIdempotencyMigrationUpgradeAndNonemptyDowngradeGuard` uses the real migrator
against isolated schemas. It reconstructs version 4 with and without `created_at`,
upgrades to version 5, checks existing timestamps/key bindings survive and missing
timestamps are backfilled conservatively, then verifies 000004 refuses a downgrade
with durable keys and marks the schema dirty. The 000005 down step retains its
backward-compatible timestamp metadata. Empty up/down/up acceptance now covers
all five migration versions.

The requested commands were attempted in the
[2026-10-03 follow-up](../../reviews/order-processing-hardening/verification/followup-20261003T175704Z/summary.json).
Dependency resolution and the full test suite were blocked by download DNS;
`make integration` stopped because no dedicated TEST_DATABASE_URL was configured.
`make acceptance` stopped at Docker access. Native PostgreSQL initialization also
failed on prohibited shared-memory allocation. The new migration test, full
up/down/up round trip and nonempty downgrade guard remain unverified by execution.
