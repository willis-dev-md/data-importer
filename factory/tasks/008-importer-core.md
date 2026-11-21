# 008 – Import run orchestration

## Goal
`importer.Run` streams a source into `raw_records` as one tracked, idempotent, concurrency-safe run.

## Depends on
003, 005

## Design refs
`docs/design.md` (Import flow, §9 Failure handling), `docs/data-model.md`, `docs/testing.md`

## Scope
`internal/importer/`, `internal/store/` (additions), tests

## Out of scope
Tombstoning (`missing_policy: tombstone`), rollback command, scheduler, file retention/reparse, XLSX/API adapters, change detection by ETag.

## Acceptance criteria (each must be checkable)
- [ ] `importer.Run(ctx, Deps{Store, Adapters}, sourceID, Options{Trigger, DryRun}) (RunResult, error)`: takes the per-source advisory lock (a second concurrent run returns `ErrAlreadyRunning` and creates no run row), creates the `import_runs` row, streams records in batches (default 500, configurable), upserts, and finishes the run with counts, `shape` and `source_meta` (from `Stream.Meta()`).
- [ ] `shape` records, per field name, the observed value types and counts (e.g. `{"value":{"string":642},"period":{"string":642}}`), excluding `_`-prefixed keys. Unit-tested.
- [ ] `TryAdvisoryLock` errors are NOT treated as "already running": `ErrAlreadyRunning` only when the lock is held (ok=false, err=nil); a database error fails the call with that error (tested with a failing store).
- [ ] Records whose strings contain a NUL byte (`\u0000`, which jsonb rejects and would fail a whole batch) are handled per record: sanitised or recorded as a `RowError`, never failing the batch (tested; from task 003 validator note).
- [ ] `RowError`s are written to `import_errors` (position, raw, reason) and counted in `failed`; they do not abort the run.
- [ ] `max_error_ratio`: when failed/total exceeds it after the stream ends, status is `aborted` with an explanatory `error`; chunks already committed remain tagged with the run id (documented). A ratio of `0` means any error aborts. Tested at the boundary (equal is OK, above aborts).
- [ ] An adapter/IO error mid-stream gives status `failed`, `error` set, `finished_at` set, and no panic; the lock is always released (test with a failing stream).
- [ ] Context cancellation stops promptly, marks the run `failed` with a cancellation error, releases the lock.
- [ ] `DryRun` parses and reports counts but writes no run row, records, or errors.
- [ ] Integration test with the ONS fixture and `docs/examples/ons-l55o.source.yaml` (location overridden): run 1 gives `inserted=642, updated=0, unchanged=0, failed=0`, `source_meta.CDID == "L55O"`; run 2 gives `unchanged=642`; after editing one value in a temp copy of the CSV, run 3 gives `updated=1, unchanged=641`.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/design.md (Import flow and failure handling as implemented); docs/data-model.md (counts, shape, statuses); CHANGELOG.md

## Runbook impact
docs/runbooks/import-failures.md (failed, aborted by max_error_ratio, already running, cancelled; rejects table queries)

## Status
validated
## Log

### Worker log (round 1)
- Files: `internal/importer/{importer,shape}.go` and tests. No change to `internal/store` was needed.
- `Deps.Store` is the small `importer.Store` interface (satisfied by `*store.Store`) so a failing store can be tested; `Deps.Adapters` is keyed by source kind.
- Source config is read from `sources.config` (JSON). `importer.ConfigJSON` / `ParseConfigJSON` convert `config.Source` to/from it (JSON is valid YAML, parsed by `config.ParseSource`).
- Order: advisory lock, GetSource, adapter lookup, CreateRun, Open. Lock `ok=false, err=nil` -> `ErrAlreadyRunning` (no run row); lock error -> returned as is, never `ErrAlreadyRunning` (tested with a failing store).
- NUL decision: a record with `\u0000` in any string value or key (payload, incl. `_meta`) is recorded as a `RowError` (reason with the JSON path, raw = natural key), counted in `failed` and in the ratio; it is not sanitised, so stored data is never silently altered. The batch is never failed by it. Also non-encodable payloads (NaN) and empty keys become RowErrors.
- Return value: failed and aborted runs return the filled `RunResult` plus a non-nil error (aborted wraps `ErrAborted`); `Status` tells which. Cancellation: status `failed`, error starts with `cancelled:`; the run is closed with a non-cancelled context (10 s) so the row is always finished.
- `max_error_ratio`: `failed/total > ratio` (float division, so 1/100 equals 0.01); ratio 0 -> any failure aborts; total counts records plus row errors. Committed chunks stay tagged with the run id.
- DryRun takes no lock, writes no run/records/errors; result has `Total`, `Failed`, `Shape`, `SourceMeta` only (no inserted/updated).
- Mid-stream error: pending batch is flushed, then run `failed`. `shape` excludes `_` keys; types: string, number, boolean, null, object, array. jsonb normalises key order/whitespace when reading `shape`/`source_meta` back.
- 2026-11-21 validator r1: PASS. Orchestrator applies a small round 2 for two non-blocking findings: (1) a panic in the adapter left the run row `running` (lock was released): recover, finish the run as failed, re-panic; (2) tests missing for: panic, lock independence across different sources, ConfigJSON round trip, DB-level proof that run 2 leaves payload/imported_at untouched. Hard kills are handled by the abandoned-run criterion added to task 009.

### Worker log (round 2)
- `Run` now has a deferred recover (installed right after `CreateRun`, non-dry runs): it finishes the run as `failed` (error `panic: <value>`, non-cancelled 10 s context), then re-panics; the lock defer still runs. Hard kills stay with task 009.
- New tests in `internal/importer/importer_test.go`: adapter panic (row failed with text, lock free, panic propagates), lock independence (s1 locked, s2 runs, s1 still `ErrAlreadyRunning`), ConfigJSON/ParseConfigJSON DeepEqual round trip for the ONS example plus no secret-bearing keys, DB-level check that run 2 leaves `payload`/`imported_at` of all 642 rows unchanged and sets `last_run_id` = run 2 for all 642.

### Worker log (round 3, flaky test)
- Cause: `TestAdvisoryLockReleasedOnConnectionLoss` (internal/store/store_test.go) terminated/counted every advisory-lock backend in the cluster via `pg_locks` (cluster-wide), so concurrent packages' locks (importer tests, other store tests) were killed or miscounted. Not reproduced before the fix (not attempted); the cause is by inspection.
- Fix (test code only, the one authorised store exception): the query is now scoped to the current database and to the lock key (`classid<<32 | objid = hashtextextended('data-importer:source:src', 0)`), still excluding our own pid.
- Regression: the test now first takes the same "src" lock in a second database (bystander) and afterwards asserts that lock is still held (its session was not terminated). Runs in the normal gate, no skips.
- Audit of internal/store, internal/importer, internal/testutil, cmd: no other pg_locks/pg_stat_activity/pg_terminate_backend/connection counting. Databases are uniquely named (`t_<random>`), ports come from testdb.sh `free_port`; the only fixed address is 127.0.0.1:1 for the deliberate "unreachable" tests (no server).
- Verified with CI_REQUIRE_DB=1 and a real DB: `go test -race -count=5 -p 8 ./...` x3 passed.
- 2026-11-21 validator r3: PASS (3 gate runs + `-race -count=5 -p 8`; mutations confirm each new test fails when its behaviour is broken). The old store test REPRODUCED the flake in 5 of 5 concurrent loops (store package hung); the new test is scoped to its own database and lock key and asserts a bystander lock in a second database survives.
- Rounds: 3 (r2: panic recovery + 4 tests; r3: flaky store test fixed under an authorised exception to "no store changes": internal/store/store_test.go only). documentor + runbook-refiner ran in parallel; deterministic docs check: PASS.
- Lesson recorded in docs/testing.md: tests sharing one Postgres cluster across parallel packages must scope cluster-wide queries (pg_locks, pg_stat_activity, pg_terminate_backend) to their own database and keys.
