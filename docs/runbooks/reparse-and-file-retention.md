# Runbook: Reparse a stored file, find stored files, clean up

**Owner role:** on-call engineer (importer operator)  |  **Severity if this happens:** medium (a reparse writes records; a missing stored file blocks reparse of that run only)
**Last verified:** 2026-11-22, base 484b08e + task 014 changes (CLI on a throwaway Postgres 16 from `scripts/testdb.sh`, BoE fixture: first import, `unchanged_file`, reparse, pipeline change, missing store, default store from another directory, usage errors; the corrupt-file step was NOT reproduced, see Troubleshooting; the non-database verify block re-runs the usage checks)

## When to use
- You changed a source's pipeline (`table:` steps such as `columns`, `section`, `fill_down`, `normalize`) and the stored records must follow, without downloading the file again. Mapping changes do not need this: use `sources apply` ([ADR 0004](../adr/0004-structural-parsing-at-import.md)).
- The source server is down or no longer serves the file: reparse makes no network request.
- You need to know where fetched files are kept, or disk use grows (see Clean-up).

## Prerequisites
- `bin/importer` (`make build`), `psql`, and `DATABASE_URL` in the environment (never paste it). The database must be at version 2: `bin/importer migrate version` prints `version 2` ([database-migrations.md](database-migrations.md)).
- `FILE_STORE_DIR` set to an **absolute** path, the same one every `run` used. The default `data/files` is relative to the current directory (see below).
- The source has a run that stored a file: a real `run` (not `--dry-run`) after migration 0002. Older runs have no file.

## Where files are stored
- Directory: `FILE_STORE_DIR`. File path: `<dir>/<first 2 hex chars>/<64 hex sha256>`. The same bytes fetched by two sources are one file. Directories are 0700, files 0600.
- Each `run` (not `--dry-run`) and each `reparse` prints the directory first on stderr: `importer run: file store: <abs dir>` or `importer reparse: file store: <abs dir>`. Check it.
- Index: table `source_files` (columns `id, source_id, run_id, sha256, size_bytes, etag, last_modified, storage_ref, fetched_at`; checked with `\d source_files`). `sha256` is `bytea`: `psql` shows it as `\x2b09...`; the file name is the hex without `\x`. `storage_ref` (`dir:2b/2b09...`) is informational only, never a path.
- **Warning:** with a relative or default `FILE_STORE_DIR`, the store depends on the directory you run from. Real: `reparse` run from another directory printed `importer reparse: file store: <that dir>/W2/data/files` and the stored file was not found (see Troubleshooting). Always export an absolute path, for example `export FILE_STORE_DIR=/var/lib/data-importer/files`.

## Steps: reparse after a pipeline change
1. Put the new pipeline in the source file and apply it. Reparse uses the stored `sources.config`, so apply first. *(real: `boe-bank-rate: source updated, mapping v1 unchanged`)*
   `bin/importer sources apply --dir <dir>`
2. Reparse the latest run that has a stored file, or one run:
   `bin/importer reparse --source <id>` or `bin/importer reparse --source <id> --run <run-id>`
   *(real: `importer reparse: file store: <abs dir>` then `source=boe-bank-rate status=succeeded inserted=0 updated=0 unchanged=854 failed=0 run=3 duration=96ms`, exit 0)*
3. Read the counts (next section). Exit 1 means the run failed or was not possible: see Troubleshooting.

## How to read the counts
Reparse writes a new run (`trigger=manual`, `file_id` = the file it parsed). Each record is compared with `raw_records` by natural key and payload.

| Count | Meaning | Example |
|---|---|---|
| `unchanged` | same key, same payload | same pipeline on BoE: `unchanged=854` (run 3) |
| `updated` | same key, payload changed | evidence pack (task 014 validator): `reparse --run 1` after a pipeline change gave `updated=1 unchanged=1`. NOT reproduced here |
| `inserted` | key not stored before | BoE, `month: [trim, lower]` changed to `month: [trim]`: `inserted=853 unchanged=1` (run 4). The month key changed, so 853 rows are new |
| not counted | rows the new pipeline no longer produces | left in `raw_records`; nothing deletes them (no tombstoning yet) |

- After a key-changing pipeline change, `SELECT count(*) FROM raw_records WHERE source_id = '<id>'` grows: real, 854 then 1707 after run 4. Old-key rows stay.
- Reparse again with the same pipeline: `unchanged` equals the record count (real: run 5, `unchanged=854`).
- Reparse follows the same rules as `run`: duplicate natural keys and `max_error_ratio` ([import-failures.md](import-failures.md)).

## Clean-up (manual; no command exists)
Nothing deletes stored files, so files and `source_files` rows accumulate. A file is needed while a `source_files` row refers to it, because reparse of that run needs it.
1. Never delete a file that a `source_files` row refers to.
2. List the rows (`\d` checked the columns; this query UNVERIFIED):
   `psql "$DATABASE_URL" -X -c 'SELECT id, source_id, run_id, size_bytes, fetched_at, sha256 FROM source_files ORDER BY fetched_at'`
3. List the files on disk (UNVERIFIED): `find "$FILE_STORE_DIR" -type f | sort`
4. A file with no `source_files` row (compare the hex in its name with the `sha256` column) is unreferenced. Deleting it is expected to be safe (NOT tested here). Stop all `importer run` processes first.
5. Do not remove the directory while imports run.

## Troubleshooting
Messages are real output unless marked.

| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| `source=boe-bank-rate status=failed ... run=6` then `importer reparse: import "boe-bank-rate" run 6: stored file 2b09...: stored file is missing: 2b09...` (exit 1) | The file of that run is not under `FILE_STORE_DIR`: deleted, another directory (relative default), or an unmounted disk. The attempt leaves a `failed` run row with no `file_id` (real: runs 6 and 7). | Compare the `file store:` line with where files were written. Export an absolute `FILE_STORE_DIR` and retry. If the file is gone, reparse cannot work for that run: `bin/importer run --source <id>` fetches the source again. |
| Same as above, but run from another directory with `FILE_STORE_DIR` unset: `importer reparse: file store: <dir>/data/files` | Default relative store: the directory you are in has an empty store. | `cd` to the directory used by the earlier runs, or export the absolute path. |
| `importer reparse: import "<id>" run <N>: stored file <sha>: stored file is corrupt (SHA-256 mismatch)` (exit 1) | UNVERIFIED here: the message is from the task 014 evidence pack. The bytes no longer match their name; the file is re-hashed on read and nothing is parsed. | Do not edit the file. Fetch again with `bin/importer run --source <id>` (a fetch of the same bytes rewrites the file, per the evidence pack). Escalate if other files are corrupt too. |
| `importer reparse: FILE_STORE_DIR: file store directory "<p>": mkdir <p>: not a directory` (exit 2) | `FILE_STORE_DIR` names a file. | Point it at a directory. |
| `flag provided but not defined: -dry-run` then `Usage of reparse:` (exit 2) | reparse has no `--dry-run`. Reparse always writes. | Drop the flag. To check a source without writing, use `bin/importer run --source <id> --dry-run` (this one does fetch). |
| `importer reparse: import "nope": source "nope": not found` (exit 1) | Unknown source id. | `psql "$DATABASE_URL" -X -c 'SELECT id FROM sources'` |
| `importer reparse: reparse "<id>": source "<id>": run 999 does not exist or has no stored file: no stored file` (exit 1) | `--run` is unknown, belongs to another source, or has no `file_id` (older than migration 0002, or it failed before storing). No run row is created. | `psql "$DATABASE_URL" -X -c "SELECT id, status, file_id FROM import_runs WHERE source_id = '<id>' ORDER BY id DESC"` and pick a run with `file_id`, or omit `--run`. |
| `importer reparse: DATABASE_URL is not set` (exit 2) | Variable missing. | `export DATABASE_URL=...` (never type the value inline). |
| exit 3 | Another run of the source holds the lock. | Wait, then re-run; see [import-failures.md](import-failures.md). Do not kill it. |

## Verify
1. After a reparse with no pipeline change, the last run row is `manual` with `unchanged` equal to the record count and `file_id` set: `psql "$DATABASE_URL" -X -c 'SELECT id, status, trigger, unchanged, file_id FROM import_runs ORDER BY id DESC LIMIT 1'` (real: run 3 gave `succeeded | manual | 854 | 1`).
2. The non-database checks below print the exit codes and `messages in code: ok`:
<!-- verify -->
```bash
B=$(mktemp -d); trap 'rm -rf "$B"' EXIT
go build -o "$B/importer" ./cmd/importer || exit 1
touch "$B/notadir"
rc=0; env -u DATABASE_URL FILE_STORE_DIR="$B/files" "$B/importer" reparse --source x >/dev/null 2>&1 || rc=$?; test "$rc" -eq 2 && echo "no DATABASE_URL exit $rc"
rc=0; env -u DATABASE_URL FILE_STORE_DIR="$B/files" "$B/importer" reparse --source x --dry-run >/dev/null 2>&1 || rc=$?; test "$rc" -eq 2 && echo "dry-run exit $rc"
rc=0; env -u DATABASE_URL FILE_STORE_DIR="$B/notadir" "$B/importer" reparse --source x >"$B/out" 2>&1 || rc=$?; head -1 "$B/out"; test "$rc" -eq 2 && echo "not-a-dir exit $rc"
grep -rq --include='*.go' 'stored file is missing' internal cmd && grep -rq --include='*.go' 'SHA-256 mismatch' internal && echo "messages in code: ok"
```
*(real output: `no DATABASE_URL exit 2`, `dry-run exit 2`, `importer reparse: FILE_STORE_DIR: file store directory ...: not a directory`, `not-a-dir exit 2`, `messages in code: ok`)*

## Limits (today)
- No retention or clean-up command; no tombstoning (keys no longer produced stay in `raw_records`).
- Runs before migration 0002 have no stored file: reparse cannot use them.
- `reparse` has no `--dry-run`.
- Files live in a local directory only (no object storage).

## Roll back / undo
- Reparse cannot be undone by a command. It writes `raw_records` rows. To return to an older pipeline, re-apply the older source file with `sources apply` and reparse again (UNVERIFIED: the return to the old payloads was not run here). Rows inserted under the new key stay; delete them only with the data owner's decision. `SELECT ... FROM raw_records WHERE first_run_id = <run id>` shows them.
- Failed reparse rows (`status=failed`, no `file_id`) are audit records: leave them.

## Escalate
Send: the command and its full output (stdout and stderr, no secrets), the source id, run id, the `source_files` row and the `import_runs` rows for the source, the `FILE_STORE_DIR` value, and the output of `find "$FILE_STORE_DIR" -type f | sort`. For a `SHA-256 mismatch`, do not overwrite or delete any file before the owner has the details.
