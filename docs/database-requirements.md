# Database tables, columns and requirement mapping

The project has **10 application tables**. `orders` and `order_items` implement
the main order requirements. The other tables support customer ownership,
catalog pricing, authentication, retry protection and regional pricing.

This reference follows the [database migrations](../db/migrations). See
[architecture.md](../architecture.md) for the ER diagram and design decisions,
and the [code walkthrough](code-map.md) for the methods using these tables.
For three customers placing multiple orders and the resulting table changes, see
the [order lifecycle example](order-lifecycle-walkthrough.md).

## Requirement key

| ID | Requirement |
| --- | --- |
| R1 | Create an order with multiple items. |
| R2 | Retrieve order details. |
| R3 | Update status, including automatic PENDING → PROCESSING. |
| R4 | List orders, optionally filtered by status. |
| R5 | Cancel an order while PENDING. |

Authentication, idempotency and regional pricing are additional features beyond
the assignment's five requirements.

## 1. orders — one row per order

This is the central table for all five requirements.

| Column | Significance | Mapping |
| --- | --- | --- |
| `id` | UUID identifying the order. Used in endpoint paths, item relationships and pagination cursors. | All |
| `customer_id` | References `users.id`; establishes ownership for customer reads, lists and cancellation. | R1, R2, R4, R5 |
| `status` | Stores PENDING, PROCESSING, SHIPPED, DELIVERED or CANCELLED. Defaults to PENDING. | All |
| `currency` | Currency in which the order's final amounts are expressed. | R1, R2 |
| `total_minor` | Final order total in integer minor units—for example, USD 34.95 is stored as `3495`. | R1, R2 |
| `created_at` | Creation time; also determines worker eligibility and processing order within a batch. | R1, R3 |
| `updated_at` | Time of the latest persisted change, including status transitions. It does not provide full status history. | R2, R3, R5 |
| `quote_id` | Optional, unique quote identifier. Prevents creating multiple orders from the same quote. | R1 extension |
| `pricing_mode` | Distinguishes `base`, `quote` and pre-extension `legacy` orders. | R1, R2 extension |

The remaining pricing columns are explained in the shared pricing table under
[order_quotes](#8-order_quotes--a-price-offer-before-order-placement).

The `status` CHECK constraint restricts allowed values. **Allowed transitions
are enforced by conditional SQL**, such as:

```sql
UPDATE orders
SET status = 'CANCELLED', updated_at = now()
WHERE id = ?
  AND customer_id = ?
  AND status = 'PENDING';
```

This prevents cancellation from overwriting an order already moved to PROCESSING.
The repository separately returns success for an already-CANCELLED order.

### Why a separate order record?

An order has one owner, total and current status regardless of how many products
it contains. Keeping those values in one row gives retrieval, cancellation and
the worker a single record to address and lock. Repeating the header on every
item would require coordinating several rows for a single status change.

### Example: one purchase moves through its lifecycle

Alice buys two notebooks at 1200 minor units each and three pens at 300 each.
The application creates one order with `total_minor=3300`, `currency=USD` and
`status=PENDING`. Its two item rows describe the products. The worker changes
only the order's status and modification time to record PROCESSING; admin
requests later record SHIPPED and DELIVERED. The order ID stays the same.

### Is this necessary for the assignment?

Persistent order identity and state are central to all five requirements. This
table is the implementation's relational representation of them. It stores the
latest status; a full transition audit would require additional history storage.

## 2. order_items — multiple purchase lines belonging to an order

Separating items from the order header provides the one-to-many relationship
required by R1.

| Column | Significance | Mapping |
| --- | --- | --- |
| `order_id` | Foreign key connecting each line to its order. | R1, R2, R4 |
| `position` | Preserves item ordering. Together with `order_id`, forms the primary key. | R1, R2 |
| `product_id` | References the purchased catalog product. | R1, R2 |
| `sku` | Snapshot of the product's SKU when purchased. | R1, R2 |
| `name` | Snapshot of the product's name when purchased. | R1, R2 |
| `quantity` | Number of units purchased; must be positive. | R1 |
| `unit_price_minor` | Final purchase price per unit in the order currency. | R1, R2 |
| `line_total_minor` | Final line amount; constrained to equal quantity × unit price. | R1, R2 |
| `source_unit_price_minor` | Original unit price in the base catalog currency. Nullable for legacy data. | Regional pricing extension |
| `source_line_total_minor` | Original line amount in the base currency, with its own arithmetic constraint. | Regional pricing extension |

Important constraints:

- `PRIMARY KEY (order_id, position)` identifies each line.
- `UNIQUE (order_id, product_id)` prevents duplicate product lines.
- Foreign keys prevent references to nonexistent orders or products.
- Price snapshots preserve historical purchases independently of the catalog.
- `ON DELETE CASCADE` removes items if their order is deleted. Cancellation
  changes status and does not delete rows.

The database checks each line's arithmetic. The application calculates the order
total; there is no cross-table constraint requiring `orders.total_minor` to equal
the sum of its items.

### Why a separate item table?

R1 allows a variable number of products in an order. Child rows represent that
relationship without fixed columns such as `product_1` and `product_2`. Each
line can have its own quantity, product reference and arithmetic constraints.
Names and prices are copied from the catalog so purchase history is stable.

### Example: two lines belong to one order

Alice's order has `(order_id=A1, position=0)` for Notebook × 2, line total 2400,
and `(order_id=A1, position=1)` for Pen × 3, line total 900. These are two item
rows, not five rows for the five individual units. Cancelling A1 leaves both
lines intact. If a future catalog-editing feature changes the notebook price,
these saved purchase prices would remain unchanged.

### Is this necessary for the assignment?

Multiple-item storage is required; this separate table is appropriate for the
chosen relational design. JSON embedded in an order is another possible design,
but would change how product references, uniqueness and line checks are enforced.
This implementation uses relational constraints and a bulk item read per page.

## 3. products — authoritative catalog information

Supports R1 by supplying server-controlled names, SKUs and prices.

| Column | Significance |
| --- | --- |
| `id` | Product identity submitted in an order item. |
| `sku` | Unique business identifier for the product. |
| `name` | Display name copied into purchase snapshots. |
| `price_minor` | Positive catalog unit price, preventing clients from choosing their purchase price. |
| `created_at` | Catalog creation time. |
| `updated_at` | Catalog modification timestamp; a product-editing API is not currently implemented. |

There is no currency column here because all catalog prices use the single
`catalog_settings.base_currency`. There is also no stock column or inventory
reservation.

### Why keep products separately from purchased items?

Products are reusable catalog entries; order items are individual purchase
snapshots. A central catalog gives the server an authoritative price instead of
trusting a customer-supplied amount. The same product can appear in many orders
without creating a new catalog record for each purchase.

### Example: two customers buy the same notebook

Alice submits the notebook's product ID with quantity 2; Bob submits that same
ID with quantity 1. Both requests read the catalog price of 1200, and their
orders receive separate item snapshots with line totals 2400 and 1200. The
product row is unchanged. Submitting a custom `unit_price_minor` in the order
request is rejected by the strict request binder.

### Is this necessary for the assignment?

The brief requires order items but does not prescribe a catalog service. This
table supports the selected server-priced catalog model. An external catalog
could supply product data instead, but order creation would still need a trusted
price source and saved purchase snapshots. This table does not reserve stock.

## 4. users — customer identity and permissions

Supports ownership across the requirements and separates customer actions from
admin status updates.

| Column | Significance | Mapping |
| --- | --- | --- |
| `id` | Referenced by orders, quotes and idempotency records. | R1, R2, R4, R5 |
| `name` | Account display name. | Supporting feature |
| `email` | Unique account/login identity. | Authentication |
| `password_hash` | Stores the password hash instead of the plaintext password. | Authentication |
| `role` | Restricted to `customer` or `admin`; application middleware enforces permitted actions. | R1–R5 authorization |
| `active` | Allows authentication logic to reject inactive accounts. | Authentication |
| `created_at` | Account creation timestamp. | Account metadata |
| `updated_at` | Latest account modification timestamp. | Account metadata |

A foreign key establishes ownership relationships; the application's scoped
queries enforce which user may access them.

### Why keep users separately from orders?

One account can own many orders. A shared user record avoids copying login
credentials and roles into every purchase, and lets authentication inspect the
account's current role and active state. Orders reference identity through
`customer_id`; authorization is applied by middleware and scoped queries.

### Example: Alice and Bob have different order access

Alice owns A1 and A2, both referencing the same `users.id`. Bob owns B1. Alice's
order list returns A1 and A2; her attempt to fetch B1 returns 404. The admin's
role permits status updates, while Alice receives 403 from that endpoint.
Logging Alice out does not delete her account or purchase history.

### Is this necessary for the assignment?

Customer association supports the brief. Local passwords, JWT authentication
and admin/customer roles are additional implementation choices. With an external
identity provider, credentials could live outside this database; orders would
still need a dependable customer identifier and ownership checks.

## 5. order_idempotency — durable protection against duplicate creation

This extends R1 so a client can safely retry a request after a timeout or lost
response.

| Column | Significance |
| --- | --- |
| `customer_id` | Namespaces the key by customer. Different customers can use the same key independently. |
| `idempotency_key` | Client-supplied retry identity, with length and character restrictions. |
| `request_hash` | Fingerprint of the parsed request; detects reuse of a key with different contents. |
| `order_id` | References the order to return on a matching retry. |
| `created_at` | Database-generated binding creation time; retained unchanged on replay and available for future retention cleanup. |

`PRIMARY KEY (customer_id, idempotency_key)` provides the uniqueness guarantee.
The order and key binding are committed in the same transaction. A transaction
advisory lock serializes concurrent creates using the same customer/key.

The separate table also permits multiple keys to reference one order, which can
occur when different keys submit the same already-consumed quote.

Current behavior:

- Same key and payload → existing order's current state, HTTP 200.
- Same key with different payload → HTTP 409.
- No key → repeated items-only requests can create separate orders.
- No automatic key purge is implemented.

### Why keep retry bindings separately from orders?

The binding describes a request identity, while the order describes a purchase.
Separate rows permit several keys to identify the same order and allow a future
retention policy to remove bindings without deleting purchase history. The
current implementation does not yet perform that cleanup.

### Example: a lost response and two keys for one quote

Alice places A1 using `checkout-1`, but loses the HTTP response. Retrying the
same payload with that key finds A1 instead of creating another order. Bob can
also use `checkout-1`: his customer ID makes it a different binding.

Bob then submits quote Q1 using `checkout-alt` after Q1 already created B1.
Quote replay finds B1, and the new key is bound to B1 too. Two idempotency rows
now reference one order. Reusing either key with a different payload returns
409 rather than creating or modifying an order.

### Is this necessary for the assignment?

Retry protection is an extension. For a simpler one-key-per-order contract,
nullable key/hash columns on `orders` with a customer/key uniqueness constraint
could work. The separate table fits the current multiple-binding behavior and
independent metadata lifecycle, at the cost of another lookup and transactional
coordination. It stores an order reference, not a frozen HTTP response.

## 6. catalog_settings — shared base currency

Supports consistent pricing for R1.

| Column | Significance |
| --- | --- |
| `singleton_id` | Primary key constrained to `1`, allowing at most one settings row. |
| `base_currency` | Defines the denomination of every product's `price_minor`. |
| `initialized_at` | Records when the catalog denomination was initialized. |

Persisting the base currency prevents a configuration change from silently
reinterpreting existing product prices. The initialization logic enforces the
immutable denomination.

### Why a table with one record?

The stored product amount has meaning only when its currency is known. This
table makes that currency part of the persisted catalog, shared by every
application instance and preserved across restarts and deployments.

`singleton_id` is an ordinary primary key with a restricted value:

```sql
singleton_id SMALLINT PRIMARY KEY CHECK (singleton_id = 1)
```

The CHECK permits only `1`; the primary key prevents another row with that
value. The table can be empty before initialization and contains exactly one
row after successful initialization. **No foreign key references
`catalog_settings.singleton_id`.** Products do not need a settings foreign key
because this implementation has one catalog denomination for all products.
The repository reads it using `WHERE singleton_id = 1`.

### Example: a deployment changes currency configuration

Suppose the persisted data is:

| Table | Relevant values |
| --- | --- |
| `catalog_settings` | `singleton_id=1`, `base_currency=USD` |
| `products` | Notebook, `price_minor=1200` |

The notebook costs **USD 12.00**. A later deployment accidentally sets
`CURRENCY=INR`. If the application relied only on that environment variable,
the unchanged amount could be incorrectly interpreted as **INR 12.00**.

The current startup logic reads the stored USD denomination, detects the
conflicting explicit currency setting and rejects startup. Initialization also
rejects attempts to replace an existing denomination with a different one.
Concurrent initialization is serialized using a database transaction and table
locks. These protections are implemented in
[bootstrap](../internal/bootstrap/server.go) and the
[pricing repository](../db/gorm/pricing_repository.go); the singleton CHECK
alone does not make `base_currency` immutable against direct SQL changes.

A customer choosing region IN follows a different operation: the service keeps
the USD catalog and creates an INR quote using a valid FX rate. With an
illustrative USD → INR rate of 2, the notebook's quote price is INR 24.00
(`2400` minor units). Neither the product's `1200` nor the stored base currency
changes. Once the catalog is initialized, changing `STORE_REGION` changes the
default region for new quotes, not the catalog denomination or historical orders.

### Is this necessary for the assignment?

The five order requirements do not specifically require a settings table. A
system permanently restricted to USD could use a fixed application setting.
For this implementation, which supports regional quotes while keeping one
immutable base currency, the table supplies a durable source of truth with
negligible storage cost. Its tradeoff is additional schema and initialization
logic. Supporting several independent catalogs would require a different model,
such as catalog records referenced by `products.catalog_id`.

## 7. fx_rates — managed currency conversion rates

Supports the regional pricing extension to R1.

| Column | Significance |
| --- | --- |
| `id` | Identifies the rate referenced by quotes and orders. |
| `base_currency` | Source currency for conversion. |
| `target_currency` | Destination currency; must differ from the source. |
| `rate` | Positive exact decimal conversion factor stored as `NUMERIC(24,12)`. |
| `valid_from` | Beginning of the rate's validity window. |
| `valid_until` | End of the validity window; must be later than `valid_from`. |
| `source` | Operator-supplied provenance, such as a provider or fixture identifier. |
| `created_at` | Records rate creation/import time. |

Rate-overlap prevention is handled by serialized repository operations, rather
than a database exclusion constraint.

### Why keep rates separately from quotes and catalog settings?

The base catalog currency is a stable setting, while a conversion rate has a
currency pair, validity window and source. One imported rate can price many
quotes. Keeping rate records separately permits later rates to be imported
without rewriting older quotes or orders, which also retain rate snapshots.

### Example: later quotes use a later rate

An illustrative USD → INR rate of 2 converts a USD 12.00 notebook into INR
24.00. Bob's quote records that rate and its source. After that validity window
ends, an imported rate of 3 would price a new quote at INR 36.00. An order
already created from the earlier quote keeps its INR 24.00 unit price. An
unconsumed quote cannot be accepted after its expiry, which is bounded by the
applied rate's validity window.

### Is this necessary for the assignment?

It is needed by the chosen managed-rate regional pricing extension, not by the
five core order operations. A single-currency service could omit it. An external
FX provider would be an alternative rate source, but accepted purchases would
still need immutable pricing provenance. These illustrative rates are examples,
not market data.

## 8. order_quotes — a price offer before order placement

A quote stores an owned, expiring price snapshot. It supports regional order
creation without changing existing catalog prices.

| Column | Significance |
| --- | --- |
| `id` | Quote identifier supplied to `POST /orders`. |
| `customer_id` | Restricts quote consumption to its owner. |
| `created_at` | Quote creation time. |
| `expires_at` | Deadline after which an unconsumed quote cannot create an order. |
| `consumed_order_id` | Optional unique foreign key recording the order created from this quote. |

The following pricing fields appear in both `order_quotes` and `orders`.
Persisting them on orders preserves purchase details after quote cleanup.
Together, this table and each table's own column list cover all their columns.

| Column | Significance |
| --- | --- |
| `region` | Region selected for pricing, such as `IN`. |
| `mapping_version` | Version of the region-to-currency mapping used. |
| `source_currency` | Base catalog currency. |
| `currency` | Final purchase/quote currency. |
| `base_digits` | Number of fractional digits used by the source currency. |
| `target_digits` | Number of fractional digits used by the destination currency. |
| `rate` | Conversion factor applied; identity conversion uses `1`. |
| `rate_id` | Optional reference to the managed FX rate. |
| `rate_source` | Provenance snapshot, including `identity` when no conversion is needed. |
| `rate_valid_from` | Snapshot of the applied rate's validity start. |
| `rate_valid_until` | Snapshot of the applied rate's validity end. |
| `source_total_minor` | Total expressed in base-currency minor units. |
| `total_minor` | Final total expressed in target-currency minor units. |

For example, JPY uses zero fractional digits while USD uses two. Storing the
digit counts explains how integer amounts were converted.

`orders.quote_id` is unique but deliberately has no foreign key to `order_quotes`,
allowing old quotes to be deleted while orders retain their quote identity.

### Why distinguish a quote from an order?

A customer may request a price and abandon it. Such an offer should expire
without appearing as a placed PENDING order for the worker to process. Quotes
therefore have their own ownership, expiry and consumption lifecycle. The
service copies an accepted quote into durable order snapshots.

### Example: an accepted offer and an abandoned offer

Bob requests Q1 for Notebook × 1 and Pen × 2. At the illustrative rate of 2,
the source total is USD 18.00 and the offer is INR 36.00. Accepting Q1 before
expiry creates B1 and sets `Q1.consumed_order_id=B1` in the same transaction.
Submitting Q1 again returns B1 rather than creating another order.

Bob lets a second quote Q2 expire. Trying to consume it while its expired row
still exists returns 409; no order is created. Later quote cleanup can delete
Q1 and Q2 without deleting B1 or changing its purchase snapshots.

### Is this necessary for the assignment?

The assignment does not require a price-offer step. Direct items-only creation
already works without a quote. This table supports the additional expiring
regional checkout contract. Eliminating quotes would simplify the schema but
would also remove that saved offer/acceptance behavior.

## 9. quote_items — item snapshots belonging to a quote

Supports multi-item regional pricing before conversion into an order.

| Column | Significance |
| --- | --- |
| `quote_id` | References the parent quote; deleting the quote cascades to its items. |
| `position` | Preserves line ordering; part of the composite primary key. |
| `product_id` | References the catalog product. |
| `sku`, `name` | Product identity snapshots. |
| `quantity` | Positive number of units. |
| `source_unit_price_minor` | Catalog unit price before conversion. |
| `source_line_total_minor` | Original unit price × quantity. |
| `unit_price_minor` | Converted unit price. |
| `line_total_minor` | Converted unit price × quantity. |

Composite uniqueness prevents duplicate products within a quote. CHECK
constraints enforce positive amounts and both line-total calculations.

### Why not use order_items for a quote?

A quote can exist without an order, while every `order_items` row requires an
`order_id`. Separate quote lines hold the offered quantities and prices until
acceptance. They can then be deleted with an old quote while the copied purchase
lines remain. Sharing one table would require different parent-reference rules
and more complex lifecycle constraints.

### Example: two quote lines become two purchase lines

Q1 contains Notebook × 1 with converted line total 2400 and Pen × 2 with line
total 1200, both in INR minor units. Initially there are two `quote_items` rows
and no order items for Q1. Accepting it creates B1 and two `order_items` rows
copied from those snapshots. The quote lines remain until quote cleanup; deleting
them later does not affect the purchase lines.

### Is this necessary for the assignment?

It is necessary for the selected relational, multi-item quote design, rather
than for the core order requirements. A service without quotes would omit both
quote tables. Embedding quote lines as JSON is possible, but would trade these
per-line relational constraints for a different validation/storage approach.

## 10. denylisted_tokens — logout support

This supports authentication; it does not directly implement an order requirement.

| Column | Significance |
| --- | --- |
| `token_hash` | Primary key identifying a revoked token without storing the raw JWT. |
| `expires_at` | Determines how long revocation matters and when the record can be purged. |

Authentication checks for an unexpired revocation. Worker maintenance removes
expired entries.

### Why store revocations separately from users?

A validly signed JWT remains valid until expiry unless authentication also
checks revocation state. Deleting it from one client does not invalidate another
copy. A per-token table lets the server reject a logged-out token without
deactivating the user or invalidating every other token belonging to that user.
Using PostgreSQL makes that revocation visible to all instances using this DB.

### Example: Chen logs out from one session

Chen has separate tokens from two logins. Logging out with token A inserts A's
hash and expiry. Subsequent protected requests with A receive 401. Token B is
unaffected as long as it remains valid and Chen's account is active. Chen's
orders remain unchanged. Once A expires, maintenance may remove the denylist
entry because expiry validation already rejects that token.

### Is this necessary for the assignment?

Immediate JWT logout is an authentication extension. Without revocation storage,
a simpler short-lived JWT design would normally accept a copied token until
expiry. Stateful sessions or an external identity service are alternatives.
This implementation uses the shared database for revocation, adding a lookup
to authenticated requests; it is not a table of every active login session.

## Why timestamps differ between tables

Timestamp columns should describe a lifecycle or support an actual operation.
Every table does not need both `created_at` and `updated_at`.

| Tables | Current timestamp design | Reason |
| --- | --- | --- |
| `users`, `products`, `orders` | `created_at`, `updated_at` | Record entity creation and latest modification. Product editing is not currently exposed. |
| `order_items` | No separate timestamps | Immutable purchase snapshots inserted with the parent order; the order supplies their creation context. |
| `quote_items` | No separate timestamps | Immutable snapshots inserted with the parent quote; the quote supplies creation and expiry context. |
| `catalog_settings` | `initialized_at` | Records initialization of the immutable catalog denomination. |
| `fx_rates` | `created_at`, `valid_from`, `valid_until` | Rates are imported as records with explicit validity windows; there is no rate-edit operation. |
| `order_quotes` | `created_at`, `expires_at` | Creation and expiry govern use and cleanup. Consumption is recorded by `consumed_order_id`, but its time is not currently stored. |
| `order_idempotency` | `created_at` | Records the original binding age; replays should not reset it. No purge job is implemented yet. |
| `denylisted_tokens` | `expires_at` | Sufficient for revocation checks and expired-record cleanup; revocation time is not currently stored. |

If lifecycle auditing becomes a requirement, `order_quotes.consumed_at` and
`denylisted_tokens.revoked_at` would describe the missing events more precisely
than a generic `updated_at`. Independently editable order lines would also need
their own modification tracking. Neither change is needed for the current
immutable item design.

An `updated_at` column records only the latest change. Full order transition
history would require a separate history table containing the previous status,
new status, actor and event time. Such a table is not currently implemented.

## Indexes and their requirement mapping

| Index/key | Purpose | Mapping |
| --- | --- | --- |
| `orders` primary key | Fetch a specific order and locate rows for transitions. | R2, R3, R5 |
| `orders_pending_idx (created_at, id) WHERE status='PENDING'` | Locate eligible pending rows in bounded worker batches. | R3 |
| `orders_customer_id_idx` | Customer-scoped cursor listing. | R4 |
| `orders_customer_status_id_idx` | Customer listing filtered by status. | R4 |
| `orders_status_id_idx` | Status-filtered listing across customers. | R4 |
| `order_items (order_id, position)` primary key | Retrieve an order's lines in their saved sequence. | R2, R4 |
| `order_idempotency` composite primary key | Resolve and uniquely bind a customer's retry key. | R1 extension |
| `fx_rates_pair_validity_idx` | Find applicable rates for a currency pair. | Regional pricing |
| `order_quotes_expiry_idx` | Find expired quotes for bounded cleanup. | Maintenance |
| `denylisted_tokens_expiry_idx` | Support expired revocation cleanup. | Maintenance |

Tables store state, constraints protect data integrity, transactions keep related
writes atomic, and conditional updates plus row locking protect concurrent
operations. The five-minute schedule itself lives in the Go worker; it is not
stored or implemented by a database table.
