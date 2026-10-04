# Implementation checklist

The [engineering design](02-engineering-doc.md) describes the implementation.
Use [TESTING.md](../../../TESTING.md) to run checks locally.

| Area | Implementation | Local check |
| --- | --- | --- |
| Dependencies and build | Pinned Go modules and readonly builds | `go mod tidy`, `go mod verify`, `make build`, `make vet` |
| Money and domain | Integer minor units, checked arithmetic and item snapshots | Domain and quote service tests |
| Persistence | Explicit migrations, transactional writes and rollback guards | PostgreSQL integration and disposable migration round trip |
| Order API | Creation, retrieval, list/filter, cancellation and status changes | HTTP/PostgreSQL lifecycle test |
| Retry safety | Customer-scoped keys, payload fingerprints and transaction deadlines | Concurrent retry, conflict, rollback and timeout tests |
| Worker and readiness | Bounded locked batches, conditional transitions and health state | Worker unit tests and repository concurrency tests |
| Authentication | Customer/admin roles, JWT revocation and bounded auth limits | Auth, limiter and ownership tests |
| Operations | Configuration validation and structured logs | Configuration and log-redaction tests |
| Local acceptance | Isolated database and Docker API smoke runner | `make acceptance` |

Inventory, payments, durable status history and automatic idempotency-key expiry
remain outside the implemented scope. Local results are generated under ignored
`.cache/` storage; no run logs or dated sign-off records are required for submission.
