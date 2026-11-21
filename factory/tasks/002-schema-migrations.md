# 002 – Core schema and migration runner

## Goal
The Postgres schema for sources, mappings, runs, raw records and import errors exists, versioned and reversible, with `importer migrate`.

## Depends on
001

## Design refs
`docs/data-model.md`, `docs/operations.md` (CLI, config), `docs/adr/0002-stack-go-postgres-grpc.md`

## Scope
`migrations/*.sql`, `internal/store/migrate.go` (+ tests), `cmd/importer` (wire `migrate`), `docs/data-model.md` if it must change

## Out of scope
Groups, schedules, `source_files`, `mapping_templates` and the `schedule/enabled/next_run_at` columns (later milestones add them in their own migrations). No query code beyond the migrator.

## Acceptance criteria (each must be checkable)
- [ ] `migrations/0001_core.up.sql` / `.down.sql` create/drop exactly: `sources` (id, kind, config jsonb, active_mapping_version, created_at), `mappings`, `import_runs` (with `trigger`, `scheduled_for`, `source_meta`, `shape`, counts, `status` CHECK including `unchanged_file`), `raw_records` (PK `(source_id, natural_key)`, GIN index `jsonb_path_ops` on payload, `content_hash bytea`, `deleted_at`), `import_errors`, matching `docs/data-model.md` (core tables only; note deliberate omissions in the log).
- [ ] SQL is embedded (`embed`) and applied by `store.Migrate(ctx, url, direction)` using `golang-migrate` (or an equivalent embedded runner recorded in `schema_migrations`); it is safe to call twice.
- [ ] `importer migrate up|down|version` reads `DATABASE_URL`, prints the resulting version, exits non-zero with a clear message when `DATABASE_URL` is missing or unreachable.
- [ ] Integration test: up creates all tables and the CHECK/PK/FK constraints (assert via `information_schema`/`pg_indexes`); inserting a `raw_records` row for an unknown source fails; `status='bogus'` is rejected; down removes everything; up/down/up leaves a working schema.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/data-model.md (as implemented); docs/operations.md (`importer migrate`); CHANGELOG.md

## Runbook impact
docs/runbooks/database-migrations.md (apply, check version, roll back one step, dirty-migration recovery)

## Status
validated
## Log
- Used golang-migrate v4.18.3 (pgx/v5 driver, iofs source); go.mod `go`/`toolchain` unchanged.
- `migrations/embed.go` added (needed for `embed`; slightly beyond the `*.sql` scope).
- Omitted per scope: source_groups, mapping_templates, source_files, sources.group_id/schedule/enabled/next_run_at/last_scheduled_at, and the FK on import_runs.file_id (no source_files yet). The `idx sources_due` index is omitted with them.
- `kind` CHECK now also allows 'xlsx' (project imports Excel); data-model.md updated.
- `down` rolls back one step; down at version 0 is a no-op; `Migrate` returns the version.
- 2026-11-21 validator r1 (code): PASS. documentor + runbook-refiner ran; validator docs-check: PASS. The docs-check also confirmed that `up` works after a clean `down` (the runbook's "version = 0" note only concerns a hand-edited row).
- Decisions: golang-migrate v4.18.3 (pgx5 + iofs); `down` rolls back one step, no-op at version 0; sources.kind CHECK allows csv, xlsx, http_json; sources_due index and import_runs.file_id FK deferred; migrations/embed.go added for embed. Recorded in docs/data-model.md.
