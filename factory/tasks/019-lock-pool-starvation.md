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
validated
## Log
(created by the orchestrator from the task 018 worker's report)

### Worker, round 1
- **Reproduced** (`TestConcurrentRunsSmallPool`, `internal/importer/lockpool_test.go`): with the lock connection taken from the work pool (mutation of the new code: also `pool.Acquire` per lock), cases "1 conn, 1 run", "4 conns, 8 runs" and "2 conns, 32 runs" hang until the 90 s test deadline (the gate would hang for 10 min without it).
- **Cause**: `TryAdvisoryLock` held one WORK pool connection for the whole run; the run then needs more work connections (`GetSource`, `CreateRun`, upserts, `FinishRun`). With N >= MaxConns concurrent runs of different sources every connection is a lock holder and all wait forever for a second one.
- **Fix**: lock connections are dedicated `pgx.Conn`s outside the work pool (connected with the pool's `ConnConfig`, closed on unlock so the session drops the lock), bounded by a semaphore in `Store` (`Options.LockConns`). A lock holder only ever waits for work connections, which other runs release, so no cycle exists. Not a second `pgxpool`: nothing to Close and no idle connections left behind in tests.
- **Decisions**: (1) env vars (read by `store.OpenPool`, used by `cmd/importer` `openStore` and `serve`): `DB_MAX_CONNS` work pool size (default `max(4, NumCPU)`, pgxpool's default), `DB_LOCK_MAX_CONNS` concurrent source locks per process (default 8), `DB_LOCK_WAIT` Go duration (default 30s). Invalid values (non-numeric, <= 0) fail startup with a message naming the variable. Total DB connections per process = `DB_MAX_CONNS + DB_LOCK_MAX_CONNS` (document in docs/operations.md; keep below Postgres `max_connections`). (2) No free lock slot: wait up to `DB_LOCK_WAIT` (or until ctx is cancelled), then `store.ErrLockCapacity` ("no lock connection available: all N are in use after waiting 30s"); `importer.Run` returns it wrapped, BEFORE any run row exists (no run row, nothing written); CLI exit code is the generic failure (1), not 3. A cancelled ctx wakes the waiter at once. Runs beyond the slots just queue, so 32 runs with 3 slots complete. (3) `store.New(pool)` keeps its signature (defaults); `NewWithOptions` is new. (4) `TestAdvisoryLockErrorIsNotHeld` changed: a closed work pool no longer fails lock acquisition (the lock does not use it); the error case is now an unreachable database, and it also checks the slot is not leaked.
- **Tests**: importer: `TestConcurrentRunsSmallPool` (1 conn/1 run, 4 conns/8 runs, 4 conns/32 runs/3 slots, 2 conns/32 runs), `TestConcurrentSameSourceOneWinner` (16 runs, one winner, 15 ErrAlreadyRunning, one run row), `TestLockCapacityBoundedWait` (ErrLockCapacity, no run row, cancel wakes waiter), `TestLockSlotFreedOnEveryExit` (single slot; success, failure, abort, cancel, panic each followed by a run that gets the slot; no 'running' rows). store: `TestAdvisoryLockSlotsBounded`, `TestOpenPoolEnv`; existing connection-loss, release and database-scope tests still pass.
- Unsure: the lock connection does not use pool hooks (`BeforeConnect`/`AfterConnect`), none are set today.

### Worker, round 2 (validator FAIL: invalid pool env was exit 1 "connect to database")
- `store.ErrConfig` wraps every invalid `DB_MAX_CONNS` / `DB_LOCK_MAX_CONNS` / `DB_LOCK_WAIT`; `OpenPool` validates the environment BEFORE parsing the URL or connecting. `openStore` (run, sources apply) and `serve` map it to exit 2 with `importer <cmd>: DB_MAX_CONNS="0": want a positive integer` (DB_LOCK_WAIT: `want a positive Go duration such as 30s`). `migrate` does not use the pool: unaffected.
- Safety net: `store.WarnIfOverBudget` (once per process via `sync.Once`, after connect, in openStore and serve) runs `SHOW max_connections` and logs a WARN naming both numbers when `DB_MAX_CONNS + DB_LOCK_MAX_CONNS` exceeds it; never fails; errors ignored. Decision is the pure `ConnectionBudgetWarning` (`TestConnectionBudgetWarning`). `serve` also counts the lock default although it takes no locks (conservative).
- Tests: `cmd/importer/poolenv_test.go` (real binary, DATABASE_URL = closed port; each of 6 invalid values x run/serve/sources apply: exit 2, names the variable and form, no "connect to database", no panic); `TestOpenPoolEnv` checks ErrConfig.
- 2026-11-22 validator r1: FAIL (invalid DB_MAX_CONNS / DB_LOCK_MAX_CONNS / DB_LOCK_WAIT exited 1 with a misleading connect message instead of usage exit 2). Worker round 2: typed store.ErrConfig, exit 2 for run/serve/sources apply, plus a once-per-process max_connections WARN. validator r2: PASS (deadlock reproduced on the old code and fixed; 8 and 32 concurrent runs with pools of 2 and 4; no connection leaks; same-source loser exit 3; ONS 642/unchanged 642, BoE 854/unchanged 854).
- documentor and runbook-refiner (budgets 20/25, evidence packs) ran sequentially. docs-check FAIL: `docs/runbooks/import-failures.md: broken link operations.md#connection-pool-and-source-locks` (a link relative to docs/runbooks/ needs `../`): fixed first with the strong model (runbook-refiner on sonnet); the check and the link gate then passed. The smaller model made a path error here: watch it in the quality guard.
- Pre-existing hole found by the validator, queued as task 020: a lock session killed mid-run releases the lock while the run continues (a second run of the source can start; its sweep marks the live run failed).
- Not verified (documented): TLS for lock connections; ErrLockCapacity from the CLI (unreachable: one run per process); abort/panic lock release through the real binary (unit tests only).
