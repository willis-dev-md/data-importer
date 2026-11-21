# data-importer

Go service that imports data from CSV files, Excel workbooks and HTTP JSON APIs (structure differs per setup)
into Postgres as raw JSONB, and serves it through a gRPC API that applies per-source mappings on read.
Design lives in `docs/` (start at `docs/design.md`); the docs are the source of truth. If implementation and
docs disagree, change the docs in the same task or raise it, never silently diverge.

## Conventions
- Go 1.24, module `github.com/willis-dev-md/data-importer`. Layout is in `README.md` (`cmd/`, `internal/`, `proto/`, `migrations/`).
- Prefer the standard library. Approved dependencies: `github.com/jackc/pgx/v5`, `github.com/golang-migrate/migrate/v4`
  (or embedded SQL runner), `gopkg.in/yaml.v3`, `google.golang.org/grpc`, `google.golang.org/protobuf`,
  `golang.org/x/text` (encodings), `github.com/xuri/excelize/v2` (xlsx, later). Anything else needs a note in the task log.
- Tests: table-driven, `-race`. Integration tests use a real Postgres via `scripts/testdb.sh` (`DATABASE_URL`), never mocks of the DB.
- Fixtures: `testdata/ons/l55o.csv`, `testdata/boe/baserate-trimmed.xlsx`. Never fetch the network in tests.
- Secrets are never committed or logged. Source configs reference env var names only.
- Errors: wrap with `%w`; no panics on bad input; bad rows are data (recorded), not crashes.


## Communication with the user
- Keep messages short: about half the usual length.
- Message the user only for: a decision they must make; a finished spike or task (one short summary: result, next step); a blocker or anything risky or irreversible.
- No progress narration, no restating what agents or hooks said, no "waiting" updates. Give a one-line status only when asked.
- Put detail in files (task Log, docs, reports) and link to it instead of pasting it.
- Hook reminders (e.g. about uncommitted changes): reply in one line, or not at all.

## Software factory

Work is produced by a factory: an **orchestrator** (the main session) splits work into tasks and four roles do it:
a **worker** implements, a **validator** independently checks, a **documentor** keeps the documentation true, and a
**runbook-refiner** (support) keeps runbooks and troubleshooting guides true. The four roles exist in every factory; the documentor and
runbook-refiner are skipped for a task only when its spec says `none: <reason>` and the validator accepted the reason.

- Workers never validate their own work; validators never edit code; documentor and runbook-refiner never edit code.
- A task is done only when `scripts/validate.sh` passes, the validator returns `PASS`, the documentor and runbook-refiner have run
  (or the spec says `none: <reason>` and the validator accepted it), AND `scripts/factory-docs-check.py check NNN` passes. That script is a
  deterministic check (no agent): only docs changed after the code PASS, listed docs/runbooks changed, links valid, one `Last verified` per
  runbook, and command blocks tagged `<!-- verify -->` still run. A second validator pass happens only if it escalates a warning.
- Model tiering: the worker and validator use the session's strong model; the documentor and runbook-refiner run on a smaller model (`model:` in their agent files, deterministic check + validator as the safety net). A failed docs check is fixed first with the strong model. Track the quality guard in `/factory-report` (role x model table, docs-check FAIL lines in task Logs).
- Docs-role budget: the documentor (20 tool calls) and runbook-refiner (25) reuse an evidence pack from the validator's report and the task Log; they do not re-run whole flows (gate, e2e, demo). `/factory-report` lists launches over budget.
- Max 3 worker→validator rounds per task, then escalate to the user.
- Task specs live in `factory/tasks/NNN-slug.md` (copy `factory/tasks/_template.md`); each has `Docs impact` and `Runbook impact`.
- Runbooks live in `docs/runbooks/` (index in `docs/runbooks/README.md`, template in `_template.md`); only commands that were run are documented.
- Project checks are scripts in `scripts/checks.d/`; add one per quality gate (tests, lint, a11y, ...).
  `00-factory-roles.sh` enforces the roles and the impact sections and is installed by the kit: do not remove it.
- Run the loop with `/factory <goal>`.
- Usage metrics: every agent prompt starts with `TASK <NNN> · round <R> · phase <...>`; `scripts/factory-metrics.py record` keeps the ledger
  `factory/metrics/usage.jsonl` (no model cost, deduplicated per request); `/factory-report` shows the per-task split by role and the
  cross-task review, on request only. The report proposes refinements; the user decides what changes in the factory.
- The documentor and runbook-refiner also run **on request, never on a schedule**: `/document <task|change|topic>` and
  `/runbook <task|incident|refine>`. Do not create scheduled or background jobs for them.
