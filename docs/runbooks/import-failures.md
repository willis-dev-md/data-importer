# Runbook: Import runs that failed, aborted, were cancelled, or are stuck

**Owner role:** on-call engineer  |  **Severity if this happens:** medium (stale data for one source; other sources are unaffected)
**Last verified:** 2026-11-21, task 008 (`importer.Run` driven against a throwaway Postgres 16 from `scripts/testdb.sh` on `testdata/ons/l55o.csv` for every scenario below; every SQL statement and error text here is real output; CLI steps are UNVERIFIED)

## When to use
An import ended in status `failed` or `aborted`, `importer.Run` returned `import already running for this source`, a run row stays `running`, or you need to see which rows a run rejected.

## Prerequisites
`psql` on PATH and `DATABASE_URL` in the environment (see [database-migrations.md](database-migrations.md); never paste the URL into tickets). Read access to the tables `import_runs`, `import_errors`, `raw_records`.
CLI: UNVERIFIED / future. There is no `importer run` yet (planned in task 009), so imports are started from code that calls `importer.Run`. Everything below is database-level and was run. Where a step says "re-run the import", use whatever starts it in your deployment (task 009: `importer run <source>`, UNVERIFIED).

## How a run is recorded
One row in `import_runs` per run that got past the lock. `status` is one of:

| status | meaning |
|---|---|
| `running` | in progress, or abandoned (see "Run stays `running` forever") |
| `succeeded` | stream read to the end; `failed` rows (if any) were within `max_error_ratio` |
| `aborted` | stream read to the end but `failed / total` was above `max_error_ratio`; `error` says why |
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
| `import "ons-ok": import already running for this source` (no run id, no `import_runs` row created) | Another session holds the source's advisory lock: a live import (scheduler or another operator). Confirm with the lock query below. This is NOT a database error. | Wait for the other run to finish (step 3 shows it) and re-run. Do not kill it. If step 3 shows no live run and the lock query shows no holder, see "Run stays `running`". Other sources are not blocked (lock is per source). |
| An error starting `import "<source>": advisory lock for "<source>": acquire connection: ...` or other connection text, no `already running` | The attempt to lock failed (database down, pool closed, context cancelled). It says nothing about who holds the lock, and no run row exists. | Fix database connectivity (`psql "$DATABASE_URL" -c 'select 1'`), then re-run. Nothing was written. |
| status `aborted`, `error` = `import aborted: 20 of 662 rows failed, above max_error_ratio 0.01` | Rejects exceed the source's `max_error_ratio` (config, default per source; `0` means any reject aborts; equal to the ratio is fine). Look at the rejects: step 2. | Fix the cause (bad source rows, wrong `csv.columns`/delimiter: runbook source-layout-changed is planned) or raise `max_error_ratio` in the source config after reviewing the rows, then re-run. See "What an aborted run leaves behind". |
| status `succeeded` but `failed > 0` | Rejects were within `max_error_ratio`; the good rows were imported. Not an incident. | Read step 2; fix the source if the rejects matter. |
| status `failed`, `error` = `open source: fetch "/path/nope.csv": open /path/nope.csv: no such file or directory` | The adapter could not open the source (wrong `location`, file missing, no permission). No records written, counts 0. | Correct `location` in the source config or restore the file, re-run. |
| status `failed`, `error` starts `read source: ` or `write batch: ` | Mid-stream read error, or the database rejected a batch. The batch parsed so far is flushed (read errors) and stays; later rows are missing. | Read the rest of the text, fix the cause (file truncated, database full or down), re-run (idempotent). |
| status `failed`, `error` = `cancelled: read source: context canceled` | The caller's context was cancelled (shutdown, Ctrl-C, timeout) while streaming. The row is still closed and the lock released. Rows of batches already flushed stay. | Re-run. If cancellations repeat, find who cancels (deadline too short for the source size). |
| status `failed`, `error` = `panic: boom from adapter` (the text after `panic: ` is the panic value) | A bug in an adapter. The run row is closed as `failed`, the lock released, then the panic continues in the caller (the process may crash: expected). | Not fixable by an operator: collect the run id, the `error` text and the process log (has the stack trace) and escalate. |
| A run row stays `running` after the process is gone | Hard kill / OOM / host loss: nothing could close the row. Known limitation until the abandoned-run sweep of task 009. | See "Run stays `running` forever". |
| Rejects with reason `string contains a NUL byte (\u0000) at $.<field>; not storable as jsonb` | A source value holds a NUL byte. Only that record is rejected (raw = natural key); the batch and run continue. The value is never silently altered. | Clean the value at the source, or accept the reject (counted toward `max_error_ratio`). UNVERIFIED here: covered by the task's unit test, not reproduced by hand. |

### Is the source lock held? (read-only, safe)
Replace `ons-stuck` with the source id (it appears once):
```
psql "$DATABASE_URL" -c "SELECT l.pid, a.state, a.query_start FROM pg_locks l JOIN pg_stat_activity a USING (pid) WHERE l.locktype = 'advisory' AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database()) AND ((l.classid::bigint << 32) | l.objid::bigint) = hashtextextended('data-importer:source:' || 'ons-stuck', 0)"
```
*(real: 0 rows when nothing holds the lock; one row with the holder's `pid` while a session holds it. A live import is the only expected holder. `a.query_start` and `pg_stat_activity` also show the client; the lock is session-level and disappears when that connection closes.)*

## What an aborted (or failed) run leaves behind
Chunks are committed as the stream goes (500 records each by default), not in one transaction. So after `aborted` or a mid-stream `failed`:
1. Rows already written stay in `raw_records`, tagged with the run id (`first_run_id`/`last_run_id`). Real example: aborted run 4 left 642 rows: `SELECT count(*) FROM raw_records WHERE source_id = 'ons-abort' AND first_run_id = 4` gave `642`.
2. The rejects stay in `import_errors` for that run.
3. There is no rollback command yet (out of scope of task 008). The data is valid source data, only the run is flagged.
4. Re-running is idempotent: the second run of the same aborted source gave `inserted=0 updated=0 unchanged=642 failed=20`, still `aborted`, with no duplicate rows (642 rows total) and `last_run_id` moved to the new run. Unchanged rows keep their payload and `imported_at`.
5. A cancelled run before its first batch (run 6 above, cancelled after 99 rows) wrote 0 rows.

## Run stays `running` forever (known limitation)
After `kill -9`, OOM or host loss the row is never closed, and `finished_at` stays NULL. Task 009 adds an abandoned-run sweep; until then:
1. Find candidates with step 3 (old `age`, and no importer process still running on any host).
2. Confirm no live import holds that source's lock: run the lock query above. A killed process's session is gone, so the result must be **0 rows**. If a row appears, a live process (or a hung connection) still holds it: do NOT edit the run; find the `pid` (`SELECT pid, client_addr, state FROM pg_stat_activity WHERE pid = <pid>`) and escalate.
3. Mark it failed by hand (the `status = 'running'` guard makes it a no-op if the run was closed meanwhile):
   ```
   psql "$DATABASE_URL" -c "UPDATE import_runs SET status = 'failed', finished_at = now(), error = 'abandoned: process killed, marked failed by hand' WHERE id = 8 AND status = 'running' RETURNING id, status, finished_at, error"
   ```
   *(real: one row returned with `status` `failed`; run again: `UPDATE 0`)*
4. Re-run the import. Rows the dead run had already flushed are valid; re-running is idempotent. Counts on the hand-closed row stay 0 (they were never saved).

## Verify
1. Step 1 shows the new run `succeeded` (or `unchanged`-only counts for a re-run) with `finished_at` set.
2. Step 3 returns no row for the source and the lock query returns 0 rows.
3. Sanity check that the repo still carries the messages quoted above:
<!-- verify -->
```bash
grep -n -e 'import already running for this source' -e 'import aborted' -e 'above max_error_ratio' -e 'panic: %v' internal/importer/importer.go
```

## Roll back / undo
No rollback command exists (out of scope for task 008). A hand-closed run (`UPDATE` above) is undone with `UPDATE import_runs SET status = 'running', finished_at = NULL, error = NULL WHERE id = <id>` (UNVERIFIED: not run; only do it if the run turns out to be alive). Never delete `raw_records` rows to "undo" an aborted run without the data owner's decision: they are tagged by run id, so `SELECT ... WHERE last_run_id = <id>` shows exactly what a run touched.

## Escalate
Escalate to the importer maintainers when: a panic text appears; the lock query shows a holder but no import should be running; `write batch:` errors repeat; a run is `running` and its lock is held for longer than the source normally takes. Include: source id, run id(s), the `import_runs` row (step 1), the `import_errors` count, the lock query output, and the process log around `started_at`.
