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
| [mapping-validation-errors.md](mapping-validation-errors.md) | `importer mapping validate` prints issues or exits 1/2; every validation code with fix |

## Seeds for later runbooks

Facts from task logs, not yet verified as operator procedures. The owning task confirms them by running them.

- Task 003, owned by 008/009: `store.TryAdvisoryLock` returns `err != nil` when the attempt itself fails (database down,
  connection error) and `ok=false, err=nil` when another run holds the lock. Only the second means "already running".
- Task 003, owned by 008/009: `UpsertRaw` counts Inserted, Updated and Unchanged. Unchanged rows keep their payload but get
  `last_run_id` set. Duplicate keys in one batch count once (last wins).
- Task 003, owned by 008: numbers are canonicalised as exact plain decimals. Expansions above 20000 digits are rejected.
  Postgres jsonb can reject huge values with "value overflows numeric format". A NUL byte in a string fails the whole batch.
  See the 003 log and `docs/data-model.md`.

- Task 007 adds the runtime FieldError codes MISSING_REQUIRED, TYPE_MISMATCH, PARSE_FAILED and VALIDATION_FAILED to
  `mapping-validation-errors.md`.

Planned (created by the tasks in `factory/tasks/`):
import-failures, add-a-source, run-the-grpc-server, demo-and-smoke-test, source-layout-changed, reparse-and-file-retention,
source-url-not-found, uk-hpi-monthly-update, ashe-annual-release.
