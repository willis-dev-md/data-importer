# Changelog

All notable user-visible changes. Maintained by the `documentor` role. Newest first.

## Unreleased
- Design documentation, source analyses (ONS CPIH, Bank of England base rate, UK HPI, ASHE Table 20), and the software factory with its worker, validator, documentor and runbook-refiner roles.
- Factory: per-task token usage ledger (`scripts/factory-metrics.py`, `/factory-report`), tagged agent launches, and a runbook for it.
- Project scaffold: `cmd/importer` (`version`, `help`; other subcommands print "not implemented"), Makefile (`build`, `test`, `lint`, `fmt`, `testdb-up/down`), ephemeral Postgres test database (`scripts/testdb.sh`) and `internal/testutil`.
