<!-- factory-kit: customized (Go) -->
# 022 – Scheduler loop inside `importer serve`

## Goal
`importer serve` runs due scheduled imports on time, safely with several replicas, and `importer schedule list` shows each source's next run.

## Depends on
021

## Design refs
`docs/design.md` (Scheduler), `docs/operations.md` (serve, locks, DB_* env, lock monitor), task 019/020 Logs (lock pool, lock loss), task 021 Log, `docs/runbooks/import-failures.md`

## Decisions already made by the user
- A failed run is NOT retried; the next slot is the retry. Default timezone Europe/London.

## Scope
`internal/scheduler/` (new), `internal/store/` (claiming queries), `internal/importer/` (trigger and scheduled_for passed to Run), `cmd/importer/` (serve wiring, `schedule list`, env), tests, task Log

## Constraints from task 021
- `schedule.Plan` must be called with the RAW slot/now, not slot+jitter offset: with `on_missed: skip` an offset over `SkipGrace` (5m) would count the slot as missed and skip it. Apply the offset only to the actual start time.
- Schema version is now 3 (migration 0003_schedule).

## Out of scope
cron expressions; metrics endpoint (separate task); retries; per-host rate limiting (document as not implemented); source groups.

## Acceptance criteria (each must be checkable)
- [ ] Loop: every `SCHEDULER_POLL_INTERVAL` (default 30s, range 100ms..1h, invalid => exit 2 like other env) it claims due sources (`enabled AND next_run_at <= now`) with `SELECT ... FOR UPDATE SKIP LOCKED` in a short transaction that advances `next_run_at` (from the scheduled slot via `schedule.Plan`, never the finish time) and sets `last_scheduled_at`, then runs the import with `trigger='schedule'` and `scheduled_for` = the slot. The claim transaction never holds a lock for the duration of the import.
- [ ] Concurrency: at most `IMPORT_CONCURRENCY` (default 2, positive integer, invalid => exit 2) scheduled runs at once; jitter offset from 021 applied to the start time; overlap policy skip: a source whose lock is held (running) is not started again and a skipped slot is recorded in the run log or error text (no duplicate run row storm).
- [ ] Failures: a failed or aborted scheduled run is NOT retried; `next_run_at` already points to the next slot. One bad source never stops the loop; a panic in a run is recovered as today.
- [ ] Missed slots: after downtime `run_once` runs once for the latest missed slot (`scheduled_for` = that slot), `skip` runs none; verified with a fake clock and a test that stops/starts the scheduler.
- [ ] Multiple replicas: a test with 3 schedulers over one database and one due source starts exactly ONE run for the slot (assert run rows and `scheduled_for`).
- [ ] Graceful shutdown: SIGTERM stops claiming, lets in-flight runs finish within the existing shutdown deadline (or cancels them and finishes them `aborted` as manual runs are), joins every goroutine (leak test), exit 0; existing serve behaviour (health, TLS, gRPC) unchanged and `SCHEDULER_ENABLED=false` (default true) lets a replica serve without scheduling.
- [ ] `importer schedule list` prints id, enabled, schedule summary, timezone, `next_run_at`, `last_scheduled_at` and the last run status; exit 0; works without `serve`.
- [ ] End-to-end test with the real binary: a source whose schedule is due in about 1s runs by itself against a local TLS server and the fixture, `trigger=schedule`, 642 inserted; the second slot yields `unchanged_file`; serve stays healthy.
- [ ] Time is injected (no sleep-and-hope in tests; polled deadlines); `-race`; ONS 642 / BoE 854 / HPI / ASHE e2e counts unchanged; `scripts/validate.sh` passes

## Docs impact
docs/design.md (Scheduler as built; what is not implemented); docs/operations.md (serve, `SCHEDULER_ENABLED`, `SCHEDULER_POLL_INTERVAL`, `IMPORT_CONCURRENCY`, `schedule list`, replicas); docs/data-model.md decisions; docs/roadmap.md (M7 scheduler done, services remaining); README (status, usage); CHANGELOG.md

## Runbook impact
docs/runbooks/import-failures.md (a scheduled run failed, missed run, overlap skipped) and a new docs/runbooks/scheduled-imports.md (set up a schedule, check next runs, pause with `enabled: false`, run now, replicas, shutdown); update docs/runbooks/README.md index

## Status
validated
## Log
(created by the orchestrator)

### Worker, round 1
Files: new `internal/scheduler/{doc,scheduler,scheduler_test}.go`, `internal/store/schedule.go` (ClaimDue, ListSchedules; no store test file, covered through the scheduler tests), `internal/store/store.go` (`CreateRunFor`, `Run.ScheduledFor`), `internal/importer/importer.go` (`Options.ScheduledFor`; `Store` interface `CreateRun` -> `CreateRunFor`), `cmd/importer/{serve,schedulecmd,runcmd,main}.go`, `cmd/importer/scheduler_cli_test.go`, `cmd/importer/serve_test.go` (FILE_STORE_DIR temp dir). go.mod untouched, no new dependency.

What it does:
- `store.ClaimDue(ctx, now, max)`: one short tx, `SELECT id, schedule, next_run_at ... WHERE enabled AND schedule IS NOT NULL AND next_run_at <= now ORDER BY next_run_at,id FOR UPDATE SKIP LOCKED`, per row `schedule.Plan`, then `UPDATE sources SET next_run_at = <slot after the scheduled one>, last_scheduled_at = <slot>`; commit; the import starts after the commit (no lock held during the import). At most `max` rows with a slot to run are claimed; the rest stay due for the next poll. Rows whose slots were only passed over (`skip`) are advanced and do not use a run slot.
- Jitter (constraint from 021): a row is claimed once `next_run_at + Offset(id, jitter) <= now`, and `Plan` is called with `now - offset` (raw slots), so a `skip` source with an offset over SkipGrace is not treated as missed. A not-yet-due-because-of-jitter row is not written.
- Plan history: `Plan(spec, next_run_at - 1ns, now - offset)`, not `last_scheduled_at`. next_run_at is the first unhandled slot, so after a schedule change the old schedule's slots are never counted as missed. `last_scheduled_at` is only set when a slot runs.
- `scheduler.Scheduler{Store, Run, Clock, PollInterval, Concurrency, ShutdownGrace, Logger}`: `Run(ctx)` polls once at once and then every PollInterval (clock ticker), `Poll`/`Wait` exposed for tests. Runs are goroutines under a semaphore of IMPORT_CONCURRENCY; Poll claims only the free count. Failed/aborted runs are not retried (next slot is the retry). Panic in a run is recovered, counted and logged, loop continues (note: `importer.Run` still closes the row failed and re-panics, the scheduler goroutine recovers it).
- Overlap: `importer.ErrAlreadyRunning` => slot skipped, NO run row, logged at WARN: `scheduler: slot skipped, the previous run of this source is still running source=<id> slot=<slot>`. Skipped/passed-over slots are logged (`passed over`) and counted in `Stats`. Nothing else records them in the database.
- Shutdown: ctx cancel stops claiming; in-flight runs get `ShutdownGrace` (serve passes `server.ShutdownTimeout`, 10s); after that the run context is cancelled and the runs finish `failed` with `cancelled: ...` exactly like a manual run on SIGTERM (not `aborted`; aborted is reserved for max_error_ratio); `Run` returns only after the WaitGroup is done. serve starts the scheduler next to the gRPC server, stops it when the server stops or fails and always joins it.
- Env (exit 2, same style as DB_*): `SCHEDULER_ENABLED` (bool, default true), `SCHEDULER_POLL_INTERVAL` (Go duration 100ms..1h, default 30s), `IMPORT_CONCURRENCY` (positive int, default 2). With the scheduler enabled serve opens the file store (`FILE_STORE_DIR`, default `data/files`, same rules and message as `run`; exit 2 if unusable); with `SCHEDULER_ENABLED=false` nothing changes from before.
- Scheduled run (cmd): loads the source, resolves `auth` secrets from the environment like `importer run` (a missing secret fails that run, logged, no row), csv/xlsx adapters, file store, `Trigger: schedule`, `ScheduledFor: slot`. Same limitation as `run`: http_json has no adapter yet.
- `importer schedule list`: tab table `ID ENABLED SCHEDULE TIMEZONE NEXT_RUN_AT LAST_SCHEDULED_AT LAST_RUN`, UTC RFC3339, `-` for none, `manual` for a source without schedule, LAST_RUN = status of the latest run plus `(manual|schedule)`. Read-only, works without serve. Exit 0; 1 database error; 2 usage/env.

Tests (all real Postgres, fake clock, polled deadlines, no sleep-and-hope; `-race`): settings table; one run per slot with trigger/scheduled_for/next/last (642 inserted, then unchanged_file); disabled and manual-only not claimed; failed run not retried and next slot retries, a bad source does not stop a good one; missed slots run_once/skip with the scheduler stopped and started again; jitter (offset > SkipGrace, skip, not claimed before slot+offset); 3 replicas x 5 slots exactly one run each (run rows and scheduled_for); overlap skip; concurrency limit 2 of 5; panic recovered; shutdown joins goroutines (grace open/expired) + real run cancelled closes row failed; unreadable stored schedule reported without blocking others. cmd: invalid env exit 2, usage, summary text, and the real-binary e2e (local TLS server, SSL_CERT_FILE, serve + `schedule list` + health + SIGTERM exit 0).

Decisions (not in the docs):
- Schedules have minute granularity, so the e2e cannot wait for a real slot in about 1s. It makes today's slot (the current minute, UTC) due with `UPDATE sources SET next_run_at = <slot>` while serve runs; the scheduler claims and runs it by itself. The "second slot" is the same slot made due again (so two runs share scheduled_for); the unit tests cover distinct consecutive slots with the fake clock.
- The ONS example has no `change_detection`; the tests add `change_detection: [content_hash]` to get `unchanged_file` (as BoE/HPI/ASHE examples do).
- Scheduled runs that are cancelled at shutdown end `failed` (`cancelled: ...`), not `aborted`, consistent with manual runs.
- serve does not call SweepAbandonedRuns (only `run`/`reparse` do); not in scope.
- `IMPORT_CONCURRENCY` is not checked against `DB_LOCK_MAX_CONNS` (default 8); a value above it just makes runs wait for a lock slot (DB_LOCK_WAIT) and fail with ErrLockCapacity. Docs should say to keep it <= DB_LOCK_MAX_CONNS.
- Clocks of replicas are used for `now`; replicas with skewed clocks claim at slightly different times but still exactly once (the claim is in the database).
- Per-host rate limiting not implemented (documented as such by the docs roles).

Docs/runbooks the documentor / runbook-refiner must say:
- design.md Scheduler as built: claim tx (`FOR UPDATE SKIP LOCKED`, advance next_run_at from the slot, set last_scheduled_at, import after commit), Plan with next_run_at-1ns and now-offset, jitter applies to start time only, overlap skip (logged only, no run row), no retry, shutdown behaviour (grace = 10s, then cancelled runs end failed), not implemented: cron, retries, per-host rate limiting, source groups, metrics endpoint.
- operations.md: serve runs the scheduler by default; `SCHEDULER_ENABLED`, `SCHEDULER_POLL_INTERVAL` (100ms..1h, 30s), `IMPORT_CONCURRENCY` (default 2, keep <= DB_LOCK_MAX_CONNS), invalid => exit 2; serve needs `FILE_STORE_DIR` and source secrets in ITS environment; replicas: any number, one run per slot, set `SCHEDULER_ENABLED=false` on replicas that should only serve; `importer schedule list` columns; replace the "scheduling does not run yet" text.
- data-model.md decisions: claim query, `scheduled_for` = slot, `last_scheduled_at` only set when a slot runs, next_run_at is the Plan baseline.
- roadmap.md M7 scheduler done, services remaining; README status/usage (`schedule list`, new env vars); CHANGELOG.md.
- Runbooks: import-failures.md (a scheduled run failed: find with `schedule list` / run rows `trigger=schedule`, not retried, next slot or `importer run`; missed run after downtime; `slot skipped, the previous run ... still running` log line, no row; failed `cancelled` at shutdown); new scheduled-imports.md (set up schedule in the source file + `sources apply`, `schedule list`, pause with `enabled: false` + apply, run now with `importer run`, replicas, shutdown, serve env); update runbooks/README.md index. Also uk-hpi-monthly-update.md's "scheduler is not implemented" is now false.

### Worker, round 2 (validator FAIL round 1)
(a) Sweep: `scheduler.Config.Sweep` (serve passes `store.SweepAbandonedRuns`) runs at the start of every poll, including the first one at scheduler start; failures only logged, found rows logged at WARN (`marked abandoned runs failed`). It only fails rows whose source lock is not held (existing rule), so a live replica's run is never touched. Test `TestSweepsRunOfKilledReplicaThenSlotRuns`: row `running` + a lock session, polls leave it alone, `pg_terminate_backend` of the lock session (the kill -9 effect), a poll closes it `failed` / `abandoned (process died)`, the slot then runs exactly once.
(b) `serve` exits 2 when `IMPORT_CONCURRENCY` > `DB_LOCK_MAX_CONNS` (default 8; only when the scheduler is enabled): `importer serve: IMPORT_CONCURRENCY=N is more than DB_LOCK_MAX_CONNS=M: raise DB_LOCK_MAX_CONNS or lower IMPORT_CONCURRENCY` (before connecting; tests explicit and default budget). New `store.RecordFailedRun`; `scheduler.Config.Fail` is called when a scheduled run returns an error with no run row (RunID 0, not ErrAlreadyRunning): lock capacity/wait timeout, GetSource error, bad stored config, missing secret env, cancelled before start. It writes a finished `failed` row (trigger=schedule, scheduled_for=slot, error text, which names the variable but never a secret), so the lost slot shows in import_runs and `schedule list` (`failed (schedule)`). Never retried (next_run_at already advanced). Tests: `TestPreRunFailuresAreRecorded` (lock capacity with LockConns=1 and a stub GetSource error), `TestScheduledRunMissingSecretIsRecorded` (cmd, real `scheduledRun` with an unset token_env, then `schedule list`), `TestServeRejectsConcurrencyAboveLockBudget`. The ErrAlreadyRunning overlap skip still writes no row (by design, logged only).
(c) Root cause of both flakes was a CODE race in `store.monitorLock`/unlock, not the tests: `unlock` cancelled the monitor's context, and a lock check in flight at that moment was cancelled mid-query, which breaks the pgx connection. The explicit `pg_advisory_unlock` then failed and the lock stayed held on the server until it noticed the closed socket, so a run started right after (next replica-test day, or the panic run in `TestLockMonitorJoinedOnEveryExit`) saw "already running" and returned without a row/panic ("1 runs, want 2", "panic was swallowed"). Fix: a check is no longer cancelled by unlock (`context.WithoutCancel`, still bounded by the 10s check timeout), unlock waits for it and then unlocks on a healthy connection. Cost: unlock can take up to the in-flight check (normally ms; at most 10s on a hung connection). The replica test now prints runs, next/last, stats and logs on failure. 50x `-race -p 8` under background `go test -race` load: TestThreeReplicasOneRunPerSlot, the sweep and pre-run tests, TestLockMonitorJoinedOnEveryExit and the other lock-monitor tests all green.
Docs/runbooks additionally: design/operations: sweep runs at every scheduler poll; `IMPORT_CONCURRENCY` must be <= `DB_LOCK_MAX_CONNS` (exit 2); a scheduled run that fails before starting leaves a failed `schedule` run row with the error (lock capacity, missing token env ...); runbook import-failures: how to find them (`schedule list` LAST_RUN failed (schedule), error text), kill -9 leaves a row that is closed `abandoned (process died)` by the next poll of any replica; locking text: unlock waits for an in-flight monitor check.

### Worker, round 3 (validator round 2: two test races)
- `TestShutdownJoinsEverything`: the goroutine check ran right after `Run` returned, while Run's helper goroutine was still finishing; it now polls (`eventually`) until no scheduler goroutine remains (30s bound). It was the only single-check goroutine assertion in the new tests (the importer lock-monitor checks are made after `unlock` joined the monitor, so they are deterministic).
- `TestServeRunsScheduledSourceByItself`: the run row is closed before the source lock is released, so making the slot due again at once was (correctly) skipped as "previous run still running". The test now polls `pg_locks` (this database, this source's key) until the lock is free before the second due-UPDATE. No other test makes a slot due again right after a run (the scheduler tests use `Wait`, which joins the run including its unlock).
- Config-error tests for `serve` (`TestServeRejectsBadSchedulerEnv`, `TestServeRejectsConcurrencyAboveLockBudget`) now use `serveFast`: unreachable DATABASE_URL, `GRPC_ADDR=127.0.0.1:0`, 3s context, and they assert the exit 2 came in under 2s. If a check is removed, serve runs until the deadline and the test fails in about 3s instead of hanging to the 30s test timeout.
- For the documentor: `unlock` can wait up to 10s (lockCheckTimeout) for a lock check in flight on a hung connection, so SIGTERM during a hung check can delay exit up to 10s past the shutdown grace period. Mention in operations.md (shutdown) and design.md (locking).
- Validator round 3: PASS (rounds 1-2 FAIL: no sweep in serve, lost slots above lock budget, flaky tests; all fixed). Known gaps (documented): no serve-level test of the sweep wiring; per-host rate limiting not implemented; cron unsupported. Docs roles ran (relaunched once after a container restart); docs-check OK; gate OK.
