# Runbook: Scheduled imports (the scheduler inside `importer serve`)

**Owner role:** on-call engineer  |  **Severity if this happens:** medium (stale data for the scheduled sources; other sources are unaffected)
**Last verified:** 2026-11-24, task 022 (real binary on a throwaway Postgres 16 from `scripts/testdb.sh`: `sources apply` of `docs/examples/uk-hpi-average-prices.source.yaml` with and without `enabled: false`, `importer schedule list` output, and the `serve` env exit codes; the scheduled run itself, the kill -9 sweep, failed-run and replica results are the validator's evidence pack, UNVERIFIED here)

## When to use
- You want a source to run by itself at a set time, or check when it runs next.
- You want to pause a scheduled source, or run it now.
- `importer serve` exits 2 at start, or a scheduled source did not run as expected.

## Prerequisites
- `DATABASE_URL` in the environment (never paste it into tickets); schema version 3 (`importer migrate up`, see [database-migrations.md](database-migrations.md)).
- The schedule rules (`every`, `at`, `timezone`, `on_missed`, `jitter`, `enabled`): [onboarding-a-new-source.md](onboarding-a-new-source.md#schedule-optional).
- `serve` runs the scheduler by default. Its **own** environment needs `DATABASE_URL`, a file store (`FILE_STORE_DIR`, default `data/files` relative to the working directory: set an absolute path) and every secret named by a source's `token_env`. A missing secret fails that run (see [import-failures.md](import-failures.md)).
- Scheduled runs use the csv and xlsx adapters only (`http_json` has no adapter yet).

## Steps
1. Add a `schedule:` block to the source file and store it:
   `bin/importer sources apply --dir <dir>`
   *(real, throwaway database: `uk-hpi-average-prices: source created, mapping none, next run 2026-12-20T10:00:00Z`, exit 0)*
2. Check when each source runs next:
   `bin/importer schedule list`
   *(real output, one source with a monthly schedule, not yet run:)*
   ```
   ID                     ENABLED  SCHEDULE                    TIMEZONE       NEXT_RUN_AT           LAST_SCHEDULED_AT  LAST_RUN
   uk-hpi-average-prices  true     monthly on day 20 at 10:00  Europe/London  2026-12-20T10:00:00Z  -                  -
   ```
   Columns: `NEXT_RUN_AT` is the next unhandled slot (UTC); `LAST_SCHEDULED_AT` is the last slot that started a run; `LAST_RUN` is the latest run's status and trigger, for example `succeeded (schedule)` or `failed (schedule)`. A source without a schedule shows `manual`; `-` means none. `schedule list` only reads the database and works without `serve`.
3. Make sure a `serve` process is running with the scheduler on (default). Its log says `scheduler started` with `poll_interval` and `concurrency` (wording from the code; the output line itself is UNVERIFIED here).
4. Pause a source: set `enabled: false` in its source file, then `bin/importer sources apply --dir <dir>`.
   *(real, throwaway database, the source applied with `enabled: false`: `uk-hpi-average-prices: source created, mapping none, schedule cleared (manual only or disabled)`; then `schedule list` showed `false  monthly on day 20 at 10:00  Europe/London  -  -  -`. The re-apply of an already stored source with `enabled: false` was not run here.)*
5. Resume: remove `enabled: false` (or set `enabled: true`), `sources apply` again, and check step 2. UNVERIFIED: not run.
6. Run a source now, outside its schedule: `bin/importer run --source <id>` (trigger `manual`). Exit 3 means a run of that source is already running (see the troubleshooting table). UNVERIFIED here: whether a manual run moves `next_run_at`.
7. Several replicas: start any number of `serve` processes on the same database. Each due slot starts exactly one run (the claim is in the database). A replica that should only answer gRPC sets `SCHEDULER_ENABLED=false`.

### Settings (serve environment)
| Variable | Default | Accepted | Invalid |
|---|---|---|---|
| `SCHEDULER_ENABLED` | `true` | `true` or `false` | exit 2 |
| `SCHEDULER_POLL_INTERVAL` | `30s` | Go duration from `100ms` to `1h` | exit 2 |
| `IMPORT_CONCURRENCY` | `2` | positive integer, at most `DB_LOCK_MAX_CONNS` (default `8`) | exit 2 |

`IMPORT_CONCURRENCY` is the number of scheduled runs at once in one replica. Each run holds one lock connection, so keep it at or below `DB_LOCK_MAX_CONNS`. The check against `DB_LOCK_MAX_CONNS` applies only when the scheduler is enabled.

## Troubleshooting
| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| `importer serve: SCHEDULER_POLL_INTERVAL="50ms": want a Go duration between 100ms and 1h0m0s such as 30s` (exit 2; the same for `2h` and `abc`) | The value is outside 100ms to 1h or not a Go duration. Checked before any database connection (real run, `DATABASE_URL` on a closed port). | Unset it or set a value such as `30s`, restart `serve`. |
| `importer serve: IMPORT_CONCURRENCY="0": want a positive integer` (exit 2; the same for `x` and `-1`) | Not a positive integer. Checked before any database connection (real run). | Unset it or set a positive integer at most `DB_LOCK_MAX_CONNS`, restart. |
| `importer serve: SCHEDULER_ENABLED="maybe": want true or false` (exit 2) | Not a boolean (real run). | Set `true` or `false`, restart. |
| `importer serve: IMPORT_CONCURRENCY=9 is more than DB_LOCK_MAX_CONNS=8: raise DB_LOCK_MAX_CONNS or lower IMPORT_CONCURRENCY` (exit 2, from the code; the output was not run here, UNVERIFIED) | More concurrent scheduled runs than lock connections. Runs would wait and lose slots. | Lower `IMPORT_CONCURRENCY` or raise `DB_LOCK_MAX_CONNS` (keep the total under the server's `max_connections`, see [import-failures.md](import-failures.md)), restart. |
| `LAST_RUN` shows `failed (schedule)` | A scheduled run failed. It is never retried: `next_run_at` already points to the next slot. | See [import-failures.md](import-failures.md) "A scheduled run failed". |
| `NEXT_RUN_AT` shows `-` and `ENABLED` shows `false` | Paused on purpose (`enabled: false`). Real: see step 4. | Set `enabled: true`, `sources apply` (step 5). |
| `NEXT_RUN_AT` shows `-` and `SCHEDULE` shows `manual` | The source has no `schedule:` block. | Add a schedule (onboarding-a-new-source.md) or run it by hand. |
| `NEXT_RUN_AT` is in the past and `LAST_SCHEDULED_AT` does not move | No `serve` with the scheduler is running for this database, or `SCHEDULER_ENABLED=false` on all replicas. UNVERIFIED here. | Start `serve` with the scheduler on. Check the process and its environment. The next poll claims due sources. |
| Log `scheduler: slot skipped, the previous run of this source is still running source=<id> slot=<slot>` (WARN) | The previous run of that source still holds its lock, so this slot is not run. No `import_runs` row is written for a skipped slot. | See [import-failures.md](import-failures.md) "Overlap skipped". Normal while a long import runs. |
| Log `scheduler: missed slots passed over (on_missed: skip)` or `scheduler: running the latest missed slot once; older ones passed over` (WARN) | Downtime: slots were missed. `skip` runs none; `run_once` runs the latest once. | See [import-failures.md](import-failures.md) "Missed slots after downtime". |
| SIGTERM: log `scheduler shutdown: cancelling runs still in flight`, then runs end `failed` with `cancelled: ...` | Runs did not finish within the 10 s shutdown grace (`server.ShutdownTimeout`). They are cancelled like a manual run; the next slot is the retry. | Nothing, if shutdown is intended. If runs are always cut off, find the slow source. |
| Process exit about 10 s later than the grace period after SIGTERM | A lock check was in flight on a hung connection; releasing the lock waits for it (at most 10 s). | Nothing: it ends by itself. Escalate if it repeats with a healthy database. |

## Verify
1. `bin/importer schedule list` shows `ENABLED true`, a `NEXT_RUN_AT` in the future, and `LAST_RUN` `-` or the last status (step 2).
2. Source file checked: `bin/importer sources apply --dir <dir>` prints `next run ...` for an enabled schedule (step 1).
3. Constants in the code still match this runbook (grep):
<!-- verify -->
```bash
test -n "$(grep -e 'slot skipped, the previous run of this source is still running' internal/scheduler/scheduler.go)" && test -n "$(grep -e 'SCHEDULER_POLL_INTERVAL' internal/scheduler/scheduler.go)" && test -n "$(grep -e 'abandoned (process died)' internal/store/mappings.go)" && echo ok
```
*(expected: `ok`)*

## Roll back / undo
- Stop the scheduler on one replica: `SCHEDULER_ENABLED=false` and restart it. Other replicas keep running due slots.
- Pause a source: `enabled: false` and `sources apply` (step 4).
- Restore: remove the flag, `sources apply`, check `schedule list`. UNVERIFIED: not run.

## Escalate
Escalate to the importer maintainers when: a scheduled source has `failed (schedule)` on consecutive slots with the same error; two runs of one source overlap in `import_runs`; `NEXT_RUN_AT` does not move after a slot passed; or a panic appears in the `serve` log. Include: source id, the `schedule list` output, the `import_runs` rows (`trigger`, `scheduled_for`, `status`, `error`), the `serve` log around the time, and the names (never values) of the `DB_*`, `SCHEDULER_*` and `IMPORT_CONCURRENCY` variables in use.
