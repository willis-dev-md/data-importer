# 019 – Concurrent runs must not deadlock on pool exhaustion

## Goal
The per-source advisory lock must not consume connections from the pool the run itself needs, so many concurrent runs (a scheduler will start them) can never deadlock.

## Depends on
018

## Design refs
`docs/design.md` (Import flow, failure handling), `docs/data-model.md` (decisions for tasks 003 and 008), the reproduction in the task 018 Log (4 concurrent runs with the default pool deadlock on pool exhaustion)

## Scope
`internal/store/` (lock acquisition, pool configuration), `internal/importer/`, `cmd/importer` (pool construction only), tests, plus the task Log

## Out of scope
The scheduler itself; changing the advisory-lock keying; new adapters.

## Acceptance criteria (each must be checkable)
- [ ] REPRODUCE first (from the task 018 Log): N concurrent `importer.Run` calls for N different sources with the default pool settings deadlock (each holds a dedicated lock connection and then waits for a work connection). State the cause in the Log.
- [ ] Fix at the root: lock connections come from a separate, bounded pool (or another structure that cannot starve the work pool); work pool size and lock pool size are configurable (env vars documented) with safe defaults; a run that cannot get a lock connection fails fast or waits with a bounded, documented timeout (never deadlocks).
- [ ] Test (real Postgres, -race, deterministic, no sleeps-and-hope): 8 and 32 concurrent runs for distinct sources with a SMALL pool (e.g. max 4) all complete; two runs of the SAME source still yield exactly one winner and `ErrAlreadyRunning` for the other; the lock is still released on success, failure, abort, cancel, panic and connection loss.
- [ ] No regression: ONS runs 642 / unchanged 642 / updated 1 + unchanged 641; BoE 854 then unchanged 854; the task 008/009/018 tests still pass; the advisory lock still scopes to its own database (docs/testing.md isolation rule).
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/design.md (failure handling / concurrency); docs/operations.md (pool size env vars); docs/data-model.md decisions; CHANGELOG.md

## Runbook impact
docs/runbooks/import-failures.md (a run that waits or fails for lack of a lock connection: symptom, diagnosis, fix)

## Status
pending
## Log
(created by the orchestrator from the task 018 worker's report)
