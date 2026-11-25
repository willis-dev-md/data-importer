# ADR-0008: Scheduler loop decisions (task 022)

Status: accepted (task 022)

## Context
Task 021 stored each source's schedule and `next_run_at` ([ADR-0007](0007-schedule-engine.md)). Task 022 adds the loop
that starts due runs inside `importer serve`, so that several replicas run each slot once, and `importer schedule list`.

## Decision
1. **Claim in the database.** A poll claims due sources in one short transaction (`FOR UPDATE SKIP LOCKED`), advances
   `next_run_at` from the scheduled slot and sets `last_scheduled_at`, then commits. The import runs after the commit,
   so the claim never holds a lock for the length of an import. No leader election.
2. **Raw slots for the schedule maths.** `schedule.Plan` gets the raw slot and `now - offset`, never the slot plus the
   jitter offset. The offset (ADR-0007, item 7) moves the start time only. An offset above `SkipGrace` would otherwise
   turn an on-time slot into a missed one under `skip`.
3. **Overlap is skipped and logged, with no run row.** A slot whose source lock is held is skipped with a WARN log.
   Writing a row for each skipped slot would fill `import_runs` while a long run holds the lock.
4. **A pre-run failure leaves a failed row.** If a scheduled run cannot start (lock capacity or wait timeout, missing
   token variable, unreadable source), the scheduler writes a `failed` row with `trigger = 'schedule'` and the slot, so
   the lost slot shows in `import_runs` and in `schedule list`. The error text names the variable, never its value.
5. **No retry.** Unchanged from ADR-0007: a failed or aborted scheduled run is not retried; the next slot is the retry.
6. **Shutdown.** SIGTERM stops claiming. In-flight runs get the serve shutdown timeout (10 s). Then they are cancelled
   and end `failed` with `cancelled: ...`, the same as a manual run on SIGTERM. They do not end `aborted`, which stays
   reserved for `max_error_ratio`. `Run` returns only after every goroutine has been joined.
7. **Sweep on every poll.** Each poll runs `SweepAbandonedRuns` (the rule of task 009 and task 020). A killed replica's
   row is closed by the next poll of any replica, and its slot then runs once. This supersedes the task 022 log entry
   that said serve does not sweep.
8. **Connection budget checked at start.** With the scheduler enabled, `serve` exits 2 when `IMPORT_CONCURRENCY` is larger
   than `DB_LOCK_MAX_CONNS`. This supersedes the earlier plan to leave the check to the operator. Without it, runs
   beyond the lock connections would wait and fail with `ErrLockCapacity`.
9. **Lock unlock waits for the monitor.** `unlock` waits for a lock check in flight and does not cancel it. The earlier
   cancel broke the connection, so the explicit unlock failed and the lock stayed held on the server (a race found by
   the validator). Cost: `unlock` can take up to 10 s on a hung connection, so exit can run past the shutdown grace.
10. **Environment.** `SCHEDULER_ENABLED` (default `true`), `SCHEDULER_POLL_INTERVAL` (default `30s`, 100 ms to 1 h),
    `IMPORT_CONCURRENCY` (default `2`, positive). Invalid values exit 2 like the other settings.

## Consequences
- Operators set `IMPORT_CONCURRENCY` at or below `DB_LOCK_MAX_CONNS` (default `8`). See [operations](../operations.md#scheduler).
- A scheduled slot runs once for the whole database, whichever replica claims it. Each replica still needs its own file
  store and the token variables; cross-host file sharing is not tested.
- Per-host rate limiting is not implemented. Cron is not supported. Both are in the [roadmap backlog](../roadmap.md#hardening-backlog).
- The sweep wiring in `serve` has no serve-level test; a follow-up adds one.
- Test choice (not a product rule): the end-to-end test makes the current minute's slot due by an `UPDATE` while serve
  runs, because schedules have minute granularity. Unit tests use a fake clock and distinct slots.
