# Runbook: Import runs that failed, aborted, were cancelled, or are stuck

**Owner role:** on-call engineer  |  **Severity if this happens:** medium (stale data for one source; other sources are unaffected)
**Last verified:** 2026-11-22, base 484b08e + task 014 changes (`unchanged_file` status and the run-time `importer run: file store:` stderr line, real on a throwaway Postgres 16 with the BoE fixture, see [reparse-and-file-retention.md](reparse-and-file-retention.md)). Earlier: 2026-11-22, 19a3f12 + task 019 changes (pool env exit 2 block re-run with a fresh build; `max_connections` WARN and `SHOW max_connections` run once on a throwaway Postgres from `scripts/testdb.sh`; lock-capacity from the CLI and the killed-lock-session item are from the validator's evidence pack, UNVERIFIED here). Earlier: 2026-11-22, d8652aa (duplicate-key abort and dry run re-run on a COPY of the BoE fixture with a shifted column, throwaway Postgres 16 from `scripts/testdb.sh`: dry run, real run, `import_errors` counts; the verify block re-runs the grep. Earlier: 2026-11-21, task 009 on `testdata/ons/l55o.csv`: exit codes, `already running` (exit 3), the abandoned-run sweep; the other scenarios through `importer.Run` in task 008; every SQL statement and error text here is real output)

## When to use
An import ended in status `failed` or `aborted`, `importer run` exited non-zero (see exit codes), `importer.Run` returned `import already running for this source`, a run row stays `running`, or you need to see which rows a run rejected.

## Prerequisites
`psql` on PATH and `DATABASE_URL` in the environment (see [database-migrations.md](database-migrations.md); never paste the URL into tickets). Read access to the tables `import_runs`, `import_errors`, `raw_records`.
Imports are started with `bin/importer run --source <id>` (see [add-a-source.md](add-a-source.md)). "Re-run the import" below means that command.

**Exit codes of `importer run`:** 0 `succeeded` or `unchanged_file`; 1 `failed`/`aborted`, unknown source, DB error, no lock connection free within `DB_LOCK_WAIT` (summary line on stdout, error on stderr); 2 usage (missing `--source`, `DATABASE_URL` or a `token_env` variable; invalid `DB_MAX_CONNS`, `DB_LOCK_MAX_CONNS` or `DB_LOCK_WAIT`); 3 already running. Pool settings are described in [operations.md](../operations.md#connection-pool-and-source-locks).

## How a run is recorded
One row in `import_runs` per run that got past the lock. `status` is one of:

| status | meaning |
|---|---|
| `running` | in progress, or abandoned (see "Run stays `running` after a crash") |
| `succeeded` | stream read to the end; `failed` rows (if any) were within `max_error_ratio` |
| `aborted` | stream read to the end but `failed / total` was above `max_error_ratio`; `error` says why |
| `unchanged_file` | the file is the same as the last successful run's file: the server answered `304` to the stored ETag/Last-Modified, or the bytes have the same SHA-256 and `change_detection` lists `content_hash`. Counts 0, `file_id` set, no records written. Not a failure (exit 0). |
| `failed` | open/read/write error, cancellation or panic; `error` has the text |

Columns: `inserted/updated/unchanged/failed` (counts), `shape` (per field, observed value types and counts, `_` keys excluded), `source_meta` (e.g. `{"CDID":"L55O",...}`), `started_at`, `finished_at` (NULL while running), `error` (NULL on success). `import_errors` holds the rejected rows of a run: `run_id`, `position`, `raw`, `reason`.

## Steps
1. Latest runs for a source (newest first):
   ```
   psql "$DATABASE_URL" -c "SELECT id, status, trigger, inserted, updated, unchanged, failed, started_at, finished_at, left(error,100) AS error FROM import_runs WHERE source_id = 'ons-l55o' ORDER BY id DESC LIMIT 10"
   ```
   *(expected: one row per run; the `error` column is the first thing to read)*
2. Rows rejected by one run, with position, raw text and reason:
   ```
   psql "$DATABASE_URL" -c "SELECT position, raw, reason FROM import_errors WHERE run_id = 3 ORDER BY id"
   ```
   *(real output: `651 | "x","y","z0" | expected 2 fields, got 3`)*. Overview of all runs with rejects: `SELECT run_id, count(*) FROM import_errors GROUP BY 1 ORDER BY 1 DESC`.
3. Runs that are `running` right now (should be only live imports):
   ```
   psql "$DATABASE_URL" -c "SELECT id, source_id, started_at, now() - started_at AS age FROM import_runs WHERE status = 'running' ORDER BY started_at"
   ```
4. Shape and metadata of a run: `SELECT shape, source_meta FROM import_runs WHERE id = 1` *(real: `{"value": {"string": 642}, "period": {"string": 642}}` and `CDID` = `L55O`; jsonb reorders keys)*.
5. Pick the matching row in Troubleshooting.

## Troubleshooting
All error texts are the real `import_runs.error` / returned error. The returned error additionally starts with `import "<source>" run <id>: `.

| Symptom (exact message) | Diagnosis | Fix |
|---|---|---|
| `importer run: import "ons-l55o": import already running for this source` (exit 3; library text `import "ons-ok": import already running for this source`; no run id, no `import_runs` row created) | Another session holds the source's advisory lock: a live import (scheduler or another operator). Confirm with the lock query below. This is NOT a database error. | Wait for the other run to finish (step 3 shows it) and re-run. Do not kill it. If step 3 shows no live run and the lock query shows no holder, see "Run stays `running`". Other sources are not blocked (lock is per source). |
| An error starting `import "<source>": advisory lock for "<source>": acquire connection: ...` or other connection text, no `already running` | The attempt to lock failed (database down, pool closed, context cancelled). It says nothing about who holds the lock, and no run row exists. | Fix database connectivity (`psql "$DATABASE_URL" -c 'select 1'`), then re-run. Nothing was written. |
| status `aborted`, `error` = `import aborted: 20 of 662 rows failed, above max_error_ratio 0.01` | Rejects exceed the source's `max_error_ratio` (config, default per source; `0` means any reject aborts; equal to the ratio is fine). Look at the rejects: step 2. | Fix the cause (bad source rows, wrong `csv.columns`/delimiter: see [source-layout-changed.md](source-layout-changed.md)) or raise `max_error_ratio` in the source config after reviewing the rows, then re-run. See "What an aborted run leaves behind". |
| status `aborted`, `error` = `import aborted: 89 of 862 rows failed, above max_error_ratio 0`, and `import_errors` rows with reason `duplicate natural key (first seen at 10)` (`position` = the later record, `raw` = its natural key) | Two different source records have the same natural key in this run. The first record is stored; each later one is rejected, counted in `failed` and in `max_error_ratio`. In the reason, the number is the position of the first record, or `unknown` when the first record was in an earlier batch (500 records per batch by default). Usual causes: a layout change moved key columns (see [source-layout-changed.md](source-layout-changed.md) case 2), or the source's `natural_key` is not unique for the file. Confirm: step 2 with `WHERE run_id = <id> AND reason LIKE 'duplicate natural key%'`. | 1. Compare the file with the previous one (source-layout-changed step 3); if columns shifted, fix `columns` there. 2. If the file is right, the key is not unique: add the missing key part to `natural_key` (UNVERIFIED: not run; the effect on rows already stored under the old key is not checked). 3. `--dry-run` until `failed=0`, then the real run. |
| status `succeeded` but `failed > 0` | Rejects were within `max_error_ratio`; the good rows were imported. Not an incident. | Read step 2; fix the source if the rejects matter. |
| stdout `source=boe-bank-rate status=unchanged_file inserted=0 updated=0 unchanged=0 failed=0 run=2 duration=9ms`, exit 0 (real: second `run` of the same BoE file, `change_detection` lists `content_hash`) | Not a failure: nothing was parsed or written because the bytes equal the last successful run's file. A `304` gives the same status. | If the server file really changed, check `location` and the server's validators. To parse the stored file again after a pipeline change, use `importer reparse --source <id>` ([reparse-and-file-retention.md](reparse-and-file-retention.md)). |
| stderr first line `importer run: file store: <absolute dir>` on every `run` (not `--dry-run`); real: `importer run: file store: /.../rr014/files` | Informational: where fetched files are kept (`FILE_STORE_DIR`). | If the path is not the one you expect (for example a relative `data/files` in another directory), export an absolute `FILE_STORE_DIR` before running. See [reparse-and-file-retention.md](reparse-and-file-retention.md). |
| exit 1 with `status=failed` on a first run (no stored ETag/Last-Modified yet) after the server answered `304` | A `304` is only valid for a conditional request; a first run has none. Evidence pack (task 014 validator): a first-run `304` fails the run. The error text is not captured in this runbook: UNVERIFIED. | Read `error` in step 1. Check the server's response to a plain request (for example `curl -sI <url>`, not run here). Escalate with the run id if it repeats. |
| status `failed`, `error` = `open source: fetch "/path/nope.csv": open /path/nope.csv: no such file or directory` | The adapter could not open the source (wrong `location`, file missing, no permission). No records written, counts 0. | Correct `location` in the source config or restore the file, re-run. |
| status `failed`, `error` starts `read source: ` or `write batch: ` | Mid-stream read error, or the database rejected a batch. The batch parsed so far is flushed (read errors) and stays; later rows are missing. | Read the rest of the text, fix the cause (file truncated, database full or down), re-run (idempotent). |
| status `failed`, `error` = `cancelled: read source: context canceled` | The caller's context was cancelled (shutdown, Ctrl-C, timeout) while streaming. The row is still closed and the lock released. Rows of batches already flushed stay. | Re-run. If cancellations repeat, find who cancels (deadline too short for the source size). |
| status `failed`, `error` = `panic: boom from adapter` (the text after `panic: ` is the panic value) | A bug in an adapter. The run row is closed as `failed`, the lock released, then the panic continues in the caller (the process may crash: expected). | Not fixable by an operator: collect the run id, the `error` text and the process log (has the stack trace) and escalate. |
| A run row stays `running` after the process is gone | Hard kill / OOM / host loss: nothing could close the row. | Usually nothing: the next real `importer run` sweeps it (stderr `importer run: marked 1 abandoned run(s) failed: [7]`). See "Run stays `running` after a crash". |
| exit 1 with a summary line `status=failed ... run=6` and `importer run: import "demo-api" run 6: open source: fetch ...` | A run row exists and is `failed`; the stderr text is `import_runs.error` | Match the `error` text in this table |
| Rejects with reason `string contains a NUL byte (\u0000) at $.<field>; not storable as jsonb` | A source value holds a NUL byte. Only that record is rejected (raw = natural key); the batch and run continue. The value is never silently altered. | Clean the value at the source, or accept the reject (counted toward `max_error_ratio`). UNVERIFIED here: covered by the task's unit test, not reproduced by hand. |
| `dry run: more than 10000000 distinct natural keys; duplicate detection is bounded, use a real run (it detects duplicates in the database)` (dry run only) | The dry run keeps one hash per distinct natural key in memory, up to a fixed limit. The source has more distinct keys than the limit (10,000,000 per the validator's evidence pack; not run here). | Nothing is wrong with the data by itself. Use the real run (`bin/importer run --source <id>`), which checks duplicates in the database but writes rows. UNVERIFIED: no source over the limit was run. |
| `importer run: DB_MAX_CONNS="0": want a positive integer` (exit 2; same form for `DB_MAX_CONNS="abc"` and `DB_LOCK_MAX_CONNS="-1"`; `importer run: DB_LOCK_WAIT="0": want a positive Go duration such as 30s` for `DB_LOCK_WAIT="0"` or `"xx"`). Printed before any database connection, no run row. | The variable is set to a value that is not a positive integer (or, for `DB_LOCK_WAIT`, a positive Go duration with a unit). Check: `env \| grep '^DB_'`. Unset or empty means the default. | Unset the variable or set a valid value (for example `DB_LOCK_WAIT=30s`) where the import is started, then re-run. Verification block 5 in Verify shows the exit codes. |
| `no lock connection available: all 8 are in use after waiting 30s` (exit 1; the importer wraps it with the run prefix, text UNVERIFIED from the CLI). No run row is created. | Too many runs of this process at once: every lock connection (`DB_LOCK_MAX_CONNS`, default 8) is held by another run, and none was freed within `DB_LOCK_WAIT` (default 30s). From the CLI this is UNVERIFIED: each `importer run` process starts one run with a free lock connection; the validator's evidence pack covers in-process callers (store and importer tests). Check the open connections with the count query in the `max_connections` row below. | Nothing was written, so re-run later. To allow more concurrent runs per process, raise `DB_LOCK_MAX_CONNS` and/or `DB_LOCK_WAIT`, keeping the total below `max_connections` (next row), or start fewer runs at once. Escalate if a single `importer run` CLI process shows this message. |
| Hang, then `acquire connection: context deadline exceeded` or `get source` errors, when several runs of one source/process start with a small `DB_MAX_CONNS` (history: builds before task 019) | Old builds took source locks from the work pool. With 4 concurrent runs and a pool of 4 or fewer connections, every connection was a lock holder and no run could get a connection to finish (deadlock; reproduced by the validator before the fix, not re-run here). | Use a build that includes task 019 (lock connections separate from the pool). On an old build, raise `DB_MAX_CONNS` above the number of concurrent runs, or run one import at a time. Verify: the runs finish. |
| WARN on stderr, once per process: `DB_MAX_CONNS (60) + DB_LOCK_MAX_CONNS (50) = 110 exceeds the server's max_connections (100); connections may be refused under load` (the run continues; not a failure) | The per-process total `DB_MAX_CONNS + DB_LOCK_MAX_CONNS` is larger than the server's `max_connections`. Real check on a throwaway database: `SHOW max_connections;` gave `100`; `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database();` gave `1` when idle. The WARN text with `DB_MAX_CONNS=60 DB_LOCK_MAX_CONNS=50` was printed (the same run then failed on a missing `sources` table, unrelated). | Lower `DB_MAX_CONNS` or `DB_LOCK_MAX_CONNS` so the sum leaves room for other clients, or raise `max_connections` on the server (a server restart is needed; UNVERIFIED here). Verify: the next run prints no WARN. No action is needed if the runs succeed and the WARN does not appear. |
| **KNOWN LIMITATION (task 020, not fixed yet, validator evidence pack):** two runs of one source overlap in `import_runs`, and a run is marked `failed` with `abandoned (process died)` while its process is still alive | The lock connection of a live run was killed (for example `pg_terminate_backend` or a dropped connection to the lock session). The lock is released, the run continues, a second run of the same source starts and succeeds, and its abandoned-run sweep marks the first run `failed`. Both runs write the same source. Confirm: `SELECT id, status, started_at, finished_at, error FROM import_runs WHERE source_id = '<id>' ORDER BY started_at` shows overlapping `started_at`/`finished_at` (UNVERIFIED here: query not run); `pg_stat_activity` lists the lock session of a live run. | Workaround: do not kill sessions of an import. If it happened, let the runs end (do not kill them), then restart the import for that source once only one run remains. Record both run ids and escalate; the fix is planned in task 020. Verify: no overlapping runs for the source in later imports. |

### Is the source lock held? (read-only, safe)
Replace `ons-stuck` with the source id (it appears once):
```
psql "$DATABASE_URL" -c "SELECT l.pid, a.state, a.query_start FROM pg_locks l JOIN pg_stat_activity a USING (pid) WHERE l.locktype = 'advisory' AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database()) AND ((l.classid::bigint << 32) | l.objid::bigint) = hashtextextended('data-importer:source:' || 'ons-stuck', 0)"
```
*(real: 0 rows when nothing holds the lock; one row with the holder's `pid` while a session holds it. A live import is the only expected holder. `a.query_start` and `pg_stat_activity` also show the client; the lock is session-level and disappears when that connection closes.)*

## Check that no record was lost (duplicates, dry run)
**Arithmetic:** rows read = written + rejected. The `of N rows` in the abort text is rows read; `inserted + updated + unchanged` is written; `failed` is rejected (all reasons together). Real output (BoE copy with a shifted column, run 1): `inserted=773 failed=89`, `of 862 rows`: 773 + 89 = 862. The 89 split into 81 `duplicate natural key` and 8 `normalize` (`WHERE run_id = 1 AND reason LIKE 'duplicate natural key%'` gave `81`; `LIKE 'normalize%'` gave `8`). `SELECT count(*) FROM raw_records WHERE first_run_id = 1` gave `773`. If the sums do not add up, escalate.

**Same key in an earlier run** (validator's evidence pack, not run here): a key stored by a previous run and repeated in this run: the first record updates or leaves the row unchanged; the second is a duplicate.

**Dry run checks duplicates too** (`bin/importer run --source <id> --dry-run`). It writes nothing and counts duplicates in `failed=`. Real output on the copy: `source=boe-bank-rate status=aborted inserted=0 updated=0 unchanged=0 failed=89 run=dry-run duration=52ms`, then stderr `importer run: import "boe-bank-rate" run 0: import aborted: 89 of 862 rows failed, above max_error_ratio 0`, exit 1. Clean BoE fixture: `failed=0` (validator's evidence pack, not run here). The dry run writes nothing (validator's evidence pack: 0 rows in `import_runs`, `raw_records`, `import_errors`; the check in this run came after a real run, so it does not prove it alone). The CLI prints the count only, not the duplicate reasons (backlog): to see the rows, use `import_errors` of a real run. Run the dry run before every real run after a file change: `failed > 0` means fix the config first.

## What an aborted (or failed) run leaves behind
Chunks are committed as the stream goes (500 records each by default), not in one transaction. So after `aborted` or a mid-stream `failed`:
1. Rows already written stay in `raw_records`, tagged with the run id (`first_run_id`/`last_run_id`). Real example: aborted run 4 left 642 rows: `SELECT count(*) FROM raw_records WHERE source_id = 'ons-abort' AND first_run_id = 4` gave `642`.
2. The rejects stay in `import_errors` for that run.
3. There is no rollback command yet (out of scope of task 008). The data is valid source data, only the run is flagged.
4. Re-running is idempotent: the second run of the same aborted source gave `inserted=0 updated=0 unchanged=642 failed=20`, still `aborted`, with no duplicate rows (642 rows total) and `last_run_id` moved to the new run. Unchanged rows keep their payload and `imported_at`.
5. A cancelled run before its first batch (run 6 above, cancelled after 99 rows) wrote 0 rows.

## Run stays `running` after a crash
After `kill -9`, OOM or host loss the row is never closed by the dead process. The next real (not `--dry-run`) `importer run` (any source) first marks the `running` rows whose source lock is NOT held as `failed`, sets `finished_at`, and writes error `abandoned (process died)`. A live run (lock held) is never touched; its stale-looking row is left until that run ends.
1. Find candidates (step 3: old `age`).
2. Run the import for the same source (the sweep is database-wide per the task log; only the same-source case was run); stderr shows what was swept. Real run: a stale row (id 7, started 2 hours ago) was closed by the next run: `importer run: marked 1 abandoned run(s) failed: [7]`, then `SELECT id, status, error FROM import_runs WHERE id = 7` gave `failed` / `abandoned (process died)`. A `--dry-run` does not sweep (row stayed `running`).
3. While another process held that source's lock, `importer run` exited 3 and the stale row stayed `running` (also real); after the holder ended, the next run swept it.
4. Fallback, only if no further run will happen soon: confirm with the lock query above that it returns **0 rows** (if a row appears a live process holds it: do NOT edit; escalate), then close by hand (the guard makes it a no-op if already closed):
   ```
   psql "$DATABASE_URL" -c "UPDATE import_runs SET status = 'failed', finished_at = now(), error = 'abandoned (process died)' WHERE id = 8 AND status = 'running' RETURNING id, status, finished_at, error"
   ```
   *(hand-close form run in task 008 with a different error text: one row returned, repeat gave `UPDATE 0`)*
5. Rows the dead run had flushed are valid; re-running is idempotent. Counts on the closed row stay 0 (never saved).

## Verify
1. Step 1 shows the new run `succeeded` (or `unchanged`-only counts for a re-run) with `finished_at` set.
2. Step 3 returns no row for the source and the lock query returns 0 rows.
3. Sanity check that the repo still carries the messages quoted above:
<!-- verify -->
```bash
grep -n -e 'import already running for this source' -e 'import aborted' -e 'above max_error_ratio' -e 'panic: %v' internal/importer/importer.go
```
4. The duplicate-key and dry-run messages are still in the code (real grep, run for this change):
<!-- verify -->
```bash
test -n "$(grep -e 'duplicate natural key (first seen at' internal/importer/importer.go)" && test -n "$(grep -e 'dry run: more than' internal/importer/importer.go)" && echo ok
```
*(expected: `ok`)*
5. The pool settings are validated before any connection (real run for this change; `DATABASE_URL` points at a closed port, so each run exits 2 without a database). Real output: `importer run: DB_MAX_CONNS="0": want a positive integer`, `DB_MAX_CONNS="abc": want a positive integer`, `DB_LOCK_MAX_CONNS="-1": want a positive integer`, `DB_LOCK_WAIT="0": want a positive Go duration such as 30s`, `DB_LOCK_WAIT="xx": want a positive Go duration such as 30s`:
<!-- verify -->
```bash
B="$(mktemp -d)/importer" && go build -o "$B" ./cmd/importer
for v in DB_MAX_CONNS=0 DB_LOCK_MAX_CONNS=-1 DB_LOCK_WAIT=0; do rc=0; env "$v" DATABASE_URL=postgres://postgres@127.0.0.1:1/x "$B" run --source ons-l55o >/dev/null 2>&1 || rc=$?; echo "$v exit $rc"; test "$rc" -eq 2 || exit 1; done
echo ok
```
*(real output: `DB_MAX_CONNS=0 exit 2`, `DB_LOCK_MAX_CONNS=-1 exit 2`, `DB_LOCK_WAIT=0 exit 2`, `ok`)*

## Roll back / undo
No rollback command exists (out of scope for task 008). A hand-closed run (`UPDATE` above) is undone with `UPDATE import_runs SET status = 'running', finished_at = NULL, error = NULL WHERE id = <id>` (UNVERIFIED: not run; only do it if the run turns out to be alive). Never delete `raw_records` rows to "undo" an aborted run without the data owner's decision: they are tagged by run id, so `SELECT ... WHERE last_run_id = <id>` shows exactly what a run touched.

## Escalate
Escalate to the importer maintainers when: a panic text appears; the lock query shows a holder but no import should be running; `write batch:` errors repeat; a run is `running` and its lock is held for longer than the source normally takes; `no lock connection available` comes from a single `importer run` CLI process; two runs of one source overlap (known limitation, task 020). Include: source id, run id(s), the `import_runs` row (step 1), the `import_errors` count, the lock query output, the `DB_*` values in use (names only, no URL), and the process log around `started_at`.
