# Native deployed API smoke — 2026-10-04

**PASS**, based on the user's terminal transcript. The native server was started
with `bin/orders server` on port 8090. `/api/v1/ready` reported database and worker
`ok`, with no recorded worker failure. An INR rate of 2 was imported as a test
fixture before the smoke run. Credentials are omitted from this record.

```text
SMOKE_EXPECT_WORKER=1 python3 scripts/smoke.py
API smoke test passed: auth, catalog, orders, idempotency, isolation, transitions, cancellation, pagination, logout
```

The script waits for automatic PENDING → PROCESSING within its 20-second smoke
budget when `SMOKE_EXPECT_WORKER=1`; it does not perform the manual PROCESSING
transition used by its alternative mode. This is a fast smoke result, not a
measurement of the default five-minute interval. Successful pending cancellation
is independently covered by the passing controlled integration suite; the fast
smoke allows a 409 if processing wins the cancellation race.

Together with the [native race/integration run](../native-postgres-20261003/README.md),
this verifies the application through the native database and running HTTP API.
Docker image/Compose acceptance and the actual five-minute live demonstration
remain unverified. [Machine-readable summary](summary.json).

The attempted `cd /path/to/order_management` was a placeholder and failed. The
shell was already in the project directory, so subsequent commands still ran.
The actual project path is `/Users/amritraj/Downloads/projects/order_management`.
