# Order Processing System — architecture design discussion

This document explains the implementation and the decisions behind it, and can
be used during an architecture interview or the live demo. The system is a **layered monolith with an embedded worker and one
PostgreSQL database**. Its main correctness mechanisms are database transactions,
conditional state changes, immutable purchase snapshots and bounded batch work.

Run the checks in [TESTING.md](TESTING.md) locally.

## 1. Requirements and design boundaries

| Requirement | Design response |
| --- | --- |
| Create orders with multiple items | Transactionally persist an order and its item snapshots, using catalog prices and the authenticated customer. |
| Retrieve an order | Fetch by UUID with customer ownership enforced; admins can retrieve any order. |
| Update status | Enforce one forward transition through PENDING, PROCESSING, SHIPPED and DELIVERED. |
| Automatically process pending orders every five minutes | Run a ticker in the service; drain eligible rows in bounded PostgreSQL batches. |
| List orders, optionally filtered by status | Use owner/status filters and cursor pagination. |
| Cancel only while pending | Apply a conditional PENDING → CANCELLED update in a transaction. |

Additional features beyond the assignment include customer/admin authentication, an admin-managed catalog,
regional price quotes backed by managed exchange rates, quote replay protection,
authentication limits, readiness checks, structured logs and acceptance tooling.
Payments, inventory reservation, delivery-provider integration, product editing,
frontend UI, password recovery and refresh tokens are not implemented.
SHIPPED and DELIVERED are recorded statuses; there is no external shipment action.

## 2. Why Go for this system?

Go fits a small network service with concurrent HTTP requests, a periodic worker
and explicit transactional rules. The decision prioritizes a straightforward
deployment and code that makes state changes easy to review. It is not based on
a benchmark claiming that another language could not satisfy the requirements.

| Go capability | Concrete use here | Tradeoff |
| --- | --- | --- |
| Compiled, statically typed code | Domain types, repository interfaces and compile-time adapter assertions connect the layers. | Compilation cannot establish money correctness, authorization or transaction safety; those still need tests. |
| Goroutines and synchronization primitives | Bootstrap runs HTTP serving and the worker concurrently; a mutex protects worker health, and a buffered channel bounds concurrent auth work. | Goroutines need cancellation and resource bounds; they do not coordinate different machines. |
| Interfaces and explicit construction | Bootstrap supplies repositories and services; tests use small fakes for quote/worker behavior. | Some plumbing and error propagation are intentionally explicit. |
| Standard tooling | `gofmt`, `go vet`, `go test` and race-enabled tests support consistent verification. | The race detector only finds races exercised at runtime; it does not prove database concurrency correctness. |
| Binary deployment | The Docker build uses `CGO_ENABLED=0` and copies the built executable into a non-root Alpine image. | The service still requires PostgreSQL, configuration, certificates, dependency preparation and operational monitoring. |
| Standard numeric and context packages | `math/big.Rat` implements exact conversion; context flows from handlers/worker into database operations. | Go has no application money type that automatically enforces currency, rounding and overflow rules; these are implemented explicitly. |

Go's language and concurrency design are described in the [Go FAQ](https://go.dev/doc/faq).
Cancellation/deadline propagation follows the [context contract](https://pkg.go.dev/context).
The limitations of dynamic race checking are described in the
[Go race detector documentation](https://go.dev/doc/articles/race_detector).
The concrete uses above are visible in [bootstrap](internal/bootstrap/server.go),
[money conversion](domain/money/money.go), [Dockerfile](Dockerfile) and
[Makefile](Makefile).

### Why not Java, .NET, Node.js, Python or Rust?

All are viable. The following is a project-specific tradeoff assessment, not a
general performance ranking. Existing team expertise could reasonably change the
choice without changing the domain model or PostgreSQL consistency strategy.

| Alternative | Why it would work | Why Go is reasonable here / when to choose the alternative |
| --- | --- | --- |
| Java with a web framework | Java supports concurrent network services, including [virtual threads](https://docs.oracle.com/en/java/javase/25/core/virtual-threads.html). | This assignment does not require an existing JVM application platform. Go keeps the selected implementation in one executable with explicit composition. A JVM-skilled team or existing Java services could favor Java. Virtual threads still need DB connection limits. |
| C# / ASP.NET Core | ASP.NET Core supports asynchronous request handling; Microsoft documents [async I/O and blocking-work considerations](https://learn.microsoft.com/en-us/aspnet/core/fundamentals/best-practices?view=aspnetcore-10.0). | Go is a practical choice for this repository's existing types, worker and tooling. An established .NET team could implement the same design effectively; deployment simplicity alone is not a reason to reject .NET. |
| TypeScript / Node.js | Well suited to asynchronous I/O, with [event-loop and worker-pool guidance](https://nodejs.org/learn/asynchronous-work/dont-block-the-event-loop). | Go lets the HTTP server and worker share one concurrency model. Node remains suitable, but CPU-heavy password work must avoid blocking the event loop and money needs an explicit exact representation. A TypeScript-first team may prefer it. |
| Python | [asyncio](https://docs.python.org/3/library/asyncio.html) supports concurrent network and database I/O. | Go provides compile-time interface checks and an executable deployment for this implementation. Python is reasonable where team productivity or a Python ecosystem matters more; production process/concurrency choices still require measurement. |
| Rust | [Ownership](https://doc.rust-lang.org/book/ch04-01-what-is-ownership.html) provides memory-safety guarantees without a garbage collector. | The assignment has no measured requirement for avoiding GC or for unusually tight resource control. Go is sufficient for the chosen design; Rust becomes attractive where those constraints or team expertise justify it. |

Go's garbage collector, scheduler and allocations still affect latency. Explicit
error handling can be repetitive. Echo and GORM do not eliminate framework or
database coupling. Most importantly, changing language would not solve duplicate
submissions, cancellation races or expired quotes: those are data-design problems.

## 3. High-level design (HLD)

```mermaid
flowchart LR
    C[Customer or admin API client] --> HTTP
    subgraph APP[Go service process]
        HTTP[Echo HTTP API] --> M[Authentication, roles and validation]
        M --> S[Order, product, quote and auth services]
        S --> D[Domain rules and exact money arithmetic]
        S --> R[Repositories and transaction unit of work]
        W[Periodic processing worker] --> R
        H[Readiness endpoint] --> R
        H --> W
    end
    R --> PG[(PostgreSQL)]
    CLI[Operator CLI: migrations, admin, rates, catalog] --> PG
    APP --> LOG[Structured stdout logs]
```

The deployment in [Compose](docker-compose.yml) starts PostgreSQL, runs the
migration command, then starts the API after migrations succeed. API and worker
share a process and connection pool. The database stores orders, catalog, users,
token revocations, rates and quotes. The CLI is another invocation of the same
binary, used for privileged setup and maintenance.

The monolith keeps order creation, quote consumption and status changes inside
one database transaction boundary. Microservices would introduce network failure
and cross-service coordination without a current requirement for separate
ownership or deployment. These packages are logical modules, not independently
deployed services. Separate command/query handlers organize code; there are no
separate CQRS read databases, event store, Redis cache or message broker.

PostgreSQL supplies relational constraints and transactional updates. GORM handles
ordinary persistence, while the queue and cleanup paths use explicit SQL where
locking and query shape matter. Explicit versioned SQL migrations make schema
changes reviewable; startup does not perform GORM auto-migration.

Existing-data upgrades require a maintenance window: stop old writers, apply
migrations, explicitly adopt the original catalog currency where required, import
rates and start the compatible application. Readiness currently expects exactly
schema version 5, so zero-downtime mixed-version upgrades are not established.
Down migrations guard against losing pricing provenance; after new monetary
writes, prefer forward repair. Migration rollback tests use disposable data.

## 4. Low-level design (LLD)

### Packages, responsibilities and dependency direction

| Layer / source | Responsibility and key contracts |
| --- | --- |
| [HTTP routes](api/rest/routes/routes.go) and [handlers](api/rest/v1/order_handler.go) | Route registration, DTO validation, role/owner scope, response formatting and HTTP status selection. |
| [Order service](service/order/command_handler.go) | `HandlePlace` coordinates keyed/unkeyed items and quotes; `HandleCreate`, `HandleStatus`, `HandleCancel`; read handlers delegate scoped queries. |
| [Quote service](service/quote/service.go) and [quote consumption](service/order/quote_handler.go) | Select region/rate, freeze a quote, then atomically consume it or replay its existing order. |
| [Domain order](domain/order/order.go) and [money](domain/money/money.go) | Pure item/total validation, previous-state mapping, conversion, precision and overflow checks. No HTTP or GORM dependency. |
| [Unit-of-work interface](service/uow/uow.go) | Execute a callback using repositories bound to one transaction; read database time. |
| [Persistence](db/gorm/order_repository.go) | Implement repositories, scoped reads, inserts, conditional updates and worker batch SQL. |
| [Auth adapter](service/auth/jwt/client.go) | Password verification, JWT issuance/validation, current-user lookup and logout revocation. |
| [Worker](service/processing/worker.go) | Schedule drains, enforce batch/run bounds and publish [local health](service/processing/status.go). |
| [Bootstrap](internal/bootstrap/server.go) and [configuration](configs/config.go) | Construct dependencies, validate settings, start/stop HTTP and worker, configure readiness. |

The domain declares repository abstractions; persistence implements them. Services
depend on those abstractions and the unit of work. Bootstrap supplies concrete
implementations. HTTP request DTOs and GORM row structs remain outside the domain.

### Data model and database safeguards

The diagram shows the application tables and their declared foreign keys. Fields
are abbreviated to highlight keys and the main business data.

```mermaid
erDiagram
    users ||--o{ orders : places
    users ||--o{ order_quotes : requests
    users ||--o{ order_idempotency : scopes
    orders ||--o{ order_items : contains
    products ||--o{ order_items : snapshots
    orders ||--o{ order_idempotency : resolves_to
    order_quotes ||--o{ quote_items : contains
    products ||--o{ quote_items : snapshots
    fx_rates |o--o{ orders : prices
    fx_rates |o--o{ order_quotes : prices
    orders |o--o| order_quotes : consumed_order_id

    users {
        uuid id PK
        varchar email UK
        varchar role
        boolean active
    }
    products {
        uuid id PK
        varchar sku UK
        varchar name
        bigint price_minor
    }
    orders {
        uuid id PK
        uuid customer_id FK
        uuid quote_id UK "Nullable logical reference, no FK"
        uuid rate_id FK "Nullable"
        varchar status
        char currency
        bigint total_minor
    }
    order_items {
        uuid order_id PK, FK
        integer position PK
        uuid product_id FK
        varchar sku
        varchar name
        bigint quantity
        bigint unit_price_minor
        bigint line_total_minor
    }
    order_idempotency {
        uuid customer_id PK, FK
        varchar idempotency_key PK
        char request_hash
        uuid order_id FK
        timestamptz created_at
    }
    order_quotes {
        uuid id PK
        uuid customer_id FK
        uuid rate_id FK "Nullable"
        uuid consumed_order_id FK, UK "Nullable"
        char region
        char currency
        bigint total_minor
        timestamptz expires_at
    }
    quote_items {
        uuid quote_id PK, FK
        integer position PK
        uuid product_id FK
        bigint quantity
        bigint source_unit_price_minor
        bigint unit_price_minor
        bigint line_total_minor
    }
    fx_rates {
        uuid id PK
        char base_currency
        char target_currency
        numeric rate
        timestamptz valid_from
        timestamptz valid_until
    }
    catalog_settings {
        smallint singleton_id PK
        char base_currency
        timestamptz initialized_at
    }
    denylisted_tokens {
        char token_hash PK
        timestamptz expires_at
    }
```

`||` means exactly one, `|o`/`o|` zero or one, and `o{` zero or many.
The item tables use composite primary keys; the database allows an empty parent,
while order and quote creation require at least one item in application validation.
`order_idempotency` has a composite customer/key primary key, so multiple keys
can resolve to one order. `consumed_order_id` is nullable and unique, making the
quote-to-consumed-order relationship optional one-to-one.

`catalog_settings` is a singleton used by pricing logic, and `denylisted_tokens`
is looked up by token hash; neither declares a foreign key. `schema_migrations`
is migration-tool metadata and is omitted from the application model.

| Table | Important fields / relationships | Safeguards |
| --- | --- | --- |
| `users` | UUID, normalized email, password hash, role, active flag | Unique email; customer/admin role constraint. Registration always creates a customer. |
| `products` | UUID, SKU, name, positive `price_minor` | Unique SKU; price in the catalog's single base currency. |
| `catalog_settings` | Singleton row containing `base_currency` | Singleton key; supported currency constraint; application rejects later currency mismatch. |
| `orders` | UUID, customer FK, status, currency, total, optional quote ID, pricing provenance | Valid status and positive total; unique non-null quote ID; provenance checks. |
| `order_idempotency` | Customer FK, case-sensitive key, SHA-256 request hash, order FK, DB-generated `created_at` | Primary key `(customer_id, idempotency_key)`; non-null result ID; stored in the creation transaction; no expiry. |
| `order_items` | Order FK, position, product FK, name/SKU/price snapshots, quantity, line total | Unique product per order; positive values; `line_total = quantity × unit_price`; delete with parent order. |
| `fx_rates` | UUID, base/target, decimal rate, finite validity interval, source | Positive `NUMERIC(24,12)` rate; valid interval; application import serializes overlap checks. |
| `order_quotes` | Customer FK, amounts, region/mapping version, rate snapshot, expiry, optional consumed-order FK | Expiry after creation; positive values; unique consumed order. |
| `quote_items` | Quote FK, position, product FK, source and converted prices | Unique product per quote; checked line totals; delete with parent quote. |
| `denylisted_tokens` | SHA-256 token hash and expiry | Primary key makes revocation insertion repeatable; raw token is not persisted here. |

See the [initial schema](db/migrations/000001_initial.up.sql),
[pricing migration](db/migrations/000002_pricing.up.sql) and
[quote migration](db/migrations/000003_order_quotes.up.sql).

`orders.quote_id` intentionally has no FK to the temporary quote table: the
accepted order and its retry identity survive quote cleanup. Conversely,
`order_quotes.consumed_order_id` references the durable order. Order item snapshots
preserve purchased names and prices even if catalog values later change.

Database constraints are defense in depth, not every business rule. The aggregate
order total is calculated in the application; no cross-row sum constraint enforces
it. There is no database trigger enforcing the full status graph. Catalog
immutability and non-overlapping rates depend on authorized application/CLI write
paths; direct SQL writers must follow the same rules.

| Query | Index / bounding strategy |
| --- | --- |
| Pending queue | Partial `(created_at, id)` index where `status = 'PENDING'`; oldest first, bounded batch. |
| Customer order list | `(customer_id, id DESC)`; owner plus optional cursor. |
| Customer status filter | `(customer_id, status, id DESC)`. |
| Admin status filter | `(status, id DESC)`; unfiltered order UUID primary key also supports ID traversal. |
| Active rates | `(base_currency, target_currency, valid_from, valid_until)`. |
| Expired quotes / revoked tokens | Separate expiry indexes. Quote deletion is bounded; token purge currently deletes all eligible rows in one operation. |

Order/product pagination uses UUIDv7 descending and `id < cursor`, fetching one
extra row to determine `next_cursor`. This avoids deep offset scans. UUID order is
not a cross-machine commit sequence or a snapshot of all pages; newly inserted or
status-changing rows can change results between requests. Each nonempty order
page loads its items in one additional query, avoiding a query per order.

### Multi-item order creation

1. Validate JWT and read the current user/role; derive the customer from that user.
2. Parse a strict JSON body. Accept 1–100 distinct products with positive quantities.
3. Begin a unit-of-work transaction. If a key is supplied, acquire its customer-scoped
   transaction advisory lock and look up the durable record. Matching fingerprints
   replay the owned order with 200; a mismatch returns 409.
4. For a new items request, fetch products, catalog currency and database time, then
   build immutable item snapshots. For quotes, use the existing locked consumption flow.
5. Check multiplication and total addition before `int64` overflow; reject unknown
   products, duplicate items or invalid amounts.
6. Insert order/items and, when supplied, the key/hash/order ID in the same transaction.
   Quote consumption also belongs to this transaction. A failure rolls everything back.
7. After commit, log the mutation and return 201 for creation or 200 for replay,
   with a `Location` header in both cases.

No client price, customer ID or role can replace these server-controlled values.
Unkeyed items-only requests can still duplicate after an ambiguous failure.

### Decision: durable idempotency records

**Decision:** use `order_idempotency` instead of adding a single key/hash pair to
`orders`. This supports create retries and is implemented in
[migration 000004](db/migrations/000004_order_idempotency.up.sql), the
[transaction coordinator](service/order/place_handler.go) and
[repository](db/gorm/idempotency_repository.go).

**Reason:** an order is a business entity, while a key identifies an accepted
customer request. Usually they are one-to-one, but existing quote replay allows
several distinct keys to refer to one order. This is the concrete distinguishing
case, not a claim that a table is necessary for basic items-only idempotency:

| Request sequence, same customer | Required result | Single key/hash on the order |
| --- | --- | --- |
| Quote Q, key A | Create O, 201; bind A to Q/O | Can store A. |
| Quote Q, key B | Replay O, 200; also bind B to Q/O | Cannot retain both A and B in one scalar key column. |
| Different items or quote, key B | 409 payload conflict | Ignoring B previously would allow unintended reuse. |
| Retry original quote Q, key A | Replay O, 200 | Overwriting A with B loses A's durable binding. |

The unique `orders.quote_id` already prevents quote duplication. The new table
prevents **forgetting or overwriting an accepted key's payload association**.
For items-only requests, storing nullable key/hash columns on orders with unique
`(customer_id, key)` is a sound simpler alternative. We could also reject a second
key for an existing quote-backed order; that would deliberately narrow the API
contract. We chose consistent key acceptance for both supported create forms.

**Transaction and concurrency:** `HandlePlace` acquires
`pg_advisory_xact_lock` on a 64-bit hash of customer ID and key before the lookup,
including absent keys. The full customer/key composite primary key determines
identity; a lock-hash collision only serializes unrelated requests. Requests
using different keys for the same quote additionally serialize on the quote row.
Lock order is always key then quote; the worker does not acquire either lock.
Creation, item insertion, quote consumption and insertion of the completed key
record use the same transaction. Commit exposes them together; rollback/crash
exposes none and releases the lock. Waiting retries then read the committed
record under READ COMMITTED. Database errors propagate, never masquerade as replay.

**Contract:** the optional key is case-sensitive and owner-scoped, 1–128 ASCII
letters/digits or `._:-`; invalid/empty/multiple headers return 422. The versioned
SHA-256 fingerprint covers decoded quote ID or ordered items, excluding the key
and owner (the owner scopes uniqueness). JSON whitespace/property ordering and
UUID spelling normalize; reordered items or changed quantities are different
payloads. Keep fingerprint encoding stable across upgrades. Matching replay
returns the existing order's current state and frozen purchase prices, not a
stored byte-for-byte response. A mismatch returns 409 with
`details.reason = idempotency_key_conflict`. No failed response/key is retained.
Keys currently persist indefinitely; cancellation and quote cleanup do not free
them. Same key under another customer belongs to a separate namespace.

**Why optional:** preserving the existing API contract lets old clients keep
placing orders. Clients that implement retries for items-only creation should
require a durable key in their own request workflow. A mandatory server header
would be a breaking change, requiring a versioned or announced rollout.

**Why current state on replay:** return the same business resource and immutable
purchase snapshots, including its current lifecycle status. After cancellation,
replay returns CANCELLED with 200. An alternative is persisting the original HTTP
status/body, which gives exact response replay but returns historical state and
adds response storage/versioning concerns. This API intentionally guarantees
purchase identity, not byte-for-byte reproduction of the first response.

**Migration compatibility:** 000004 is kept unchanged. Some earlier installations
may have applied it before `created_at` was added, so
[migration 000005](db/migrations/000005_idempotency_created_at.up.sql) uses
`ADD COLUMN IF NOT EXISTS` to bring either version-4 schema forward. Existing
column values are preserved; legacy keys without a timestamp receive the upgrade
time rather than a guessed historical time. This conservatively delays any future
expiry. The 000005 down step retains this backward-compatible column/metadata;
000004 still refuses to drop a nonempty table. Readiness requires schema version 5.
A guard failure leaves golang-migrate dirty and requires operator investigation;
tests asserting refusal use disposable schemas, never a live application database.

**Retention decision:** the database writes `created_at = clock_timestamp()` on
first successful insertion. GORM reads this field but does not generate or update
it; replay does not extend retention. The proposed production window is **30 days
from creation**, with 24 hours considered only when measured client retry and
reconciliation windows support it. Current code performs **no expiry or purge**,
so existing guarantees remain indefinite. Before activation: publish the window,
add an age index and bounded cleanup, coordinate deletion through the same key
lock protocol, and test cleanup/retry races. After deletion an items-only retry
can create a new order, whereas the separate unique quote ID still protects quote
replay. Clients must not recycle keys. No expiration date is promised by the
current API.

**Cost and boundaries:** one extra row and primary-index entry per distinct
accepted key, with a lookup and insert on first keyed creation. The timestamp
adds retention metadata. A keyed operation has a **10-second context deadline**
covering connection-pool acquisition through transaction completion; a shorter
caller deadline wins. Before acquiring the advisory lock, PostgreSQL receives
transaction-local `lock_timeout = 2s` and `statement_timeout = 5s`. These settings
also bound subsequent quote/row locks and SQL statements, and revert at transaction
end instead of leaking to a pooled connection. Total time remains bounded even
across multiple individually successful statements. Context cancellation asks the
driver to cancel/roll back; a broken network may delay server-side cleanup, so these
are execution budgets, not a strict response-time guarantee.

PostgreSQL lock/statement cancellation and context deadline failures produce a
safe 503 with `Retry-After: 1`. SQLSTATE 57014 covers query cancellation more
broadly than statement timeout, including cancellation after a client disconnect.
In that case the 503 may have no receiver; distinguish caller cancellation from
server timeout when adding error metrics to avoid overstating server failures. Clients retry with backoff using the original key
and payload; a timeout near commit can have an ambiguous outcome. Timeout tests
check rollback and successful retry after releasing the blocking condition.
These limits reduce stalls in the 20-connection pool per instance but cannot
prevent saturation by many simultaneous callers. Admission controls, pool-wait
metrics and load tests remain future work. Unkeyed creation and other endpoints
do not acquire these keyed-request SQL limits.

Indefinite retention grows storage; enabling the proposed bounded retention or a
maximum keys-per-order policy requires an explicit contract. The down migration
refuses nonempty key data so rollback cannot silently erase retry protection.

**Tests:** the service tests cover replay/conflict, ownership, unchanged historical prices,
current status, unkeyed compatibility and fingerprint stability. PostgreSQL tests
cover concurrent same-key requests, transaction rollback including quote
consumption, durable replay and guarded migration rollback; HTTP integration
covers normalization, response codes, owner scope and multiple keys per quote.
Use the [local acceptance plan](TESTING.md) to run these checks.

### State machine and cancellation race

```mermaid
stateDiagram-v2
    [*] --> PENDING: Create
    PENDING --> PROCESSING: Worker or admin
    PROCESSING --> SHIPPED: Admin
    SHIPPED --> DELIVERED: Admin
    PENDING --> CANCELLED: Owning customer
    DELIVERED --> [*]
    CANCELLED --> [*]
```

The repository performs a conditional update using ID, expected previous status
and, for cancellation, customer ID. It then reads the resulting order in the same
transaction. A missing/inaccessible order returns 404; an accessible order in the
wrong state returns 409. It does not read PENDING first and later update blindly.
Manual transitions and cancellation set `updated_at` using PostgreSQL `now()`,
matching the worker's database clock rather than relying on the API host's clock.

For cancellation versus processing, PostgreSQL row locks serialize competing
writes, and the losing operation cannot also change the old PENDING state. A
worker skips a row already locked by cancellation; cancellation that encounters an
already processed order returns 409. This protects the transition across processes,
where an in-memory Go mutex would be insufficient.

Repeating cancellation of an owned CANCELLED order returns 200 with the current
order, without another write or timestamp change. The repository recognizes this
only after the conditional update and ownership-scoped read. PROCESSING, SHIPPED
and DELIVERED still return 409; a foreign or missing order returns 404. Repeated
admin status transitions retain their existing 409 behavior.

### Five-minute processing algorithm

The first tick occurs five minutes after worker startup, followed by five-minute
ticks. Orders need not be five minutes old. Each run captures a cutoff, processes
PENDING rows created at or before it, and repeats batches until none are selected
or the run fails/times out. Newer rows wait for a later run.

The following is the implemented SQL shape, with named placeholders for clarity:

```sql
WITH batch AS (
    SELECT id FROM orders
    WHERE status = 'PENDING' AND created_at <= :cutoff
    ORDER BY created_at, id
    LIMIT :batch_size FOR UPDATE SKIP LOCKED
), updated AS (
    UPDATE orders AS o
    SET status = 'PROCESSING', updated_at = now()
    FROM batch
    WHERE o.id = batch.id AND o.status = 'PENDING'
    RETURNING o.id
)
SELECT count(*) FROM updated;
```

Default batch size is 500, allowed range 1–10000. This is **500 per transaction,
not 500 per five minutes**: a tick can drain many batches. Each run is sequential
within one process and has a deadline equal to its interval. Successful prior
batches stay committed if a later batch fails. A short batch does not end the
drain; an empty one does. Locked rows can remain pending until a later tick.

`SKIP LOCKED` permits queue consumers to avoid waiting on one another; it does
not provide a consistent general-purpose read or strict FIFO fairness. PostgreSQL
documents this queue-oriented use in its [SELECT locking clauses](https://www.postgresql.org/docs/18/sql-select.html).
The literal PENDING predicate matches the partial index, keeping historical orders
out of the queue index; query-plan tests must still verify actual index use.

### Regional pricing, exact money and quote consumption

The catalog has one persisted base currency. On a fresh catalog, `STORE_REGION`
selects its initial currency unless `CURRENCY` explicitly sets it. Thereafter,
changing the region changes default new quotes only; a mismatched explicit
currency fails startup. Existing data requires explicit currency adoption, and
conflicting historical currencies are rejected.

Supported mappings are US→USD, IN→INR, GB→GBP, JP→JPY, KW→KWD and DE/FR→EUR.
An operator imports immutable dated rate versions through the CLI. There is no
external FX feed or silent fallback for an unavailable rate. Imports lock the
catalog settings row while checking overlaps and inserting, serializing concurrent
imports. This conservatively serializes all pairs, which is adequate for infrequent
operator changes.

For each unit price:

```text
target_minor = half_even(source_minor × target/base rate × 10^target_digits / 10^source_digits)
line_total   = target_minor × quantity
order_total  = sum(line_total)
```

Calculations use exact rational arithmetic; JPY has 0 minor digits, KWD 3 and the
other supported currencies 2. Round once per unit, then multiply by quantity.
Reject zero-rounded or out-of-range values. With the demo fixture USD→INR rate 2,
USD 3495 minor units becomes INR 6990 for the demonstrated cart. The rate is a
test fixture, not a market-price claim.

A quote persists owner, items, region/mapping version, source values, converted
values, rate provenance and expiry. Expiry is the earlier of quote TTL (default
five minutes) and rate expiry. Submission uses only `quote_id`:

```mermaid
sequenceDiagram
    participant C as Customer
    participant S as Order service
    participant DB as PostgreSQL
    C->>S: POST orders with quote_id
    S->>DB: Begin transaction
    S->>DB: Find order by quote and owner
    alt Existing order
        DB-->>S: Durable order snapshot
        S->>DB: Finish transaction
        S-->>C: 200, same order ID
    else No existing order
        S->>DB: Lock owned quote FOR UPDATE
        DB-->>S: Quote and consumed state
        alt Another request consumed it while this request waited
            S->>DB: Read consumed order
            S->>DB: Finish transaction
            S-->>C: 200, same order ID
        else Unconsumed quote
            S->>DB: Read clock_timestamp after lock acquisition
            alt Quote expired
                S->>DB: Roll back
                S-->>C: 409 quote_expired
            else Quote valid
                S->>DB: Insert order and items
                S->>DB: Mark quote consumed
                S->>DB: Commit transaction
                S-->>C: 201, new order ID
            end
        end
    end
```

The quote lock and unique order quote ID prevent multiple committed orders for
one quote. Expiry uses database time after waiting for the lock, so a quote cannot
pass an earlier time check and then be consumed after expiry. Insert and consume
are atomic. Replays return the existing order even after quote expiry or cleanup;
cancelling that order does not make the quote reusable. A different customer gets
404. Workers remove up to 500 quotes per tick once they are at least 24 hours past
expiry; durable order snapshots retain the accepted terms.

### Regional pricing setup and API examples

Existing products/orders require an operator to confirm the original denomination:

```sh
docker compose run --rm api catalog init --currency USD --confirm-existing
```

Stop old writers before migration/adoption. Conflicting historical currencies
require investigation; initialization never guesses or repairs stored amounts.
For existing deployments: back up data, stop the API, apply migrations, confirm
currency, import rates, then start the new API. Fresh catalogs initialize at startup.

Import a positive decimal target/base rate with a finite UTC validity interval:

```sh
# Set RATE, VALID_FROM and VALID_UNTIL from your reviewed pricing configuration.
docker compose run --rm api rates add --target INR --rate "$RATE" \
  --valid-from "$VALID_FROM" --valid-until "$VALID_UNTIL" --source operator-config
```

Rates are immutable, non-overlapping per pair, and manually managed. These are
configured conversion prices, not a live market feed. No rates are seeded by normal
startup; the isolated Docker smoke uses an explicitly labelled fixture. Schedule
consecutive rate validity intervals before expiry to keep regional quotes available.
Base-to-base pricing uses rate 1. Missing/expired FX rates return 409 without fallback.

```sh
curl -s http://localhost:8080/api/v1/order-quotes \
  -H "Authorization: Bearer $CUSTOMER_TOKEN" -H 'Content-Type: application/json' \
  -d '{"region":"IN","items":[{"product_id":"PRODUCT_UUID_1","quantity":2}]}'

# Copy the quote ID. Omit region above to use STORE_REGION.
curl -s http://localhost:8080/api/v1/orders \
  -H "Authorization: Bearer $CUSTOMER_TOKEN" -H 'Content-Type: application/json' \
  -d '{"quote_id":"QUOTE_UUID"}'
```

### Migration and upgrade procedures

Migrations 000002/000003 add catalog/rates/quotes and optional legacy provenance.
Migration 000004 adds durable idempotency records. Migration 000005 adds
`created_at` if an earlier version-4 database lacks it, preserving timestamps in
databases that already have the column. Apply all migrations before starting this
binary; readiness requires version 5. Existing keys missing timestamps receive
the upgrade time, conservatively starting their retention age then. No database
volume reset is needed. Migration 000005's down step keeps the compatible column
and its data; 000004's down step refuses while keys exist to preserve retry
guarantees. An expected refused downgrade leaves golang-migrate's dirty flag set;
acceptance tests exercise that refusal only in disposable schemas.
Use a maintenance window; old writers cannot safely coexist with the new schema.
The quote down migration refuses while quotes or new-format orders exist. The
pricing down migration refuses while rates or mismatched order currencies exist.
After new monetary writes, use forward repair; restoring a backup loses later
writes and is a separate operator decision. Reversibility tests use disposable data.

## 5. API contract

The authoritative field schemas are in [OpenAPI](docs/openapi.yaml); route registration
is in [routes.go](api/rest/routes/routes.go). All paths below use `/api/v1`
unless explicitly shown otherwise. Protected routes use `Authorization: Bearer`.

| Method / path | Access | Request / success |
| --- | --- | --- |
| `POST /auth/register` | Public, limited | `{name,email,password}` → 201 customer session/token. |
| `POST /auth/login` | Public, limited | `{email,password}` → 200 session/token. |
| `GET /auth/session`, `GET /me` | Optional bearer | 200 authenticated user or `authenticated:false` for absent/invalid token. Infrastructure errors still fail. |
| `POST /auth/logout` | Token to revoke; absent token is a no-op | 204; valid supplied token is denylisted. Invalid supplied token returns 401. |
| `POST /products` | Admin | `{sku,name,price_minor}` → 201 product. |
| `GET /products` | Customer/admin | Optional `limit,cursor` → 200 page. |
| `GET /products/{id}` | Customer/admin | 200 catalog product. |
| `POST /orders` | Customer | `{items:[{product_id,quantity},...]}` **or** `{quote_id}` → 201 order; matching key or quote replay → 200 original order; key/payload conflict → 409. |
| `GET /orders/{id}` | Owner/admin | 200 order with items, currency, totals, status and pricing provenance. |
| `GET /orders` | Customer's orders / all orders for admin | Optional `status,limit,cursor` → 200 `{items,next_cursor}`. |
| `PATCH /orders/{id}/status` | Admin | `{status:"PROCESSING"}`, `SHIPPED` or `DELIVERED` → 200 after legal transition. |
| `POST /orders/{id}/cancel` | Owning customer | No body required → 200 CANCELLED from PENDING or on an already CANCELLED retry; later fulfillment states → 409. |
| `POST /order-quotes` | Customer | `{region?,items:[...]}` → 201 quote with rate, amounts and expiry. |
| `GET /health`, also absolute `/health` | Public | 200 process liveness. |
| `GET /ready` | Public | 200/503 based on DB/schema/catalog and local worker health. |
| Absolute `GET /api/swagger/openapi.yaml` | Public | Embedded API specification. |
| Absolute `GET /api/swagger/index.html` | Public | Swagger UI; its static UI assets require the configured CDN. |

Order lists accept all five statuses including CANCELLED, default to 20 items and
cap pages at 100. Keep the same filter when following a cursor. Item-only orders
always purchase in base currency; regional purchases must submit a quote. Request
bodies containing both items and quote ID are invalid.

Example create request (replace IDs with actual catalog UUIDs):

```json
{
  "items": [
    {"product_id": "01960000-0000-7000-8000-000000000001", "quantity": 2},
    {"product_id": "01960000-0000-7000-8000-000000000002", "quantity": 3}
  ]
}
```

Responses use integer minor amounts and decimal strings for rates. Clients must
preserve integer precision when handling large `int64` JSON values; a future
JavaScript client should not silently round them. API versioning or agreed bounds
would be needed before changing amount representation.

## 6. Validation, security and failure handling

| Condition | Current handling / client expectation |
| --- | --- |
| Malformed JSON, unknown fields or multiple JSON values | 400; strict binder accepts one JSON value and caps body reads at 1 MiB. Oversized decode currently also maps to 400. |
| Unsupported content type | 415; body endpoints expect `application/json`. |
| Invalid quantity, UUID, status/filter, duplicate item, unknown product or amount | 422; no partial order committed. |
| Missing/invalid/expired/revoked credentials | 401 on protected routes; session inspection can return unauthenticated instead. |
| Valid user with wrong role | 403. Customer cannot mutate catalog or advance status; admin cannot place/cancel customer orders. |
| Missing or foreign-owned order/quote | 404 to avoid revealing another customer's resource. |
| Wrong current state, duplicate email/SKU | 409. Read current state before deciding the next user action. |
| Expired quote or missing active FX rate | 409 with `details.reason` of `quote_expired` or `rate_unavailable`; request a fresh quote or restore valid rate availability. |
| Auth rate/capacity limit | 429 and Retry-After; no password work admitted beyond the configured capacity. |
| Unexpected database/infrastructure failure | Generic 500 for unexpected request errors, retryable 503 for lock/statement/context timeouts, and 503 for failing readiness. Internal errors are not returned verbatim. No general automatic transaction retry loop is implemented. |
| Worker batch fails | Roll back that batch, retain earlier commits, record failure, retry remaining pending work on a later tick. |
| Process stops during a transaction | Uncommitted database work rolls back when the connection ends. Committed data survives; an interrupted HTTP response leaves the client uncertain about commit. |

The error envelope is `{ "error": "message", "details": { ... } }`; details are
optional. Mutation logs are emitted after successful service completion and include
committed/replayed outcomes. Request logs carry a generated X-Request-ID, route,
status and duration; raw bodies, tokens, passwords and query strings are excluded.
Post-commit logs are operational evidence, not a transactional audit ledger: a
crash between commit and logging can leave a missing log event.
There is no `order_status_history` table. A durable history enhancement would
insert order ID, previous/new status, actor, reason and database timestamp in the
same transaction as every successful transition, including worker updates.

Passwords use bcrypt and length checks. JWT validation restricts HS256, issuer,
expiry and matching subject/user ID. Each protected request checks the persisted
token denylist and current active user/role; a token's embedded role cannot override
current database permissions. Logout hashes and revokes only the supplied token.
Missing-user login performs dummy password verification; inactive-user login also
performs verification, reducing obvious shortcuts without claiming equal latency.

Login defaults to 10 attempts/minute/IP and registration to 5; each route tracks
at most 10000 keys, with four shared in-flight auth slots per process. Limits use
the socket peer, ignore forwarded headers, and can therefore group clients behind
a proxy. They are process-local and multiply across replicas. Shared ingress
limits and a deliberate proxy trust policy are needed for a distributed deployment.

HTTP server timeouts are 5s for headers, 15s read, 30s write and 60s idle. DB startup
ping has a 5s deadline; readiness has 2s. The worker run has one interval and
maintenance up to 30s. Shutdown cancels the shared service context and allows up
to 10s for HTTP shutdown/worker completion, so in-flight DB work may be cancelled.
HTTP socket timeouts do not establish a general database statement deadline;
keyed creation now has a 10s context budget and transaction-local 2s lock/5s
statement limits. General deadlines for other requests remain future work.

Liveness says the process responds. Readiness additionally verifies schema version
5, a clean migration state, initialized catalog settings and local worker health.
The worker has two intervals of startup grace; a failed run is unhealthy immediately,
a success older than two intervals is stale, and an active attempt lasting one
interval is stale. Successful empty runs restore health. Maintenance cleanup errors
are logged separately and do not themselves mark order processing failed.

## 7. Distributed-system behavior and guarantees

| Concern | Implemented behavior | Boundary / remaining work |
| --- | --- | --- |
| Multiple API replicas | Order/quote/user/revocation state is in shared PostgreSQL. Replicas using the same JWT configuration do not need sticky sessions. | Rate-limit counters and worker health remain local; Compose currently declares one API service. |
| Concurrent workers | PostgreSQL locks and conditional updates coordinate rows; a process does not overlap its own drains. | No leader election or cluster-wide schedule exists. Each process starts its own five-minute ticker. |
| Global timing | One process demonstrates the assignment's periodic behavior. | Staggered replicas can cause processing runs more often than once per five minutes across the deployment. Repeated restarts also reset the first-run delay. A strict global cadence needs dedicated scheduling/coordination. |
| Duplicate quote submission | Unique quote-to-order identity plus quote locking gives one committed order per quote through the supported write paths; retry returns that order. | This is a DB-scoped guarantee, not exactly-once payment, shipment or message delivery. Items-only creation is deduplicated when the caller supplies an Idempotency-Key. |
| Database or network outage | Protected operations require authoritative DB state and fail when it cannot be read/written; readiness fails. | There is no offline write acceptance or partition-tolerant multi-primary design. Recovery/HA is not provisioned by this application. |
| Clock differences | Quote validation reads DB time after locking; persisted quote expiry is authoritative. | Worker cutoff and JWT checks use application clocks; creation uses DB time. Synchronize hosts; a DB-derived worker cutoff would reduce skew sensitivity. |
| Lost response after commit | Retry create with the same key/payload, or a quote submission with its original quote ID. | Unkeyed items-only retries risk duplicates. Repeating an admin status change yields 409; repeating an owned cancellation yields 200/CANCELLED. |
| Read consistency | Current reads and writes use the primary DB. Ownership is part of the query scope. | Multi-page reads are not a frozen snapshot; no read replica or cache consistency policy is implemented. |
| External side effects | None are performed by order status updates. | If payment/shipment/notifications are added, use an outbox and idempotent consumers; DB commit plus a direct network call would introduce a failure gap. |

In a crash, PostgreSQL remains the source of truth. Rows already committed as
PROCESSING no longer match the pending queue; uncommitted changes can be retried.
This supports safe repeated scanning, but does not imply that a complete worker
run is atomic. Concurrency tests exercise the critical row-level cases; deployment
failover and partition behavior have not been tested.

Creation and quote unit-of-work transactions explicitly use READ COMMITTED, so
lookups after waiting for key/quote locks see newly committed results. Other
transactions use the database session setting, normally READ COMMITTED. Each statement
can see a different committed snapshot. Transactions, row locks and unique
constraints enforce the specific order/quote invariants; this is not a blanket
claim of serializable workflows or a snapshot spanning multiple API requests.
[PostgreSQL isolation behavior](https://www.postgresql.org/docs/18/transaction-iso.html).

Even if each drain held a cross-process lock, staggered replicas could acquire
it one after another, so that would not establish a global five-minute schedule.
The current implementation prevents overlapping drains only within one process.
A strict global cadence needs one active scheduler
or coordinated scheduled slots, with explicit failover/restart behavior. Merely
moving the same ticker into several dedicated worker replicas retains the issue.

## 8. Scaling strategy

### What already bounds work

The application limits request bodies, items per order, page size, auth concurrency,
auth limiter keys, worker batch size and quote cleanup. The DB pool is currently
hard-coded to 20 open / 5 idle connections per process, with a 30-minute lifetime.
HTTP and worker share that pool. The partial pending index keeps historical
non-PENDING orders out of the queue lookup. Batch size bounds the number of rows
updated, but does not necessarily bound the rows scanned by the update's join.

In the standard integration fixture (1,201 pending rows) PostgreSQL chose a
bitmap scan over the pending set for the `UPDATE ... FROM batch` side. An opt-in
test (`BACKLOG_ROWS=200000`, see [TESTING.md](TESTING.md#optional-large-backlog-plan-comparison))
seeds 200,000 pending and 50,000 delivered rows and drains them in 400 committed
batches of 500. In the author's PostgreSQL 18.4 run (one local connection, warm
cache, no concurrent traffic), the planner used primary-key lookups for the
selected rows at both full backlog and with 10% remaining. Batch execution under
`EXPLAIN ANALYZE` took 8.80 ms and 8.26 ms respectively; committed batches had a
median of 5.75 ms and p95 of 9.83 ms, with a total drain time of 2.64 s. Per-batch
time did not grow over the measured drain. The queue-index scan read more buffers
near the end (13 initially, 910 with 10% remaining), consistent with processed
rows leaving dead index entries pending cleanup. Autovacuum settings for `orders`
matter under sustained churn. A primary-key-array rewrite was about 10% faster
and was not adopted. These are single-machine plan and timing observations,
not production throughput claims.

Successful authenticated requests also perform a denylist lookup and a current
user lookup. Measure that cost alongside order queries. Caching either can delay
revocation or role changes, so any cache policy must define the acceptable delay.
Read replicas similarly require an explicit stale-read policy. Neither is a
transparent correctness-preserving switch for every endpoint.

The worker deadline limits each drain, and its cutoff prevents an endless chase
of new arrivals. Locks, DB I/O, WAL, vacuum and competing API traffic still determine
capacity. A large backlog or long locks can exceed the intended processing delay;
the five-minute interval is not a proven five-minute end-to-end SLA.

### Grow in measured stages

| Stage | Change / decision | Evidence needed before proceeding |
| --- | --- | --- |
| 1 — Establish a working baseline | Local acceptance and a five-minute scheduler run were performed by the author; the scheduler run is recorded in the [demo note](docs/reviews/order-processing-hardening/verification/native-five-minute-demo/README.md). Next, measure one API/worker process and PostgreSQL with representative carts/history and pending backlogs. | API p50/p95/p99 latency, errors, order throughput, DB pool waits, query plans, locks, CPU/I/O and oldest pending age. No load-test baseline numbers are available yet. |
| 2 — Tune the current deployment | Tune DB resources, indexes/query plans and processing batch size; make connection limits configurable if needed. | Confirm that throughput improves without making API latency or lock waits unacceptable. More goroutines alone cannot increase DB capacity. |
| 3 — Add API replicas | Use a load balancer and shared primary database; consistent JWT/region settings, readiness routing and global ingress limits. | Budget up to `20 × replica_count` application connections with room for migrations/admin/monitoring; load and revocation consistency tests. A different deployment configuration is required. |
| 4 — Separate scheduling/work capacity | Add independent API/worker modes and a dedicated scheduler or coordinated schedule if global cadence matters. | Failure/restart tests and a clearly defined timing contract. Do not merely add a worker: the current API always embeds one, so it must be disabled/extracted as part of this change. |
| 5 — Add database resilience / read capacity | Provision backups, restore drills, HA/failover; consider reporting replicas once stale-read semantics are acceptable. | Recovery objectives, failover tests and replica-lag behavior. Keep correctness-sensitive order/auth reads on the primary unless their consistency policy changes. |
| 6 — Add asynchronous integrations when required | Transactional outbox → broker → idempotent consumers for notifications or fulfillment. | Publish/retry/deduplication/reconciliation tests, versioned events, dead-letter handling and operational ownership. These components are not implemented. |

For sizing, measure batch time `t` for `B` rows under representative contention.
`B/t` is a local observed drain rate, not a promised service capacity. Compare it
with arrivals and backlog, including the idle period before a tick and retry
costs. Account for shared database limits before increasing worker count.

Avoid sharding until measurements establish a need: it would complicate customer
lists, quote uniqueness and transactions. Catalog caching could eventually help
reads, but accepted quotes/order snapshots must continue to use authoritative
pricing semantics. None of caching, sharding, autoscaling or a broker is required
to demonstrate the current assignment.

## 9. Coverage and test cases

The detailed [test plan](docs/plans/order-processing-hardening/04-test-plan.md) maps
every requirement to source tests and the [live demo](docs/plans/order-processing-hardening/05-live-demo.md).
The following matrix explains the architectural risks covered by those tests.

| Area | Existing cases |
| --- | --- |
| Order domain | Multiple item snapshots, exact total 3495, UUIDv7/time values, empty/unknown/duplicate items, zero/negative quantities, line/aggregate overflow, permitted predecessor mapping. [Tests](domain/order/order_test.go). |
| Money and quote use case | Identity conversion, JPY/KWD precision, half-even ties, invalid rates/overflow; quote snapshot, TTL cap, foreign owner, expired quote, missing rate, replay after cleanup. [Money](domain/money/money_test.go), [quote service](service/quote/service_test.go). |
| Worker/configuration/health state | Full/short/empty batches, cancellation/failure, no work before initial tick in the tested cancellation case, two real short-interval ticker events and shutdown, health boundaries/recovery, concurrent health reads, configuration bounds. [Worker tests](service/processing/worker_test.go), [status](service/processing/status_test.go), [config](configs/config_test.go). |
| Auth and HTTP boundary | Registration/login/logout, invalid JWT/current role, inactive/missing-user verification, password bounds, limiter capacity/concurrency/forwarded-header behavior, JSON/content-type errors and log redaction. [JWT](service/auth/jwt/client_test.go), [middleware](api/rest/middleware/rate_limit_test.go), [server](api/rest/server_test.go). |
| Request UUID guards | Malformed/missing/nil product IDs and malformed/nil quote IDs return 422 with both the production binder and a decoding-only binder, before service access. [Handler tests](api/rest/v1/order_handler_test.go). |
| Requirements through API + PostgreSQL | Roles/ownership, catalog, multi-item order, totals, skip/cancel rules, processing/delivery, filtering/cursors, quote creation/replay, logout. [Scenario](api/rest/routes/routes_integration_test.go). |
| Create retry safety | Service replay/conflict/owner scope and fingerprint tests; DB concurrency, rollback, quote aliases and key retention. [Service tests](service/order/place_handler_test.go), [DB tests](db/gorm/idempotency_integration_test.go). |
| Order persistence/concurrency | Snapshot persistence after catalog change, transactional rollback, concurrent batches/cutoff, 20 cancellation races, skipping locked rows, failed batch rollback, pending index EXPLAIN. [Tests](db/gorm/order_repository_integration_test.go). |
| Backlog plan comparison (opt-in) | 200,000 pending plus 50,000 delivered rows, production batch SQL against a primary-key rewrite, EXPLAIN at full backlog and near the end, committed-batch timings, final-state assertions. [Test](db/gorm/pending_backlog_plan_integration_test.go). |
| Transition timestamps | A transition's `updated_at` matches database transaction time, independent of application timestamp generation. [Repository tests](db/gorm/order_repository_integration_test.go). |
| Pricing persistence/concurrency | Eight concurrent submissions producing one order, replay after cleanup, currency mismatch, serialized initialization/rate imports, legacy adoption, expiry after lock wait, rollback if consumption fails. [Tests](db/gorm/pricing_integration_test.go). |
| Migrations and cleanup | Migration round trip/legacy preservation, down guards, quote-expiry cleanup index EXPLAIN. [Tests](db/gorm/migrations_integration_test.go). |
| Deployed API smoke and live timing | Docker smoke exercises auth/catalog/orders/quotes and automatic processing on a five-second tick. The live runbook checks automatic transitions and preserved terminal states. |

The controlled API integration test is designed to verify successful pending cancellation. The
fast Docker smoke accepts 200 or 409 for cancellation because its worker can win
the race; that flexible smoke result alone is insufficient evidence of successful
cancellation. The standard pending query-plan fixture includes 20,000 delivered
and 1,201 pending rows; it tests query behavior, not production throughput. The
opt-in backlog test above covers 200,000 pending rows.

Use `make acceptance` locally for dependency, build, vet, race,
migration, PostgreSQL integration and Docker checks. It stores diagnostics in ignored `.cache/acceptance/`. The runner currently does not produce a statement-coverage
profile. If a percentage is requested, run the following after dependency setup
against a dedicated disposable DB with `TEST_DATABASE_URL` configured:

```sh
mkdir -p .cache/coverage
go test -mod=readonly -count=1 -race -tags=integration -covermode=atomic \
  -coverpkg=./... -coverprofile=.cache/coverage/combined.out ./...
go tool cover -func=.cache/coverage/combined.out
go tool cover -html=.cache/coverage/combined.out -o .cache/coverage/index.html
```

This runs unit and integration tests and measures Go statement coverage. It does
not include live-demo requests, prove all branches, establish an SLA, or replace
concurrency assertions. No full-repository coverage percentage is available now.

## 10. Enhancements already implemented and next priorities

| Implemented enhancement beyond basic CRUD | Why it matters |
| --- | --- |
| Customer/admin authorization and owner-scoped queries | Prevents one customer reading or mutating another customer's orders. |
| Catalog-controlled prices and item snapshots | Makes totals authoritative and preserves purchase history. |
| Exact conversion, persisted base currency and versioned regional quotes | Prevents region changes from reinterpreting old amounts; preserves accepted rate/price terms. |
| Atomic quote consumption and durable retry identity | Prevents duplicate orders when the same quote is submitted concurrently or retried. |
| Conditional status transitions, partial queue index and locked batches | Preserves cancellation rules while bounding scheduled work. |
| Cursor pagination and batched item reads | Bounds API responses and avoids fetching every order or issuing per-order item queries. |
| Token revocation, bounded auth work and strict input handling | Limits authentication abuse and avoids trusting client-controlled identity/prices. |
| Worker-aware readiness and correlated structured logs | Makes failed/stale processing visible alongside API availability. |
| Explicit migrations, legacy adoption checks and rollback guards | Avoids silently guessing original currency or discarding new pricing provenance. |
| Isolated acceptance runner, coverage matrix and live-demo runbook | Makes local requirement checks repeatable. |

The following are **future work**, not claims about the delivered implementation:

| Priority | Enhancement | Trigger / validation |
| --- | --- | --- |
| Before promising load targets | Add metrics/tracing, representative load/soak tests and explicit latency/backlog objectives. | Use measured bottlenecks; no numerical throughput promise yet. |
| Before multi-replica/global scheduling commitments | Define global cadence, separate worker lifecycle if needed, use DB time for cutoff, and coordinate scheduling/recovery. | Test staggered starts, repeated restarts, clock skew and loss of a scheduler. |
| Before broad client retries | Require callers to reuse keys and retain the passing idempotency regressions in CI. | Exercise lost responses, concurrent conflicting reuse and crash recovery on a real database. |
| When support/audit history is required | Add transactional order status history covering customer, admin and worker changes. | Verify rollback leaves no history entry and concurrent attempts record only the committed transition. |
| Before production exposure | Set TLS/secret rotation/ingress policy, shared auth limits, DB connection/deadline budgets and tested backup/restore procedures. | Deployment and failure tests; local Compose is not an HA deployment. |
| As cleanup volume grows | Bound revoked-token purge and monitor quote cleanup backlog. | Verify bounded transactions under high expiration volume. |
| If external fulfillment/payments enter scope | Add transactional outbox, idempotent consumers and reconciliation; consider compensation for multi-step business operations. | Test crash windows and duplicate delivery; agree new requirements first. |

## 11. Discussion prompts for the walkthrough

| Reviewer question | Answer grounded in this implementation |
| --- | --- |
| Why Go? | It fits the explicit domain rules, HTTP plus worker concurrency, testable interfaces and executable deployment. Team skill and measured constraints could justify another language. |
| Why a monolith? | Current operations benefit from one transaction boundary; separate services have no demonstrated ownership or scaling need yet. |
| Why PostgreSQL as the queue? | The work is a conditional status change already in PostgreSQL. Locked indexed batches coordinate it without an additional delivery system. |
| What prevents cancelling an order while it is being processed? | Conditional writes plus row locks ensure only one competing PENDING transition succeeds. |
| Is processing exactly once? | There is one committed transition out of PENDING through supported paths; repeated scanning is safe. No exactly-once external side-effect guarantee is claimed. |
| What happens if the customer retries? | Same customer/key and payload return the original order with 200; conflicting reuse returns 409. Quote ID replay also works without a key. Unkeyed items-only requests can duplicate. |
| What happens when the region changes? | Obtain a new regional quote. Existing catalog denomination, quotes and accepted orders retain their snapshots. |
| Can it scale horizontally? | DB coordination supports concurrent application instances, but shared limits, connection budgets, deployment configuration and global scheduling semantics need additional work. |
| How can a reviewer test it? | Run `make acceptance` for the full local suite, or follow TESTING.md for native PostgreSQL and unit-only options. |
