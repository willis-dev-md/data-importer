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
pending
## Log
(created by the orchestrator from the task 019 validator's finding)
