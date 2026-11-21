# Runbook: Demo and smoke test (ONS slice: CSV to Postgres to gRPC)

**Owner role:** release engineer  |  **Severity if this happens:** medium (a failing smoke test blocks a release; the demo itself changes nothing outside its own temp files and database)
**Last verified:** 2026-11-21, task 012 (ran `scripts/demo.sh` twice with its own database, with a caller-supplied `DATABASE_URL`, with `psql` and with `python3` hidden from PATH, interrupted with SIGINT, SIGTERM and SIGKILL; ran `go test -race -count=1 ./internal/e2e` with a database, without one and against an unreachable port; Postgres 16.15 from `scripts/testdb.sh`; `grpcurl` not installed, so its branch is UNVERIFIED)

## When to use
Before a release, after a change to the ONS slice (`scripts/demo.sh`, `internal/e2e`, `cmd/importer`, `ListRecords`), to show the demo to someone, or when `scripts/demo.sh` or `go test ./internal/e2e` fails.
Related: [local-development.md](local-development.md) (test database), [run-the-grpc-server.md](run-the-grpc-server.md) (`serve`).

## Prerequisites
- Run from the repository root. No network and no secrets are needed.
- Go 1.24 (`go version` printed `go version go1.24.7 linux/amd64` here).
- Postgres 16 server binaries, found by `pg_config --bindir` or `/usr/lib/postgresql/*/bin` (`postgres --version` printed `postgres (PostgreSQL) 16.15` here). As root this is fine: `testdb.sh` runs them as `postgres` via `runuser`.
- `psql` on PATH (`psql (PostgreSQL) 16.15` here) and `python3` on PATH (`Python 3.13.18` here).
- Optional: `grpcurl`. Not installed here (`which grpcurl` printed nothing), so the grpcurl branch of the demo is UNVERIFIED.
- If `DATABASE_URL` is set, the demo uses that server and drops only its own temp database. If it is not set, the demo starts its own cluster and stops it on exit.

## Steps
1. Run the demo. It takes about 3 s (2.5 s and 2.7 s on two runs here). *(expected: exit code 0, `source=ons-l55o status=succeeded inserted=642`, three `==>` call titles, empty stderr; full output below)*
<!-- verify -->
```bash
scripts/demo.sh
```

Expected output (real, first run; record bodies shortened, see the note under the list):
```text
==> starting a temporary Postgres
==> building the importer
==> migrate up
version 1
==> sources apply
ons-l55o: source created, mapping v1 created
==> run (import the fixture)
source=ons-l55o status=succeeded inserted=642 updated=0 unchanged=0 failed=0 run=1 duration=52ms
==> serve (plaintext gRPC on a free local port)
    listening on 127.0.0.1:37697

==> first 3 records
    go run ./scripts/demo-client -addr 127.0.0.1:37697 -size 3
{ "records": [3 records], "nextPageToken": "WyJMNTVPIiwiMTk4OSBERUMiXQ" }
==> monthly records
    go run ./scripts/demo-client -addr 127.0.0.1:37697 -type month -size 3
{ "records": [3 records], "nextPageToken": "WyJMNTVPIiwiMTk4OSBERUMiXQ" }
==> monthly records with value >= 3.5
    go run ./scripts/demo-client -addr 127.0.0.1:37697 -type month -min 3.5 -size 3
{ "records": [3 records], "nextPageToken": "WyJMNTVPIiwiMTk4OSBERUMiXQ" }

==> done; stopping the server and removing the temporary database
```
The three records in each call are `ons-l55o:["L55O","1989 APR"]`, `1989 AUG` and `1989 DEC` (values 5.6, 5.5, 5.8). The port, the `duration=` and the `importedAt` timestamps change per run. With `grpcurl` installed the call lines read `grpcurl -plaintext -d ...` instead (UNVERIFIED).

A failing run stops at the step it was on: the last `==>` line is the step that failed. The script prints its own message to stderr (`demo: psql is required`, `demo: serve did not start`, ...).

2. Check that nothing is left behind. *(expected: no output, exit code 0)*
<!-- verify -->
```bash
! pgrep -af '[d]ata-importer-[d]emo[.]'
! ls -d /tmp/data-importer-[d]emo.* 2>/dev/null
```

3. Run the end-to-end test. It builds the real binary and needs a database: start one first with `eval "$(scripts/testdb.sh start)"`. *(expected: `--- PASS: TestONSSliceEndToEnd` and `ok  	github.com/willis-dev-md/data-importer/internal/e2e`, about 3 s; the test takes about 2.7 s here)*
<!-- verify: db -->
```bash
CI_REQUIRE_DB=1 go test -race -count=1 -v ./internal/e2e
```

4. Stop the test database when done: `scripts/testdb.sh stop` *(prints `testdb: stopped`; safe to run twice)*.

## Troubleshooting
Every row was reproduced by the runbook author; the exact message is copied from the output.

| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| `demo: psql is required` (exit 1; last `==>` line is `==> starting a temporary Postgres`) | `command -v psql` finds nothing. Reproduced with PATH hiding `psql`. | Install the `psql` client for Postgres 16 and put it on PATH; re-run step 1. The script stopped its own cluster, so step 2 passes. |
| `scripts/testdb.sh: line 30: python3: command not found`, then `testdb: no free port`, then `scripts/demo.sh: line 38: DATABASE_URL: unbound variable` | `python3` is missing. The last line is a side effect: `testdb.sh start` failed, so `DATABASE_URL` was never set and `set -u` stopped the script. Reproduced with PATH hiding `python3`. | Install `python3` and put it on PATH. Read the first error line, not the last. |
| `initdb: error: could not access directory ".../data-importer-testdb.XXXXXX/data": Permission denied`, then `testdb: initdb failed` | `TMPDIR` points into a directory the `postgres` user cannot traverse. Reproduced with `TMPDIR` under `/tmp/claude-0/...` (mode 700). | See the row of the same message in [local-development.md](local-development.md) (`unset TMPDIR`). Not repeated here. |
| `importer serve: listen tcp 127.0.0.1:47011: bind: address already in use` | `serve` was given a fixed `GRPC_ADDR` that another process holds. Reproduced with a Python listener on port 47011. The demo and the e2e test use port 0 and do not hit this. | Find the holder: `(exec 3<>/dev/tcp/127.0.0.1/47011) && echo open` (`ss` is not installed here). Stop it or choose another `GRPC_ADDR`. |
| `importer serve: DATABASE_URL is not set` (exit 1) | `serve` run without the URL. Reproduced with `env -u DATABASE_URL`. | `eval "$(scripts/testdb.sh start)"`, or set `DATABASE_URL`. |
| `pgrep` shows `postgres -D /tmp/data-importer-testdb.XXXXXX/data ...` and `/tmp/data-importer-demo.XXXXXX/importer serve`, or step 2 fails | The demo was killed with SIGKILL, which cannot be trapped, so its cleanup did not run. Reproduced with `kill -KILL` during `serve`. | Follow "Leftover processes" below. |
| `--- SKIP: TestONSSliceEndToEnd` and `DATABASE_URL is not set; run scripts/testdb.sh start` (exit 0) | No database and `CI_REQUIRE_DB` unset. Reproduced. A skip is not a pass. | Start the database (step 3) and re-run. The release gate uses `CI_REQUIRE_DB=1`. |
| `DATABASE_URL is not set and CI_REQUIRE_DB=1` (`--- FAIL`, exit 1) | Intended: with `CI_REQUIRE_DB=1` a missing database is a failure. Reproduced. | Start the database and re-run. |
| `connect to admin database: failed to connect to ... dial tcp 127.0.0.1:1: connect: connection refused` (`--- FAIL`) | `DATABASE_URL` points at a server that is not running. Reproduced with port 1. A stale port after a stop/start is the usual cause. | `eval "$(scripts/testdb.sh start)"` (prints the current URL) and re-run. |
| Exit code 130 after Ctrl+C, 143 after `kill -TERM` | Not an error: the script's traps run cleanup and then exit with the signal's code. Reproduced with SIGINT and SIGTERM during `serve`. | None. Check step 2 to confirm nothing is left. |

### Leftover processes (after an interrupted run)
1. List them. *(expected: a `postgres -D /tmp/data-importer-testdb...` line and/or a `/tmp/data-importer-demo...importer serve` line; a `[postgres] <defunct>` line is a reaped zombie and is harmless)*
   `pgrep -af 'postgres -D /tmp/data-importer-testdb|[d]ata-importer-[d]emo.*importer serve'`
2. Stop the demo's server: `kill <PID of importer serve>` *(it shuts down on SIGTERM; reproduced: it exited)*.
3. Stop the test cluster the state file points at: `scripts/testdb.sh stop` *(prints `testdb: stopped`; removes the cluster directory and `/tmp/data-importer-testdb.<uid>.state`)*.
4. Remove the demo's temp directory, only after step 2: `rm -rf /tmp/data-importer-demo.*`
5. Verify: step 2 of Steps prints nothing and exits 0; the port is closed (`(exec 3<>/dev/tcp/127.0.0.1/<port>) && echo open || echo closed` prints `closed`).

## Verify
- Step 1 exits 0 with `inserted=642` and the three calls (and `inserted=642` again on a second run: each run uses a new temp database, so the count is not `unchanged`).
- Step 2 prints nothing and exits 0.
- Step 3 ends with `ok  	github.com/willis-dev-md/data-importer/internal/e2e`.

## Smoke-test checklist (release)
Run from the repository root. Results are from task 012 on this machine.

| # | Check | How | Expected |
|---|---|---|---|
| 1 | Demo completes | `scripts/demo.sh; echo "exit=$?"` | `exit=0`; `inserted=642`; three calls; empty stderr; about 3 s |
| 2 | Demo leaves nothing | Steps, block 2 | exit 0, no output |
| 3 | Demo interrupted cleanly | start the demo, wait for `listening on`, then `kill -INT` (or `-TERM`) on the `scripts/demo.sh` PID | exit 130 (143 for TERM); no leftovers (step 2) |
| 4 | End-to-end test | Steps, block 3 with a database | `PASS`, `ok` |
| 5 | Test not silently skipped | `CI_REQUIRE_DB=1 go test -count=1 ./internal/e2e` with no database | `FAIL` with `DATABASE_URL is not set and CI_REQUIRE_DB=1` (exit 1) |

Not in this checklist, UNVERIFIED: the grpcurl branch and reflection (grpcurl not installed); `scripts/validate.sh` (the factory gate, run by the validator, not by this runbook).

## Roll back / undo
Nothing in the repository changes. The demo writes only `/tmp/data-importer-demo.*` and its own temp database (or cluster). For leftovers, see "Leftover processes".

## Escalate
Send to the repository owner: the full output of `scripts/demo.sh` (stdout and stderr), or of `CI_REQUIRE_DB=1 go test -race -count=1 -v ./internal/e2e`; `go version`; `psql --version`; `python3 --version`; `pg_config --bindir`; `id -u`; `echo $TMPDIR`; the output of the leftover-process listing. Never paste `DATABASE_URL` or any secret.
