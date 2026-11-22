# 014 – Fetched-file retention, change detection and reparse

## Goal
A run skips unchanged files cheaply, keeps every fetched file by hash, and can re-run the structural pipeline on a stored file without downloading it again.

## Depends on
008, 013

## Design refs
`docs/design.md` (Import flow step 2a), `docs/data-model.md` (`source_files`), `docs/adr/0004-structural-parsing-at-import.md`, `docs/operations.md`

## Scope
`migrations/` (new migration for `source_files`, `import_runs.file_id`), `internal/store/`, `internal/source/` (conditional fetch), `internal/importer/`, `cmd/importer` (`reparse`), tests

## Out of scope
Object-storage backends (store in Postgres large objects or a local directory behind an interface; record the choice in the log), retention policies/cleanup (a TODO in docs is fine).

## Acceptance criteria (each must be checkable)
- [ ] Migration adds `source_files` exactly as in `docs/data-model.md` and `import_runs.file_id`; up/down tested.
- [ ] Fetch sends `If-None-Match` / `If-Modified-Since` from the last successful run's stored `etag`/`last_modified` when `change_detection` lists them; a `304` or an identical SHA-256 closes the run with status `unchanged_file`, zero parse work, and no record writes (test with an `httptest` server counting requests and bodies).
- [ ] The fetched bytes are stored once per `(source_id, sha256)` through a `FileStore` interface (`Put`, `Open`); a second identical fetch does not store a duplicate.
- [ ] `importer reparse --source <id> [--run <id>]` re-runs the adapter against the stored file of the latest (or given) run with no network access (test blocks all network and passes), producing a new run with `trigger=manual`; reparsing with an unchanged pipeline gives `unchanged` counts equal to the record count, and after a pipeline change only the affected records are `updated`.
- [ ] Files above a configured size are still hashed and processed in a streaming way (no whole-file buffering for CSV) and the size limit from task 004 still applies.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/design.md (Import flow step 2a); docs/data-model.md (`source_files`); docs/operations.md (`importer reparse`); CHANGELOG.md

## Runbook impact
docs/runbooks/reparse-and-file-retention.md (reparse after a pipeline change, where files are stored, clean-up)

## Status
validated
## Log

### Worker log (round 1)
Decisions:
- **FileStore backing:** a local directory behind `store.FileStore` (`Put`, `Open`), `store.DirFileStore`. Content-addressed: `<dir>/<sha[:2]>/<sha256>`; the only input to a path is a 64-hex SHA-256 (`Open` rejects anything else; `source_files.storage_ref` is informational, `dir:<sha[:2]>/<sha>`, never used as a path). Same bytes from two sources = one blob, one `source_files` row per source. Writes go to a temp file in the store dir, fsync, atomic rename (safe for concurrent runs); a repeated Put rewrites the blob, which heals corruption. Reads re-hash and fail with `ErrFileCorrupt` at EOF; the importer also verifies the whole file (streaming) before parsing and drains the rest after the parse, so a corrupt file is refused with no record written. Modes 0700/0600.
- **Where:** env `FILE_STORE_DIR`, default `data/files` relative to the working directory (`/data/` added to `.gitignore`). Created if missing; not creatable/writable/not a directory = usage error (exit 2) before any database work, for `run` (not `--dry-run`) and `reparse`.
- **Max size:** none of its own; `Put` takes the fetch limit (`FetchOptions.MaxBytes`: 256 MB default, xlsx 100 MB workbook limit from tasks 004/013). Oversize = `ErrTooLarge`, nothing stored, run failed. CSV is streamed fetch -> store -> verify -> parse (never buffered); xlsx is read into memory by its adapter as before.
- **Retention:** none in this task. Files are kept forever; no cleanup, no refcount (TODO for docs/runbook).
- **Change detection (`importer.Run`, only with `Deps.Files` and a `source.FileAdapter`):** `LastFile` = file of the latest run with status `succeeded` or `unchanged_file`. `If-None-Match` / `If-Modified-Since` are sent only when `change_detection` lists `etag` / `last_modified` and a stored value exists (ETag verbatim, Last-Modified rendered as IMF-fixdate from the stored timestamptz; an unparsable Last-Modified is stored as NULL). A 304 (only honoured for a conditional request) closes the run as `unchanged_file`: nothing stored or parsed. After a 200 the bytes are stored/registered; identical SHA-256 to the last successful file closes the run `unchanged_file` only when `change_detection` lists `content_hash`. On an identical fetch the existing `source_files` row keeps its `run_id` and gets the new etag/last_modified/fetched_at. A, B, A: the third differs from the last successful file, so it is parsed and reuses A's row. Without `change_detection` every run parses (identical bytes still stored once).
- **`unchanged_file` run row:** `status=unchanged_file`, `finished_at` set, `inserted/updated/unchanged/failed/tombstoned` all 0, `shape`/`source_meta`/`error` NULL, `file_id` = the file found unchanged. No `import_errors`, no `raw_records` writes (last_run_id/imported_at untouched). CLI prints `status=unchanged_file ... unchanged=0`, exit 0. `import_runs.file_id` is also set for succeeded/failed/aborted runs.
- **Reparse:** `importer.Options.Reparse` goes through `importer.Run` (lock on its dedicated connection, duplicate-key rule, max_error_ratio, counts). The stored file is resolved under the lock before any run row exists (no file = error, no run). New run `trigger=manual`, `file_id` = the reparsed file, no network, no change detection, current `sources.config`. Counts are the normal upsert counts against `raw_records`: identical pipeline -> `unchanged` = record count; after a pipeline/config change only records whose canonical payload hash changed are `updated`, keys not seen before are `inserted`; keys no longer produced are left alone (no tombstoning exists yet).
- **CLI:** `importer reparse --source <id> [--run <id>]`; `--run` must be > 0 when given. Exit codes as `run`: 0 ok, 1 failed/aborted/nothing to reparse (no stored file, unknown run or run of another source, corrupt or missing file, unknown source), 2 usage (missing `--source`, extra args, bad `--run`, bad `FILE_STORE_DIR`/env, no DATABASE_URL), 3 already running. It needs no auth secrets (never fetches). Prints the same one-line summary as `run`.
- Migration `0002_source_files`: `source_files` exactly as in docs/data-model.md; FK `import_runs.file_id -> source_files(id)`; indexes on `import_runs(file_id)` and `(source_id, id)`. Down clears `file_id`, drops the FK and the table (so up/down/up works). Schema version is now 2 (tests and runbook command output updated from `version 1`: the database-migrations runbook still says `version 1`, for the runbook-refiner).
- Existing tests changed: `migrate up` now prints `version 2`; `migrate down` one step goes 2 -> 1; BoE xlsx CLI test: the second run is `unchanged_file` (its example lists `content_hash`); CLI/e2e tests set `FILE_STORE_DIR` to a temp dir.
- Docs now stale (for documentor): data-model.md decisions task 008 ("`unchanged_file` is not written yet"), design.md "Statuses ... reserved", database-migrations runbook (`version 1`, 0001 only).
- No new dependencies; go.mod untouched. `change_detection` values are still not validated by config (unknown entries are ignored).
Unsure: default `data/files` relative to cwd (vs requiring the env var); sha-skip requires `content_hash` in `change_detection` (spec text is ambiguous); Last-Modified is stored as timestamptz and re-rendered canonically, so a non-IMF-fixdate original would not be resent byte for byte.

### Worker log (round 2)
- `scripts/demo.sh` (scope widened for this fix) exports `FILE_STORE_DIR="$WORK/files"`; `$WORK` is removed by the existing EXIT/INT/TERM trap. After validate, `git status --short --ignored` is unchanged and `data/` does not exist. internal/e2e and cmd/importer tests already use temp `FILE_STORE_DIR`; serve and other scripts do not use the file store.
- `run` (not `--dry-run`) and `reparse` print `importer <cmd>: file store: <absolute path>` on stderr at start (`openFileStore`); the default `data/files` is unchanged. Tested in `TestCLIReparse` (binary) and `TestOpenFileStoreReportsAbsolutePath` (default and relative dir resolved to absolute).

### Worker log (round 3, test-only: internal/source/fetch_test.go)
- `TestFetchHTTPS/timeout`: the `/slow` handler now blocks on a `release` channel (closed by a defer that runs before `srv.Close`), so no response can arrive before the client timeout whatever the load. The fetch runs in a goroutine; the test asserts a timeout-class error (`context.DeadlineExceeded` or `net.Error.Timeout()`) and waits at most 30 s (only a hang fails). Old pattern raced a 100 ms client timeout against a handler that returned on `r.Context().Done()` or 5 s; I could not reproduce the old failure here (100 runs, `-cpu 1`, 6 busy loops) but the new test cannot take the "response arrived" path at all.
- Proof: `-race -count=50 -run TestFetchHTTPS` green; same with `-count=100 -cpu 1` under 6 busy loops green; `go test -race -count=3 -p 8 ./...` three times green. (`-count=50` of the WHOLE source package exceeds the default 10 min go test timeout, 20 s per iteration: not a failure.)
- Other tests in internal/source and internal/importer: no `time.Sleep` assumptions; the only tight timings are `LockWait: 100ms` in importer/lockpool_test.go (tests that the wait is bounded when all lock slots are held; the slot is held by the test, so the result does not depend on speed) and 90 s context deadlines. Not changed.
- 2026-11-22 validator r1: FAIL (scripts/demo.sh left `data/files/...` in the working tree: no temp FILE_STORE_DIR); worker r2: demo.sh uses a temp FILE_STORE_DIR removed by its trap, `run`/`reparse` print the absolute file store path on stderr. validator r2: PASS (demo normal/SIGINT 130/forced failure 1: tree clean; conditional fetch, FileStore integrity, reparse with the source server killed). A load-sensitive `TestFetchHTTPS/timeout` flake (second sighting) was fixed in worker r3 (test-only exception: the handler now blocks until released; the old handler could return an empty 200 so a 100 ms timeout raced it); validator r3: PASS (three mutations fail the new test; 100 runs plain and under load green; the old failure could not be reproduced, so the root cause is shown from the code).
- documentor and runbook-refiner (budgets 20/25, evidence packs) ran sequentially. docs-check FAIL x2: tagged verify blocks in docs/operations.md and docs/runbooks/reparse-and-file-retention.md documented commands that are supposed to exit non-zero and aborted under `bash -e` (same class as task 011). Fixed first with the strong model (both roles on sonnet) by wrapping each expected failure and asserting the exit code. The rule is now written into the documentor and runbook-refiner agent files (kit commit 06f63c2). Deterministic check PASS afterwards.
- Not verified (documented): read-only or full disk (tests ran as root), kill mid-write (code reading only), the 256 MB limit beyond existing tests, corrupt-file message from the runbook-refiner's own run (verified by the validator).
