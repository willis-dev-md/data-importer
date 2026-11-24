# Runbook: Database migrations (apply, check version, roll back, dirty recovery)

**Owner role:** on-call engineer / database owner  |  **Severity if this happens:** medium (dirty database: high, importer cannot migrate)
**Last verified:** 2026-11-24, task 021 (`migrate up` then `migrate version` on a fresh throwaway Postgres 16 from `scripts/testdb.sh`: `version 3` both times; earlier checks from 2026-11-22, base 484b08e + task 014 (`version 0`, then `version 2`); task 002, 2026-11-21, for 0001 only; the `down` steps for 2 to 1 and 3 to 2, the dirty-version recovery and `pg_restore` are UNVERIFIED)

## When to use
Applying the schema, checking which version a database is at, rolling back one step, or `importer migrate` fails (especially with `Dirty database version`).

## Prerequisites
`bin/importer` (`make build`), `psql`/`pg_dump` on PATH, `DATABASE_URL` set in the environment (`postgres://user:pass@host:5432/db?sslmode=...`). Migrations are embedded in the binary (`migrations/*.sql`); state is the single-row table `schema_migrations(version, dirty)`. Current schema version: 3 (`0001_core`: sources, mappings, import_runs, raw_records, import_errors; `0002_source_files`: `source_files` and `import_runs.file_id`; `0003_schedule`: adds to `sources` the columns `schedule`, `enabled`, `next_run_at`, `last_scheduled_at` and the partial index `sources_due`).

## Steps
1. Check version: `bin/importer migrate version`  *(expected: `version 0` on an empty database, `version 3` after all migrations; exit 0. Real: `version 0` then `version 2` (task 014); `version 3` after `migrate up` (task 021))*
2. Apply all pending: `bin/importer migrate up`  *(expected: `version 3`, exit 0; on a fresh database it applies 0001, 0002 and 0003. Real (task 021, fresh database): `version 3`, then `migrate version` printed `version 3`. A second `migrate up` run is UNVERIFIED for 0003)*
3. Roll back exactly one migration: `bin/importer migrate down`  *(expected: `version 2` from version 3 (drops the `sources` schedule columns and `sources_due`; UNVERIFIED, the down file was not run in task 021); from version 2 it prints `version 1` (drops `source_files` and clears `import_runs.file_id`; the 2 to 1 step is UNVERIFIED here); from version 1 it prints `version 0`; at version 0 it is a no-op and prints `version 0`)*. This DROPS the columns or tables of that migration and their data: back up first (below). The files on disk are not deleted by a migration, but reparse cannot find them without their `source_files` rows.
4. Inspect the schema:
   `psql "$DATABASE_URL" -c '\dt'` *(expected after up: import_errors, import_runs, mappings, raw_records, schema_migrations, source_files, sources; 0003 adds columns, not tables)*
   `psql "$DATABASE_URL" -c '\d raw_records'` *(expected: PK `(source_id, natural_key)`, index `raw_records_payload_gin ... gin (payload jsonb_path_ops)`, FKs to `sources` and `import_runs`)*
   `psql "$DATABASE_URL" -c '\d source_files'` *(real: columns `id, source_id, run_id, sha256 bytea, size_bytes, etag, last_modified, storage_ref, fetched_at`; FK `import_runs.file_id -> source_files(id)`)*
   `psql "$DATABASE_URL" -c '\d sources'` *(after 0003: the columns `schedule`, `enabled`, `next_run_at`, `last_scheduled_at`; index `sources_due`. Column list not captured in task 021 as a block; UNVERIFIED as text)*
   `psql "$DATABASE_URL" -c 'select * from schema_migrations'`

### Against a non-test database (safe procedure)
1. Take a backup and confirm it is readable: `pg_dump --format=custom --file=pre-migrate.dump "$DATABASE_URL"` then `pg_restore --list pre-migrate.dump | head`  *(dump exit 0; `pg_restore --list` lists the tables)*. Store the file outside the repo.
2. Provide `DATABASE_URL` through the environment only (secret store, or `read -rs DATABASE_URL; export DATABASE_URL`). Do not type the URL inline in a command (it lands in shell history), do not commit it, do not paste it into tickets or logs. Error messages from `importer` print the host and database user, not the password.
3. Run `bin/importer migrate version`, confirm it is the version you expect, then `bin/importer migrate up`, then `bin/importer migrate version`.
4. Run no `down` on production data unless the backup from step 1 exists and you accept losing that migration's data.

## Troubleshooting
| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| `importer migrate: DATABASE_URL is not set` (exit 1) | Variable missing in this shell/service. | `export DATABASE_URL=...` (see above), re-run. |
| `usage: importer migrate up\|down\|version` (exit 2) | No direction given. | Add one of `up`, `down`, `version`. |
| `importer migrate: unknown direction "sideways" (want up, down or version)` (exit 2) | Bad argument. | Use `up`, `down` or `version`. |
| `importer migrate up: connect to database: failed to open database: failed to connect to ...: dial tcp 127.0.0.1:1: connect: connection refused` (exit 1) | Wrong host/port or server down. Confirm: `psql "$DATABASE_URL" -c 'select 1'`. | Fix the URL or start the server; re-run. Nothing was changed. |
| `... hostname resolving error: lookup <name> on <dns>: no such host` (exit 1) | The host part of the URL is wrong (typo, or the URL is malformed so a path segment is read as a host). | Correct `DATABASE_URL`; re-run. |
| `importer migrate up: migrate up: Dirty database version 1. Fix and force version.` (exit 1); `migrate down` prints the same with `migrate down:`; `migrate version` prints `importer migrate version: database is at dirty migration version 1: fix manually, then force the version (see docs/runbooks/database-migrations.md)` | A migration was interrupted or failed and `schema_migrations.dirty` is true. The number is the version that is dirty (it can be 2 or 3 too). Confirm: `psql "$DATABASE_URL" -c 'select * from schema_migrations'` shows `dirty = t`. | Follow "Recover a dirty migration" below. |
| `importer migrate up: migrate up: migration failed: relation "sources" already exists (column 0) in line 1: -- Core schema: ...` followed by the whole SQL file (exit 1) | Tables already exist but `schema_migrations` has no row (or says version 0), e.g. the row was deleted by hand, or the schema was created outside the migrator. Reproduced (task 002). After this failure the database IS dirty (`migrate version` reports `dirty migration version 1`). | The effects already exist, so use case A of the recovery below (set version 1, clear dirty), after checking the tables match `migrations/0001_core.up.sql`. |
| `importer migrate up: migrate up: no migration found for version 0: read down for version 0 .: file does not exist` | `schema_migrations` was edited to `version = 0`. Reproduced: golang-migrate cannot resume from a version-0 row. | `delete from schema_migrations;` (no row = version 0), then `migrate up`. |

### Recover a dirty migration
The CLI has no `force`; recovery is SQL on `schema_migrations`. Work on a backup-protected database (take the backup first).

1. Stop every process that might migrate (deploys, other operators).
2. Find the dirty version: `psql "$DATABASE_URL" -c 'select * from schema_migrations'` (e.g. `1 | t`, `2 | t` or `3 | t`).
3. Read the migration `migrations/NNNN_name.up.sql` and compare it with what is in the database (`psql "$DATABASE_URL" -c '\dt'`, `\d <table>`). Do not continue until you know which statements took effect. Decide:
   - **A. All effects are present** (schema matches the up file): mark it clean:
     `psql "$DATABASE_URL" -c 'update schema_migrations set dirty = false'`
     Verified (task 002): `migrate version` then printed `version 1`. For a dirty version 2 or 3, check `\d source_files` or `\d sources` first; UNVERIFIED.
   - **B. None or only some effects are present** (tables or columns missing, or half there): remove the leftovers with the matching down files, which use `DROP ... IF EXISTS`, then reset to "nothing applied" and re-run. Dirty version 1:
     `psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -1 -f migrations/0001_core.down.sql`
     `psql "$DATABASE_URL" -c 'delete from schema_migrations'`
     `bin/importer migrate up`
     Verified (task 002) on a database with `import_errors` and `raw_records` dropped: down file ran (exit 0, NOTICE "does not exist, skipping"), `migrate version` printed `version 0`, `migrate up` printed `version 1` (now `version 3` after all migrations; UNVERIFIED), `\dt` showed 6 tables (now 7 with `source_files`). WARNING: this deletes the data in those tables; only use it when they hold nothing you need or after a verified backup.
     Dirty version 3 or 2 (UNVERIFIED, not run): run the down file of each applied migration from the highest down (`migrations/0003_schedule.down.sql` for 3, then `migrations/0002_*.down.sql`, then `migrations/0001_core.down.sql`), using the same command shape, then `delete from schema_migrations`, then `bin/importer migrate up`.
   - Do NOT use `update schema_migrations set version = 0`: it breaks `migrate up` (see table).
4. Verify: `bin/importer migrate version` prints `version N` with exit 0, and `psql "$DATABASE_URL" -c 'select * from schema_migrations'` shows `dirty = f`.

## Verify
`bin/importer migrate version` exits 0 and prints the expected version (`version 3` after `migrate up`); `\dt` lists the seven tables above; `select dirty from schema_migrations` is `f`.

## Roll back / undo
`bin/importer migrate down` (one step at a time; from version 3 it removes the schedule columns and `sources_due`, back to version 2; from version 2 it drops `source_files` and the `import_runs.file_id` link, back to version 1; both steps UNVERIFIED). To restore data: `pg_restore --clean --if-exists --dbname="$DATABASE_URL" pre-migrate.dump`. UNVERIFIED: `pg_restore` was not run, only `pg_dump` and `pg_restore --list` (dump readable, 10 TABLE entries, task 002).

## Escalate
A dirty database where you cannot tell which statements took effect, any dirty state on production, or a failure message other than those above: send the full `importer migrate` output, `select * from schema_migrations`, `\dt` output and the backup file name to the repository owner / database owner. Do not run the down file on production data without a backup.
