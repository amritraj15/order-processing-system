# Hardening verification — 2026-10-03

Implementation is present. **Full acceptance remains blocked**, not passed.
Engineering and overview approvals are recorded; no further approval is needed to
continue verification when the environment can run it.

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

## Finding disposition

- **B1 remains open:** full build/runtime verification and final `go mod tidy` are pending.
- **B2:** adapter assertions, wrapped operation errors and contextual logging are
  implemented in source; full adapter compilation/auth/HTTP tests remain unverified.
- **R0 addressed:** approved overview/design, root guidance and current tracker exist.
- **R1–R6:** source mitigations and regression tests are added. Local arithmetic,
  quote/use-case and worker tests pass; DB restart/currency/concurrency, auth/HTTP
  and smoke evidence remains pending under B1.

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

The latest [attempt](acceptance-20261003T131746Z-9ac01b21/summary.json) remains **blocked** at Docker access.
Go and Docker Compose version checks pass; Docker socket access is denied.
A separate dependency retry still fails on proxy DNS, and localhost PostgreSQL
port 5432 does not respond. No remote executor is connected or specified, so no
external environment was used and no full-suite pass is claimed.
