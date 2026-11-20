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


## Software factory

Work is produced by a factory: an **orchestrator** (the main session) splits work into tasks,
**workers** implement them, **validators** independently check them.

- Workers never validate their own work; validators never edit code.
- A task is done only when `scripts/validate.sh` passes AND the validator agent returns `PASS`.
- Max 3 worker→validator rounds per task, then escalate to the user.
- Task specs live in `factory/tasks/NNN-slug.md` (copy `factory/tasks/_template.md`).
- Project checks are scripts in `scripts/checks.d/`; add one per quality gate (tests, lint, a11y, ...).
- Run the loop with `/factory <goal>`.
