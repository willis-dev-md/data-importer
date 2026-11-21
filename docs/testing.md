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
| End to end | Import the ONS fixture, then query through gRPC with exact counts and spot checks; `scripts/demo.sh` for a human (see [below](#end-to-end-test-and-demo-task-012)) |
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

## End-to-end test and demo (task 012)
`internal/e2e/e2e_test.go` (`TestONSSliceEndToEnd`) builds the real binary (`cmd/importer`) and runs it against an ephemeral
database from `internal/testutil`. It asserts, in order:
1. `migrate up` prints `version 1`.
2. `sources apply` on a temp copy of `docs/examples/ons-l55o.source.yaml` and `ons-l55o.mapping.yaml`, whose `location` is `file://` plus `testdata/ons/l55o.csv`, prints `ons-l55o: source created`.
3. `run --source ons-l55o` prints `inserted=642`.
4. `serve` on `127.0.0.1:0` (plaintext) answers `ListRecords` from a real gRPC client with exact counts: 642 (all), 454 (`period_type` month), 37 (year), 151 (quarter), 108 (month and `value` >= 3.5).
5. Three spot records, matched by `period_type` and `period_start` because the label (`1989`, `1989 Q1`, `2026 OCT`) is not a record field: `1989` year, value 5.7, 1989-01-01 to 1989-12-31; `1989 Q1` quarter, value 5.8, 1989-01-01 to 1989-03-31; `2026 OCT` month, value 3.7, 2026-10-01 to 2026-10-31. All three have `series_id` L55O, `measure` cpih_annual_rate and unit `%`.
6. A second `run` prints `unchanged=642`, and the counts of step 4 still hold.
7. SIGTERM stops `serve` with exit code 0.

It runs inside `scripts/validate.sh` (through `go test`, with `CI_REQUIRE_DB=1`).

**No-network guard.** Every child process gets `HTTP_PROXY`, `HTTPS_PROXY` and the lowercase forms set to `http://127.0.0.1:1`, a closed port, and empty `NO_PROXY`, so an outbound HTTP(S) request by the binary fails the run. The source location is `file://`, so no network call is expected at all.
Limits, found in validation (the "no network" property is established by code reading and was not tested at the network layer):
- A `file://` location never uses `net/http`, which is what the code reading shows. The proxy guard covers only HTTP(S) requests. Go's `net/http` does not use a proxy for loopback addresses, so a direct dial to a loopback address would not be caught by it.
- `unshare -n` (a network namespace without a network) was not usable in the validation environment, so no kernel-level block was tested.

**Demo (`scripts/demo.sh`).** Run from the repository root. It starts its own database with `scripts/testdb.sh start` (unless `DATABASE_URL` is set), creates a fresh database for the run, does `migrate up`, `sources apply`, `run`, starts `serve` on a free local port and makes three `ListRecords` calls: the first 3 records, `period_type` month, and `period_type` month with `value` >= 3.5. It uses `grpcurl` when it is installed and otherwise `scripts/demo-client`. On exit it stops the server and removes its temp directory; with its own database it also stops that temporary cluster, and with a caller-supplied `DATABASE_URL` it drops only its own database. Cleanup on Ctrl+C (SIGINT, exit 130), SIGTERM (exit 143) and a forced failure (exit 1) was verified by the task validator and by the runbook-refiner (see [demo-and-smoke-test](runbooks/demo-and-smoke-test.md)); the `grpcurl` branch was not run. The real output is in the [README Quick start](../README.md#demo-in-one-command).
Limits, found in validation:
- The `grpcurl` branch and gRPC reflection through it were not run: `grpcurl` was not installed. `scripts/demo-client` does not use reflection.

**Decisions (task 012).**
- The proxy variables point at a closed port (`127.0.0.1:1`), so any outbound dial by the binary fails the run instead of going direct.
- `scripts/demo-client` is a helper for the demo only, used when `grpcurl` is absent; it is not a product command.

## Decisions (task 001, scaffold)
- **Test database:** `scripts/testdb.sh start|stop` runs an ephemeral Postgres 16 (as `postgres` via `runuser` when root); state is kept in `/tmp/data-importer-testdb.<uid>.state`. `internal/testutil` offers `DatabaseURL`, `NewDatabaseURL` (fresh database per test) and `DB` (pgx pool). Tests skip when `DATABASE_URL` is unset, but FAIL when `CI_REQUIRE_DB=1` (used by `scripts/validate.sh`).
- **Tool pins:** staticcheck v0.6.1 and buf v1.57.0, because the latest releases need Go 1.26 and the project uses Go 1.24.7 (`go 1.24.0` + `toolchain go1.24.7` in `go.mod`). Raise the pins when the Go version is raised.
- **Dependencies:** only `github.com/jackc/pgx/v5` has been added so far.
- **CLI stubs:** the planned `serve` subcommand exits 1 with "not implemented" (superseded: `serve` runs the gRPC server since task 010); unknown subcommands exit 2.
