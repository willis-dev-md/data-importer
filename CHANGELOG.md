# Changelog

All notable user-visible changes. Maintained by the `documentor` role. Newest first.

## Unreleased
- CSV source adapter (library, no CLI change): strict source config validation (`internal/config`), fetch from path, `file://` or `https://` with size/time limits and SHA-256, and a row-error-tolerant CSV reader (`internal/source`).
- Design documentation, source analyses (ONS CPIH, Bank of England base rate, UK HPI, ASHE Table 20), and the software factory with its worker, validator, documentor and runbook-refiner roles.
- Factory: per-task token usage ledger (`scripts/factory-metrics.py`, `/factory-report`), tagged agent launches, and a runbook for it.
- Project scaffold: `cmd/importer` (`version`, `help`; other subcommands print "not implemented"), Makefile (`build`, `test`, `lint`, `fmt`, `testdb-up/down`), ephemeral Postgres test database (`scripts/testdb.sh`) and `internal/testutil`.
- Core Postgres schema (`sources`, `mappings`, `import_runs`, `raw_records`, `import_errors`) as embedded golang-migrate migration `0001_core`, and `importer migrate up|down|version` (`down` rolls back one step).
- Store layer (library, no CLI change): natural-key encoding and content hash (`internal/record`), `UpsertRaw` with inserted/updated/unchanged counts, import-run lifecycle and per-source advisory lock (`internal/store`).
