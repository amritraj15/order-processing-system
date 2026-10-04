# Database tables, columns and requirement mapping

The project has **10 application tables**. `orders` and `order_items` implement
the main order requirements. The other tables support customer ownership,
catalog pricing, authentication, retry protection and regional pricing.

This reference follows the [database migrations](../db/migrations). See
[architecture.md](../architecture.md) for the ER diagram and design decisions,
and the [code walkthrough](code-map.md) for the methods using these tables.

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

## 10. denylisted_tokens — logout support

This supports authentication; it does not directly implement an order requirement.

| Column | Significance |
| --- | --- |
| `token_hash` | Primary key identifying a revoked token without storing the raw JWT. |
| `expires_at` | Determines how long revocation matters and when the record can be purged. |

Authentication checks for an unexpired revocation. Worker maintenance removes
expired entries.

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
