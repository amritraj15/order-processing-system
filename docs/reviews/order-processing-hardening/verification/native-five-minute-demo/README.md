# Five-minute scheduler demo — 2026-10-04

**PASS.** Executed by the user on a local macOS arm64 host (Homebrew PostgreSQL 18.4)
against a fresh database, with the default `PROCESSING_INTERVAL` (5m), one server
instance on port 8090. Source: terminal transcript and [run5m.log](run5m.log).
This is not a multi-instance or production-failover result.

Startup log reported `"processing_interval":300000000000` (5 minutes).

## Tick cadence

| Tick | Logged at (IST) | Gap | Processed |
| --- | --- | --- | --- |
| 1 | 13:01:04.751 | - | 3 |
| 2 | 13:06:04.746 | 299.994 s | 0 |
| 3 | 13:11:04.756 | 300.010 s | 1 |
| 4 | 13:16:04.746 | 299.990 s | 3 |
| 5 | 13:21:04.807 | 300.061 s | 1 |

Start-to-start gaps (log time minus duration) are within about 3 ms of 300 s.

## Order behaviour

| Case | Evidence |
| --- | --- |
| First processing occurred at the first tick (inferred) | Three orders created 12:57:18 first changed at 13:01:05; inference uses their stored update times |
| Created before a tick is processed by it | Order created 13:15:55 (about 10 s before) became PROCESSING at 13:16:05 |
| Created after a tick waits until the next scheduled tick | Orders created 13:11:09 and 13:11:10 became PROCESSING at 13:16:05; order created 13:16:15 at 13:21:05 |
| Cancelled orders remain cancelled across the observed ticks | Two orders cancelled before the first tick kept `updated_at` 12:57:18 through five ticks |
| Cancel after processing is refused | `POST /orders/{id}/cancel` on a processed order returned 409 |

Final database state: 8 orders PROCESSING, 2 CANCELLED. `/api/v1/ready` reported
database and worker `ok`, with no recorded worker failure.

## Limits

- A single instance was tested. With several API replicas each has its own ticker, so
  the deployment-wide cadence is approximate (see the main README).
- The pre-tick status checks (before 13:01) were not saved; "nothing processed early"
  is inferred from the orders' `updated_at` values.
- Only one run on one machine.
