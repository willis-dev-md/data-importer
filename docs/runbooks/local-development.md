# Runbook: Local development (build, test, ephemeral test database)

**Owner role:** developer / on-call engineer  |  **Severity if this happens:** low
**Last verified:** 2026-11-21, task 001 (commands run by the runbook-refiner and re-run by the validator)

## When to use
Building the importer, running tests, or starting/stopping the throwaway Postgres (`scripts/testdb.sh`), or when one of those fails.

## Prerequisites
Go 1.24.x, Postgres 16 binaries (`/usr/lib/postgresql/*/bin` or `pg_config` on PATH), `python3`, `runuser` (when root).

## Steps
1. Build: `make build`  *(expected: `go build -o bin/importer ./cmd/importer`, no errors)*
2. Test with an automatic DB: `make test`  *(starts a DB, runs `go test -race -count=1 ./...`, stops it)*
3. Or manage the DB yourself:
   `eval "$(scripts/testdb.sh start)"`  *(exports `TESTDB_DIR` and `DATABASE_URL=postgres://postgres@127.0.0.1:<port>/postgres?sslmode=disable`; a second `start` just re-prints the exports)*
   then `go test -race -count=1 ./...`
4. Stop and delete the DB: `scripts/testdb.sh stop`  *(prints `testdb: stopped`; safe to run twice)*
5. Full gate: `scripts/validate.sh` (runs `scripts/checks.d/*`; `10-go.sh` sets `CI_REQUIRE_DB=1`).

State file: `/tmp/data-importer-testdb.<uid>.state` (BINDIR, TESTDB_DIR, TESTDB_PORT). Data dir: `/tmp/data-importer-testdb.XXXXXX/` (logs `initdb.log`, `postgres.log`, `pg_ctl.log` inside).

## Troubleshooting
| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| Postgres "root" execution refused | Container runs as root (`id -u` = 0); Postgres will not run as root | Nothing to do: `testdb.sh` runs `initdb`/`pg_ctl` as the `postgres` user via `runuser`. Do not run Postgres by hand as root. |
| `initdb: error: could not access directory ".../data-importer-testdb.XXXXXX/data": Permission denied` then `testdb: initdb failed` | Reproduced with `TMPDIR` pointing into a directory the `postgres` user cannot traverse (e.g. under `/tmp/claude-0/...`). | `unset TMPDIR` (or point it at a world-traversable dir such as `/tmp`), re-run `start`. |
| `pgrep -a postgres` shows `[postgres] <defunct>` | Zombie of a stopped server; the container init does not reap children. Reproduced after every `stop`. | Harmless; not a running server. A live one shows `postgres -D /tmp/data-importer-testdb...`. Ignore. |
| `go.mod` shows `go 1.26.x` (or a newer `toolchain`) after adding a tool | `go get -tool` pulled latest buf/staticcheck needing Go 1.26 and bumped the `go` directive. Cause from task log; not re-run here. | Pin: `go get -tool honnef.co/go/tools/cmd/staticcheck@v0.6.1` and `go get -tool github.com/bufbuild/buf/cmd/buf@v1.57.0`, then restore `go 1.24.0` / `toolchain go1.24.7` in `go.mod`, `go mod tidy`. Check `head -5 go.mod`. (UNVERIFIED: exact commands not re-run.) |
| Test fails: `DATABASE_URL is not set and CI_REQUIRE_DB=1` | Intended: with `CI_REQUIRE_DB=1` DB tests fail rather than skip. Reproduced. | Start the DB (step 3) and re-run, or `unset CI_REQUIRE_DB` for a local run. |
| DB tests show `--- SKIP` | `DATABASE_URL` and `CI_REQUIRE_DB` both unset. Reproduced. | Expected locally; start the DB for real coverage. A skip is not a pass in CI. |
| `start` after the server died or was killed | Stale state file. Reproduced with `pg_ctl -m immediate stop`: `start` detected it, cleaned the old dir and started a fresh DB on a new port. | None; just run `start` again and re-`eval` the exports (port changes). |
| `testdb: pg_ctl start failed (port N in use? ...)` | Port taken between `free_port` and start (race). UNVERIFIED: not reproducible safely. | Read `postgres.log` printed above the message; re-run `start` (a new port is picked). |
| `testdb: cannot find Postgres binaries` | No `pg_config`/`/usr/lib/postgresql/*/bin`. UNVERIFIED. | Install Postgres 16 server package. |

## Verify
`make test` ends with `ok` for each package and `pgrep -af 'postgres -D /tmp/data-importer-testdb'` prints nothing after stop.

## Roll back / undo
`scripts/testdb.sh stop`. If the state file was lost, `ls -d /tmp/data-importer-testdb.*` and remove only those directories after stopping any live `postgres -D` process shown by `pgrep -af`.

## Escalate
Persistent `initdb`/`pg_ctl` failure: send `initdb.log`/`postgres.log`, `id -u`, `echo $TMPDIR`, Postgres version to the repo owner.
