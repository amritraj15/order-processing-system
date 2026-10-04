# Example walkthrough: three customers and five multi-item orders

This worked example follows the implemented service through creation, retries,
reads, cancellation, automatic processing and delivery. It shows the application
tables read and written at each step, then accounts for the rows left at the end.
It is an explanatory scenario, not a recorded test run or executable script.

Names such as `A1`, `Q1` and `BOOK` are readable aliases for real UUIDs. Request
examples use these aliases; replace them with actual UUIDs when calling the API.
All HTTP paths are relative to `/api/v1`. Money is stored in integer minor units.

### Reading the before-and-after snapshots

Snapshots show committed data immediately before and after an operation. They
display the relevant columns of affected rows; omitted columns and unlisted rows
retain their previous values. `Absent` means no row exists for the specified
key, not necessarily that its entire table is empty. Read-only and rejected
operations explicitly show no change.

Labels such as `t_A1`, `t_cancel_A2` and `t_worker` represent database timestamps,
not recorded execution times. `H_A1`, `H_A2` and `H_Q1` stand for request hashes;
password/token hashes are also illustrative labels, not literal stored values.
Creation times remain unchanged when status updates change `updated_at`.

See the [database reference](database-requirements.md) for every column and
constraint, [code walkthrough](code-map.md) for methods, and
[architecture](../architecture.md) for the ER diagram.

## 1. People, catalog and initial rows

An operator provisions one admin. Three customers register through
`POST /auth/register`.

| Person | User alias | Role | Example activity |
| --- | --- | --- | --- |
| Store administrator | ADMIN | admin | Creates products and advances shipment statuses. |
| Alice | ALICE | customer | Places two direct orders and cancels one. |
| Bob | BOB | customer | Accepts an INR quote and later lets another quote expire. |
| Chen | CHEN | customer | Retries an unkeyed purchase and cancels the resulting duplicate. |

There are now **four `users` rows**. Each stores an identity, password hash, role
and account metadata. Login issues a JWT without inserting a session row.

The catalog is initialized in USD. The admin creates these three products:

| Product alias | SKU | Name | `products.price_minor` | Display price |
| --- | --- | --- | ---: | --- |
| BOOK | BOOK-01 | Notebook | 1200 | USD 12.00 |
| PEN | PEN-01 | Pen | 300 | USD 3.00 |
| MUG | MUG-01 | Mug | 1500 | USD 15.00 |

The operator imports a valid USD → INR rate of **2**. This deliberately simple
fixture is not a real exchange-rate claim. USD and INR both use two fractional
digits, so USD 12.00 becomes INR 24.00 in this example.

| Table | Setup result |
| --- | --- |
| `users` | Four rows: ADMIN, ALICE, BOB, CHEN. |
| `catalog_settings` | One row: `singleton_id=1`, `base_currency=USD`. |
| `products` | Three rows: BOOK, PEN, MUG. |
| `fx_rates` | One row, alias RATE1: USD → INR, rate 2, with a validity window and source. |
| Other six application tables | Empty. |

Assume the rate remains valid throughout checkout. Quotes use a five-minute TTL.
Orders A1, A2, B1, C1 and C2 are created before the first worker tick; A2 and C2
are also cancelled before that tick. The later expired quote is requested
separately. This ordering keeps the example deterministic.

### Setup: before and after

Starting with an empty application schema, each setup operation produces the
following row. The six order/quote/retry/revocation tables remain empty.

| Operation and table key | Before | After: selected values |
| --- | --- | --- |
| Initialize `catalog_settings(1)` | Absent | `singleton_id=1, base_currency=USD, initialized_at=t_init` |
| Provision `users(ADMIN)` | Absent | `name=Store administrator, email=admin@example.com, role=admin, active=true, password_hash=HASH_ADMIN` |
| Register `users(ALICE)` | Absent | `name=Alice, email=alice@example.com, role=customer, active=true, password_hash=HASH_ALICE` |
| Register `users(BOB)` | Absent | `name=Bob, email=bob@example.com, role=customer, active=true, password_hash=HASH_BOB` |
| Register `users(CHEN)` | Absent | `name=Chen, email=chen@example.com, role=customer, active=true, password_hash=HASH_CHEN` |
| Create `products(BOOK)` | Absent | `sku=BOOK-01, name=Notebook, price_minor=1200` |
| Create `products(PEN)` | Absent | `sku=PEN-01, name=Pen, price_minor=300` |
| Create `products(MUG)` | Absent | `sku=MUG-01, name=Mug, price_minor=1500` |
| Import `fx_rates(RATE1)` | Absent | `base_currency=USD, target_currency=INR, rate=2, source=example-fixture, valid_from=t_rate_start, valid_until=t_rate_end, created_at=t_import` |

New user/product rows also receive creation and modification timestamps. Login
reads a user and issues a JWT; that user row stays unchanged and no session or
denylist row is inserted.

### Reads common to authenticated requests

Before an authenticated order, quote or product handler executes, authentication
checks `denylisted_tokens` and loads the current account from `users`. A valid
token must belong to an active user with an allowed role. These reads apply in
addition to the business-table reads listed below. An invalid/revoked token can
stop the request before the handler runs.

No order operation decrements stock or charges a payment: those capabilities are
outside this implementation.

## 2. Alice places direct order A1 with two items

Alice sends:

```http
POST /orders
Idempotency-Key: checkout-1

{"items":[{"product_id":"BOOK","quantity":2},{"product_id":"PEN","quantity":3}]}
```

The service reads catalog prices and computes:

| Item | Quantity | Unit price, USD minor units | Line total |
| --- | ---: | ---: | ---: |
| BOOK | 2 | 1200 | 2400 |
| PEN | 3 | 300 | 900 |
| Total | | | **3300 = USD 33.00** |

`HandlePlace` runs one unit-of-work transaction:

1. Acquire the transaction advisory lock for `(ALICE, checkout-1)` and look for
   that binding in `order_idempotency`. None exists.
2. Read BOOK and PEN from `products` and USD from `catalog_settings`.
3. Validate the items, calculate totals and obtain database time.
4. Insert A1 into `orders`: owner ALICE, status PENDING, currency USD, total 3300,
   pricing mode `base`, identity rate 1 and no quote ID.
5. Insert two `order_items` rows containing product/name/SKU/price snapshots.
6. Insert `(ALICE, checkout-1, request_hash, A1)` into `order_idempotency`.
7. Commit and return HTTP **201**.

| Table | Business operation |
| --- | --- |
| `order_idempotency` | Lookup, then insert one binding. |
| `products` | Read two products. |
| `catalog_settings` | Read the base currency. |
| `orders` | Insert one row. |
| `order_items` | Insert two rows. |
| `order_quotes`, `quote_items`, `fx_rates` | No business read or write for this direct purchase. |

If an insert fails before commit, the transaction rolls back all three sets of
writes: the order, its items and its retry binding.

### A1 creation: before and after

| Table and row key | Before | After |
| --- | --- | --- |
| `orders(A1)` | Absent | `customer_id=ALICE, status=PENDING, currency=USD, total_minor=3300, pricing_mode=base, quote_id=NULL, created_at=t_A1, updated_at=t_A1` |
| `order_items(A1,0)` | Absent | `product_id=BOOK, sku=BOOK-01, name=Notebook, quantity=2, unit_price_minor=1200, line_total_minor=2400, source_unit_price_minor=1200, source_line_total_minor=2400` |
| `order_items(A1,1)` | Absent | `product_id=PEN, sku=PEN-01, name=Pen, quantity=3, unit_price_minor=300, line_total_minor=900, source_unit_price_minor=300, source_line_total_minor=900` |
| `order_idempotency(ALICE,checkout-1)` | Absent | `request_hash=H_A1, order_id=A1, created_at=t_key_A1` |
| `products(BOOK)` | `price_minor=1200` | `price_minor=1200` |
| `products(PEN)` | `price_minor=300` | `price_minor=300` |
| `catalog_settings(1)` | `base_currency=USD` | `base_currency=USD` |

A1 also stores `source_currency=USD, source_total_minor=3300, rate=1,
base_digits=2, target_digits=2, rate_source=identity`; no FX rate is referenced.
After commit there is **1 order, 2 order items and 1 binding**. If the transaction
rolls back, all four newly inserted rows above remain absent.

### Alice retries after losing the response

She resends the same key and payload. The service finds the binding, compares the
request hash, and loads A1 plus its two items. It returns **200** with the same
order ID. No product repricing or new row insertion occurs.

If two matching requests arrive concurrently, the key lock serializes them. If
the first commits and the waiting request proceeds within its timeout, the second
returns the committed order. If waiting times out, the caller can retry with the
same key and payload.

Changing BOOK quantity to 3 while keeping `checkout-1` returns **409**. The stored
hash prevents silently treating a different purchase as the original request.

| Retry operation | Stored data before | Stored data after |
| --- | --- | --- |
| Same key and same cart → 200 | A1 PENDING; two item rows; `(ALICE,checkout-1,H_A1,A1,t_key_A1)` binding. | Exactly the same rows and timestamps. |
| Same key, BOOK quantity changed to 3 → 409 | A1 has BOOK quantity 2, total 3300; binding hash H_A1. | Quantity stays 2, total stays 3300, hash stays H_A1. No new rows. |

## 3. Alice places A2, then cancels it

Alice uses a new key, `checkout-2`, for BOOK × 1 and MUG × 1.

The same direct-order path creates:

| Table | New rows |
| --- | --- |
| `orders` | A2: ALICE, PENDING, USD, total **2700**. |
| `order_items` | BOOK: 1 × 1200 = 1200; MUG: 1 × 1500 = 1500. |
| `order_idempotency` | `(ALICE, checkout-2) → A2`. |

Alice calls `POST /orders/A2/cancel` before the worker processes it.

The repository conditionally updates A2 only where its owner is ALICE and its
status is PENDING. It sets `status=CANCELLED`, updates `updated_at`, reads the
order/items for the response, and commits. HTTP **200** is returned.

The two item rows and the idempotency binding remain unchanged. Cancellation
does not delete the order, restore inventory or issue a refund.

Repeating the cancellation returns **200** without another state change or
timestamp update. Replaying `checkout-2` returns A2 in its **current CANCELLED
state**, not the original PENDING response.

### A2 creation and cancellation: before and after

Creating A2 adds these rows while all A1 rows remain unchanged:

| Table and row key | Before creation | After creation |
| --- | --- | --- |
| `orders(A2)` | Absent | `customer_id=ALICE, status=PENDING, currency=USD, total_minor=2700, pricing_mode=base, quote_id=NULL, created_at=t_A2, updated_at=t_A2` |
| `order_items(A2,0)` | Absent | `product_id=BOOK, quantity=1, unit_price_minor=1200, line_total_minor=1200` |
| `order_items(A2,1)` | Absent | `product_id=MUG, quantity=1, unit_price_minor=1500, line_total_minor=1500` |
| `order_idempotency(ALICE,checkout-2)` | Absent | `request_hash=H_A2, order_id=A2, created_at=t_key_A2` |

These item rows also save the catalog SKU/name and source prices equal to final
USD prices. Counts become **2 orders, 4 order items and 2 bindings**.

| Table and row key | Before cancellation | After cancellation → 200 |
| --- | --- | --- |
| `orders(A2)` | `status=PENDING, created_at=t_A2, updated_at=t_A2, total_minor=2700` | `status=CANCELLED, created_at=t_A2, updated_at=t_cancel_A2, total_minor=2700` |
| `order_items(A2,0)` | BOOK, quantity 1, line total 1200. | Same row. |
| `order_items(A2,1)` | MUG, quantity 1, line total 1500. | Same row. |
| `order_idempotency(ALICE,checkout-2)` | `request_hash=H_A2, order_id=A2, created_at=t_key_A2` | Same row. |

| Later operation | Before | After |
| --- | --- | --- |
| Repeat cancel → 200 | A2 CANCELLED, `updated_at=t_cancel_A2`. | A2 CANCELLED, `updated_at=t_cancel_A2`; no new update. |
| Replay `checkout-2` → 200 | A2 CANCELLED; two items and H_A2 binding exist. | Same data; response contains CANCELLED. |

## 4. Bob requests a regional quote Q1

Bob first requests an INR price offer:

```http
POST /order-quotes

{"region":"IN","items":[{"product_id":"BOOK","quantity":1},{"product_id":"PEN","quantity":2}]}
```

Within a transaction, the quote service reads `catalog_settings`, the two
`products`, and the active RATE1 from `fx_rates`. Region IN maps to INR through
the application's versioned mapping. It converts each unit price and then
multiplies by quantity.

| Item | Quantity | Source USD unit/line | Target INR unit/line |
| --- | ---: | --- | --- |
| BOOK | 1 | 1200 / 1200 | 2400 / 2400 |
| PEN | 2 | 300 / 600 | 600 / 1200 |
| Total | | **1800 = USD 18.00** | **3600 = INR 36.00** |

The service writes:

- One `order_quotes` row Q1: owner BOB, region IN, USD/INR currencies, mapping
  version, digit counts, RATE1 identity and validity snapshot, source total 1800,
  final total 3600, creation/expiry times and `consumed_order_id=NULL`.
- Two `quote_items` rows with both source and converted price snapshots.

HTTP **201** returns the quote. There is still no Bob order, order item or
idempotency row. A quote is an offer, not a placed order.

### Q1 creation: before and after

| Table and row key | Before | After |
| --- | --- | --- |
| `order_quotes(Q1)` | Absent | `customer_id=BOB, region=IN, source_currency=USD, currency=INR, source_total_minor=1800, total_minor=3600, consumed_order_id=NULL, created_at=t_Q1, expires_at=t_Q1_expiry` |
| `quote_items(Q1,0)` | Absent | `product_id=BOOK, quantity=1, source_unit_price_minor=1200, source_line_total_minor=1200, unit_price_minor=2400, line_total_minor=2400` |
| `quote_items(Q1,1)` | Absent | `product_id=PEN, quantity=2, source_unit_price_minor=300, source_line_total_minor=600, unit_price_minor=600, line_total_minor=1200` |
| `fx_rates(RATE1)` | USD → INR, rate 2 and its validity window. | Same row. |
| Bob's `orders`, `order_items`, `order_idempotency` rows | None. | None. |

Q1 also stores `mapping_version=markets-v1, base_digits=2, target_digits=2,
rate=2, rate_id=RATE1, rate_source=example-fixture` and the rate validity window.
Its expiry is the earlier of creation plus five minutes and `t_rate_end`.
Quote items also save the corresponding SKU/name. There are now **2 quote items
belonging to 1 quote**; Alice's 2 orders, 4 items and 2 bindings remain.

## 5. Bob accepts Q1 and creates B1

Before expiry, Bob sends:

```http
POST /orders
Idempotency-Key: checkout-1

{"quote_id":"Q1"}
```

Bob can use the same key text as Alice: the unique identity includes the customer.
The service performs these operations in one transaction:

1. Lock and look up `(BOB, checkout-1)` in `order_idempotency`.
2. Search `orders` for an existing order owned by Bob with `quote_id=Q1`.
3. With no existing order, lock Bob's Q1 in `order_quotes` using `FOR UPDATE`,
   and read its `quote_items`.
4. Check consumption and expiry, reading database time after acquiring the lock.
5. Insert B1 into `orders`: BOB, PENDING, INR, total 3600, pricing mode `quote`,
   quote ID Q1 and the saved pricing provenance.
6. Copy Q1's two item snapshots into two `order_items` rows for B1.
7. Update Q1's `consumed_order_id` to B1.
8. Insert `(BOB, checkout-1, request_hash, B1)` into `order_idempotency` and commit.

HTTP **201** returns B1. Product prices and FX rates are not fetched again to
reprice this checkout; it uses the accepted quote snapshots. Foreign-key checks
still validate referenced records when writes occur.

The order, its items, quote consumption and key binding succeed or roll back
together. The quote rows remain after successful checkout.

### Q1 consumption: before and after

| Table and row key | Before acceptance | After acceptance → 201 |
| --- | --- | --- |
| `order_quotes(Q1)` | `consumed_order_id=NULL, total_minor=3600, expires_at=t_Q1_expiry` | `consumed_order_id=B1, total_minor=3600, expires_at=t_Q1_expiry` |
| `quote_items(Q1,0)` | BOOK, quantity 1, source line 1200, target line 2400. | Same row. |
| `quote_items(Q1,1)` | PEN, quantity 2, source line 600, target line 1200. | Same row. |
| `orders(B1)` | Absent | `customer_id=BOB, status=PENDING, currency=INR, total_minor=3600, source_total_minor=1800, pricing_mode=quote, quote_id=Q1, created_at=t_B1, updated_at=t_B1` |
| `order_items(B1,0)` | Absent | `product_id=BOOK, quantity=1, source_unit_price_minor=1200, source_line_total_minor=1200, unit_price_minor=2400, line_total_minor=2400` |
| `order_items(B1,1)` | Absent | `product_id=PEN, quantity=2, source_unit_price_minor=300, source_line_total_minor=600, unit_price_minor=600, line_total_minor=1200` |
| `order_idempotency(BOB,checkout-1)` | Absent | `request_hash=H_Q1, order_id=B1, created_at=t_key_B1` |

B1 copies Q1's pricing provenance; its items copy the saved SKU/name as well.
Counts become **3 orders, 6 order items, 3 bindings, 1 quote and 2 quote items**.
On rollback, the new order/items/binding would be absent and Q1 would still have
`consumed_order_id=NULL`.

### A second key still refers to B1

Bob submits Q1 again using `checkout-alt`. The new key has no binding, but the
service finds B1 through its unique `orders.quote_id` and owner.

It returns **200**, inserts one additional idempotency binding
`(BOB, checkout-alt) → B1`, and creates no order or item rows. This is a concrete
reason the current design permits multiple keys per order in a separate table.

Two concurrent attempts to consume the same quote cannot create two committed
orders: the quote lock coordinates consumption and `orders.quote_id` is unique.

| Table and row key | Before second-key request | After second-key request → 200 |
| --- | --- | --- |
| `order_idempotency(BOB,checkout-alt)` | Absent | `request_hash=H_Q1, order_id=B1, created_at=t_key_B1_alt` |
| `order_idempotency(BOB,checkout-1)` | `request_hash=H_Q1, order_id=B1, created_at=t_key_B1` | Same row. |
| `orders(B1)` | PENDING, INR 3600, quote Q1. | Same row. |
| `order_items(B1,0)` and `(B1,1)` | The two purchase snapshots above. | Same rows. |
| `order_quotes(Q1)` | `consumed_order_id=B1` | `consumed_order_id=B1` |

Both keys have H_Q1 because the request fingerprint covers the quote payload,
not the header key. There are now **4 bindings but still only 3 orders**.

## 6. Chen retries without a key and gets C1 and C2

Chen buys PEN × 1 and MUG × 2 using the items-only endpoint, without an
`Idempotency-Key`.

| Item | Quantity | Unit price, USD minor units | Line total |
| --- | ---: | ---: | ---: |
| PEN | 1 | 300 | 300 |
| MUG | 2 | 1500 | 3000 |
| Total | | | **3300 = USD 33.00** |

The first request creates C1 and two items, returning **201**. Suppose the response
is lost and Chen submits the same request again. That creates C2 and two more
items, also returning **201**. Neither request creates an idempotency binding.

The service cannot distinguish an intended second purchase from this retry.
No quote identity is present either. This is the documented unkeyed-create gap.

Chen notices both orders in `GET /orders` and cancels C2 before processing.
C2 becomes CANCELLED with **200**; C1 remains PENDING. Both orders retain their
two item rows. If the worker had already processed C2, cancellation would return
409 and this implementation has no refund or fulfillment-reversal workflow.

### Chen's requests: before and after

| Operation and row key | Before | After |
| --- | --- | --- |
| First create: `orders(C1)` | Absent | `customer_id=CHEN, status=PENDING, currency=USD, total_minor=3300, pricing_mode=base, quote_id=NULL, created_at=t_C1, updated_at=t_C1` |
| First create: `order_items(C1,0)` | Absent | PEN, quantity 1, unit 300, line 300. |
| First create: `order_items(C1,1)` | Absent | MUG, quantity 2, unit 1500, line 3000. |
| Unkeyed retry: `orders(C2)` | Absent | `customer_id=CHEN, status=PENDING, currency=USD, total_minor=3300, pricing_mode=base, quote_id=NULL, created_at=t_C2, updated_at=t_C2` |
| Unkeyed retry: `order_items(C2,0)` | Absent | PEN, quantity 1, unit 300, line 300. |
| Unkeyed retry: `order_items(C2,1)` | Absent | MUG, quantity 2, unit 1500, line 3000. |
| Both creates: Chen's `order_idempotency` rows | None. | None. |
| Cancel C2: `orders(C2)` | `status=PENDING, updated_at=t_C2` | `status=CANCELLED, updated_at=t_cancel_C2` |
| Cancel C2: `order_items(C2,0)` and `(C2,1)` | PEN line 300; MUG line 3000. | Same two rows. |

The first request takes counts to 4 orders/8 items; its retry takes them to
**5 orders/10 items**. Cancellation changes no counts. All four Chen item rows
include catalog SKU/name snapshots and source amounts equal to final USD amounts.

## 7. Customers retrieve and list their orders

At this point:

| Caller/request | Result | Business tables read |
| --- | --- | --- |
| Alice: `GET /orders/A1` | 200; A1 plus two items. | `orders`, `order_items` |
| Alice: `GET /orders` | A1 and A2, with their items. | `orders`, `order_items` |
| Alice: `GET /orders?status=CANCELLED` | A2 only. | `orders`, `order_items` |
| Bob: `GET /orders` | B1 only. | `orders`, `order_items` |
| Chen: `GET /orders` | C1 and C2. | `orders`, `order_items` |
| Bob: `GET /orders/A1` | 404; Alice's order is outside Bob's scope. | Scoped lookup in `orders`; no item read after the miss. |
| Admin: `GET /orders` | All five orders, subject to pagination. | `orders`, `order_items` |

These operations do not write business rows. A nonempty list page loads its
orders and then bulk-loads their items; it does not issue one item query per order.
Historical details come from order snapshots, not current product or FX prices.

### Read operations: stored data before and after

For every GET listed above, including Bob's rejected request, this projection of
the order table stays identical:

| `orders.id` | Owner | Before: status / total | After: status / total |
| --- | --- | --- | --- |
| A1 | ALICE | PENDING / USD 3300 | PENDING / USD 3300 |
| A2 | ALICE | CANCELLED / USD 2700 | CANCELLED / USD 2700 |
| B1 | BOB | PENDING / INR 3600 | PENDING / INR 3600 |
| C1 | CHEN | PENDING / USD 3300 | PENDING / USD 3300 |
| C2 | CHEN | CANCELLED / USD 3300 | CANCELLED / USD 3300 |

The ten item rows, four bindings, one quote and two quote items also stay
identical, including their timestamps. Filtering changes the returned rows, not
what is stored. Authentication reads leave users and revocation data unchanged.

## 8. The worker advances three orders to PROCESSING

For one continuously running instance with the default interval, the first run
is five minutes after worker startup, followed by recurring five-minute ticks.
This is not a five-minute timer attached to each order.

Before the first tick, the order states are:

```text
A1 PENDING     A2 CANCELLED     B1 PENDING     C1 PENDING     C2 CANCELLED
```

The worker's processing statement uses only `orders`:

1. Select up to the configured batch size, default 500, with status PENDING and
   `created_at` no later than the run's cutoff.
2. Order candidates by `created_at, id` and lock them with
   `FOR UPDATE SKIP LOCKED`.
3. Conditionally update the selected PENDING rows to PROCESSING and set their
   `updated_at` using database time, in the same statement and transaction.

A1, B1 and C1 become PROCESSING. A2 and C2 stay CANCELLED. The worker does not
load or change `order_items`, quote prices or idempotency bindings. It continues
calling batches until none are returned or the run stops on error/cancellation.

The run's maintenance hook separately checks expired `denylisted_tokens` and
cleans a bounded batch of quotes whose expiry is at least 24 hours old. No rows
in this example are eligible yet.

### Worker batch: before and after

| `orders.id` | Before | After the processing transaction |
| --- | --- | --- |
| A1 | `status=PENDING, updated_at=t_A1` | `status=PROCESSING, updated_at=t_worker` |
| A2 | `status=CANCELLED, updated_at=t_cancel_A2` | `status=CANCELLED, updated_at=t_cancel_A2` |
| B1 | `status=PENDING, updated_at=t_B1` | `status=PROCESSING, updated_at=t_worker` |
| C1 | `status=PENDING, updated_at=t_C1` | `status=PROCESSING, updated_at=t_worker` |
| C2 | `status=CANCELLED, updated_at=t_cancel_C2` | `status=CANCELLED, updated_at=t_cancel_C2` |

All three selected orders fit in one batch here, so PostgreSQL `now()` supplies
the same transaction time `t_worker`. Owner, totals, currencies, `created_at` and
pricing snapshots stay unchanged. All other application rows stay unchanged;
the following empty processing batch and ineligible cleanup checks add no rows.

### What if cancellation races processing?

For a separate timing variation, suppose Chen cancels C2 at the same instant
the worker tries to process it. Both paths require its old status to be PENDING.

| Winning update | Result |
| --- | --- |
| Cancellation commits first | C2 is CANCELLED; the worker cannot overwrite it as PROCESSING. |
| Processing commits first | C2 is PROCESSING; cancellation returns 409. |

There is no extra lock table. PostgreSQL row locks and conditional updates enforce
this behavior. Multiple worker instances skip each other's locked candidates;
they still have independently phased schedules. This variation is not an extra
order or a change to the main scenario's final counts.

## 9. The admin records shipment and delivery

For each of A1, B1 and C1, the admin sends these requests in order:

```http
PATCH /orders/A1/status

{"status":"SHIPPED"}
```

```http
PATCH /orders/A1/status

{"status":"DELIVERED"}
```

Each successful request returns **200**. The domain supplies the expected prior
status; the repository conditionally updates `orders.status` and `updated_at`
within a transaction, then reads the order and items for the response.

The same sequence is applied to B1 and C1. No new order rows or item rows are
created. Recording SHIPPED does not call a carrier, and recording DELIVERED does
not capture payment. No status-history row is written because there is no history
table; structured logs describe mutations and the order retains its latest state.

Expected rejection examples:

- A customer attempting the admin status endpoint receives **403**.
- Cancelling a PROCESSING, SHIPPED or DELIVERED order receives **409**.
- Skipping PENDING directly to SHIPPED receives **409**.
- Repeating DELIVERED on an already-DELIVERED order receives **409**.

Alice can still replay `checkout-1`: it returns A1 with status DELIVERED and
HTTP **200**, without another order or status transition.

### Shipment and delivery: before and after each request

| Request / `orders` row | Before | After → 200 |
| --- | --- | --- |
| Ship A1 | `status=PROCESSING, updated_at=t_worker` | `status=SHIPPED, updated_at=t_ship_A1` |
| Deliver A1 | `status=SHIPPED, updated_at=t_ship_A1` | `status=DELIVERED, updated_at=t_deliver_A1` |
| Ship B1 | `status=PROCESSING, updated_at=t_worker` | `status=SHIPPED, updated_at=t_ship_B1` |
| Deliver B1 | `status=SHIPPED, updated_at=t_ship_B1` | `status=DELIVERED, updated_at=t_deliver_B1` |
| Ship C1 | `status=PROCESSING, updated_at=t_worker` | `status=SHIPPED, updated_at=t_ship_C1` |
| Deliver C1 | `status=SHIPPED, updated_at=t_ship_C1` | `status=DELIVERED, updated_at=t_deliver_C1` |

A2 and C2 stay CANCELLED with their cancellation timestamps. All ten item rows,
four bindings, Q1 and its two quote items remain unchanged. Creation times and
monetary values do not change during any of these six successful requests.

These additional checks illustrate rejected or replayed requests at the stated
status; they do not add operations that change the final state:

| Request | Before | After |
| --- | --- | --- |
| Customer tries to ship A1 → 403 | A1 PROCESSING, `updated_at=t_worker`. | Same row. |
| Cancel A1 after processing → 409 | A1 PROCESSING, `updated_at=t_worker`. | Same row. |
| Skip A1 from PENDING to SHIPPED → 409, if attempted before the worker | A1 PENDING, `updated_at=t_A1`. | Same row. |
| Cancel A1 after shipping → 409 | A1 SHIPPED, `updated_at=t_ship_A1`. | Same row. |
| Cancel A1 after delivery → 409 | A1 DELIVERED, `updated_at=t_deliver_A1`. | Same row. |
| Repeat DELIVERED on A1 → 409 | A1 DELIVERED, `updated_at=t_deliver_A1`. | Same row. |
| Replay Alice's `checkout-1` → 200 | A1 DELIVERED; binding H_A1 → A1. | Same rows; response uses current DELIVERED status. |

## 10. Bob leaves a second quote Q2 unused

Bob requests another INR quote containing PEN × 2 and MUG × 1. It stores:

| Item | Source USD line | Target INR line |
| --- | ---: | ---: |
| PEN × 2 | 600 | 1200 |
| MUG × 1 | 1500 | 3000 |
| Total | **2100** | **4200** |

This inserts Q2 into `order_quotes` and two rows into `quote_items`. Bob waits
until Q2 expires and then submits it with a new key, `expired-checkout`.

The create transaction finds no key binding or existing order, locks Q2, reads
its items and checks database time. Because it has expired, the request returns
**409** and the transaction rolls back. There is no B2 order, no new order items,
and no `expired-checkout` binding. Q2 remains unconsumed until cleanup.

Quote expiry itself does not delete a row or cancel any order. B1 remains
DELIVERED even when its previously consumed quote Q1 expires.

### Q2 creation, expiry and rejected checkout: before and after

| Table and row key | Before quote creation | After quote creation → 201 |
| --- | --- | --- |
| `order_quotes(Q2)` | Absent | `customer_id=BOB, region=IN, source_currency=USD, currency=INR, source_total_minor=2100, total_minor=4200, consumed_order_id=NULL, created_at=t_Q2, expires_at=t_Q2_expiry` |
| `quote_items(Q2,0)` | Absent | `product_id=PEN, quantity=2, source_unit_price_minor=300, source_line_total_minor=600, unit_price_minor=600, line_total_minor=1200` |
| `quote_items(Q2,1)` | Absent | `product_id=MUG, quantity=1, source_unit_price_minor=1500, source_line_total_minor=1500, unit_price_minor=3000, line_total_minor=3000` |

Q2 saves the same RATE1 provenance and digit/mapping fields as Q1; its item rows
save the corresponding SKU/name. Quotes now number **2**, with **4 quote items**.

| Operation and row key | Before | After |
| --- | --- | --- |
| Clock passes Q2's expiry: `order_quotes(Q2)` | `expires_at=t_Q2_expiry, consumed_order_id=NULL` | Same stored values; expiry is evaluated against current time, not by writing an EXPIRED status. |
| Expired checkout: `order_quotes(Q2)` | `consumed_order_id=NULL, total_minor=4200` | Same row; request returns 409. |
| Expired checkout: Q2's two `quote_items` | PEN line 1200; MUG line 3000. | Same rows. |
| Expired checkout: hypothetical `orders(B2)` and its items | Absent. | Absent. |
| Expired checkout: `order_idempotency(BOB,expired-checkout)` | Absent. | Absent. |

Orders remain **5**, order items **10**, and bindings **4** after the failed
transaction. No lasting row is inserted merely by acquiring a transaction lock.

## 11. Chen logs out

`POST /auth/logout` stores the hash and expiry of Chen's token in
`denylisted_tokens`. It does not delete CHEN from `users` or change C1/C2.
Later authenticated order requests using that revoked token receive **401**.
Logging in again can issue a new token.

### Logout and subsequent authentication: before and after

| Table and row key | Before logout | After logout |
| --- | --- | --- |
| `denylisted_tokens(TOKEN_HASH_CHEN)` | Absent | `token_hash=TOKEN_HASH_CHEN, expires_at=t_token_expiry` |
| `users(CHEN)` | `role=customer, active=true` | `role=customer, active=true` |
| `orders(C1)` | DELIVERED, USD 3300. | Same row. |
| `orders(C2)` | CANCELLED, USD 3300. | Same row. |

There is now **1 denylist row**. A subsequent GET using the revoked token returns
401 and leaves all these rows unchanged. A fresh login also leaves them unchanged:
it issues a new token rather than removing the old token's revocation record.

## 12. Final rows before maintenance cleanup

All five orders contain two item lines:

These final tables consolidate the row snapshots from the preceding operations.

| Order | Customer | Items | Currency | `total_minor` | Final status | Quote |
| --- | --- | --- | --- | ---: | --- | --- |
| A1 | Alice | BOOK × 2, PEN × 3 | USD | 3300 | DELIVERED | None |
| A2 | Alice | BOOK × 1, MUG × 1 | USD | 2700 | CANCELLED | None |
| B1 | Bob | BOOK × 1, PEN × 2 | INR | 3600 | DELIVERED | Q1 |
| C1 | Chen | PEN × 1, MUG × 2 | USD | 3300 | DELIVERED | None |
| C2 | Chen | PEN × 1, MUG × 2 | USD | 3300 | CANCELLED | None |

The complete set of `order_items` rows is shown below. Currency is obtained from
the parent order; it is not a column on `order_items`. Each row also retains its
saved product SKU/name.

| `order_id` | `position` | `product_id` | `quantity` | `source_unit_price_minor` | `source_line_total_minor` | `unit_price_minor` | `line_total_minor` |
| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: |
| A1 | 0 | BOOK | 2 | 1200 | 2400 | 1200 | 2400 |
| A1 | 1 | PEN | 3 | 300 | 900 | 300 | 900 |
| A2 | 0 | BOOK | 1 | 1200 | 1200 | 1200 | 1200 |
| A2 | 1 | MUG | 1 | 1500 | 1500 | 1500 | 1500 |
| B1 | 0 | BOOK | 1 | 1200 | 1200 | 2400 | 2400 |
| B1 | 1 | PEN | 2 | 300 | 600 | 600 | 1200 |
| C1 | 0 | PEN | 1 | 300 | 300 | 300 | 300 |
| C1 | 1 | MUG | 2 | 1500 | 3000 | 1500 | 3000 |
| C2 | 0 | PEN | 1 | 300 | 300 | 300 | 300 |
| C2 | 1 | MUG | 2 | 1500 | 3000 | 1500 | 3000 |

The idempotency table contains four rows:

| `customer_id` | `idempotency_key` | `request_hash` | `order_id` | `created_at` |
| --- | --- | --- | --- | --- |
| ALICE | `checkout-1` | H_A1 | A1 | t_key_A1 |
| ALICE | `checkout-2` | H_A2 | A2 | t_key_A2 |
| BOB | `checkout-1` | H_Q1 | B1 | t_key_B1 |
| BOB | `checkout-alt` | H_Q1 | B1 | t_key_B1_alt |

The retained quote headers and line values are:

| `order_quotes.id` | `customer_id` | `currency` | `source_total_minor` | `total_minor` | `consumed_order_id` | `expires_at` |
| --- | --- | --- | ---: | ---: | --- | --- |
| Q1 | BOB | INR | 1800 | 3600 | B1 | t_Q1_expiry |
| Q2 | BOB | INR | 2100 | 4200 | NULL | t_Q2_expiry |

| `quote_id` | `position` | `product_id` | `quantity` | `source_unit_price_minor` | `source_line_total_minor` | `unit_price_minor` | `line_total_minor` |
| --- | ---: | --- | ---: | ---: | ---: | ---: | ---: |
| Q1 | 0 | BOOK | 1 | 1200 | 1200 | 2400 | 2400 |
| Q1 | 1 | PEN | 2 | 300 | 600 | 600 | 1200 |
| Q2 | 0 | PEN | 2 | 300 | 600 | 600 | 1200 |
| Q2 | 1 | MUG | 1 | 1500 | 1500 | 3000 | 3000 |

| Table | Row count | What remains |
| --- | ---: | --- |
| `users` | 4 | Three customers and one admin. |
| `products` | 3 | Original catalog products; purchase quantities have not reduced inventory. |
| `catalog_settings` | 1 | USD catalog denomination. |
| `fx_rates` | 1 | RATE1; importing a rate does not consume it. |
| `orders` | 5 | Three DELIVERED and two CANCELLED orders. |
| `order_items` | 10 | Two immutable purchase lines per order, including cancelled orders. |
| `order_idempotency` | 4 | Bindings for A1, A2 and B1; two bindings point to B1. |
| `order_quotes` | 2 | Q1 consumed by B1; Q2 unconsumed and expired. |
| `quote_items` | 4 | Two price-offer lines per quote. |
| `denylisted_tokens` | 1 | Chen's revoked token, until its expiry permits cleanup. |

Counts assume no unrelated activity, no additional logout and no cleanup yet.
Migration tooling also maintains `schema_migrations`; it is infrastructure
metadata rather than one of these ten application tables.

### After eligible maintenance runs

Once each quote has been expired for at least 24 hours, a successful maintenance
pass can delete Q1 and Q2. Their four `quote_items` rows disappear through
`ON DELETE CASCADE`. B1 retains `quote_id=Q1` and its pricing/item snapshots:
`orders.quote_id` deliberately has no foreign key to the removable quote.

A later replay of Bob's Q1 purchase can still find B1 through `orders.quote_id`
and owner, even without an idempotency key. An expired, unconsumed Q2 submission
after Q2 has been deleted instead returns 404 because no quote or order exists.

After Chen's token expires, maintenance can delete its denylist row. Expiration
still makes the JWT unusable. No automatic purge removes the four idempotency
bindings. The five orders, ten order items, users, products, catalog settings and
FX rate remain. Quote and token cleanup do not delete purchase history.

### Cleanup: before and after

This snapshot is taken after enough time has passed for both quotes and Chen's
token to be eligible, and successful maintenance has removed them.

| Table and row key | Before eligible cleanup | After cleanup |
| --- | --- | --- |
| `order_quotes(Q1)` | Owner BOB, total 3600, consumed by B1. | Absent. |
| `order_quotes(Q2)` | Owner BOB, total 4200, `consumed_order_id=NULL`. | Absent. |
| `quote_items(Q1,0)` and `(Q1,1)` | BOOK line 2400; PEN line 1200. | Both absent through cascade. |
| `quote_items(Q2,0)` and `(Q2,1)` | PEN line 1200; MUG line 3000. | Both absent through cascade. |
| `denylisted_tokens(TOKEN_HASH_CHEN)` | Hash and `expires_at=t_token_expiry`. | Absent. |
| `orders(B1)` | DELIVERED, INR 3600, `quote_id=Q1`, saved RATE1 provenance. | Same row, including `quote_id=Q1`. |
| `order_items(B1,0)` and `(B1,1)` | BOOK line 2400; PEN line 1200. | Same purchase rows. |
| All four `order_idempotency` rows | The four bindings listed above. | Same rows and original creation times. |
| Remaining orders/items, users, products, settings and RATE1 | Final values listed above. | Same rows. |

| Request after cleanup, using a valid token | Before | After |
| --- | --- | --- |
| Bob submits Q1 without a key → 200 | Q1 absent, B1 present with `quote_id=Q1`. | Same data; returns B1, with no new binding. |
| Bob submits Q2 without a key → 404 | Q2 absent; no order references Q2. | Same data; no new order or quote. |

Final counts after cleanup are **5 orders, 10 order items, 4 bindings, 0 quotes,
0 quote items and 0 denylist rows**, plus the unchanged setup rows.

## Requirement coverage in this example

| Requirement | Example | Main tables |
| --- | --- | --- |
| Create with multiple items | All five orders contain two products. | `orders`, `order_items`; `products` supplies direct-order prices. |
| Retrieve details | Customers fetch their own order snapshots; another customer's request gets 404. | `orders`, `order_items`, with ownership from `users`. |
| Update status | Worker processes A1/B1/C1; admin records SHIPPED then DELIVERED. | Conditional updates to `orders`. |
| Automatic five-minute processing | Default periodic worker claims eligible PENDING rows in bounded locked batches. | `orders` and its pending index. |
| List/filter | Alice lists A1/A2 and filters for CANCELLED A2; admin can list all five. | `orders`, `order_items`. |
| Cancel only while pending | A2/C2 cancel successfully; repeat cancel succeeds; cancellation after processing fails. | `orders`; existing items remain. |
| Additional retry protection | Alice's matching retries, conflicting payload rejection and Bob's two keys. | `order_idempotency`, `orders`, `order_items`. |
| Additional regional pricing | Bob accepts Q1 but cannot consume expired Q2. | `catalog_settings`, `fx_rates`, `order_quotes`, `quote_items`. |
| Additional logout support | Chen's token is revoked without deleting his orders. | `denylisted_tokens`, `users`. |
