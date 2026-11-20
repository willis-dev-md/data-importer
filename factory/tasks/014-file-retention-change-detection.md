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
pending
## Log
