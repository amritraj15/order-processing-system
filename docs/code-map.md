# Project files and requirement mapping

This reference explains every source, test, migration, configuration, script and
documentation file in the project. It lists declared functions, receiver methods
and interface methods, then connects each file to the assignment. Start with the
request paths below; use the file reference when walking through the code.
Generated binaries, dependency caches, local secret files and Git internals are
not project implementation files and are excluded. Runtime files with no methods
are identified as types, contracts or configuration instead.

## Requirement legend

| ID | Assignment behavior |
| --- | --- |
| R1 | Create an order with multiple items. |
| R2 | Retrieve an order by ID. |
| R3a | Update an order through PENDING, PROCESSING, SHIPPED and DELIVERED. |
| R3b | Automatically move PENDING orders to PROCESSING every five minutes. |
| R4 | List orders, optionally filtered by status. |
| R5 | Cancel a PENDING order. Repeating an already-CANCELLED request succeeds without another state change. |

R3a and R3b split the assignment's third requirement. **All** means shared support
for the five requirements, not that the file independently implements each one.
Authentication, catalog administration, regional quotes, idempotency and operational
health are supporting features beyond the five core requirements.

## Paths through the implementation

All HTTP paths below pass through `routes.Mount`, authentication/role middleware
and the shared HTTP error handler. The worker path starts from bootstrap.

| Requirement | Main function and method path | Where correctness is enforced |
| --- | --- | --- |
| R1 items | `OrderHandler.Create` → `Service.HandlePlace` → `UnitOfWork.Do` → `createItems` → `order.New` → `OrderRepository.Insert` | Domain item/amount validation plus one transaction for order/items and an optional key binding. |
| R1 quote alternative | `OrderHandler.Create` → `HandlePlace` → `UnitOfWork.Do` → `createFromQuote` → `FindByQuote` / `GetForUpdate` → `Insert` + `MarkConsumed` | Owned quote lock, expiry check after waiting, unique quote identity and atomic writes. |
| R2 | `OrderHandler.Get` → `Service.HandleGet` → `OrderRepository.Get` | Customer-scoped lookup; foreign and missing orders return 404. |
| R3a | `OrderHandler.Status` → `Service.HandleStatus` → `order.PreviousStatus` → `OrderRepository.Transition` | Admin role check and SQL `WHERE id = ? AND status = ?`; zero-match invalid state returns 409. |
| R3b | `RunServer` → `Worker.Run` → `Worker.Drain` → `OrderRepository.ProcessBatch` → `PendingBatchSQL` | Bounded `FOR UPDATE SKIP LOCKED` selection and conditional update in one transaction; first tick follows the configured interval. |
| R4 | `OrderHandler.List` → `Service.HandleList` → `OrderRepository.List` | Ownership/status filters, cursor/limit validation and one bulk item query per nonempty page. |
| R5 | `OrderHandler.Cancel` → `Service.HandleCancel` → `OrderRepository.Transition` | Customer-scoped PENDING update; already CANCELLED returns 200, later fulfillment states 409. |

Domain entities do not acquire PostgreSQL locks. `PreviousStatus` defines the
allowed transition, while `Transition` enforces its expected old state atomically.
Order creation uses an explicit UoW; transitions and worker batches use explicit
repository transactions. Cancellation therefore cannot be overwritten by a worker
that expected PENDING. There are no `Order.Cancel` or `Order.UpdateStatus` methods
in the current domain model.

## How to read the method listings

`Type.Method` identifies a receiver method; a plain name is a package function.
Interface methods declare capabilities that adapters implement. Helpers such as
`TableName`, row converters and fake repository methods are included so the lists
match the source. Links jump to the declaration. Named nested Python functions
and shell recipe functions are described in the tooling entries.

## Startup and configuration

### [cmd/orders/main.go](../cmd/orders/main.go)

**Mapping:** All; operations.

Application entry point. `main` installs JSON logging and signal cancellation; `run` dispatches server, migration, admin, seed, catalog and rate commands. `pricingCommand` parses catalog/rate inputs; `safeCommandError` limits CLI error disclosure.

**Functions and methods:** [main](../cmd/orders/main.go#L28), [run](../cmd/orders/main.go#L39), [safeCommandError](../cmd/orders/main.go#L126), [pricingCommand](../cmd/orders/main.go#L132).

### [configs/config.go](../configs/config.go)

**Mapping:** All; R3b configuration.

`Load` reads and validates database, HTTP, JWT, currency, quote, limiter and worker settings. The processing interval defaults to five minutes and batch size to 500. `value` supplies environment defaults.

**Functions and methods:** [value](../configs/config.go#L21), [Load](../configs/config.go#L27).

### [internal/bootstrap/server.go](../internal/bootstrap/server.go)

**Mapping:** All; operations.

`RunServer` constructs repositories, services, HTTP routes, auth limits and the worker; starts HTTP/worker goroutines and coordinates shutdown. Its readiness callback checks migrations and catalog settings; maintenance purges expired tokens and eligible quotes. `Catalog` initializes or validates the immutable catalog denomination.

**Functions and methods:** [RunServer](../internal/bootstrap/server.go#L30), [Catalog](../internal/bootstrap/server.go#L115).

### [internal/clock/clock.go](../internal/clock/clock.go)

**Mapping:** R3b; supporting auth limits.

`Real.Now` supplies the real clock behind `ports.Clock`, allowing worker and limiter tests to substitute controlled time.

**Functions and methods:** [Real.Now](../internal/clock/clock.go#L10).

### [internal/logging/logging.go](../internal/logging/logging.go)

**Mapping:** All; observability.

`WithRequest` and `WithActor` add identifiers to context. `Handler.Handle` attaches them to structured records; `WithAttrs` and `WithGroup` preserve the wrapper. `Mutation` logs action, resource, outcome and status after successful application operations.

**Functions and methods:** [WithRequest](../internal/logging/logging.go#L15), [WithActor](../internal/logging/logging.go#L18), [Handler.Handle](../internal/logging/logging.go#L24), [Handler.WithAttrs](../internal/logging/logging.go#L33), [Handler.WithGroup](../internal/logging/logging.go#L34), [Mutation](../internal/logging/logging.go#L35).

## HTTP routes and handlers

### [api/rest/server.go](../api/rest/server.go)

**Mapping:** All; HTTP input and error handling.

`jsonBinder.Bind` enforces JSON content type, bounded bodies, known fields, one JSON value and validation tags. `NewServer` installs request IDs, safe request logging, recovery and error translation: malformed input 400, validation 422, conflict 409, missing resource 404 and retryable timeout 503.

**Functions and methods:** [jsonBinder.Bind](../api/rest/server.go#L28), [NewServer](../api/rest/server.go#L44).

### [api/rest/routes/routes.go](../api/rest/routes/routes.go)

**Mapping:** All; endpoint and role wiring.

`Mount` registers order, product, quote and auth endpoints with authentication/role middleware. Health reports liveness; readiness combines database and worker state. `optionalTime` represents absent worker observations as null.

**Functions and methods:** [Mount](../api/rest/routes/routes.go#L28), [optionalTime](../api/rest/routes/routes.go#L81).

### [api/rest/middleware/auth.go](../api/rest/middleware/auth.go)

**Mapping:** All; ownership and roles.

`Bearer` extracts the Authorization token; `RequireAuth` validates it and stores claims/actor context; `Claims` reads those claims. `RequireRole` restricts creation/cancellation to customers and status/product mutations to admins.

**Functions and methods:** [Bearer](../api/rest/middleware/auth.go#L16), [Claims](../api/rest/middleware/auth.go#L23), [RequireAuth](../api/rest/middleware/auth.go#L27), [RequireRole](../api/rest/middleware/auth.go#L47).

### [api/rest/middleware/rate_limit.go](../api/rest/middleware/rate_limit.go)

**Mapping:** Supporting authentication protection.

`NewAuthLimiter` creates bounded fixed-window state. `peer` normalizes the socket address; `admit` expires buckets and enforces quotas. `Middleware` also limits concurrent auth work; `rejectLimit` returns 429 with Retry-After. Limits are process-local.

**Functions and methods:** [NewAuthLimiter](../api/rest/middleware/rate_limit.go#L27), [peer](../api/rest/middleware/rate_limit.go#L30), [AuthLimiter.admit](../api/rest/middleware/rate_limit.go#L41), [rejectLimit](../api/rest/middleware/rate_limit.go#L72), [AuthLimiter.Middleware](../api/rest/middleware/rate_limit.go#L76).

### [api/rest/v1/order_handler.go](../api/rest/v1/order_handler.go)

**Mapping:** R1, R2, R3a, R4, R5; retry extension.

`Create` binds items or quote ID and the optional idempotency key; `Get`, `List`, `Status` and `Cancel` invoke their service use cases. `parseItemInputs`, `parseID` and `parsePagination` validate inputs; `customerScope` restricts customer reads; `orderView` builds response DTOs. Creation returns 201 and replay 200.

**Functions and methods:** [parseItemInputs](../api/rest/v1/order_handler.go#L24), [orderView](../api/rest/v1/order_handler.go#L65), [parseID](../api/rest/v1/order_handler.go#L78), [parsePagination](../api/rest/v1/order_handler.go#L85), [customerScope](../api/rest/v1/order_handler.go#L103), [OrderHandler.Create](../api/rest/v1/order_handler.go#L111), [OrderHandler.Get](../api/rest/v1/order_handler.go#L156), [OrderHandler.List](../api/rest/v1/order_handler.go#L167), [OrderHandler.Status](../api/rest/v1/order_handler.go#L186), [OrderHandler.Cancel](../api/rest/v1/order_handler.go#L202).

### [api/rest/v1/product_handler.go](../api/rest/v1/product_handler.go)

**Mapping:** Supporting catalog for R1.

`Create` exposes admin catalog creation; `Get` and `List` expose reads. `view` adds the persisted catalog currency to product responses. Products supply authoritative order prices.

**Functions and methods:** [ProductHandler.view](../api/rest/v1/product_handler.go#L33), [ProductHandler.Create](../api/rest/v1/product_handler.go#L36), [ProductHandler.Get](../api/rest/v1/product_handler.go#L49), [ProductHandler.List](../api/rest/v1/product_handler.go#L60).

### [api/rest/v1/quote_handler.go](../api/rest/v1/quote_handler.go)

**Mapping:** Supporting regional pricing for R1.

`Create` handles quote requests and returns quote metadata and item snapshots. `pricingView` renders source currency, conversion/rate provenance and optional quote identity for quote/order responses.

**Functions and methods:** [pricingView](../api/rest/v1/quote_handler.go#L30), [QuoteHandler.Create](../api/rest/v1/quote_handler.go#L48).

### [api/rest/v1/auth_handler.go](../api/rest/v1/auth_handler.go)

**Mapping:** Supporting authentication for all endpoints.

`Register`, `Login`, `Session` and `Logout` translate HTTP requests into authenticator calls and return safe session responses. Logout revokes the supplied token.

**Functions and methods:** [AuthHandler.Register](../api/rest/v1/auth_handler.go#L21), [AuthHandler.Login](../api/rest/v1/auth_handler.go#L32), [AuthHandler.Session](../api/rest/v1/auth_handler.go#L43), [AuthHandler.Logout](../api/rest/v1/auth_handler.go#L50).

### [api/validator/validator.go](../api/validator/validator.go)

**Mapping:** All; input validation.

`Struct` converts validation-library failures into a structured field-error map. `ValidationError.Error` identifies this class of error for the central HTTP handler.

**Functions and methods:** [ValidationError.Error](../api/validator/validator.go#L13), [Struct](../api/validator/validator.go#L21).

## Domain models and rules

### [domain/order/order.go](../domain/order/order.go)

**Mapping:** R1, R2, R3a, R4, R5; retry contract.

Defines orders, item snapshots, filters, statuses and repository contracts. `New` rejects invalid or duplicate products/quantities, snapshots catalog data, checks arithmetic overflow and creates a PENDING order with UUIDv7. `Status.Valid` validates statuses; `PreviousStatus` identifies the allowed predecessor for an admin transition. The repository interface describes storage operations; it does not execute SQL or provide locking itself.

**Functions and methods:** [Status.Valid](../domain/order/order.go#L27), [PreviousStatus](../domain/order/order.go#L34), [New](../domain/order/order.go#L68).

**Interface methods:** [Repository.Insert](../domain/order/order.go#L116), [Repository.Get](../domain/order/order.go#L117), [Repository.FindByQuote](../domain/order/order.go#L118), [Repository.List](../domain/order/order.go#L119), [Repository.Transition](../domain/order/order.go#L120), [Repository.LockAndFindIdempotency](../domain/order/order.go#L123), [Repository.InsertIdempotency](../domain/order/order.go#L124).

### [domain/money/money.go](../domain/money/money.go)

**Mapping:** R1 currency validation; regional pricing extension.

`Currency` maps supported regions to currency codes. `Digits` supplies minor-unit precision. `ParseRate` validates a positive decimal rate; `Convert` performs exact rational conversion, half-even rounding and range checks. No floating-point money calculations are used.

**Functions and methods:** [Currency](../domain/money/money.go#L16), [Digits](../domain/money/money.go#L23), [ParseRate](../domain/money/money.go#L30), [Convert](../domain/money/money.go#L40).

### [domain/user/user.go](../domain/user/user.go)

**Mapping:** Supporting customer/admin identity.

Defines User and roles. `HashPassword` validates the 8–72 byte bound and hashes with bcrypt; `User.Authenticate` checks the password and active flag. Repository methods declare user insertion and lookups.

**Functions and methods:** [HashPassword](../domain/user/user.go#L29), [User.Authenticate](../domain/user/user.go#L37).

**Interface methods:** [Repository.Insert](../domain/user/user.go#L43), [Repository.Get](../domain/user/user.go#L44), [Repository.GetByEmail](../domain/user/user.go#L45).

### [domain/product/product.go](../domain/product/product.go)

**Mapping:** Supporting catalog for R1.

Defines Product and the catalog repository interface. Insert/Get/GetMany/List declare persistence capabilities; the type itself has no product creation method or stock reservation behavior.

**Interface methods:** [Repository.Insert](../domain/product/product.go#L19), [Repository.Get](../domain/product/product.go#L20), [Repository.GetMany](../domain/product/product.go#L21), [Repository.List](../domain/product/product.go#L22).

### [domain/pricing/pricing.go](../domain/pricing/pricing.go)

**Mapping:** Supporting pricing for R1.

Defines persisted catalog settings, initialization inputs, rates and pricing snapshots. Repository contracts read/initialize settings, select a valid rate and import one. Monetary conversion is implemented in the money package.

**Interface methods:** [Repository.Settings](../domain/pricing/pricing.go#L30), [Repository.Initialize](../domain/pricing/pricing.go#L31), [Repository.ActiveRate](../domain/pricing/pricing.go#L32), [Repository.ImportRate](../domain/pricing/pricing.go#L33).

### [domain/quote/quote.go](../domain/quote/quote.go)

**Mapping:** Supporting quoted creation for R1.

Defines Quote with owner, items, expiry, pricing and consumed-order identity. Repository contracts insert quotes, lock an owned quote, mark it consumed and delete expired batches.

**Interface methods:** [Repository.Insert](../domain/quote/quote.go#L21), [Repository.GetForUpdate](../domain/quote/quote.go#L22), [Repository.MarkConsumed](../domain/quote/quote.go#L23), [Repository.DeleteExpiredBatch](../domain/quote/quote.go#L24).

### [domain/shared/shared.go](../domain/shared/shared.go)

**Mapping:** All; common errors and R4 pagination.

Defines shared error values used by services, repositories and HTTP translation, plus Pagination and generic Page response containers. Contains no functions.

**Declared types:** `Pagination`, `Page`.

## Ports and transaction contracts

### [ports/auth.go](../ports/auth.go)

**Mapping:** Supporting authentication for all endpoints.

Declares authenticator and token-denylist boundaries, authentication/session DTOs and auth error values. Interfaces permit the JWT adapter and test fakes to be used without coupling callers to JWT implementation details.

**Interface methods:** [Authenticator.Register](../ports/auth.go#L29), [Authenticator.Login](../ports/auth.go#L30), [Authenticator.ValidateToken](../ports/auth.go#L31), [Authenticator.Session](../ports/auth.go#L32), [Authenticator.Logout](../ports/auth.go#L33), [TokenDenylist.Add](../ports/auth.go#L36), [TokenDenylist.Exists](../ports/auth.go#L37).

### [ports/clock.go](../ports/clock.go)

**Mapping:** R3b; supporting auth limits.

Declares Clock.Now so scheduling/health/limiter logic can receive a clock implementation.

**Interface methods:** [Clock.Now](../ports/clock.go#L5).

### [ports/processing.go](../ports/processing.go)

**Mapping:** R3b.

Declares PendingProcessor.ProcessBatch with context, cutoff and limit. The worker depends on this interface; the PostgreSQL order repository implements it.

**Interface methods:** [PendingProcessor.ProcessBatch](../ports/processing.go#L9).

### [service/uow/uow.go](../service/uow/uow.go)

**Mapping:** R1; retry and quote atomicity.

Declares UnitOfWork.Do and transaction-scoped Repositories accessors for orders, products, pricing, quotes and database time. The GORM implementation supplies every accessor from the same transaction.

**Interface methods:** [Repositories.Orders](../service/uow/uow.go#L14), [Repositories.Products](../service/uow/uow.go#L15), [Repositories.Pricing](../service/uow/uow.go#L16), [Repositories.Quotes](../service/uow/uow.go#L17), [Repositories.Now](../service/uow/uow.go#L18), [UnitOfWork.Do](../service/uow/uow.go#L21).

## Application services

### [service/order/service.go](../service/order/service.go)

**Mapping:** R1, R2, R3a, R4, R5.

Holds the order Repository, UnitOfWork and Currency fields used when wiring the service. This file declares dependencies, not executable handlers; items creation reads authoritative currency through pricing settings inside its transaction.

**Declared types:** `Service`.

### [service/order/commands.go](../service/order/commands.go)

**Mapping:** R1, R3a, R5.

Defines CreateCommand, StatusCommand and CancelCommand input DTOs. Contains no executable methods.

**Declared types:** `CreateCommand`, `StatusCommand`, `CancelCommand`.

### [service/order/command_handler.go](../service/order/command_handler.go)

**Mapping:** R1, R3a, R5.

`HandleCreate` delegates to HandlePlace. `createItems` reads products, catalog settings and DB time, calls domain/order.New and inserts the order. `HandleStatus` derives the expected previous status and calls the conditional repository transition; `HandleCancel` requests PENDING to CANCELLED with customer scope.

**Functions and methods:** [Service.HandleCreate](../service/order/command_handler.go#L13), [createItems](../service/order/command_handler.go#L18), [Service.HandleStatus](../service/order/command_handler.go#L45), [Service.HandleCancel](../service/order/command_handler.go#L52).

### [service/order/query_handler.go](../service/order/query_handler.go)

**Mapping:** R2, R4.

`HandleGet` delegates the scoped ID lookup; `HandleList` delegates status/customer/cursor filtering. The repository owns the SQL and item loading.

**Functions and methods:** [Service.HandleGet](../service/order/query_handler.go#L12), [Service.HandleList](../service/order/query_handler.go#L15).

### [service/order/place_handler.go](../service/order/place_handler.go)

**Mapping:** R1; idempotency extension.

`ValidIdempotencyKey` validates key syntax; `requestHash` validates and fingerprints the decoded purchase input. `HandlePlace` applies the keyed request deadline, opens a UoW, obtains a key lock, returns replay or conflict, creates from items/quote and stores the key binding atomically.

**Functions and methods:** [ValidIdempotencyKey](../service/order/place_handler.go#L27), [requestHash](../service/order/place_handler.go#L39), [Service.HandlePlace](../service/order/place_handler.go#L76).

### [service/order/quote_handler.go](../service/order/quote_handler.go)

**Mapping:** R1; quoted creation extension.

`HandleCreateFromQuote` delegates to HandlePlace. `createFromQuote` first looks for an existing owned order, then locks the quote, handles consumed replay, checks expiry after the lock and inserts an order plus consumption marker within the caller transaction.

**Functions and methods:** [Service.HandleCreateFromQuote](../service/order/quote_handler.go#L14), [createFromQuote](../service/order/quote_handler.go#L18).

### [service/product/service.go](../service/product/service.go)

**Mapping:** Supporting catalog for R1.

Holds the product repository dependency. No methods are declared in this file.

**Declared types:** `Service`.

### [service/product/commands.go](../service/product/commands.go)

**Mapping:** Supporting catalog for R1.

Defines CreateCommand with SKU, name and minor-unit price. Contains no methods.

**Declared types:** `CreateCommand`.

### [service/product/command_handler.go](../service/product/command_handler.go)

**Mapping:** Supporting catalog for R1.

`HandleCreate` trims and validates product fields, generates UUIDv7 and persists a catalog product. Role checks belong to HTTP middleware.

**Functions and methods:** [Service.HandleCreate](../service/product/command_handler.go#L15).

### [service/product/query_handler.go](../service/product/query_handler.go)

**Mapping:** Supporting catalog for R1.

`HandleGet` retrieves a product; `HandleList` delegates cursor pagination to the product repository.

**Functions and methods:** [Service.HandleGet](../service/product/query_handler.go#L12), [Service.HandleList](../service/product/query_handler.go#L15).

### [service/quote/service.go](../service/quote/service.go)

**Mapping:** Supporting regional pricing for R1.

`HandleCreate` selects the requested/default region, loads catalog prices and the active rate in one UoW, validates items via order.New, converts each unit price, checks totals and stores quote/item snapshots with bounded expiry.

**Functions and methods:** [Service.HandleCreate](../service/quote/service.go#L27).

### [service/auth/jwt/client.go](../service/auth/jwt/client.go)

**Mapping:** Supporting authentication for all endpoints.

`NewClient` prepares JWT configuration and dummy password-check data. `Register` creates a customer; `Login` verifies credentials; `issueSession` signs a session. `parse` checks claims, `authenticatedUser` checks revocation and current user state, and `ValidateToken` returns claims. `Session` reports authentication, `Logout` stores revocation, `hashToken` hashes tokens and `info` creates safe user DTOs.

**Functions and methods:** [NewClient](../service/auth/jwt/client.go#L39), [Client.Register](../service/auth/jwt/client.go#L49), [Client.Login](../service/auth/jwt/client.go#L70), [Client.parse](../service/auth/jwt/client.go#L86), [hashToken](../service/auth/jwt/client.go#L97), [Client.authenticatedUser](../service/auth/jwt/client.go#L101), [Client.ValidateToken](../service/auth/jwt/client.go#L126), [info](../service/auth/jwt/client.go#L133), [Client.Session](../service/auth/jwt/client.go#L136), [Client.Logout](../service/auth/jwt/client.go#L146), [Client.issueSession](../service/auth/jwt/client.go#L156).

### [service/processing/worker.go](../service/processing/worker.go)

**Mapping:** R3b; race safety with R5.

`Run` waits for periodic ticks, starts one drain at a time per process, tracks health and invokes maintenance. `Drain` repeatedly calls ProcessBatch until empty/error/cancellation and logs count/outcome. `now` uses the injected clock. This is a loop over SQL batches, not individual orders.

**Functions and methods:** [Worker.Run](../service/processing/worker.go#L25), [Worker.Drain](../service/processing/worker.go#L54), [Worker.now](../service/processing/worker.go#L82).

### [service/processing/status.go](../service/processing/status.go)

**Mapping:** R3b; operational health.

`NewStatus` initializes synchronized worker observations. `Start`, `Finish` and `Stop` update lifecycle state; `Snapshot` derives readiness state from startup grace, failures, stale success and run duration.

**Functions and methods:** [NewStatus](../service/processing/status.go#L21), [Status.Start](../service/processing/status.go#L24), [Status.Finish](../service/processing/status.go#L30), [Status.Stop](../service/processing/status.go#L41), [Status.Snapshot](../service/processing/status.go#L47).

## Database adapters and embedded assets

### [db/gorm/db.go](../db/gorm/db.go)

**Mapping:** All; database operations.

`Open` configures GORM/PostgreSQL, connection limits and a connection check. `Migrate` runs embedded migrations up or one version down. `mapError` translates storage failures into shared errors for HTTP/service handling.

**Functions and methods:** [Open](../db/gorm/db.go#L20), [Migrate](../db/gorm/db.go#L40), [mapError](../db/gorm/db.go#L67).

### [db/gorm/order_repository.go](../db/gorm/order_repository.go)

**Mapping:** All; transaction and concurrency enforcement.

`Insert` stores order/items; `Get` loads an owned order plus snapshots; `List` applies filters/cursors and bulk-loads items. `Transition` performs an expected-status conditional update in a transaction and permits already-CANCELLED retries. `ProcessBatch` executes PendingBatchSQL using LIMIT and FOR UPDATE SKIP LOCKED. `FindByQuote` supports replay. `UnitOfWork.Do` uses READ COMMITTED; repository accessors share its transaction, and `Now` reads DB time. Row converters, table names and `scoped` support these operations.

**Functions and methods:** [orderRow.TableName](../db/gorm/order_repository.go#L32), [itemRow.TableName](../db/gorm/order_repository.go#L43), [orderFrom](../db/gorm/order_repository.go#L44), [itemFrom](../db/gorm/order_repository.go#L47), [OrderRepository.Insert](../db/gorm/order_repository.go#L53), [scoped](../db/gorm/order_repository.go#L67), [OrderRepository.Get](../db/gorm/order_repository.go#L73), [OrderRepository.List](../db/gorm/order_repository.go#L89), [OrderRepository.Transition](../db/gorm/order_repository.go#L123), [OrderRepository.ProcessBatch](../db/gorm/order_repository.go#L162), [repositories.Orders](../db/gorm/order_repository.go#L174), [repositories.Products](../db/gorm/order_repository.go#L175), [UnitOfWork.Do](../db/gorm/order_repository.go#L176), [OrderRepository.FindByQuote](../db/gorm/order_repository.go#L187), [repositories.Pricing](../db/gorm/order_repository.go#L194), [repositories.Quotes](../db/gorm/order_repository.go#L195), [repositories.Now](../db/gorm/order_repository.go#L196).

### [db/gorm/product_repository.go](../db/gorm/product_repository.go)

**Mapping:** Supporting R1 catalog; R4 shared paging helper.

`Insert`, `Get`, `GetMany` and `List` implement catalog persistence and bulk lookup. `pageQuery` applies cursor/limit ordering and is also used by order listing. `productFrom` maps database rows into domain products; TableName fixes the table mapping.

**Functions and methods:** [productRow.TableName](../db/gorm/product_repository.go#L21), [productFrom](../db/gorm/product_repository.go#L22), [ProductRepository.Insert](../db/gorm/product_repository.go#L28), [ProductRepository.Get](../db/gorm/product_repository.go#L34), [ProductRepository.GetMany](../db/gorm/product_repository.go#L40), [pageQuery](../db/gorm/product_repository.go#L49), [ProductRepository.List](../db/gorm/product_repository.go#L55).

### [db/gorm/user_repository.go](../db/gorm/user_repository.go)

**Mapping:** Supporting authentication for all endpoints.

UserRepository inserts and finds users by ID/email; `userFrom` maps stored rows. TokenDenylist adds hashed revocations, checks unexpired revocations and purges expired entries. TableName methods map both tables.

**Functions and methods:** [userRow.TableName](../db/gorm/user_repository.go#L23), [userFrom](../db/gorm/user_repository.go#L24), [UserRepository.Insert](../db/gorm/user_repository.go#L30), [UserRepository.Get](../db/gorm/user_repository.go#L33), [UserRepository.GetByEmail](../db/gorm/user_repository.go#L38), [tokenRow.TableName](../db/gorm/user_repository.go#L49), [TokenDenylist.Add](../db/gorm/user_repository.go#L53), [TokenDenylist.Exists](../db/gorm/user_repository.go#L56), [TokenDenylist.Purge](../db/gorm/user_repository.go#L61).

### [db/gorm/pricing_repository.go](../db/gorm/pricing_repository.go)

**Mapping:** Supporting pricing for R1.

`Settings` reads catalog denomination; `Initialize` serializes initialization/adoption and rejects conflicting data. `ActiveRate` selects a valid currency-pair rate; `ImportRate` serializes validated rate imports and overlap checks. `wrap` adds operation context while retaining translated error identity; TableName methods specify storage tables.

**Functions and methods:** [settingsRow.TableName](../db/gorm/pricing_repository.go#L23), [rateRow.TableName](../db/gorm/pricing_repository.go#L31), [PricingRepository.Settings](../db/gorm/pricing_repository.go#L37), [PricingRepository.Initialize](../db/gorm/pricing_repository.go#L44), [PricingRepository.ActiveRate](../db/gorm/pricing_repository.go#L91), [PricingRepository.ImportRate](../db/gorm/pricing_repository.go#L102), [wrap](../db/gorm/pricing_repository.go#L137).

### [db/gorm/quote_repository.go](../db/gorm/quote_repository.go)

**Mapping:** Supporting R1 quotes; R3b maintenance.

`Insert` persists quote/items through the supplied transaction. `GetForUpdate` locks an owned quote; `MarkConsumed` conditionally sets the order reference. `DeleteExpiredBatch` performs bounded locked cleanup. `pricingTo` and `pricingFrom` convert pricing snapshots; TableName methods map quote tables.

**Functions and methods:** [pricingTo](../db/gorm/quote_repository.go#L23), [pricingFrom](../db/gorm/quote_repository.go#L29), [quoteRow.TableName](../db/gorm/quote_repository.go#L70), [quoteItemRow.TableName](../db/gorm/quote_repository.go#L80), [QuoteRepository.Insert](../db/gorm/quote_repository.go#L86), [QuoteRepository.GetForUpdate](../db/gorm/quote_repository.go#L97), [QuoteRepository.MarkConsumed](../db/gorm/quote_repository.go#L112), [QuoteRepository.DeleteExpiredBatch](../db/gorm/quote_repository.go#L125).

### [db/gorm/idempotency_repository.go](../db/gorm/idempotency_repository.go)

**Mapping:** R1; durable retry protection.

`LockAndFindIdempotency` installs transaction-local lock/statement limits, acquires the customer/key advisory lock and finds a stored binding. `InsertIdempotency` persists the request hash/order mapping; the database generates created_at. TableName selects order_idempotency.

**Functions and methods:** [idempotencyRow.TableName](../db/gorm/idempotency_repository.go#L22), [OrderRepository.LockAndFindIdempotency](../db/gorm/idempotency_repository.go#L24), [OrderRepository.InsertIdempotency](../db/gorm/idempotency_repository.go#L45).

### [db/migrations/embed.go](../db/migrations/embed.go)

**Mapping:** All; schema deployment.

Embeds the SQL migration files into the binary through embed.FS. Contains no functions; database.Migrate consumes this filesystem.

### [docs/embed.go](../docs/embed.go)

**Mapping:** All; API discoverability.

Embeds openapi.yaml. `Mount` serves the specification and Swagger UI endpoints; Swagger UI assets are loaded from an external CDN.

**Functions and methods:** [Mount](../docs/embed.go#L14).

## Tests and test support

Files ending in `_test.go` are compiled by `go test` and excluded from the application binary. Integration files require `-tags=integration` and PostgreSQL; the backlog comparison additionally requires `BACKLOG_ROWS`. Test/fake methods listed here exercise behavior and do not serve production requests.

### [internal/testutil/postgres.go](../internal/testutil/postgres.go)

**Mapping:** All; integration test infrastructure.

`PostgreSQL` requires TEST_DATABASE_URL, creates an isolated schema, applies migrations, initializes USD catalog settings and registers cleanup. It does not create or destroy the caller database. Compiled only with the integration tag.

**Functions and methods:** [PostgreSQL](../internal/testutil/postgres.go#L21).

### [api/rest/middleware/rate_limit_test.go](../api/rest/middleware/rate_limit_test.go)

**Mapping:** Auth extension.

Checks bounded limiter storage, concurrent admission, forwarded-header handling and release of in-flight slots; testClock controls time.

**Functions and methods:** [testClock.Now](../api/rest/middleware/rate_limit_test.go#L15), [TestLimiterBoundsAndConcurrency](../api/rest/middleware/rate_limit_test.go#L16), [TestLimiterIgnoresForwardedHeadersAndReleasesSlot](../api/rest/middleware/rate_limit_test.go#L48).

### [api/rest/routes/readiness_test.go](../api/rest/routes/readiness_test.go)

**Mapping:** R3b health.

Checks that worker failure makes readiness unavailable even when the database check succeeds.

**Functions and methods:** [TestReadinessReportsWorkerFailureWithHealthyDatabase](../api/rest/routes/readiness_test.go#L17).

### [api/rest/routes/routes_integration_test.go](../api/rest/routes/routes_integration_test.go)

**Mapping:** All; auth, pricing and retry extensions.

Exercises the HTTP/PostgreSQL lifecycle: roles, ownership, products, orders, status changes, cancellation/retry, list/filter/pagination, quotes, idempotency replay/conflict and logout.

**Functions and methods:** [TestCustomerAndAdminOrderFlow](../api/rest/routes/routes_integration_test.go#L30).

### [api/rest/server_test.go](../api/rest/server_test.go)

**Mapping:** All; HTTP boundary.

Checks strict JSON binding, field validation and safe error envelopes, retryable timeout responses and request-log redaction.

**Functions and methods:** [TestJSONBinderAndErrorEnvelope](../api/rest/server_test.go#L17), [TestRetryableTimeoutResponse](../api/rest/server_test.go#L55), [TestRequestLogsDoNotExposeBodyOrErrors](../api/rest/server_test.go#L73).

### [api/rest/v1/order_handler_test.go](../api/rest/v1/order_handler_test.go)

**Mapping:** R1; retry/input validation.

Checks malformed/missing/nil UUID rejection with normal and decoding-only binders, plus invalid idempotency headers before service work.

**Functions and methods:** [decodingOnlyBinder.Bind](../api/rest/v1/order_handler_test.go#L23), [TestCreateRejectsInvalidUUIDs](../api/rest/v1/order_handler_test.go#L27), [TestCreateRejectsInvalidIdempotencyHeaders](../api/rest/v1/order_handler_test.go#L71).

### [configs/config_test.go](../configs/config_test.go)

**Mapping:** All; R3b defaults.

Checks configuration defaults and invalid currency, token TTL, interval, batch size, region, quote TTL and auth-limit settings.

**Functions and methods:** [TestLoadDefaultsAndValidation](../configs/config_test.go#L5), [TestHardeningConfiguration](../configs/config_test.go#L27).

### [db/gorm/idempotency_integration_test.go](../db/gorm/idempotency_integration_test.go)

**Mapping:** R1; retry extension.

Checks concurrent identical/conflicting keyed requests, lock/statement timeout recovery and rollback of order/items/quote writes when key persistence fails. countRows supports database assertions.

**Functions and methods:** [countRows](../db/gorm/idempotency_integration_test.go#L24), [TestIdempotentCreateConcurrentRequests](../db/gorm/idempotency_integration_test.go#L32), [TestIdempotencyLockTimeoutAndRecovery](../db/gorm/idempotency_integration_test.go#L122), [TestIdempotencyStatementTimeoutRollsBack](../db/gorm/idempotency_integration_test.go#L158), [TestIdempotencyFailureRollsBackOrderAndQuote](../db/gorm/idempotency_integration_test.go#L184), [TestConcurrentIdempotencyPayloadConflict](../db/gorm/idempotency_integration_test.go#L241).

### [db/gorm/idempotency_migrations_integration_test.go](../db/gorm/idempotency_migrations_integration_test.go)

**Mapping:** R1 retry schema compatibility.

Tests upgrades from both forms of version 4 and refusal to drop a nonempty idempotency table.

**Functions and methods:** [TestIdempotencyMigrationUpgradeAndNonemptyDowngradeGuard](../db/gorm/idempotency_migrations_integration_test.go#L19).

### [db/gorm/migrations_integration_test.go](../db/gorm/migrations_integration_test.go)

**Mapping:** All schema; quote maintenance.

Tests pricing migration round trips and legacy preservation, plus expiry-index use during quote cleanup.

**Functions and methods:** [TestPricingMigrationsRoundTripAndLegacyPreservation](../db/gorm/migrations_integration_test.go#L17), [TestQuoteCleanupUsesExpiryIndex](../db/gorm/migrations_integration_test.go#L61).

### [db/gorm/order_repository_integration_test.go](../db/gorm/order_repository_integration_test.go)

**Mapping:** All; especially R3b/R5 concurrency.

Checks atomic snapshots/status, database-clock transitions, concurrent bounded batches and cutoff, query plans, cancellation/processing races, skipped locks and failed-batch rollback. fixtures creates isolated customer/product data.

**Functions and methods:** [fixtures](../db/gorm/order_repository_integration_test.go#L26), [TestOrderTransactionSnapshotsAndStatus](../db/gorm/order_repository_integration_test.go#L40), [TestOrderTransitionUsesDatabaseTime](../db/gorm/order_repository_integration_test.go#L97), [TestPendingBatchesConcurrencyCutoffAndQueryPlan](../db/gorm/order_repository_integration_test.go#L126), [TestCancellationAndProcessingRace](../db/gorm/order_repository_integration_test.go#L219), [TestPendingSkipsLockedRowsAndRollsBackFailedBatch](../db/gorm/order_repository_integration_test.go#L260).

### [db/gorm/pending_backlog_plan_integration_test.go](../db/gorm/pending_backlog_plan_integration_test.go)

**Mapping:** R3b; opt-in plan investigation.

Compares production batch SQL with a test-only primary-key-array variant. Helpers seed isolated schemas, explain rolled-back batches, summarize plan features and time committed drains. Final counts and batch bounds are checked; plan shape and latency are reported rather than asserted. Skips unless BACKLOG_ROWS is positive.

**Functions and methods:** [backlogEnvInt](../db/gorm/pending_backlog_plan_integration_test.go#L57), [backlogMS](../db/gorm/pending_backlog_plan_integration_test.go#L70), [backlogMean](../db/gorm/pending_backlog_plan_integration_test.go#L74), [backlogExplain](../db/gorm/pending_backlog_plan_integration_test.go#L87), [backlogShape](../db/gorm/pending_backlog_plan_integration_test.go#L118), [backlogDrain](../db/gorm/pending_backlog_plan_integration_test.go#L139), [TestPendingBacklogBatchPlanComparison](../db/gorm/pending_backlog_plan_integration_test.go#L262).

### [db/gorm/pricing_integration_test.go](../db/gorm/pricing_integration_test.go)

**Mapping:** Pricing extension for R1.

Checks concurrent quote consumption, persisted snapshots/replay, serialized initialization/import, explicit legacy adoption, expiry after waiting for a lock and consumption rollback.

**Functions and methods:** [TestQuoteConcurrencyAndPersistentSnapshots](../db/gorm/pricing_integration_test.go#L21), [TestInitializationAndRateImportsAreSerialized](../db/gorm/pricing_integration_test.go#L97), [TestLegacyCatalogRequiresExplicitAdoption](../db/gorm/pricing_integration_test.go#L146), [TestQuoteExpiryCheckedAfterLockWait](../db/gorm/pricing_integration_test.go#L161), [TestQuoteConsumptionFailureRollsBackOrder](../db/gorm/pricing_integration_test.go#L207).

### [domain/money/money_test.go](../domain/money/money_test.go)

**Mapping:** Pricing extension for R1.

Checks exact conversion, currency precision, half-even rounding and invalid/range cases without a database.

**Functions and methods:** [TestConversion](../domain/money/money_test.go#L10).

### [domain/order/order_test.go](../domain/order/order_test.go)

**Mapping:** R1, R3a; status vocabulary used by R4/R5.

Checks catalog snapshots/totals, input rejection and the permitted predecessor mapping. SQL concurrency is covered in repository integration tests.

**Functions and methods:** [TestNewSnapshotsCatalogPricesAndTotals](../domain/order/order_test.go#L15), [TestNewRejectsInvalidOrders](../domain/order/order_test.go#L32), [TestStatusTransitions](../domain/order/order_test.go#L52).

### [domain/user/user_test.go](../domain/user/user_test.go)

**Mapping:** Auth extension.

Checks password length/byte constraints and active-user authentication behavior.

**Functions and methods:** [TestPasswordLimitsAndActiveUser](../domain/user/user_test.go#L11).

### [internal/logging/logging_test.go](../internal/logging/logging_test.go)

**Mapping:** All; observability.

Checks that request and actor context survive adding logger attributes.

**Functions and methods:** [TestContextSurvivesLoggerAttributes](../internal/logging/logging_test.go#L11).

### [service/auth/jwt/client_test.go](../service/auth/jwt/client_test.go)

**Mapping:** Auth extension for all endpoints.

Uses fake users and denylist to exercise registration, login, session, logout, invalid tokens, current roles, missing accounts and inactive accounts.

**Functions and methods:** [usersFake.Insert](../service/auth/jwt/client_test.go#L20), [usersFake.Get](../service/auth/jwt/client_test.go#L29), [usersFake.GetByEmail](../service/auth/jwt/client_test.go#L35), [denylistFake.Add](../service/auth/jwt/client_test.go#L49), [denylistFake.Exists](../service/auth/jwt/client_test.go#L53), [newFixture](../service/auth/jwt/client_test.go#L60), [TestRegisterLoginSessionLogout](../service/auth/jwt/client_test.go#L70), [TestRejectsInvalidJWTAndChecksCurrentRole](../service/auth/jwt/client_test.go#L116), [TestLoginVerifiesMissingAndInactiveAccounts](../service/auth/jwt/client_test.go#L163).

### [service/order/place_handler_test.go](../service/order/place_handler_test.go)

**Mapping:** R1; retry extension.

Uses fake transaction/repository implementations to check replay, conflict, ownership, unkeyed creation, validation before transactions, fingerprint stability and keyed deadlines.

**Functions and methods:** [testOrders.Insert](../service/order/place_handler_test.go#L25), [testOrders.Get](../service/order/place_handler_test.go#L29), [testOrders.LockAndFindIdempotency](../service/order/place_handler_test.go#L36), [testOrders.InsertIdempotency](../service/order/place_handler_test.go#L43), [testProducts.GetMany](../service/order/place_handler_test.go#L54), [testPricing.Settings](../service/order/place_handler_test.go#L61), [testUnit.Do](../service/order/place_handler_test.go#L71), [testUnit.Orders](../service/order/place_handler_test.go#L72), [testUnit.Products](../service/order/place_handler_test.go#L73), [testUnit.Pricing](../service/order/place_handler_test.go#L74), [testUnit.Now](../service/order/place_handler_test.go#L75), [TestPlaceReplayConflictOwnershipAndUnkeyed](../service/order/place_handler_test.go#L77), [TestPlaceValidationBeforeTransaction](../service/order/place_handler_test.go#L115), [TestRequestFingerprintContract](../service/order/place_handler_test.go#L139), [unitFunc.Do](../service/order/place_handler_test.go#L160), [TestKeyedCreateDeadline](../service/order/place_handler_test.go#L164).

### [service/processing/status_test.go](../service/processing/status_test.go)

**Mapping:** R3b health.

Checks health-state boundaries, failure/recovery and concurrent observations.

**Functions and methods:** [TestStatusBoundariesFailureRecovery](../service/processing/status_test.go#L10), [TestConcurrentSnapshots](../service/processing/status_test.go#L37).

### [service/processing/worker_test.go](../service/processing/worker_test.go)

**Mapping:** R3b.

Fake processors exercise full/short/empty batches, failures, cancellation, waiting for the first tick, recurring real short-interval ticks and shutdown.

**Functions and methods:** [fakeProcessor.ProcessBatch](../service/processing/worker_test.go#L21), [quiet](../service/processing/worker_test.go#L39), [TestDrainProcessesFullAndShortBatches](../service/processing/worker_test.go#L40), [TestDrainStopsOnFailureAndCancellation](../service/processing/worker_test.go#L51), [TestRunWaitsForFirstTick](../service/processing/worker_test.go#L65), [tickProcessor.ProcessBatch](../service/processing/worker_test.go#L78), [TestRunProcessesRecurringTicksAndStops](../service/processing/worker_test.go#L87).

### [service/quote/service_test.go](../service/quote/service_test.go)

**Mapping:** Pricing extension for R1.

Uses in-memory fakes to check quote snapshots, expiry, ownership and replay; PostgreSQL locking is covered separately.

**Functions and methods:** [pricingFake.Settings](../service/quote/service_test.go#L24), [pricingFake.ActiveRate](../service/quote/service_test.go#L27), [productsFake.GetMany](../service/quote/service_test.go#L39), [quotesFake.Insert](../service/quote/service_test.go#L48), [quotesFake.GetForUpdate](../service/quote/service_test.go#L49), [quotesFake.MarkConsumed](../service/quote/service_test.go#L55), [ordersFake.FindByQuote](../service/quote/service_test.go#L65), [ordersFake.Insert](../service/quote/service_test.go#L71), [ordersFake.Get](../service/quote/service_test.go#L72), [unit.Do](../service/quote/service_test.go#L84), [unit.Now](../service/quote/service_test.go#L85), [unit.Pricing](../service/quote/service_test.go#L86), [unit.Products](../service/quote/service_test.go#L87), [unit.Quotes](../service/quote/service_test.go#L88), [unit.Orders](../service/quote/service_test.go#L89), [TestQuoteSnapshotsExpiryOwnershipAndReplay](../service/quote/service_test.go#L90).

## Schema migrations

### [db/migrations/000001_initial.down.sql](../db/migrations/000001_initial.down.sql)

**Mapping:** All; schema rollback.

DROP TABLE removes initial tables in dependency order. Destructive rollback for disposable test databases. No application methods.

### [db/migrations/000001_initial.up.sql](../db/migrations/000001_initial.up.sql)

**Mapping:** All; base persistence.

CREATE TABLE users, denylisted_tokens, products, orders and order_items; CHECK/unique/FK constraints protect values and identity. CREATE INDEX defines the partial pending queue and order-list access paths. No application methods.

### [db/migrations/000002_pricing.down.sql](../db/migrations/000002_pricing.down.sql)

**Mapping:** R1 pricing extension.

DO-block guard refuses rollback when rates or conflicting order denominations exist; DROP TABLE removes the pricing tables only after the guard.

### [db/migrations/000002_pricing.up.sql](../db/migrations/000002_pricing.up.sql)

**Mapping:** R1 pricing extension.

CREATE TABLE adds catalog_settings and fx_rates, with singleton, rate and validity constraints plus the currency-pair validity index.

### [db/migrations/000003_order_quotes.down.sql](../db/migrations/000003_order_quotes.down.sql)

**Mapping:** R1 pricing extension.

DO-block guard refuses loss of live quote/new-format order provenance; drops quote tables and pricing fields/constraints only when allowed.

### [db/migrations/000003_order_quotes.up.sql](../db/migrations/000003_order_quotes.up.sql)

**Mapping:** R1 pricing extension.

ALTER TABLE adds quote identity and pricing provenance to orders/items; CREATE TABLE adds order_quotes and quote_items, ownership/consumption references, exact-total constraints and the quote-expiry index.

### [db/migrations/000004_order_idempotency.down.sql](../db/migrations/000004_order_idempotency.down.sql)

**Mapping:** R1 retry extension.

DO-block guard refuses to erase nonempty retry records; DROP TABLE proceeds only when empty.

### [db/migrations/000004_order_idempotency.up.sql](../db/migrations/000004_order_idempotency.up.sql)

**Mapping:** R1 retry extension.

CREATE TABLE order_idempotency binds customer/key, request fingerprint and order, with a composite primary key, FK constraints and DB-generated created_at.

### [db/migrations/000005_idempotency_created_at.down.sql](../db/migrations/000005_idempotency_created_at.down.sql)

**Mapping:** R1 retry extension compatibility.

SELECT 1 intentionally retains the backward-compatible timestamp column while the migrator lowers its recorded version.

### [db/migrations/000005_idempotency_created_at.up.sql](../db/migrations/000005_idempotency_created_at.up.sql)

**Mapping:** R1 retry extension compatibility.

ALTER TABLE ADD COLUMN IF NOT EXISTS adds created_at for older version-4 schemas, preserving timestamps when already present.

## Build configuration and test scripts

### [.dockerignore](../.dockerignore)

**Mapping:** All; build context.

Excludes local configuration, caches, Git metadata, build outputs and the reference directory from the Docker context. No methods.

### [.env.example](../.env.example)

**Mapping:** All; configuration template.

Documents environment variable names and development defaults for the DB, JWT, HTTP, currency, worker and auth limits. Copy to local .env and supply credentials; contains no methods.

### [.gitignore](../.gitignore)

**Mapping:** All; repository hygiene.

Excludes local secrets, generated caches, binaries and ordinary logs; allows retained review logs. No methods.

### [go.mod](../go.mod)

**Mapping:** All; dependencies.

Declares the order_management module, Go version and pinned direct/indirect dependencies. No application methods.

### [go.sum](../go.sum)

**Mapping:** All; dependency integrity.

Stores checksums used to verify module downloads. No application methods.

### [Dockerfile](../Dockerfile)

**Mapping:** All; deployment.

Build stage downloads modules and compiles the orders binary; runtime stage uses Alpine, CA certificates and a non-root user. Default entry point starts the server. Docker instructions, not Go methods.

### [docker-compose.yml](../docker-compose.yml)

**Mapping:** All; local deployment.

Defines PostgreSQL with a persistent volume, one-shot migrations and the API service. Health/dependency conditions start the API after migrations. Supplies runtime environment and loopback port mappings; no methods.

### [Makefile](../Makefile)

**Mapping:** All; local workflow.

Targets: build/run; test/integration/vet/fmt/fmt-check; migrate-up/migrate-down/seed; smoke/smoke-docker; up; acceptance. These invoke Go, scripts and Compose commands rather than application methods.

### [scripts/acceptance.py](../scripts/acceptance.py)

**Mapping:** All; local full-suite runner.

`main` isolates environment/configuration, checks tools, resolves/verifies dependencies, builds/vets/tests, provisions PostgreSQL, exercises migration up/down/up, runs integration and Docker smoke, then cleans up. Nested `run` captures exit codes and bounded output, `scrub` redacts secrets, `save` writes summaries and `manifests` hashes module files. Outputs stay in .cache/acceptance.

### [scripts/smoke.py](../scripts/smoke.py)

**Mapping:** All; deployed HTTP checks.

`call` sends authenticated JSON requests and checks expected HTTP codes; `main` exercises auth, catalog, multi-item orders, retry keys, isolation, transitions, cancel behavior, pagination, quotes and logout. SMOKE_EXPECT_WORKER chooses automatic processing checks; otherwise the script advances status through the API.

### [scripts/smoke-docker.sh](../scripts/smoke-docker.sh)

**Mapping:** All; container API smoke.

Top-level shell commands create an isolated Compose project, configure a five-second worker, provision an admin and rate fixture, invoke smoke.py and clean up through an EXIT trap. No named shell functions.

## API and project documentation

### [README.md](../README.md)

**Mapping:** All; submission entry point.

Maps requirements to endpoints; explains setup, API examples, limitations, local testing and links to detailed design. No executable application methods.

### [TESTING.md](../TESTING.md)

**Mapping:** All; local test instructions.

Provides Docker, native PostgreSQL and database-free routes, including migrations, smoke checks and the opt-in backlog plan command. Commands invoke code/tests described here; this file does not implement them.

### [architecture.md](../architecture.md)

**Mapping:** All; design reference.

Explains Go, architecture layers, ER schema, APIs, state/concurrency guarantees, idempotency, pricing, scale and test coverage. No application methods.

### [AGENTS.md](../AGENTS.md)

**Mapping:** All; contributor guidance.

Defines layout, conventions, scope and local validation expectations for repository changes. Not loaded by the running API.

### [docs/openapi.yaml](../docs/openapi.yaml)

**Mapping:** All; API contract.

Declares paths, request/response schemas, security and error contracts. Served by docs/embed.go; it describes the API without generating handlers.

### [docs/plans/order-processing-hardening/00-review-20261003-phase1.md](../docs/plans/order-processing-hardening/00-review-20261003-phase1.md)

**Mapping:** All; scope notes.

Summarizes scope and decisions for currency, worker health, auth boundaries and testing. No runtime methods.

### [docs/plans/order-processing-hardening/00-review-20261003.md](../docs/plans/order-processing-hardening/00-review-20261003.md)

**Mapping:** All; design review notes.

Reviews architecture, operations, monetary correctness and delivery boundaries from different perspectives. No runtime methods.

### [docs/plans/order-processing-hardening/01-overview.md](../docs/plans/order-processing-hardening/01-overview.md)

**Mapping:** All; design scope.

Records goals, non-goals, hardening areas, currency direction and engineering approach. No runtime methods.

### [docs/plans/order-processing-hardening/02-engineering-doc.md](../docs/plans/order-processing-hardening/02-engineering-doc.md)

**Mapping:** All; detailed design plan.

Specifies contracts, migration behavior, transactions, readiness, auth limits, logging and local checks. No runtime methods.

### [docs/plans/order-processing-hardening/03-todo.md](../docs/plans/order-processing-hardening/03-todo.md)

**Mapping:** All; implementation checklist.

Maps implemented areas to local validation commands and states remaining scope boundaries. No runtime methods.

### [docs/plans/order-processing-hardening/04-test-plan.md](../docs/plans/order-processing-hardening/04-test-plan.md)

**Mapping:** All; requirement coverage.

Maps R1–R5 and supporting features to named unit/integration tests and demo steps. No runtime methods.

### [docs/plans/order-processing-hardening/05-live-demo.md](../docs/plans/order-processing-hardening/05-live-demo.md)

**Mapping:** All; manual local demo.

Shell recipe prepares isolated fixtures and demonstrates the endpoints and automatic worker. `dc` wraps Compose safely, `api` validates HTTP responses, and `wait_processed` polls for worker advancement. These are example shell functions, not Go application methods.

### [docs/reviews/order-processing-system/00-review-20261003.md](../docs/reviews/order-processing-system/00-review-20261003.md)

**Mapping:** All; implementation review notes.

Summarizes monetary correctness, concurrency, readiness, retries, authentication, traceability and test boundaries. No runtime methods.

### [docs/reviews/order-processing-hardening/verification/native-five-minute-demo/README.md](../docs/reviews/order-processing-hardening/verification/native-five-minute-demo/README.md)

**Mapping:** R3b, R5; optional local observation.

Explains the retained single-instance scheduler run and cancellation observations, with stated limits. No runtime methods; not a production load or multi-replica test.

### [docs/reviews/order-processing-hardening/verification/native-five-minute-demo/run5m.log](../docs/reviews/order-processing-hardening/verification/native-five-minute-demo/run5m.log)

**Mapping:** R3b, R5; optional local output.

Saved output used by the adjacent scheduler note. Contains observations, not executable methods or an additional test implementation.

### [docs/code-map.md](code-map.md)

**Mapping:** All; code walkthrough.

This reference connects files and callable declarations to requirements. It contains no runtime methods.

### [docs/database-requirements.md](database-requirements.md)

**Mapping:** All; database reference.

Explains each application table and column, important constraints and indexes, and their relationship to the requirements and supporting features. It contains no runtime methods.
