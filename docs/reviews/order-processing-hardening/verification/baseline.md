# Baseline — 2026-10-03

Go: go1.26.2 darwin/arm64.

- `go mod tidy` with workspace GOMODCACHE/GOCACHE: exit 1; proxy.golang.org DNS lookup fails for pinned Echo/JWT/GORM/migrate/validator/crypto dependencies.
- `docker info --format {{.ServerVersion}}`: exit 1; permission denied connecting to Docker socket.
- Full build/live acceptance remain blocked. No dependency versions were downgraded.

- `initdb -D .cache/pg-hardening -A trust --no-locale`: exit 1; cannot create shared memory segment (`shmget`: Operation not permitted).
