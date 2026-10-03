# Hardening verification — 2026-10-03

**Latest result: full automated acceptance PASS**, 2026-10-04 IST.
[Runner summary](acceptance-20261003T183917Z-2cf6c389/summary.json) records 22 successful stages:
dependencies/verification, formatting, build, vet, race tests, disposable PostgreSQL,
all five migration downs and re-up, integration, Docker smoke and both cleanups.
The recorded before/after module manifest hashes are identical and match the
current files. **B1 is closed** for the requested automated acceptance scope.

[Native integration](native-postgres-20261003/README.md) and
[native deployed smoke](native-smoke-20261004/README.md) also passed. The actual
five-minute live demo remains unverified; fast smoke does not measure that cadence.
Results were executed by the user on the local host and checked against saved
artifacts. Earlier sandbox failures remain historical evidence, not current blockers.

The checks below preserve the **earlier agent-environment evidence**. Their DNS
and PostgreSQL blockers do not describe the user's now-working native setup.
The agent's own loopback restriction remains; the native pass is supported by the
user's retained log, not an agent-executed database run.

## Passing checks

- [Race-enabled local tests](local-race.log): `domain/money`, `domain/order`,
  `service/quote`, `service/processing`, `configs`, `internal/logging`.
- [Local vet](local-vet.log): those packages plus the listed pure domain/ports,
  service and clock packages. Packages reported `[no test files]` were compiled,
  not claimed to have behavioral coverage.
- `make fmt-check` passes.
- All application Go files parse and the source import-use check passes. This
  is a syntax check, **not** full type checking or an adapter compilation result.
- OpenAPI and Compose YAML parse; all 79 OpenAPI references resolve across 15 paths.
- `sh -n scripts/smoke-docker.sh` and Python smoke-script compilation pass.

Local Go verification uses workspace GOMODCACHE/GOCACHE, the existing local module
proxy cache and GOSUMDB=off; it does not change dependency versions. Go version:
`go1.26.2 darwin/arm64`.

## Blocked checks

| Check | Result / evidence |
| --- | --- |
| `go mod tidy` | Proxy DNS unavailable; pinned dependencies cannot be downloaded. Manifests are not claimed finalized. |
| Full build | [Exit 1 at dependency loading](full-build.log); no complete application binary validated. |
| Full vet | [Exit 1 at dependency loading](full-vet.log). |
| Full race suite | [Exit 1](full-race.log); Echo/auth/DB packages cannot load. |
| PostgreSQL integration suite | [Exit 1 at dependency loading](integration.log); no live migration, concurrency, EXPLAIN or API/DB pass claimed. |
| Local PostgreSQL | `initdb -D .cache/pg-hardening -A trust --no-locale` exits 1: `shmget` operation not permitted. initdb removes its failed scratch directory. |
| Docker smoke | Docker socket permission denied; no container/API smoke pass claimed. |

The network and runtime blockers were rechecked at implementation start; see
[baseline](baseline.md). Full commands were attempted again after implementation.
HTTP/auth/readiness, migration/adoption/rollback, quote concurrency/rollback and
cleanup EXPLAIN tests are present for execution in a capable environment. Their
existence is not execution evidence.

## Current finding disposition

- **B1 closed:** the full acceptance runner passed; dependency tidy/verification, build/vet, race/integration, migration round trip, Docker smoke and cleanup are recorded.
- **B2:** adapter assertions, wrapped operation errors and contextual logging are
  implemented; full build and auth/HTTP tests passed in the acceptance run.
- **R0 addressed:** approved overview/design, root guidance and current tracker exist.
- **R1–R6:** source mitigations and regression tests are added. Local arithmetic,
  quote/use-case, worker, currency/concurrency, auth/HTTP, migration and smoke
  regressions passed. Production failover and the five-minute live demo are not
  implied by this acceptance result.

## Continue verification

Use a machine with Go 1.26, network access and Docker/PostgreSQL. Resolve the pinned
module graph with `go mod tidy`; review resulting go.mod/go.sum changes without
downgrading versions merely to match a cache. Then run:

```sh
make fmt-check
make build
make vet
make test
# Set TEST_DATABASE_URL to a dedicated test DB with schema-creation privileges.
make integration
make smoke-docker
```

Integration tests isolate their schemas and include migration round trips/guards,
concurrent quote consumption, failed-consumption rollback and EXPLAIN output.
Preserve successful command output and plans here before closing B1 or marking the
plan Done. Do not run destructive migration rollback on an application database.

## Full-environment acceptance follow-up

A single `make acceptance` command now provisions disposable PostgreSQL, checks
full dependency/build/vet/race/migration/integration/EXPLAIN and Docker smoke, and
retains redacted logs plus before/after module manifests. Its Python and shell
syntax checks pass.

An earlier [attempt](acceptance-20261003T131746Z-9ac01b21/summary.json) was blocked at Docker access; the successful run linked at the top supersedes that blocker.
Go and Docker Compose version checks pass; Docker socket access is denied.
A separate dependency retry still fails on proxy DNS, and localhost PostgreSQL
port 5432 does not respond. No remote executor is connected or specified, so no
external environment was used and no full-suite pass is claimed.

## Submission review follow-up

The working tree was rechecked after fixing status error context, adding explicit
request UUID parsing and switching manual transition timestamps to PostgreSQL
time. [Local race-enabled tests](submission-review-local.log) passed, including
`TestRunProcessesRecurringTicksAndStops`, which observes two real 25ms ticker
events and cancellation. This does not measure five-minute cadence or DB behavior.
The order service package compiled but has no behavioral test files.

Exact local-cache invocation from the project root:

```sh
env GOMODCACHE="$PWD/.cache/gomod" GOCACHE="$PWD/.cache/gobuild" \
  GOPROXY=file:///Users/amritraj/go/pkg/mod/cache/download GOSUMDB=off \
  go test -mod=readonly -count=1 -race \
  ./domain/order ./domain/money ./service/order ./service/quote \
  ./service/processing ./configs ./internal/logging
```

That module-cache path is specific to the original machine. On a connected host,
use normal module verification and `make acceptance` to reproduce full acceptance.
Earlier logs describe their recorded source/check scope, not a full pass of later
changes. These fresh [HTTP](submission-review-http.log) and
[database](submission-review-database.log) attempts failed at dependency loading
because proxy DNS is unavailable. The new invalid-UUID and database-clock tests
therefore remain unexecuted. Format, whitespace and documentation-link checks pass.
PostgreSQL tests are excluded from plain `go test ./...` by the `integration` tag.
