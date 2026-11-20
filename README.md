# data-importer

A configurable import pipeline that pulls data from **CSV files**, **Excel workbooks** and **HTTP/JSON APIs** or **MCP servers** whose structure
differs per setup, lands it in **Postgres** exactly as received, and serves it through a **gRPC API**
that applies a per-source mapping to present a stable, typed shape.

> Status: **design phase**. No code yet. Start with [docs/design.md](docs/design.md).

## Approach in one paragraph

We use **ELT, not ETL** ([ADR-0001](docs/adr/0001-elt-over-etl.md)). Source adapters parse CSV/API data into
plain key/value records and store them untouched as JSONB in `raw_records`. A versioned **mapping** per source
(YAML) describes how to turn a raw record into the canonical `Record` shape. The gRPC layer applies the mapping
at read time, so fixing a mapping never requires a re-import and no source data is ever lost.

## Documentation

| Doc | Purpose |
|---|---|
| [docs/design.md](docs/design.md) | Goals, architecture, components, flows |
| [docs/data-model.md](docs/data-model.md) | Postgres schema |
| [docs/source-groups.md](docs/source-groups.md) | Many datasets from one definition |
| [docs/sources/](docs/sources) | Analysis of each real source (ONS CSV, Bank of England XLSX) |
| [docs/mapping-spec.md](docs/mapping-spec.md) | Source + mapping YAML format, transforms, errors |
| [docs/api.md](docs/api.md) | gRPC services and proto sketch |
| [docs/operations.md](docs/operations.md) | Config, running, security, observability |
| [docs/testing.md](docs/testing.md) | Test strategy and quality gates |
| [docs/roadmap.md](docs/roadmap.md) | Milestones and task breakdown |
| [docs/open-questions.md](docs/open-questions.md) | Decisions still to make |
| [docs/adr/](docs/adr) | Architecture decision records |
| [docs/examples/](docs/examples) | Sample source configs |

## Planned stack

Go, Postgres (JSONB), gRPC (+ reflection and health), `golang-migrate`, `testcontainers-go`.

## Planned layout

    cmd/importer/        CLI + server entrypoint
    internal/source/     csv and http adapters
    internal/mapping/    mapping parser, validator, evaluator
    internal/store/      postgres access, migrations
    internal/importer/   run orchestration
    internal/server/     gRPC services
    proto/               .proto definitions
    migrations/          SQL migrations
    docs/                design documentation
