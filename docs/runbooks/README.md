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

Planned (created by the tasks in `factory/tasks/`): local-development, database-migrations, mapping-validation-errors,
import-failures, add-a-source, run-the-grpc-server, demo-and-smoke-test, source-layout-changed, reparse-and-file-retention,
source-url-not-found, uk-hpi-monthly-update, ashe-annual-release.
