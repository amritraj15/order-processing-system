# Native PostgreSQL execution — 2026-10-03

Evidence supplied by the user from their local terminal and the accompanying
`integration-passed.log`; these are not claims that the agent bypassed its sandbox.
Environment: Homebrew PostgreSQL 18.4, macOS arm64. The pasted `go version` command
included an inline comment interpreted as arguments by interactive zsh, so that
line did not establish the Go version. Subsequent compilation and tests ran.

| Check | Observed result |
| --- | --- |
| `go mod tidy`, `go mod verify` | User transcript: no tidy errors; all modules verified. |
| Format, build and vet | User transcript: completed without reported errors. |
| Fresh full unit/HTTP/auth race suite | User transcript: all tested packages passed. |
| Empty migration up / five down steps / up | User transcript: commands completed without reported errors. Individual exit codes were not retained. |
| Native PostgreSQL integration | PASS on the full rerun with `-count=1 -race -tags=integration`; all tested packages passed. |
| 000004 nonempty downgrade guard and 000005 compatibility | PASS for version 4 both with and without timestamp, through the real migration test. |
| Idempotency contention, lock/statement timeout, rollback and quote aliases | PASS. |
| Worker/cancel race, concurrent consumers, queue and cleanup EXPLAIN | PASS. |
| Locked-row skip / failed-batch rollback | PASS on rerun after correcting the test-fixture UUID scan. |
| Deployed native smoke | Subsequently passed on 2026-10-04; [separate evidence](../native-smoke-20261004/README.md). |
| Docker acceptance / actual five-minute demo | No passing result supplied. |

The original failed test used GORM `Scan(&uuid.UUID)`, which treats the UUID's underlying
16-byte array as an array destination and attempts to scan the textual UUID into
a byte. The fix uses `Row().Scan(&id)` to invoke `database/sql.Scanner`, retaining
the same SELECT FOR UPDATE transaction and adding an ID equality assertion.
No matching raw top-level UUID scans were found elsewhere in application/test code.

The focused rerun in the agent environment compiled, then failed to connect to
127.0.0.1:5432 with `connect: operation not permitted`. This is a sandbox limit,
separate from the user's running database. It does not verify the fixed test.

The earlier [pre-fix log](integration.log) is retained as failure history. The
[user-supplied full rerun](integration-passed.log) now passes, including the locked-row
skip and failed-batch rollback assertions. The test output contains no FAIL or race
warning. This establishes the native suite result; it does not establish a Docker
image/Compose pass or the actual five-minute demo. A deployed native HTTP smoke
result was subsequently supplied and is recorded separately above.

[Passing native suite summary](summary.json)

Passing log SHA-256: `5a34bf19a3e9b5d57073c784af7495d3edb6b11c43ef564d571c88c28ada8d2d`

For future captures, enable `set -o pipefail` before piping Go tests to `tee` so a
failing test propagates a nonzero pipeline exit status.
