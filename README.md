# data-importer

A configurable import pipeline that pulls data from **CSV files**, **Excel workbooks** and **HTTP/JSON APIs** whose structure
differs per setup, lands it in **Postgres** exactly as received, and serves it through a **gRPC API**
that applies a per-source mapping to present a stable, typed shape.

> Status: **early**. `migrate`, `mapping validate`, `sources apply` and `run` (CSV sources) work; `serve` is planned. Start with [docs/design.md](docs/design.md).

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
| [docs/sources/](docs/sources) | Analysis of each real source (ONS CPIH CSV, Bank of England XLSX, UK HPI CSV, ASHE Table 20 XLSX in a zip) |
| [docs/mapping-spec.md](docs/mapping-spec.md) | Source + mapping YAML format, transforms, errors |
| [docs/api.md](docs/api.md) | gRPC services and proto sketch |
| [docs/operations.md](docs/operations.md) | Config, running, security, observability |
| [docs/testing.md](docs/testing.md) | Test strategy and quality gates |
| [docs/runbooks/](docs/runbooks) | Runbooks and troubleshooting guides (kept by the runbook-refiner role) |
| [CHANGELOG.md](CHANGELOG.md) | Changes (kept by the documentor role) |
| [docs/roadmap.md](docs/roadmap.md) | Milestones and task breakdown |
| [docs/open-questions.md](docs/open-questions.md) | Decisions still to make |
| [docs/parked/](docs/parked) | Ideas deferred for now (MCP source) |
| [docs/adr/](docs/adr) | Architecture decision records |
| [docs/examples/](docs/examples) | Sample source configs |

## Usage

Register a source and its mapping from files, then import. Needs `DATABASE_URL`; the CLI reference, exit codes and
the secrets rule are in [docs/operations.md](docs/operations.md#cli). This block builds the binary, points the ONS
example at the fixture and runs it (real output on an empty database):

<!-- verify: db -->
```bash
D=$(mktemp -d)
go build -o "$D/importer" ./cmd/importer
cp docs/examples/ons-l55o.mapping.yaml "$D/"
sed "s#^location:.*#location: $PWD/testdata/ons/l55o.csv#" docs/examples/ons-l55o.source.yaml > "$D/ons-l55o.source.yaml"
"$D/importer" migrate up
"$D/importer" sources apply --dir "$D"          # or set SOURCES_DIR
"$D/importer" run --source ons-l55o --dry-run   # parses and reports, writes nothing
"$D/importer" run --source ons-l55o
rm -r "$D"
```

    version 1
    ons-l55o: source created, mapping v1 created
    source=ons-l55o status=succeeded inserted=0 updated=0 unchanged=0 failed=0 run=dry-run duration=11ms
    source=ons-l55o status=succeeded inserted=642 updated=0 unchanged=0 failed=0 run=1 duration=59ms

Exit codes of `run`: 0 ok, 1 failed or aborted, 2 usage error, 3 already running.

## Planned stack

Go, Postgres (JSONB), gRPC (+ reflection and health), `golang-migrate`; integration tests use a real Postgres via `scripts/testdb.sh` (see Develop).

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

## Develop

Requires Go 1.24 and, for the integration tests, Postgres 16 server binaries (`initdb`, `pg_ctl`).

| Target | What it does |
|---|---|
| `make build` | builds `bin/importer` |
| `make test` | `go test -race ./...`; starts and stops an ephemeral Postgres unless `DATABASE_URL` is set |
| `make lint` | gofmt check, `go vet`, `go tool staticcheck` |
| `make fmt` | `gofmt -w .` |
| `make testdb-up` / `make testdb-down` | start / stop the ephemeral test database |

Test database: `eval "$(scripts/testdb.sh start)"` starts Postgres 16 in a temp dir on a random free port
(127.0.0.1 only, `trust` auth; as the `postgres` OS user via `runuser` when run as root) and exports
`DATABASE_URL` and `TESTDB_DIR`. `scripts/testdb.sh stop` stops it and deletes the directory; both are idempotent.
Tests get a fresh database each via `internal/testutil`. Without `DATABASE_URL` DB tests skip; with
`CI_REQUIRE_DB=1` they fail instead. Apply the schema with `importer migrate up|down|version` (needs `DATABASE_URL`; `down` rolls back one step;
see `docs/operations.md`). The planned subcommand `serve` exits 1 with "not implemented"; unknown ones exit 2. `scripts/validate.sh` runs the full gate.
