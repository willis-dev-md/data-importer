# 009 – CLI: apply sources, run imports

## Goal
An operator can register sources and mappings from files and run an import from the command line.

## Depends on
006, 008

## Design refs
`docs/operations.md` (CLI, configuration), `docs/mapping-spec.md`, `docs/data-model.md`

## Scope
`cmd/importer`, `internal/config/` (directory loader), `internal/store/` (mapping version storage, active version), tests, `README.md` (usage)

## Out of scope
Groups, schedules, the gRPC server, `infer`, `reparse`.

## Acceptance criteria (each must be checkable)
- [ ] `importer sources apply --dir <dir>` loads `*.source.yaml` and matching `*.mapping.yaml` files (the naming used in `docs/examples/`), validates them (mapping against `canonical-schema.yaml`, errors abort with nothing written), upserts sources, and stores each mapping as a **new version only if its content changed**, activating it. Re-running with unchanged files changes nothing (asserted).
- [ ] `importer run --source <id> [--dry-run]` runs `importer.Run` against `DATABASE_URL` and prints a one-line summary (`inserted/updated/unchanged/failed`, run id, duration); exit codes: 0 ok, 1 failed/aborted, 3 already running.
- [ ] `SOURCES_DIR` environment variable is the default for `--dir`; missing required env/args give a usage error (exit 2), not a stack trace.
- [ ] Secrets: source configs that reference `token_env` etc. are resolved from the environment at run time and never stored in the DB or printed; a test greps logs and the stored `config` jsonb for the secret value.
- [ ] End-to-end CLI test (built binary via `exec`, real Postgres): migrate, apply a temp dir containing the ONS source+mapping with `location` pointing at the fixture, run twice, assert summaries and DB counts (642 then 642 unchanged).
- [ ] Abandoned runs: at the start of `importer run` (and the future server), runs left in status `running` whose source advisory lock is NOT held by any live session are marked `failed` with error `abandoned (process died)` and `finished_at` set (a hard kill/OOM cannot run deferred cleanup). Tested with a real database by inserting a stale `running` row, and a LIVE run (lock held) is never touched. (Found in task 008 validation: a panic or kill leaves the row `running` forever.)
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/operations.md (CLI, SOURCES_DIR, exit codes); README.md (usage); CHANGELOG.md

## Runbook impact
docs/runbooks/add-a-source.md (author source + mapping, validate, apply, dry-run, run); update docs/runbooks/import-failures.md with exit codes

## Status
validated
## Log

### Worker log, round 1
Files: `cmd/importer/{main,sources,runcmd}.go` + `cli_test.go`; `internal/config/{auth,dir}.go` (+ `source.go`: `auth` field) + `dir_test.go`; `internal/store/mappings.go` + `mappings_test.go`.

Decisions:
- **Directory layout:** `<name>.source.yaml` with optional `<name>.mapping.yaml`. A mapping file with no matching source file is an error (typo guard), as is a duplicate source id, a mapping whose `source:` differs from its source id (`SOURCE_MISMATCH`), or an empty directory. All problems are reported; nothing is written if any exists (store.Apply is also one transaction).
- **Mapping storage:** `mappings.spec` is the file's YAML converted to JSON as written. The `version:` key in the file is ignored; the stored `version` key is set to the storage version (max+1). Content comparison is against the ACTIVE version only, ignoring `version`, key order, number form and comments. Going back to older content creates a new version. `created_by` is `cli`.
- **Unchanged re-apply writes nothing:** the source upsert has a `WHERE kind/config IS DISTINCT` guard (test compares `xmin` of every sources/mappings row). `UpsertSource` itself is unchanged.
- **Secrets:** new optional `auth: {type: bearer|api_key, token_env: NAME, header: H}` in the source config (docs/mapping-spec.md already showed it for http_json). Only the variable NAME is validated/stored; there is no key for a literal secret (unknown keys are rejected). `run` resolves it with `Auth.Resolve` and applies it through an `http.Client` transport (set only for requests to the source's own host, so a redirect elsewhere never gets the header), via `source.FetchOptions.Client`; no change to `internal/source`. Unset/empty variable: exit 2 naming the variable. Not applied to file locations (no HTTP).
- **Exit codes:** 0 ok; 1 failed/aborted (summary is still printed on stdout, error on stderr), unknown source, invalid files, DB error; 2 usage (missing `--source`/`--dir`/`SOURCES_DIR`/`DATABASE_URL`, unreadable dir or schema, missing secret env); 3 already running. NOTE `migrate` keeps its older exit 1 for a missing `DATABASE_URL`.
- **Summary line:** `source=<id> status=<s> inserted=N updated=N unchanged=N failed=N run=<id|dry-run> duration=<d>`.
- **Sweep:** `store.SweepAbandonedRuns`, one UPDATE: `status='running'` rows whose source's advisory lock (same key expression as `TryAdvisoryLock`, matched in `pg_locks` for the current database only) is not held become `failed`, `finished_at=now()`, error `abandoned (process died)`. Called at the start of a non-dry `importer run` (stderr line `marked N abandoned run(s) failed: [ids]`); the future server should call it at start. Conservative: a stale row of a source that has a live run right now is left until that run ends. Tests: stale row + 8 live locked runs (various lock key signs) untouched; a backend terminated with `pg_terminate_backend` gets its run swept; CLI test inserts a stale row.
- `--schema` default is `./canonical-schema.yaml` (cwd), same as `mapping validate`; `sources apply` validates mappings against it (exit 2 if unreadable).
- Docs/README/CHANGELOG/runbooks not touched (other roles).
- 2026-11-21 validator r1: PASS (real binary + throwaway Postgres: ONS scenario 642 then 642 unchanged; apply all-or-nothing; exit codes 0/1/2/3; secrets never stored or printed; sweep leaves a live run alone and fails a stale one). documentor + runbook-refiner (smaller model) ran in parallel; deterministic docs check PASS (tagged blocks, incl. a database one, re-run).
- Non-blocking notes moved to the Hardening backlog in docs/roadmap.md: `--dry-run` summary prints zeros; no size cap on files in --dir; a YAML error may echo a snippet of a garbage file; redirect-does-not-leak-token is unit-tested only.
- Process note: the documentor and runbook-refiner both used the SHARED ephemeral test database while running in parallel (one cluster, one state file); no conflict occurred, but a stop by one would kill the other's database. Consider running database-using roles sequentially.
- Only `kind: csv` can be applied today (`kind: http_json` is rejected by apply); docs/examples/partner-api.source.yaml therefore does not apply yet.
