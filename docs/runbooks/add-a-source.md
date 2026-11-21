# Runbook: Add a source and run its first import

**Owner role:** on-call engineer  |  **Severity if this happens:** low (a new source; nothing existing changes unless you re-apply an existing id)
**Last verified:** 2026-11-21, task 009 (real `bin/importer` from `make build` against a throwaway Postgres 16 from `scripts/testdb.sh`, ONS example files with `location` set to `testdata/ons/l55o.csv`; every message and exit code below is real output; `http_json` sources and `token_env` with a real server are UNVERIFIED, see the end)

## When to use
You have a new CSV source (or a changed source/mapping file) and want it registered and imported from the command line.

## Prerequisites
- Built binary: `make build` (gives `bin/importer`), `psql`, a migrated database (`importer migrate up`, see [database-migrations.md](database-migrations.md)).
- `DATABASE_URL` set. Never paste the URL or any token into tickets, files or shell history.
- `canonical-schema.yaml` in the current directory (default of `--schema`), so run from the repo root.
- Only `kind: csv` sources can be applied today: `kind: http_json` is rejected with `kind: unsupported kind "http_json" (supported: csv)`.

## Steps
1. Inspect the source first (real structure, delimiter, preamble, encoding): [onboarding-a-new-source.md](onboarding-a-new-source.md).
2. Create a directory with one pair per source: `<name>.source.yaml` and optional `<name>.mapping.yaml` (see `docs/examples/ons-l55o.*.yaml`). The mapping's `source:` must equal the source's `id:`. For a local file set `location` to an absolute path. Secrets: only env var NAMES go in files, e.g. `auth: { type: bearer, token_env: MY_API_TOKEN }`; export the variable in the shell that runs `importer run`, never write the value into a file.
3. Validate each mapping (silent and exit 0 when fine; problems: [mapping-validation-errors.md](mapping-validation-errors.md)):
<!-- verify -->
```bash
make build >/dev/null && bin/importer mapping validate docs/examples/ons-l55o.mapping.yaml
```
4. Apply the directory (all-or-nothing, one transaction; `--dir` defaults to `$SOURCES_DIR`):
   ```
   bin/importer sources apply --dir ./sources
   ```
   *(real output: `ons-l55o: source created, mapping v1 created`)*. Re-run with unchanged files: `ons-l55o: source unchanged, mapping v1 unchanged` (nothing written). Changing the mapping content creates a new version and activates it: `ons-l55o: source unchanged, mapping v2 created` (the `version:` key in the file is ignored; comment/whitespace-only edits do not create a version; going back to older content creates a new version).
5. Dry run (reads the source, writes nothing; the summary shows zeros, real counts come only from a real run):
   ```
   bin/importer run --source ons-l55o --dry-run
   ```
   *(real: `source=ons-l55o status=succeeded inserted=0 updated=0 unchanged=0 failed=0 run=dry-run duration=11ms`)*
6. Real run:
   ```
   bin/importer run --source ons-l55o
   ```
   *(real: `source=ons-l55o status=succeeded inserted=642 updated=0 unchanged=0 failed=0 run=1 duration=64ms`; the second run gave `inserted=0 updated=0 unchanged=642 failed=0 run=2`)*
7. Verify in the database:
   ```
   psql "$DATABASE_URL" -c "SELECT id, status, inserted, updated, unchanged, failed FROM import_runs WHERE source_id = 'ons-l55o' ORDER BY id DESC LIMIT 3" -c "SELECT count(*) FROM raw_records WHERE source_id = 'ons-l55o'" -c "SELECT s.active_mapping_version, max(m.version) AS latest FROM sources s JOIN mappings m ON m.source_id = s.id WHERE s.id = 'ons-l55o' GROUP BY 1"
   ```
   *(real: runs `succeeded`, 642 rows in `raw_records`)*

## Exit codes
| Command | Code | Meaning |
|---|---|---|
| `importer run` | 0 | run `succeeded` |
| | 1 | run `failed`/`aborted` (summary on stdout, error on stderr), unknown source, DB error |
| | 2 | usage: missing `--source`, `DATABASE_URL`, or the env var named by `auth.token_env` |
| | 3 | already running (another session holds the source lock) |
| `sources apply` | 0 / 1 / 2 | ok / invalid files (nothing written) / usage (no `--dir` and no `SOURCES_DIR`, unreadable dir, no `DATABASE_URL`) |
| `mapping validate` | 0 / 1 / 2 | see [mapping-validation-errors.md](mapping-validation-errors.md) |
| `migrate` | 1 | missing `DATABASE_URL` (older command; the newer ones exit 2) |

## Troubleshooting
Paths in messages are the real paths you passed.

| Symptom (exact message) | Diagnosis | Fix |
|---|---|---|
| `usage: importer run --source <id> [--dry-run]` (exit 2) | `--source` missing | Pass `--source <id>` |
| `importer run: DATABASE_URL is not set` / `importer sources apply: DATABASE_URL is not set` (exit 2) | env var missing or empty | `export DATABASE_URL=...` (do not type the value inline in shared history) |
| `importer migrate: DATABASE_URL is not set` (exit 1) | same, older exit code | same |
| `importer sources apply: no directory: pass --dir or set SOURCES_DIR` (exit 2) | no `--dir` and no `SOURCES_DIR` | pass `--dir <dir>` or export `SOURCES_DIR` |
| `importer sources apply: "<dir>" is not a readable directory` (exit 2) | wrong path | fix the path |
| `importer sources apply: sources dir <dir>: no *.source.yaml files found` + `nothing was written` (exit 1) | empty or wrong directory | check file naming `<name>.source.yaml` |
| `importer run: source "nope": not found` (exit 1) | source id not applied (or typo) | `psql "$DATABASE_URL" -c "SELECT id FROM sources"`, apply the directory first |
| `<file>.mapping.yaml: SOURCE_MISMATCH: mapping says source "other-id" but <file>.source.yaml is source "ons-l55o"` then `invalid files; nothing was written` (exit 1) | `source:` in the mapping differs from the source `id:` | make them equal |
| `<dir>/typo.mapping.yaml: mapping file has no matching typo.source.yaml` then `nothing was written` (exit 1) | orphan mapping (typo in the file name) | rename to match the source file |
| `source config: yaml: unmarshal errors:` / `line 21: field bogus_key not found in type config.Source` then `nothing was written` (exit 1) | unknown YAML key (also any literal-secret key: there is none) | remove or fix the key; the `docs/examples/partner-api.source.yaml` example is rejected this way (`pagination`, `records_path`, `rate_limit_per_sec` not supported yet) |
| `importer run: environment variable DEMO_API_TOKEN (auth.token_env) is not set or empty` (exit 2) | the source names an env var that is not exported | `export` it in the shell running the import, from your secret store; never write it in a file |
| `importer run: import "ons-l55o": import already running for this source` (exit 3) | another live run holds the source lock | wait; see [import-failures.md](import-failures.md) (do not kill it) |
| `importer run: marked 1 abandoned run(s) failed: [7]` on stderr | informational: the sweep closed a stale `running` row | nothing; see [import-failures.md](import-failures.md) |

## Verify
Step 7 shows `succeeded` runs and the expected row count; a repeat run shows `unchanged` equal to that count. A self-contained check of validate/apply/idempotence against a throwaway database (writes only to that database):
<!-- verify: db -->
```bash
set -e
make build >/dev/null
d=$(mktemp -d)
cp docs/examples/ons-l55o.*.yaml "$d"/
bin/importer migrate up
bin/importer sources apply --dir "$d"
bin/importer sources apply --dir "$d" | grep -q 'source unchanged, mapping v[0-9]* unchanged'
rm -r "$d"
```

## Roll back / undo
There is no un-apply command. Mappings are versioned: to go back, re-apply the older content (it becomes a new, active version). Data from a run stays tagged by run id (see [import-failures.md](import-failures.md)). Removing a source row by hand is UNVERIFIED and not recommended.

## Escalate
Escalate to the importer maintainers when apply reports an error not in the table, or a run fails with a text not in [import-failures.md](import-failures.md). Include the command, exit code, stderr text (no secrets), source id, run id.

UNVERIFIED: a real `token_env` request against an HTTP server (only the missing-variable exit 2 was run; the secret value never appeared in output or in the stored `config`, which holds only the variable name); `kind: http_json` (rejected by apply).
