# Order Processing Hardening — Overview

- Status: Approved
- Approval: user approved items 2–4, then the refined currency item 1, on 2026-10-03.
- Slug: `order-processing-hardening`
- Form: Full Phase 1; cross-layer remediation with persistence and API implications.
- Author/date: Codex / 2026-10-03
- Source: [implementation audit](../../reviews/order-processing-system/00-review-20261003.md).
- Guidance: [AGENTS.md](../../../AGENTS.md); skills: `plan-workflow`, `plan-review`, `backend-go`.

## Request and problem

Turn the audit's two blockers and seven risks into an in-scope remediation plan.
The backend exists, but full execution evidence is missing and catalog currency,
worker visibility, auth safeguards, traceability and smoke tests need attention.
Earlier approval of the base application does not imply approval of these fixes.

## Goals and customer behavior

- Preserve multi-item creation, get/list/filter, forward status updates and PENDING-only cancellation.
- Preserve catalog-sourced prices, customer ownership, admin controls and five-minute automatic processing.
- Preserve stored base prices; region defaults select the currency of new quotes/orders.
- Expose worker failure to operators and correlate committed changes with their actor/request.
- Bound public auth attempts and remove the obvious unknown-account bcrypt shortcut.
- Supply reproducible build, migration, API, concurrency and indexed-worker evidence.

## Non-goals

Payments, inventory, frontend, product editing, live FX-provider integration, durable
audit-history tables, distributed rate-limit infrastructure, new external services,
refresh tokens and password recovery.
Limited conversion using managed rates was explicitly approved as an extension.

## Remediation scope and acceptance

| Audit ID | Planned work | Acceptance evidence |
| --- | --- | --- |
| B1 | Resolve dependencies; complete full validation. | Build/vet/race, live DB/API tests, disposable migration up/down/up, EXPLAIN and Docker smoke pass; record unavailable checks honestly. |
| B2 | Add adapter assertions, wrapped operation errors and context-aware logs. | Compile assertions; `errors.Is` behavior and safe log fields verified. |
| R0 | Persist project guidance, reviewed phases and execution progress. | Root guidance and Phase 1 now; design/tracker after their required gates; no invented past approvals. |
| R1 | Persist one base catalog currency; support region-selected order currencies using managed rates. | Region changes never relabel base prices or existing orders; conversions are reproducible, rounded and snapshotted; missing/expired rates reject conversion; legacy adoption is explicit. |
| R2 | Track worker attempts/success/failure and include worker condition in readiness. | DB success plus failed/stale worker yields unhealthy readiness; startup grace, empty runs and recovery tested. |
| R3 | Add request IDs and committed-mutation actor/resource/action/outcome logs. | HTTP and worker log tests; no passwords, tokens or request bodies; failed writes never logged as committed. |
| R4 | Perform equivalent password verification for missing/inactive account paths. | Deterministic verification-path tests with generic invalid-credential responses; no timing-equality claim. |
| R5 | Add configurable, bounded process-local register/login throttling. | Tested 429 behavior, reset and client isolation; document replica and trusted-proxy limits. |
| R6 | Remove smoke assumptions that an order remains PENDING between HTTP requests. | Controlled integration tests prove cancellation/state guards; scheduled-worker smoke follows observable state. |

## Engineering approach and sequence

Use existing domain/service/HTTP/persistence layers; one implementing agent works serially.
First check dependency/DB/Docker prerequisites, then currency and regression coverage,
worker visibility, auth safeguards, traceability/conformance, and final integrated validation.
Carry test evidence alongside each fix; B1 remains open until the full validation set passes.
Retain the partial pending index, bounded `SKIP LOCKED` batches and conditional transitions.
Phase 2 will specify exact migrations, interfaces, API/config changes, task dependencies,
shared-file ownership, validation commands and rollout; Phase 3 will track those tasks.

## Approved currency direction

1. Keep one persisted base catalog currency; use the store region to default it only for a fresh catalog. Explicitly confirm the currency of existing product data.
2. A supported country/market selects a default target currency via a maintained mapping. A changed customer region or store default affects new quotes only; base prices and existing orders remain fixed.
3. Use separate versioned, dated, manually maintained base-to-target rates for this assignment. These are configured conversion prices, not a claim of live market accuracy; rates have explicit validity and no silent fallback.
4. Quote from the original base amount every time, using decimal arithmetic and each currency's minor-unit precision. Round the converted unit price once, then multiply by quantity and sum lines with overflow checks.
5. A server-issued, expiring quote binds customer, items, quantities, region, currency and rate version. Order creation uses that quote atomically; region/items changes or expiry require a fresh quote.
6. Snapshot source amounts/currency, conversion rate/version and final target amounts/currency on the order. Rate or mapping updates never recalculate placed orders.
This adds limited multi-currency quoting to the original assignment; changing the base catalog currency and live FX integration remain excluded. Phase 2 must revisit effort and API/schema impact.

## Risks and dependencies

Existing catalog rows do not record currency; adoption must not guess a denomination.
Dependency download, Docker access and PostgreSQL execution were previously unavailable.
Worker readiness needs startup/staleness rules to avoid false alarms; limiter storage must be bounded.
Owners: Codex for implementation/design and evidence; environment access is an external prerequisite.
Planning estimate: 0.5–1 engineering day; implementation/validation: 3–5 days, excluding access delays.
These are rough effort estimates, not delivery commitments; refine after Phase 2.

## Open questions for Phase 2

1. Resolved direction: managed-rate conversion approved. Phase 2 specifies legacy adoption, supported markets, rate validity, quotes, precision and rollback.
2. What exact worker grace/staleness rules and readiness response distinguish processing failure from database failure?
3. What auth limits, client-address trust rules and bounded-storage behavior will apply, and how will tests prove them?
4. What exact task sequence, migration/rollback procedure and reproducible verification environment will close B1/B2?

## Review

[Nine-persona review](00-review-20261003.md). All four directions are approved; do not request overview approval again.
[Detailed engineering design](02-engineering-doc.md) resolves the engineering questions and has its own review gate. Implementation is present; full acceptance remains blocked as recorded in the execution tracker.
