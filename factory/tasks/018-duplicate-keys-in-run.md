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
- [ ] (added in round 2) `--dry-run` / `Options.DryRun` ALSO detects duplicate natural keys (it is the step the runbooks recommend for catching layout changes): it writes nothing, so use a compact in-memory structure (e.g. 128-bit hashes of keys) with a STATED bound (a clear error beyond e.g. 20 million keys), report the number of duplicates in the result, and print the first N duplicate reasons; tested with the BoE column-shift copy (dry run reports 81 duplicates) and a clean run (0), and memory for a 200k-record dry run stays small.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/data-model.md (decisions for tasks 003/008: duplicate-key rule); docs/design.md (failure handling); CHANGELOG.md

## Runbook impact
docs/runbooks/source-layout-changed.md (case 2 now reports duplicate-key errors: update the real output) and docs/runbooks/import-failures.md (new error reason and how to read it)

## Status
in-progress
## Log
(created by the orchestrator after the task 013 runbook-refiner's layout-change reproduction)

### Worker, round 1
- **Reproduced** (`TestBoEColumnShiftReproduction`, copy of the fixture with a column inserted at C via excelize `InsertCols` in `t.TempDir()`; testdata untouched; BoE source config unchanged apart from location): on the old code `status=aborted inserted=773 failed=8 total=862`, `raw_records`=773.
- **Real cause**: confirmed. After the shift, column C (`month`) is blank and D (`rate`) holds the old month text, so the config keys distinct records on `(series, year, "", day)`; 854 valid records collapse onto 773 keys. `UpsertRaw` overwrote them ("last wins") and counted each key once, so 81 distinct records vanished without any error. With the fix: 862 rows = 773 written + 81 `duplicate natural key` errors + 8 `normalize` errors (failed=89, aborted by `max_error_ratio: 0`), asserted in the test.
- **Winner**: the first record in the run.
- **Reason text**: `duplicate natural key (first seen at <position>)`; `RowError.Position` = position of the rejected (later) record, `Raw` = its natural key. `<position>` is the first record's position when it is in the same batch, else the literal `unknown` (positions are not stored in the database and a per-run map would be unbounded; the key in `raw` plus the first-record content in `raw_records` identify it).
- **Detection / memory**: by the database. New `store.UpsertRawUnique` skips keys whose stored row has `last_run_id = runID` (taken from the same snapshot as the existing `old` CTE, one statement). Same-batch repeats are found in Go with a map of at most one batch. The importer keeps no state between batches (memory O(batch size)); tested with 200k records (every key twice, second copy always in a later batch).
- **Store semantics**: `UpsertRaw` is unchanged (low-level; last wins within a call; a key already written by the same run is overwritten) and still tested; the importer now calls `UpsertRawUnique(ctx, run, source, recs) (Counts, []Dup, error)`. First occurrence wins; each rejected record is returned as `Dup{Index, First}` (`First` = index in recs of the written record, or -1 when written by an earlier call of the same run). Rejected rows are not counted in `Counts` (`Inserted+Updated+Unchanged+len(dups)==len(recs)`), and the stored row is left untouched even if the content differs. Rows from a previous run (including aborted ones, or tombstoned) are updated/unchanged/revived as before. Callers must not run the same run id concurrently (the importer is serial under the source lock).
- Keys compare byte for byte (case, whitespace, NFC/NFD are different keys: tested). `checkRecord` also rejects a natural key with NUL or invalid UTF-8 as a row error (natural keys from `record.Key` are JSON-escaped, so this is defensive).
- (round 1 said a dry run does not detect duplicates; superseded by round 2 below.)
- Tests: `internal/importer/dup_test.go` (BoE shift, same/next batch, 3+ times, case/whitespace/unicode, ratio, reruns/aborted/tombstone, NUL, 4 sources concurrently, 200k), `TestUpsertRawUnique` in the store tests. `newEnv` in `importer_test.go` now uses `MaxConns=32`.
- **Observation (not fixed, task 019)**: see "Pool exhaustion reproduction" below.
- Docs still to update by other roles: docs/data-model.md and docs/design.md (the "Duplicate keys in a batch: last wins" lines), runbooks source-layout-changed (case 2 output now `inserted=773 failed=89`, 81 duplicate errors) and import-failures, CHANGELOG.
- 2026-11-21 orchestrator: worker r1 reproduced the cause (81 distinct records collapsed onto equal keys; 773 + 81 + 8 = 862) and fixed the real run, but dry run does not detect duplicates: criterion added, round 2. A separate defect reported by the worker (4 concurrent runs deadlock on pool exhaustion because the lock connection comes from the work pool) is NOT in scope: task 019.

### Worker, round 2 (dry run)
- `Options.DryRun` now detects duplicates: a `map[[16]byte]int64` of 128-bit key hashes (first 16 bytes of SHA-256 of the natural key; compared byte for byte, so case/whitespace/NFC-NFD forms are different keys) to the first record's position. Collision probability is negligible (about n^2/2^129). Bound: `importer.MaxDryRunKeys` = 10,000,000 distinct keys (about 50 bytes per key, roughly 0.5 GB at the limit); beyond it the dry run FAILS with `dry run: more than N distinct natural keys; duplicate detection is bounded, use a real run`. Repeated keys do not count against the limit. Measured: 199,000 distinct keys held cost about 6.7 MB of heap (`TestDryRunMemory`, asserts < 64 MB).
- Dry-run reason uses the real first position (`duplicate natural key (first seen at 12)`), never `unknown`. Duplicates count in `Failed` and in `max_error_ratio` (status succeeded/aborted as in a real run).
- New result data: `RunResult.Duplicates` (count, also inside `Failed`) and `RunResult.DuplicateSamples` (first `MaxDuplicateSamples` = 10 as `store.RowError`), filled in real and dry runs. `cmd/importer` (out of scope) already prints `failed=N` which now includes duplicates, so the dry run shows them in `failed=`; PRINTING the sample reasons needs a small change in `cmd/importer/runcmd.go` (print `r.DuplicateSamples` after the summary line): not done, needs its own task or scope.
- Tests: BoE column-shift dry run = 81 duplicates, failed=89, total=862, aborted; clean BoE = 0 duplicates, failed=0; rules (case, whitespace, NFC/NFD distinct, 3 times) and agreement with a real run; limit test (limit lowered to 100); 200k memory test. Real-run behaviour unchanged.

### Pool exhaustion reproduction (for task 019; NOT fixed)
Each `importer.Run` holds one pool connection for the source advisory lock (`TryAdvisoryLock`, kept until the run ends) and needs further connections for `GetSource`/`CreateRun`/`UpsertRawUnique`/`RecordErrors`/`FinishRun`. With a pool whose `MaxConns` is small (default `max(4, NumCPU)` from `pgxpool.New`), N concurrent runs of N DIFFERENT sources with N >= MaxConns each hold one connection for their lock and all wait forever for a second one: the test hung for 9 min 57 s until `go test`'s 10 min timeout. Reproduction: in `internal/importer`, make `newEnv` use `pgxpool.New(ctx, url)` (default size; I raised it to `MaxConns = 32` in `importer_test.go`), register 4 sources `c1..c4`, start 4 goroutines calling `importer.Run(ctx, deps, id, importer.Options{BatchSize: 100})` with a 3000-record stream (`keyedStream` in `dup_test.go`), then `wg.Wait()`: `TestDuplicatesDifferentSourcesConcurrently` hangs. Fix directions for 019: take the lock connection out of the pool budget (dedicated `pgx.Conn` for the lock), or bound concurrent runs below `MaxConns`, or acquire with a timeout and fail with a clear error.
- 2026-11-21 SESSION STOPPED: worker rounds 1 and 2 done and `scripts/validate.sh` passed for the worker; the round-2 validator was interrupted by the end of the session and its result is unknown. Status stays `in-progress`; code committed as WIP. Resume with the validator, then documentor, runbook-refiner and the deterministic docs check. Pool-exhaustion deadlock is task 019.
