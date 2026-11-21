# 001 – Go scaffold, test database and CI

## Goal
A buildable Go module with the planned layout, a one-command ephemeral Postgres for tests, and CI, so every later task has a working gate.

## Depends on
none

## Design refs
`CLAUDE.md`, `README.md` (planned layout), `docs/testing.md`, `docs/operations.md`

## Scope
`go.mod`, `go.sum`, `cmd/importer/`, `internal/*/doc.go`, `internal/testutil/`, `Makefile`, `scripts/testdb.sh`,
`.github/workflows/ci.yml`, `.gitignore`, `README.md` (add a "Develop" section)

## Out of scope
Any real functionality (parsing, DB schema, gRPC). No proto files yet.

## Acceptance criteria (each must be checkable)
- [ ] `go.mod` has module `github.com/willis-dev-md/data-importer`, `go 1.24`, and `tool` directives for `honnef.co/go/tools/cmd/staticcheck` and `github.com/bufbuild/buf/cmd/buf` (added with `go get -tool`; pinned versions resolve).
- [ ] Packages exist with a `doc.go` stating their purpose: `internal/source`, `internal/mapping`, `internal/store`, `internal/importer`, `internal/server`, `internal/record`, `internal/config`.
- [ ] `cmd/importer/main.go` implements a tiny subcommand dispatcher: `importer version` prints a version string, `importer help` / no args prints usage listing planned subcommands (`migrate serve run mapping`), unknown subcommand exits 2. Covered by a table-driven test of the dispatcher (the dispatcher is a function, not logic inside `main`).
- [ ] `scripts/testdb.sh start` starts an ephemeral Postgres 16 (initdb in a temp dir, as the `postgres` OS user when run as root via `runuser`, random free port, `trust` auth, listening on 127.0.0.1 only), waits until ready, and prints shell `export` lines for `DATABASE_URL` and `TESTDB_DIR`. `scripts/testdb.sh stop` stops it and deletes the directory. Both are idempotent and safe to run twice. Binaries are found via `pg_config --bindir` or `/usr/lib/postgresql/*/bin`.
- [ ] `internal/testutil` provides `DB(t) *pgxpool.Pool`-style helper `testutil.DatabaseURL(t)`: returns `DATABASE_URL`; if unset it calls `t.Skip` **unless** `CI_REQUIRE_DB=1`, in which case it calls `t.Fatal`. It also creates a fresh uniquely named schema/database per test and drops it in `t.Cleanup`. A test proves it can connect and that two tests get isolated databases.
- [ ] `Makefile` has targets `build`, `test`, `lint`, `fmt`, `testdb-up`, `testdb-down`.
- [ ] `.github/workflows/ci.yml` runs on push and PR: setup-go 1.24, a Postgres 16 service, `gofmt` check, `go vet`, `go tool staticcheck ./...`, `CI_REQUIRE_DB=1 go test -race ./...`.
- [ ] `scripts/validate.sh` passes (run from a clean checkout; it starts and stops its own Postgres and leaves no postgres process behind: verify with `pgrep -a postgres`).

## Docs impact
README.md (Develop section: make targets, test database); CHANGELOG.md

## Runbook impact
docs/runbooks/local-development.md (build, test, start/stop the test database; Postgres-as-root and port-in-use fixes)

## Status
validated
## Log
- 2026-11-21 worker r1: pinned staticcheck v0.6.1 and buf v1.57.0 (latest require Go 1.26; repo uses Go 1.24); grpc-gateway pinned v2.27.2 for same reason.
- 2026-11-21 validator r1 (code): PASS. documentor + runbook-refiner ran; validator docs-check: PASS.
- 2026-11-21 orchestrator: repaired two README lines and one docs/design.md line damaged by an earlier edit helper (an `rm` helper replaced text with an empty string when parking the MCP design). Fixed here; not part of the task.
- Decisions: tools pinned (staticcheck v0.6.1, buf v1.57.0) so go.mod stays `go 1.24.0` + `toolchain go1.24.7`; only pgx v5 added; unimplemented subcommands exit 1, unknown exit 2; testdb state file in /tmp. Recorded in docs/testing.md.
