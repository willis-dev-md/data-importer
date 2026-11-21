# 018 – Duplicate natural keys within a run are reported, not silently merged

## Goal
Two different source records that map to the same natural key in ONE run must never silently overwrite each other: the later one becomes a recorded row error.

## Depends on
008, 013

## Design refs
`docs/design.md` (Import flow, failure handling), `docs/data-model.md` (decisions for tasks 003 and 008), `docs/runbooks/source-layout-changed.md` (case 2), `docs/runbooks/import-failures.md`

## Scope
`internal/importer/`, `internal/store/` (upsert reports rows already touched by the same run), tests, plus the task Log

## Out of scope
Changing natural-key semantics across runs (upsert on `(source_id, natural_key)` stays); tombstoning; new adapters.

## Acceptance criteria (each must be checkable)
- [ ] First, REPRODUCE and state the cause in the Log: with a copy of `testdata/boe/baserate-trimmed.xlsx` where a column is inserted before the rate column (letters shift) and the BoE source config unchanged apart from location, the importer reports `inserted=773 failed=8` (aborted) although 854 records are valid and 8 fail. Confirm (or refute) that the missing ~81 records are distinct records collapsed onto equal natural keys (e.g. key parts read from a now-blank column) and overwritten ("last wins", counted once).
- [ ] Within one run, a record whose natural key was already written EARLIER IN THE SAME RUN (same batch or a previous batch of that run) is NOT merged: the first record wins, the later one becomes a RowError with reason `duplicate natural key (first seen at <position>)` (position of the first record when known), written to `import_errors`, counted in `failed`, and it takes part in `max_error_ratio`. No memory growth proportional to the run size beyond a bounded structure (prefer detecting via the database, e.g. rows whose `last_run_id` equals the current run id; or a compact hash set with a stated bound and a test on 200k records).
- [ ] Re-runs are unaffected: a key last seen in a PREVIOUS run is updated or unchanged as before (ONS: run 1 inserted=642, run 2 unchanged=642, run 3 updated=1 unchanged=641; BoE: inserted=854 then unchanged=854); a second run of the same source cannot be confused with duplicates of the first.
- [ ] The column-shift BoE reproduction now yields RowErrors for every collision with the reason above, a failed run status per `max_error_ratio`, and NO silently lost distinct records: total records written + duplicate errors + other errors == rows read (assert the arithmetic in a test).
- [ ] `store.UpsertRaw` semantics are documented in the Log and docs/data-model.md decisions are updated by the documentor (the task 003 note "duplicate keys in one batch: last wins" is superseded at the importer level; keep the store-level behaviour defined and tested if it still exists).
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/data-model.md (decisions for tasks 003/008: duplicate-key rule); docs/design.md (failure handling); CHANGELOG.md

## Runbook impact
docs/runbooks/source-layout-changed.md (case 2 now reports duplicate-key errors: update the real output) and docs/runbooks/import-failures.md (new error reason and how to read it)

## Status
in-progress
## Log
(created by the orchestrator after the task 013 runbook-refiner's layout-change reproduction)
