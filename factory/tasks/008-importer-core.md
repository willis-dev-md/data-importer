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
in-progress
## Log
