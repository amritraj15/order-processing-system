# Local test plan — with or without Docker

Choose one route below and run commands from the repository root. Use a **Bash
session** for these examples, including on macOS where the default shell is zsh.
Start a child shell with `bash --noprofile --norc` and wait for its prompt before
pasting setup. If strict options were already enabled in your main shell, run
`set +e` and `set +u` there first. Do not use `exec bash`: a child shell lets a
failed strict-mode command return to the original terminal. The live-demo runbook
includes the separate launch block and an exit diagnostic.
The [requirement matrix](docs/plans/order-processing-hardening/04-test-plan.md)
lists the individual test cases and expected API results.

| Route | Dependencies | What it verifies |
| --- | --- | --- |
| A — Docker | Go 1.26, Python 3, Make, Docker Engine/Desktop with Compose, dependency/image downloads | Build, vet, race tests, migrations, PostgreSQL integration/EXPLAIN and containerized API/worker smoke. |
| B — Native PostgreSQL | Go 1.26, Python 3, Make, PostgreSQL 18 server tools, curl, dependency downloads | The same application tests, migration round trip and native API/worker smoke. Does not verify Docker image/Compose behavior. |
| C — No database | Go 1.26, Make, dependency downloads | Build, vet, domain/service/HTTP unit tests and short-interval worker scheduling. Does not verify PostgreSQL or a deployed API. |

Race-enabled Go tests also need a supported platform and a C compiler/toolchain.
Use the pinned module versions; do not downgrade dependencies to match a cache.
Go commands use readonly manifests after `go mod tidy`, which may update
`go.mod`/`go.sum`; review those changes before committing.

If running individual commands in interactive zsh, omit inline `# comments` or
first enable `setopt INTERACTIVE_COMMENTS`. Use `set -o pipefail` before piping
tests to `tee`; otherwise the pipeline can report success after a test failure.

## A — Full checks with Docker

Start Docker and confirm its engine is reachable. No manual database, admin or
product setup is needed for the acceptance runner.

```bash
set -euo pipefail
go version
python3 --version
docker info
docker compose version
make acceptance
```

Expected: every required stage passes and the final result is successful. The
runner creates an isolated PostgreSQL 18 container, runs migration up/down/up,
runs verbose integration tests and starts a separate Compose stack for API smoke.
It provisions fixtures and removes its own test containers/volumes. It stores
`summary.json` and command logs under
`.cache/acceptance/acceptance-<run-id>/`.

The PostgreSQL acceptance container uses a dynamic loopback port; Docker smoke
defaults to HTTP 18080 and PostgreSQL 15432. If those smoke ports are occupied:

```bash
SMOKE_HTTP_PORT=18082 SMOKE_POSTGRES_PORT=15435 make acceptance
```

For only the deployed API smoke, rather than full acceptance:

```bash
go mod tidy
make smoke-docker
```

Expected final output begins `API smoke test passed`. This smoke verifies automatic
processing with a **five-second** interval. It does not measure five-minute cadence
or replace the integration tests. For interactive presentation, follow the
[Docker live-demo runbook](docs/plans/order-processing-hardening/05-live-demo.md).

## C — Checks without Docker or PostgreSQL

Run this section on its own for fast checks, or as the first step of route B.
Keep the same shell when continuing to B so `LOCAL_RESULTS` remains available.

```bash
set -euo pipefail
set +x
umask 077
mkdir -p .cache
LOCAL_RESULTS="$(mktemp -d "$PWD/.cache/local-test.XXXXXX")"
go version | tee "$LOCAL_RESULTS/go-version.log"
go mod tidy 2>&1 | tee "$LOCAL_RESULTS/dependencies.log"
go mod verify 2>&1 | tee "$LOCAL_RESULTS/module-verification.log"
make fmt-check 2>&1 | tee "$LOCAL_RESULTS/format.log"
make build 2>&1 | tee "$LOCAL_RESULTS/build.log"
make vet 2>&1 | tee "$LOCAL_RESULTS/vet.log"
go test -mod=readonly -count=1 -race ./... 2>&1 | tee "$LOCAL_RESULTS/unit.log"
```

Expected: each command exits 0, tests report `ok`, and `bin/orders` exists.
`-count=1` runs tests again instead of reusing a cached result. A package reporting
`[no test files]` was compiled but has no behavioral tests of its own.

Plain `go test ./...` and `make test` exclude files tagged `integration`, so they
do not need `DATABASE_URL` or PostgreSQL. The HTTP tests use in-process handlers.
The worker suite observes two real 25ms ticks with a recording processor; that
checks the scheduling loop without a database or a five-minute wait.

## B — Full application checks without Docker

### B1. Install/check PostgreSQL tools

Complete route C first. On macOS with Homebrew, install the server tools and add
them to PATH. You do not need to start Homebrew's default database service; the
next step creates a separate cluster. [Homebrew PostgreSQL formula](https://formulae.brew.sh/formula/postgresql@18).

```bash
# macOS/Homebrew only; skip installation if PostgreSQL 18 tools are already present.
brew install postgresql@18
export PATH="$(brew --prefix postgresql@18)/bin:$PATH"
```

On Linux, install PostgreSQL 18 server/client tools through your distribution's
package manager and expose the same commands on PATH. Run the cluster commands
as a regular user, not root. [PostgreSQL initdb requirements](https://www.postgresql.org/docs/18/app-initdb.html).

```bash
postgres --version | tee "$LOCAL_RESULTS/postgres-version.log"
command -v initdb pg_ctl createdb psql pg_isready curl
```

### B2. Start a private local PostgreSQL cluster

Use an unused loopback port, 15434 below. This creates three disposable databases
in a unique temporary directory: `orders_test` for integration schemas,
`orders_migrations` for destructive migration checks, and `orders_demo` for API
smoke. It does not connect to your normal PostgreSQL service.

The cluster uses password authentication and listens only on loopback. The
commands follow PostgreSQL's [initdb](https://www.postgresql.org/docs/18/app-initdb.html),
[pg_ctl](https://www.postgresql.org/docs/18/app-pg-ctl.html) and
[createdb](https://www.postgresql.org/docs/18/app-createdb.html) interfaces.

```bash
LOCAL_PG_ROOT="$(mktemp -d /tmp/orders-local.XXXXXX)"
LOCAL_PG_PORT=15434
export PGHOST=127.0.0.1 PGPORT="$LOCAL_PG_PORT" PGUSER=orders
export PGPASSWORD="$(python3 -c 'import secrets; print(secrets.token_hex(24))')"
printf '%s\n' "$PGPASSWORD" > "$LOCAL_PG_ROOT/password"
initdb -D "$LOCAL_PG_ROOT/data" -U "$PGUSER" --auth=scram-sha-256 \
  --pwfile="$LOCAL_PG_ROOT/password" --no-locale --encoding=UTF8 \
  > "$LOCAL_RESULTS/initdb.log" 2>&1
rm "$LOCAL_PG_ROOT/password"

LOCAL_API_PID=
cleanup_local() {
  if [ -n "$LOCAL_API_PID" ]; then
    kill -TERM "$LOCAL_API_PID" 2>/dev/null || true
    wait "$LOCAL_API_PID" 2>/dev/null || true
    LOCAL_API_PID=
  fi
  if pg_ctl -D "$LOCAL_PG_ROOT/data" status >/dev/null 2>&1; then
    pg_ctl -D "$LOCAL_PG_ROOT/data" -m fast -w stop
  fi
}
trap cleanup_local EXIT

pg_ctl -D "$LOCAL_PG_ROOT/data" -l "$LOCAL_PG_ROOT/postgres.log" \
  -o "-h 127.0.0.1 -p $LOCAL_PG_PORT -k $LOCAL_PG_ROOT" -w start
pg_isready -d postgres
createdb orders_test
createdb orders_migrations
createdb orders_demo

export TEST_DATABASE_URL="postgres://orders:$PGPASSWORD@127.0.0.1:$LOCAL_PG_PORT/orders_test?sslmode=disable"
MIGRATION_DATABASE_URL="postgres://orders:$PGPASSWORD@127.0.0.1:$LOCAL_PG_PORT/orders_migrations?sslmode=disable"
export DATABASE_URL="postgres://orders:$PGPASSWORD@127.0.0.1:$LOCAL_PG_PORT/orders_demo?sslmode=disable"
export STORE_REGION=US CURRENCY=USD QUOTE_TTL=5m
export PROCESSING_INTERVAL=5s PROCESSING_BATCH_SIZE=500
export JWT_SECRET="$(python3 -c 'import secrets; print(secrets.token_hex(32))')"
export JWT_ISSUER=order-management JWT_TOKEN_TTL=24h
export AUTH_LOGIN_LIMIT=10 AUTH_REGISTER_LIMIT=5 AUTH_RATE_WINDOW=1m
export AUTH_RATE_MAX_KEYS=10000 AUTH_MAX_IN_FLIGHT=4
```

Expected: `pg_isready` reports accepting connections. The bootstrap `orders` role
owns only this disposable cluster and can create integration test schemas. Keep
shell tracing off and do not attach environment dumps or database URLs to results.

### B3. Migrations and database integration

The migration round trip below targets the empty migration database only.
Integration tests create/drop isolated schemas in `orders_test`; they apply their
own migrations and do not depend on demo products or an API process.

```bash
DATABASE_URL="$MIGRATION_DATABASE_URL" bin/orders migrate up 2>&1 \
  | tee "$LOCAL_RESULTS/migrations.log"
for version in 5 4 3 2 1; do
  DATABASE_URL="$MIGRATION_DATABASE_URL" bin/orders migrate down 2>&1 \
    | tee -a "$LOCAL_RESULTS/migrations.log"
done
DATABASE_URL="$MIGRATION_DATABASE_URL" bin/orders migrate up 2>&1 \
  | tee -a "$LOCAL_RESULTS/migrations.log"

go test -mod=readonly -count=1 -race -v -tags=integration ./... 2>&1 \
  | tee "$LOCAL_RESULTS/integration.log"
```

Expected: all commands exit 0. Tests cover the complete order lifecycle and role
rules, transaction rollback, concurrent worker/cancellation, quote expiry/replay,
database timestamps, keyed create concurrency/conflicts/rollback, repeated cancellation,
migration guards and EXPLAIN index assertions. `-v` retains
the query plans. `make integration` is also available, but the explicit command
above forces a fresh run and prints verbose local diagnostics.

### B4. Start the native API and exercise the real worker

The smoke test creates its own customers/products/orders. Provision the admin
and a labelled INR fixture rate first; no manual token copying is required.
The five-second interval above is intentional for this fast test.

```bash
bin/orders migrate up
bin/orders catalog init --currency USD
export ADMIN_EMAIL=local-admin@example.com
export ADMIN_PASSWORD=local-demo-admin-password
bin/orders admin --email "$ADMIN_EMAIL" --name 'Local Test Admin'
RATE_FROM="$(python3 -c 'from datetime import datetime,timezone,timedelta; print((datetime.now(timezone.utc)-timedelta(minutes=1)).isoformat())')"
RATE_UNTIL="$(python3 -c 'from datetime import datetime,timezone,timedelta; print((datetime.now(timezone.utc)+timedelta(hours=1)).isoformat())')"
bin/orders rates add --target INR --rate 2 --valid-from "$RATE_FROM" \
  --valid-until "$RATE_UNTIL" --source local-test-fixture

export HTTP_ADDRESS=127.0.0.1:18081
export API_URL=http://127.0.0.1:18081
bin/orders server > "$LOCAL_RESULTS/api.log" 2>&1 &
LOCAL_API_PID=$!
LOCAL_READY=0
for attempt in {1..30}; do
  kill -0 "$LOCAL_API_PID"
  if curl -fsS --max-time 2 "$API_URL/api/v1/ready" > "$LOCAL_RESULTS/ready.json"; then
    LOCAL_READY=1
    break
  fi
  sleep 1
done
test "$LOCAL_READY" = 1
SMOKE_EXPECT_WORKER=1 make smoke 2>&1 | tee "$LOCAL_RESULTS/smoke.log"
```

Expected: readiness is 200 and smoke ends with `API smoke test passed`. It checks
201 multi-item creation and totals, reads, ownership, cancellation outcomes,
automatic PROCESSING, admin SHIPPED/DELIVERED transitions, filters, cursor pages,
regional quotes, replay and logout. In worker mode, smoke waits at most 20 seconds
for processing and requests an INR quote, which is why this setup imports a rate.

The fast cancellation smoke accepts either 200 or 409 when the worker wins.
Successful pending cancellation and skipped-stage rejection are checked
deterministically in `TestCustomerAndAdminOrderFlow`. Do not use smoke alone as
the complete requirements sign-off. Repeated smoke runs create new fixtures;
respect the default registration limit of five attempts/minute/IP.

### B5. Optional five-minute live demonstration without Docker

Use a **fresh** `orders_demo` database in a new run of this guide so the live
runbook's exact order-count assertions have a clean starting point. In B4, create
the admin as `admin@example.com` with `ADMIN_PASSWORD=demo-admin-password`, import
the fixture with `--source demo-fixture`, set `PROCESSING_INTERVAL=5m`, start the
API and **skip** `make smoke`. Install jq for the runbook's response assertions.

Then use the existing [live-demo runbook](docs/plans/order-processing-hardening/05-live-demo.md)
with these substitutions; all customer/API requests and expected results stay
the same:

| Runbook part | Native equivalent |
| --- | --- |
| Compose preparation/admin/rate commands | Already performed by B2–B4 with the substitutions above. Do not run the Compose setup block. |
| Request-helper setup | Set `DEMO_TMP="$(mktemp -d /tmp/orders-live.XXXXXX)"`, `DEMO_BODY="$DEMO_TMP/response.json"`. Keep `API_URL` from B4. Paste the runbook's `api()` function and its login/customer/product fixture block. |
| D0 API restart | Stop the recorded API PID and wait for it; set `PROCESSING_INTERVAL=5m` and restart `bin/orders server` as below. Repeat D0's HTTP readiness assertions. |
| D1–D5 and D7 | Run the API commands unchanged in order; D7 follows D6. |
| D6 | Run the polling and terminal-state assertions; no Compose commands are needed. |
| Compose cleanup | Use B6 below and remove the temporary raw-response directory you created. |

Native replacement for D0's restart commands, after creating the
runbook's customer/product fixtures:

```bash
kill -TERM "$LOCAL_API_PID"
wait "$LOCAL_API_PID"
export PROCESSING_INTERVAL=5m
bin/orders server > "$LOCAL_RESULTS/api.log" 2>&1 &
LOCAL_API_PID=$!
```

Reserve at least ten minutes for two actual ticks and 15–20 minutes overall.
Never set `SMOKE_EXPECT_WORKER=1` against a five-minute worker: the smoke test's
20-second polling deadline is intentionally shorter. An ordinary `make smoke`
without that flag manually advances PROCESSING and cannot prove worker timing.

### B6. Cleanup

Stop the API and the private database in the same shell. Keep the logs and data
directory for diagnosis; the EXIT trap also stops services if a preceding command
fails. No system PostgreSQL service is stopped.

```bash
cleanup_local
trap - EXIT
cp "$LOCAL_PG_ROOT/postgres.log" "$LOCAL_RESULTS/postgres.log"
printf 'Local results: %s\nStopped PostgreSQL data: %s\n' "$LOCAL_RESULTS" "$LOCAL_PG_ROOT"
unset PGPASSWORD DATABASE_URL TEST_DATABASE_URL MIGRATION_DATABASE_URL
unset JWT_SECRET ADMIN_PASSWORD
```

After inspecting the results, you can delete the **stopped, generated**
`/tmp/orders-local.XXXXXX` directory and any `/tmp/orders-live.XXXXXX` raw responses
from this run. Start another rehearsal in a fresh Bash session with a new cluster
rather than repeating admin creation or overlapping rate imports in the old DB.

## Acceptance checklist and troubleshooting

| Check | Expected local result |
| --- | --- |
| Build/vet/unit | Exit 0; fresh tests; no compilation failure. |
| PostgreSQL integration | Exit 0 with integration tag; retain concurrency, rollback, clock and EXPLAIN results. |
| Migration round trip | Up → down five versions → up on the disposable migration DB. |
| API smoke | Passing assertion output, readiness response and worker success log; note interval 5s. |
| Real cadence, if demonstrated | Orders automatically advance on successive worker runs; cancelled orders remain cancelled. |
| Container packaging | Required only for route A; route B must report this as NOT RUN. |

Route C alone is not full
acceptance; a successful native run does not certify the Docker deployment.

| Symptom | Next step |
| --- | --- |
| Dependency download/DNS failure | Restore access to the configured Go proxy; the test has not executed. |
| Docker socket unavailable | Start Docker/check access, or choose native route B. |
| `initdb` shared-memory/permission error | Use a normal local terminal with permission to run PostgreSQL. Some restricted agent environments cannot start it. |
| Address already in use | Change the documented host port before startup; keep the URL and server settings aligned. |
| `TEST_DATABASE_URL` missing / cannot create schemas | Complete B2, or supply a dedicated database whose role can create schemas. |
| Smoke receives 429 | Respect Retry-After; repeated fixture registration consumes the IP quota. |
| Smoke receives 409 for INR quote | Verify a currently valid INR fixture rate exists and the catalog base is USD. |
| Worker smoke times out | Confirm `PROCESSING_INTERVAL=5s`, inspect readiness/API logs, and verify DB access. |
