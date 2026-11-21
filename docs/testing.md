# Testing strategy

| Layer | Approach |
|---|---|
| Source adapters | Golden-file tests: several CSVs (different delimiters/encodings/BOM/quoted newlines) and recorded API fixtures (pagination types, 429/5xx retry) |
| XLSX / row pipeline | Golden tests on `testdata/boe/baserate-trimmed.xlsx`: 854 events, year fill-down, mixed text/number years and days, `Feb `/`may` months, section switches (rows after `Repo Rate` get that series), `1933-1938 no changes` dropped, hidden-sheet read, unchanged-file skip, reparse equals fresh import. Security: oversized/zip-bomb workbooks rejected |
| Mapping engine | Table-driven unit tests per transform and error code; property test: mapping never panics on arbitrary payloads |
| Store | Integration tests against real Postgres via `scripts/testdb.sh` (no mocks, no containers) (upsert idempotency, tombstones, advisory lock) |
| Archives / URL resolution | Zip safety (bomb, nested, traversal, ratio), member regex with irregular spaces, `resolve` with a fake clock and httptest, 5xx is not 404 |
| Source groups | Expansion and placeholder validation, override deep-merge, idempotent apply (add/update/disable), 200-member scheduling spread, per-host rate limiting, one failing member does not affect others |
| Scheduler | Injected fake clock: daily/weekly/monthly next-run maths, DST, month-end, `on_missed`, no drift, two replicas never double-run, overlap skip |
| gRPC | In-process server + client; filter translation tests; pagination stability under concurrent import |
| End to end | Import sample sources, then query through gRPC and compare to golden output |
| Contract | `buf lint` and `buf breaking` on protos |

## Quality gates (CI)
`gofmt`, `go vet`, `staticcheck`, `go test -race ./...`, `buf lint`, migration up/down check.

## Definition of done per task
Spec acceptance criteria met, gates green, and independently reviewed.

## Test isolation rule (task 008)
Integration tests of all packages share ONE Postgres cluster and run in parallel. Any query against a cluster-wide
view or function (`pg_locks`, `pg_stat_activity`, `pg_terminate_backend`) must be scoped to the test's own database
and keys. A test that terminated other packages' lock sessions caused a flaky 600 s hang; fixed in
`internal/store/store_test.go`.

## Decisions (task 001, scaffold)
- **Test database:** `scripts/testdb.sh start|stop` runs an ephemeral Postgres 16 (as `postgres` via `runuser` when root); state is kept in `/tmp/data-importer-testdb.<uid>.state`. `internal/testutil` offers `DatabaseURL`, `NewDatabaseURL` (fresh database per test) and `DB` (pgx pool). Tests skip when `DATABASE_URL` is unset, but FAIL when `CI_REQUIRE_DB=1` (used by `scripts/validate.sh`).
- **Tool pins:** staticcheck v0.6.1 and buf v1.57.0, because the latest releases need Go 1.26 and the project uses Go 1.24.7 (`go 1.24.0` + `toolchain go1.24.7` in `go.mod`). Raise the pins when the Go version is raised.
- **Dependencies:** only `github.com/jackc/pgx/v5` has been added so far.
- **CLI stubs:** planned subcommands exit 1 with "not implemented"; unknown subcommands exit 2.
