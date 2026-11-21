# Runbooks

Operational guides for running, supporting and recovering this project. Maintained by the `runbook-refiner`
role of the software factory (see `CLAUDE.md`). Copy `_template.md` for a new one and list it here.
Every task spec names the runbooks it creates or updates under "Runbook impact"; runbooks that do not exist yet are
created by the task that introduces the behaviour.

| Runbook | When to use |
|---|---|
| [blocked-network-host.md](blocked-network-host.md) | a download fails with `CONNECT tunnel failed, response 403` |
| [github-push-refused.md](github-push-refused.md) | `git push` returns 403 after attaching a repo |
| [onboarding-a-new-source.md](onboarding-a-new-source.md) | inspecting a new CSV/Excel/zip/API source before writing config |
| [factory-usage-ledger.md](factory-usage-ledger.md) | record or read per-task token usage; ledger errors |
| [local-development.md](local-development.md) | build, test, start/stop the test database; Postgres-as-root, TMPDIR, go.mod bump, CI_REQUIRE_DB |
| [database-migrations.md](database-migrations.md) | `importer migrate` up/down/version, safe apply, `Dirty database version` recovery, psql schema inspection |
| [add-a-source.md](add-a-source.md) | register a CSV source and mapping (`sources apply`), dry-run, first real run, verify; `importer run` exit codes and CLI error messages |
| [import-failures.md](import-failures.md) | an import run `failed`, `aborted` (max_error_ratio), `already running`, was cancelled, or is stuck `running` (abandoned-run sweep); reading `import_runs` / `import_errors`; `importer run` exit codes |
| [mapping-validation-errors.md](mapping-validation-errors.md) | `importer mapping validate` prints issues or exits 1/2; every validation code with fix; runtime field errors (MISSING_REQUIRED, TYPE_MISMATCH, PARSE_FAILED, VALIDATION_FAILED) |

## Seeds for later runbooks

Facts from task logs, not yet verified as operator procedures. The owning task confirms them by running them.

- Task 003 (owned by 009; the Inserted/Updated/Unchanged counts were confirmed by the CLI run in add-a-source.md; still unverified: `last_run_id` set on unchanged rows, duplicate keys in one batch count once, last wins).
- Task 003, owned by 008: numbers are canonicalised as exact plain decimals. Expansions above 20000 digits are rejected.
  Postgres jsonb can reject huge values with "value overflows numeric format" (not reproduced; the NUL-byte half of this seed is
  covered in import-failures.md).

Planned (created by the tasks in `factory/tasks/`):
run-the-grpc-server, demo-and-smoke-test, source-layout-changed, reparse-and-file-retention,
source-url-not-found, uk-hpi-monthly-update, ashe-annual-release.
