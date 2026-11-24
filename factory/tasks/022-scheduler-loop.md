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
pending
## Log
(created by the orchestrator)
