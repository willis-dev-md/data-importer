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
- [ ] `scripts/validate.sh` passes.

## Status
pending
## Log
