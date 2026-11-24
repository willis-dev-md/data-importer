# Runbooks

Operational guides for running, supporting and recovering this project. Maintained by the `runbook-refiner`
role of the software factory (see `CLAUDE.md`). Copy `_template.md` for a new one and list it here.
Every task spec names the runbooks it creates or updates under "Runbook impact"; runbooks that do not exist yet are
created by the task that introduces the behaviour.

| Runbook | When to use |
|---|---|
| [blocked-network-host.md](blocked-network-host.md) | a download fails with `CONNECT tunnel failed, response 403` |
| [github-push-refused.md](github-push-refused.md) | `git push` returns 403 after attaching a repo |
| [onboarding-a-new-source.md](onboarding-a-new-source.md) | inspecting a new CSV/Excel/zip/API source before writing config; setting a `schedule:` block (daily/weekly/monthly, timezone, `sources apply` `next run`, invalid schedule) |
| [factory-usage-ledger.md](factory-usage-ledger.md) | record or read per-task token usage; ledger errors |
| [local-development.md](local-development.md) | build, test, start/stop the test database; Postgres-as-root, TMPDIR, go.mod bump, CI_REQUIRE_DB |
| [database-migrations.md](database-migrations.md) | `importer migrate` up/down/version (schema version 3), safe apply, `Dirty database version` recovery, psql schema inspection |
| [add-a-source.md](add-a-source.md) | register a CSV source and mapping (`sources apply`), dry-run, first real run, verify; `importer run` exit codes and CLI error messages |
| [import-failures.md](import-failures.md) | an import run `failed`, `aborted` (max_error_ratio), `already running`, was cancelled, or is stuck `running` (abandoned-run sweep); `unchanged_file` status (skipped, exit 0); `duplicate natural key` rejects (first record wins); dry-run duplicate check and its key limit; reading `import_runs` / `import_errors`; `importer run` exit codes; invalid `DB_MAX_CONNS` / `DB_LOCK_MAX_CONNS` / `DB_LOCK_WAIT` (exit 2); `no lock connection available`; connection-budget WARN; `lost source lock (session terminated)` (lock session killed, DB restart, network drop); invalid `DB_LOCK_CHECK_INTERVAL` (exit 2) |
| [reparse-and-file-retention.md](reparse-and-file-retention.md) | `importer reparse` after a pipeline change (no download); where fetched files are stored (`FILE_STORE_DIR`, cwd-relative default); `stored file is missing` / `corrupt`; manual clean-up of stored files |
| [source-url-not-found.md](source-url-not-found.md) | a source with `resolve:` finds no file for any month (`resolve: no candidate location exists`), a probe fails (`resolve: probing a candidate failed`), an https-to-http redirect is refused, `archive.member` matches zero or several names, `sources apply` rejects `resolve`/`archive`; where `_location` is stored; blocked hosts |
| [source-layout-changed.md](source-layout-changed.md) | a publisher changed an xlsx/csv layout: sheet renamed (`sheet ... not found; available sheets`), column letters shifted (`aborted`, `normalize: column ... is not a number`), title row or preamble changed (silent or extra row); compare, fix the config, verify |
| [mapping-validation-errors.md](mapping-validation-errors.md) | `importer mapping validate` prints issues or exits 1/2; every validation code with fix; runtime field errors (MISSING_REQUIRED, TYPE_MISMATCH, PARSE_FAILED, VALIDATION_FAILED) |
| [run-the-grpc-server.md](run-the-grpc-server.md) | start/stop `importer serve` (plaintext, TLS), health `SERVING`/`NOT_SERVING`, reflection, graceful shutdown; startup errors; `ListRecords`/`GetRecord` checks, short pages and `next_page_token`, `SCAN_LIMIT`; data-call errors by code; `mapping_errors` in responses; `Internal` in the server log |
| [demo-and-smoke-test.md](demo-and-smoke-test.md) | run `scripts/demo.sh` (ONS CSV to Postgres to gRPC), expected output, leftover processes after an interrupted run; the e2e test `go test ./internal/e2e`; release smoke-test checklist |
| [uk-hpi-monthly-update.md](uk-hpi-monthly-update.md) | monthly run of `uk-hpi-average-prices` and `uk-hpi-property-type` (files appear about two months late); expected summary and counts; `unchanged_file`; a month that is missing; sizing; scheduler not implemented yet |
| [ashe-annual-release.md](ashe-annual-release.md) | yearly ONS ASHE Table 20 release (October): add a provisional or revised edition as a standalone source; member regex in the zip (`archive member selection failed`); marker forms; choosing revised over provisional; checking the load (5812 / 5816 records) |

## Seeds for later runbooks

Facts from task logs, not yet verified as operator procedures. The owning task confirms them by running them.

- Task 003 (owned by 009; the Inserted/Updated/Unchanged counts were confirmed by the CLI run in add-a-source.md; still unverified: `last_run_id` set on unchanged rows). The duplicate-key rule ("last wins") is superseded by task 018: the first record wins, later ones are rejected as `duplicate natural key` (see import-failures.md).
- Task 003, owned by 008: numbers are canonicalised as exact plain decimals. Expansions above 20000 digits are rejected.
  Postgres jsonb can reject huge values with "value overflows numeric format" (not reproduced; the NUL-byte half of this seed is
  covered in import-failures.md).
