# data-importer

A configurable import pipeline that pulls data from **CSV files**, **Excel workbooks** and **HTTP/JSON APIs** whose structure
differs per setup, lands it in **Postgres** exactly as received, and serves it through a **gRPC API**
that applies a per-source mapping to present a stable, typed shape.

> Status: **early**. `migrate`, `mapping validate`, `sources apply` and `run` (CSV sources) work; `serve` runs the gRPC API (`GetRecord`, `ListRecords` with mapping on read). Start with [docs/design.md](docs/design.md).

## Quick start

The demo imports the ONS L55O fixture (`testdata/ons/l55o.csv`) into Postgres and serves it over gRPC. It needs no
network: the source location is a local `file://` path (established by code reading, see docs/testing.md). The commands below are the ones that `scripts/demo.sh` and the
end-to-end test (`internal/e2e`, see [docs/testing.md](docs/testing.md#end-to-end-test-and-demo-task-012)) run. Anything in
this section that they do not run is marked UNVERIFIED.

### Prerequisites

- Go 1.24 (`go.mod`: `go 1.24.0`, `toolchain go1.24.7`).
- Postgres 16 server binaries (`initdb`, `pg_ctl`), found through `pg_config --bindir` or `/usr/lib/postgresql/*/bin`; `scripts/testdb.sh` uses them.
- `psql` and `python3`; `scripts/demo.sh` needs both.
- Optional: `grpcurl`. When it is installed, `scripts/demo.sh` uses it for the example calls; otherwise it uses the small client in `scripts/demo-client`. The `grpcurl` branch is UNVERIFIED: `grpcurl` was not installed when this was written, so it was never run.

### Make targets

| Target | What it does |
|---|---|
| `make build` | builds `bin/importer` (`bin/` is git-ignored) |
| `make test` | `go test -race ./...`, starts its own database unless `DATABASE_URL` is set |
| `make lint` | gofmt check, `go vet`, `go tool staticcheck` |
| `make fmt` | `gofmt -w .` |
| `make testdb-up` / `make testdb-down` | start / stop the ephemeral test database |

`make build` was run while writing this section. `make test`, `make lint`, `make fmt` and the `testdb-*` targets are UNVERIFIED here;
they are described under [Develop](#develop).

### Demo in one command

From the repository root, `scripts/demo.sh` starts a temporary Postgres, creates a fresh database, runs `migrate up`,
`sources apply`, `run`, starts `serve` on a free local port, makes three `ListRecords` calls and removes everything on exit.
Real output from this repository. The port, timestamps and durations change per run. The first call is shown with its
first record; the records of all three calls have the same shape, so the rest of the output is marked as elided:

<!-- verify -->
```bash
scripts/demo.sh
```

    ==> starting a temporary Postgres
    ==> building the importer
    ==> migrate up
    version 1
    ==> sources apply
    ons-l55o: source created, mapping v1 created
    ==> run (import the fixture)
    source=ons-l55o status=succeeded inserted=642 updated=0 unchanged=0 failed=0 run=1 duration=61ms
    ==> serve (plaintext gRPC on a free local port)
        listening on 127.0.0.1:43497

    ==> first 3 records
        go run ./scripts/demo-client -addr 127.0.0.1:43497 -size 3
    {
      "records":  [
        {
          "id":  "ons-l55o:[\"L55O\",\"1989 APR\"]",
          "sourceId":  "ons-l55o",
          "fields":  {
            "dimensions":  {},
            "measure":  "cpih_annual_rate",
            "period_end":  "1989-04-30",
            "period_start":  "1989-04-01",
            "period_type":  "month",
            "series_id":  "L55O",
            "series_title":  "CPIH ANNUAL RATE 00: ALL ITEMS 2015=100",
            "unit":  "%",
            "value":  5.6
          },
          "extra":  {},
          "importedAt":  "2026-11-21T11:15:35.543815Z"
        },
        [records 2 and 3 of this call, elided]
      ],
      "nextPageToken":  "WyJMNTVPIiwiMTk4OSBERUMiXQ"
    }

    ==> monthly records
        go run ./scripts/demo-client -addr 127.0.0.1:43497 -type month -size 3
    [JSON response elided: 3 records, same shape as the first call]

    ==> monthly records with value >= 3.5
        go run ./scripts/demo-client -addr 127.0.0.1:43497 -type month -min 3.5 -size 3
    [JSON response elided: 3 records, same shape as the first call]

    ==> done; stopping the server and removing the temporary database

`inserted=642 ... failed=0` is the import of the whole fixture; each of the three calls asks for a page of 3 records.

### Step by step

Run these from the repository root. First start a database and keep its `DATABASE_URL`:

    eval "$(scripts/testdb.sh start)"

Then the steps, as one script (the same steps as the demo, with a `serve` in the background that the script stops on exit).
The script creates temp directories and removes them on exit. On a database that already has the source, `sources apply`
and `run` report `unchanged` instead of `inserted`; the script only checks that each step exits 0.

1. Build the binary: `go build -o "$D/importer" ./cmd/importer` (the same command the e2e test uses; `make build` writes `bin/importer` instead).
2. Make a source directory: copy the ONS mapping and write the source with its `location` pointed at the fixture.
3. `importer migrate up` prints `version 1`.
4. `importer sources apply --dir "$D" --schema canonical-schema.yaml` registers the source and mapping v1.
5. `importer run --source ons-l55o` imports the fixture (`inserted=642`).
6. `GRPC_ADDR=127.0.0.1:0 TLS_CERT_FILE= TLS_KEY_FILE= importer serve` serves plaintext gRPC on a random port; the log line with `addr=` gives the address. The e2e test stops it with SIGTERM and expects exit 0.
7. `go run ./scripts/demo-client -addr <addr> -size 3` calls `ListRecords` (`-type month` and `-min 3.5` add filters).

<!-- verify: db -->
```bash
set -eu
D=$(mktemp -d)
L=$(mktemp -d)
SRV=""
cleanup() { [ -n "$SRV" ] && kill "$SRV" 2>/dev/null || true; rm -rf "$D" "$L"; }
trap cleanup EXIT
go build -o "$D/importer" ./cmd/importer
sed -E "s|^location:.*$|location: file://$PWD/testdata/ons/l55o.csv|" docs/examples/ons-l55o.source.yaml > "$D/ons-l55o.source.yaml"
cp docs/examples/ons-l55o.mapping.yaml "$D/"
"$D/importer" migrate up
"$D/importer" sources apply --dir "$D" --schema canonical-schema.yaml
"$D/importer" run --source ons-l55o
GRPC_ADDR=127.0.0.1:0 TLS_CERT_FILE= TLS_KEY_FILE= "$D/importer" serve >"$L/serve.log" 2>&1 &
SRV=$!
ADDR=""
for _ in $(seq 1 300); do
  ADDR=$(sed -nE 's/.*serving (plaintext )?gRPC.* addr=([^ ]+).*/\2/p' "$L/serve.log" | head -1)
  [ -n "$ADDR" ] && break
  sleep 0.1
done
[ -n "$ADDR" ] || { cat "$L/serve.log"; exit 1; }
echo "serving on $ADDR"
cat "$L/serve.log"
go run ./scripts/demo-client -addr "$ADDR" -size 3 | head -n 4
```

Real output of that script (the `head -n 4` cuts the JSON response to its first four lines; the address and timestamps change per run):

    version 1
    ons-l55o: source created, mapping v1 created
    source=ons-l55o status=succeeded inserted=642 updated=0 unchanged=0 failed=0 run=1 duration=50ms
    serving on 127.0.0.1:45481
    time=2026-11-21T11:17:21.536Z level=WARN msg="TLS_CERT_FILE and TLS_KEY_FILE are not set: serving plaintext gRPC" addr=127.0.0.1:45481
    {
      "records":  [
        {
          "id":  "ons-l55o:[\"L55O\",\"1989 APR\"]",

Stop the database with `scripts/testdb.sh stop` when done.

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
see `docs/operations.md`). Unknown subcommands exit 2. `scripts/validate.sh` runs the full gate.
