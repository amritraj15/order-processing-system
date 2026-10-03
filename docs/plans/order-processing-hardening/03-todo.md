# Order Processing Hardening — Tracker

- Status: In Progress — source implementation present; acceptance blocked
- Engineering design: [approved](02-engineering-doc.md)
- Last updated: 2026-10-03
- Owner: Codex; serial execution. Commit links unavailable (root is not a Git repository).

| ID | Task | Owner | Write scope | Status | Depends on | Evidence |
| --- | --- | --- | --- | --- | --- | --- |
| T1 | Capabilities and manifests | Codex | go.mod/go.sum, verification | blocked | approval | Dependency retrieval/tidy fail on DNS; Docker denied; PostgreSQL shared-memory denied |
| T2 | Money/domain/contracts | Codex | domain, ports, service/uow, internal/clock | done | T1 attempted | Exact-money/domain/use-case race tests and local vet pass |
| T3 | Persistence/migrations/CLI | Codex | db, internal/testutil, cmd | blocked | T2 | Migrations/adapters/CLI and DB regression tests written; live execution unavailable |
| T4 | Quote/order services and HTTP | Codex | service/quote, service/order, api/rest/v1 | blocked | T3 | Services/HTTP/contract tests written; use-case tests pass; HTTP dependencies unavailable |
| T5 | Worker/readiness | Codex | service/processing, api/rest/routes | done | T4 | Worker deadlines/status/cleanup implemented; worker race tests pass; HTTP readiness check remains in T9 |
| T6 | Auth safeguards | Codex | service/auth, domain/user, middleware | blocked | T5 | Auth limiter/verifier and tests written; pinned bcrypt/JWT/Echo unavailable |
| T7 | Logging/conformance | Codex | internal/logging, api, service, db, cmd | blocked | T6 | Assertions/error/logging changes written; logging tests pass; full adapter compilation pending |
| T8 | Wiring/config/docs | Codex | configs, bootstrap, routes, CLI, build/config/docs | done | T4–T7 | Config/wiring/docs/build changes present; config tests, source parsing, YAML/ref checks pass; full wiring runtime check remains T9 |
| T9 | Acceptance evidence | Codex | scripts, tests, verification, plans | blocked | T8 | Full build/vet/race/integration stop at dependency loading; Docker/DB acceptance unavailable |

## Newly discovered work

No architectural scope change. Added quote-consumption rollback and migration/cleanup-plan tests. Full verification remains required; `go mod tidy` must complete before readonly builds can be accepted.

## Drift check

Created from approved T1–T9. No historical implementation completion inferred.

## Done log

Engineering approval received; source implementation and documentation updated. See [verification report](../../reviews/order-processing-hardening/verification/README.md). Source-complete tasks are not proof of full application acceptance.

## Acceptance environment follow-up

Added `make acceptance` / `scripts/acceptance.py` to run T9 in a capable host using
a dedicated disposable database, migration CLI round trips and verbose integration
output. Attempted locally: prerequisite check blocks on Docker permissions.
Dependency DNS and PostgreSQL availability rechecks also fail. T1/T9 remain blocked;
an accessible execution environment is required. No new scope approval is needed.
