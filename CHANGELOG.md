# Changelog

All notable user-visible changes. Maintained by the `documentor` role. Newest first.

## Unreleased
- `importer sources apply --dir <dir>` (default `$SOURCES_DIR`) registers sources and versioned mappings from files, all-or-nothing; `importer run --source <id> [--dry-run]` imports and prints a summary line (exit 0/1/2/3); source `auth.token_env` is resolved at run time and never stored; `run` marks runs left `running` by a dead process as failed.
- Importer core (library, no CLI change): `importer.Run` imports a source into `raw_records` in batches with per-source locking, dry run, row errors in `import_errors`, `max_error_ratio` abort, shape and preamble metadata on the run; a hard-killed run stays `running` until the task 009 sweep.
- Mapping evaluator (library, no CLI change): `mapping.Eval` applies a mapping to a raw payload with transforms, validators, period parsing and per-field errors; decimals are `float64` (precision limit documented in `docs/mapping-spec.md`).
- `importer mapping validate [--schema file] <file>` checks a mapping against the new `canonical-schema.yaml` (observation, v2) and reports `path: code: message` issues (exit 0/1/2); the `acme-csv` and `partner-api` examples were rewritten as observation mappings.
- CSV metadata preamble (`csv.preamble` with `lines` or `until`), headerless files with named columns, `@meta.<Key>` natural keys, and `_meta`/`_source` in each record; the ONS L55O example now loads and parses its fixture (642 records, 0 errors).
- CSV source adapter (library, no CLI change): strict source config validation (`internal/config`), fetch from path, `file://` or `https://` with size/time limits and SHA-256, and a row-error-tolerant CSV reader (`internal/source`).
- Design documentation, source analyses (ONS CPIH, Bank of England base rate, UK HPI, ASHE Table 20), and the software factory with its worker, validator, documentor and runbook-refiner roles.
- Factory: per-task token usage ledger (`scripts/factory-metrics.py`, `/factory-report`), tagged agent launches, and a runbook for it.
- Project scaffold: `cmd/importer` (`version`, `help`; other subcommands print "not implemented"), Makefile (`build`, `test`, `lint`, `fmt`, `testdb-up/down`), ephemeral Postgres test database (`scripts/testdb.sh`) and `internal/testutil`.
- Core Postgres schema (`sources`, `mappings`, `import_runs`, `raw_records`, `import_errors`) as embedded golang-migrate migration `0001_core`, and `importer migrate up|down|version` (`down` rolls back one step).
- Store layer (library, no CLI change): natural-key encoding and content hash (`internal/record`), `UpsertRaw` with inserted/updated/unchanged counts, import-run lifecycle and per-source advisory lock (`internal/store`).
