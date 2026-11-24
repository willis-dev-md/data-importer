# 020 – A run must notice when it loses its source lock

## Goal
If the session holding a run's source advisory lock dies mid-run, the run must stop with a clear error instead of continuing unprotected (two runs of one source writing concurrently, and the abandoned-run sweep failing a live run).

## Depends on
019

## Design refs
`docs/design.md` (failure handling, locking), `docs/data-model.md` (decisions for tasks 008, 009, 019), `docs/runbooks/import-failures.md`; the task 019 Log (the validator's reproduction: terminating the lock session with `pg_terminate_backend` mid-run released the lock, the run continued, a second run of the same source started and succeeded, its sweep marked the still-running run failed, both wrote the same source)

## Scope
`internal/store/` (lock monitoring), `internal/importer/`, tests, plus the task Log

## Out of scope
The scheduler; changing the lock keying; retry/resume of an aborted run.

## Acceptance criteria (each must be checkable)
- [ ] REPRODUCE first (from the task 019 Log) with a test: kill the lock session of a running import (scoped to the test database and the lock key); today the run continues and a second run of the same source can start.
- [ ] Fix: the holder watches its lock connection (a periodic cheap check, or the connection's own failure signal) and when the lock is lost the run is cancelled and finishes as `failed` with a clear error (e.g. `lost source lock (session terminated)`); batches already committed stay tagged with the run id as for other failures; the check interval is bounded and configurable by an env var with a safe default, documented.
- [ ] No false alarms and no big cost: a healthy 200k-record run is not slowed measurably and never reports a lost lock; the monitor goroutine is joined on every exit (no goroutine leak), also on panic and cancellation.
- [ ] Mutual exclusion window is stated honestly in the Log: between the loss and the detection (at most one check interval) another run could start; the sweep must not mark a LIVE run failed when its lock is merely being re-checked (state the rule and test it).
- [ ] Tests (real Postgres, -race, deterministic, polled deadlines, no sleep-and-hope): lock-session kill mid-run => run `failed` with the error; a second run of the same source started after the kill either gets ErrAlreadyRunning (if detection already stopped the first) or is allowed only after the first run is finished as failed; ONS 642/unchanged 642 and BoE 854/unchanged 854 still hold.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/design.md (locking and failure handling); docs/operations.md (env var); docs/data-model.md decisions; CHANGELOG.md

## Runbook impact
docs/runbooks/import-failures.md (symptom `lost source lock`, diagnosis, fix, and the earlier known-limitation text removed)

## Status
validated
## Log
(created by the orchestrator from the task 019 validator's finding)

### Worker round 1
- Reproduction: `internal/importer/lockloss_test.go` `TestRunStopsWhenLockSessionIsTerminated` kills the lock session (`pg_terminate_backend`, scoped to this database and the source's lock key) mid-run. Without the monitor the run is never cancelled (the test's polled deadline fires) and a second run can start (`TestSecondRunDuringDetectionWindow`, monitor effectively off, shows exactly that).
- Fix: `store.TryAdvisoryLockMonitored(ctx, id, onLost)` = `TryAdvisoryLock` plus one goroutine that every `LockCheckInterval` runs one `pg_locks` lookup on the lock connection itself (session alive AND this session holds the key). A failed query or a missing row calls `onLost(error wrapping store.ErrLockLost)` once and the monitor ends. `unlock` cancels the monitor and WAITS for it before releasing the lock (join on success, failure, cancel and panic, since `Run` defers unlock). `importer.Run` (real runs only) derives a `WithCancelCause` context; `onLost` cancels it. After the run, a lost lock outranks every other outcome (also abort or success at the last moment): status `failed`, error `lost source lock (session terminated): <driver error> (<what the run was doing>)`. Committed batches stay, tagged with the run id. `importer.Store` interface: `TryAdvisoryLock` replaced by `TryAdvisoryLockMonitored` (`store.TryAdvisoryLock` still exists, unmonitored, for the CLI/tests).
- Config: `DB_LOCK_CHECK_INTERVAL` (Go duration, default 5s, allowed 10ms..10m; anything else is `store.ErrConfig`, exit 2 like the other `DB_*` vars: `want a Go duration between 10ms and 10m0s such as 5s`). `store.Options.LockCheckInterval` (0 = default; clamped).
- Cost: one indexed-free `pg_locks` view read per interval per running import (default 1 per 5s) on the dedicated lock connection; nothing on the work pool, nothing per record.
- Mutual-exclusion window (honest): between the loss and its detection (at most one interval + the check round trip, default about 5s) the first run still writes and its lock is gone, so a second run of the same source CAN start in that window (shown by `TestSecondRunDuringDetectionWindow`). Once the first run has detected the loss it is cancelled and finishes failed within one batch; a second run started after that is legitimate. A check that times out (10s) or hits a network failure counts as lost (safe side: the run stops; the lock may in fact still be held until Postgres notices the dead session).
- Sweep rule: `SweepAbandonedRuns` fails a `running` row only when its source lock is NOT held. The monitor's check is a read-only query on the holder's own session and never releases the lock, so a live, healthy run is never swept (`TestHealthyRunNoFalseAlarmAndSweepLeavesLiveRun`: sweeps in bursts every 20 records with 10ms checks, never touches the run, no lost lock, 400/400 inserted). In the gap after a loss a sweep may mark the row `abandoned (process died)`; the run's own `FinishRun` then overwrites it with the lost-lock error (final state wins, tested: row ends `failed` with `lost source lock`, not `abandoned`).
- Goroutine joins: `TestMonitoredLockReportsLoss` (store) and `TestLockMonitorJoinedOnEveryExit` (success, failure, cancel, panic) assert no `monitorLock` goroutine in `runtime.Stack` right after unlock/return.
- Tests are real Postgres, `-race`, no sleeps (polled deadlines); ONS 642 / BoE 854 e2e untouched and passing. `cmd/importer/poolenv_test.go` gained 3 invalid `DB_LOCK_CHECK_INTERVAL` cases (exit 2 for run, serve, sources apply).
- Docs the documentor must say: design.md locking/failure handling (monitor, `lost source lock (session terminated)`, window, sweep rule above); operations.md `DB_LOCK_CHECK_INTERVAL` (default 5s, range 10ms..10m, exit 2 if invalid, the 10s check timeout, one cheap query per interval); data-model.md decisions (monitored lock, final state beats the sweep, lost lock outranks abort/success); CHANGELOG.md; runbook import-failures.md: symptom `lost source lock (session terminated)` (cause: lock session killed, DB restart/failover, network drop or idle-session killer; diagnosis: run row `failed`, error text, server log; fix: re-run, check why the session died, partial batches stay tagged with the run id and a re-run is idempotent), and remove the earlier known-limitation text.
- Validator round 1: PASS (kill test fails without the cancel; real binary: failed + exit 1, 160000 rows kept run id, rerun ok; no measurable cost). Docs roles ran; docs-check OK; gate OK.
