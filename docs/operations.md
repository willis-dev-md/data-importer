# Operations

## Configuration (env)
| Var | Purpose |
|---|---|
| `DATABASE_URL` | Postgres DSN |
| `GRPC_ADDR` | listen address, default `:8443` (all interfaces) |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | server TLS; set both or neither (see [`importer serve`](#importer-serve)) |
| `AUTH_TOKENS_FILE` | token -> roles/sources map |
| `IMPORT_CONCURRENCY` | max simultaneous import runs (default 4) |
| `SOURCES_DIR` | directory of source + mapping YAML; default for `importer sources apply --dir` |
| `SCAN_LIMIT` | `importer serve` only: raw rows one `ListRecords` call may map, default `50000`; must be a positive integer, otherwise `importer serve` exits 2 before it connects to the database ([details](#importer-serve)) |
| `DB_MAX_CONNS` | work pool size, default `max(4, NumCPU)`; positive integer, otherwise exit 2 ([details](#connection-pool-and-source-locks)) |
| `DB_LOCK_MAX_CONNS` | lock connections per process (concurrent source locks), default `8`; positive integer ([details](#connection-pool-and-source-locks)) |
| `DB_LOCK_WAIT` | how long a run waits for a free lock connection, default `30s`; positive Go duration ([details](#connection-pool-and-source-locks)) |
| token env vars | any name given in a source's `auth.token_env`; read at `importer run` time only |

## Connection pool and source locks

`importer run`, `importer sources apply` and `importer serve` each open one work pool (`DB_MAX_CONNS`) and keep
separate lock connections (`DB_LOCK_MAX_CONNS`). A source's advisory lock holds one lock connection, never a work-pool
connection, so a run always finds the connections it needs to finish, and runs cannot deadlock on the pool. Before
this change, N concurrent runs with a pool of N or fewer connections could wait forever.

| Var | Default | Meaning |
|---|---|---|
| `DB_MAX_CONNS` | `max(4, NumCPU)` | work pool size (the pgxpool default) |
| `DB_LOCK_MAX_CONNS` | `8` | lock connections per process = concurrent source locks per process |
| `DB_LOCK_WAIT` | `30s` | Go duration; how long a run waits for a free lock connection |

- An unset or empty variable means the default. An invalid value (`0`, negative, not a number, or a duration without
  a unit such as `30`) exits 2 before any database connection is made, and the message names the variable:

<!-- verify -->
```bash
go build -o bin/importer ./cmd/importer
rc=0; DB_MAX_CONNS="0" DATABASE_URL=postgres://postgres@127.0.0.1:1/x bin/importer run --source ons-l55o || rc=$?
echo "exit $rc"
test "$rc" -eq 2
```

    importer run: DB_MAX_CONNS="0": want a positive integer
    exit 2

- Total connections per process = `DB_MAX_CONNS + DB_LOCK_MAX_CONNS`; keep it below Postgres `max_connections`. After
  connecting, each process logs one WARN (never a failure) when the sum is larger than `max_connections`. `serve`
  counts the lock connections although it takes no locks, which is conservative. (The WARN was checked by the
  validator at `max_connections=10` with 8 + 8; it was not re-run for this document.)
- When every lock connection is in use, a run waits up to `DB_LOCK_WAIT` (or until its context is cancelled), then
  fails with `no lock connection available: all N are in use after waiting 30s` before it creates a run row. The CLI
  exits 1 for this. From the CLI this cannot happen today, because each CLI process starts one run and has a free
  lock connection: UNVERIFIED from the CLI, verified by a store/importer test only. It applies to in-process callers
  that start many runs at once.
- Lock connections use a copy of the work pool's connection settings (`application_name`, `search_path`, `sslmode`).
  Pool connect hooks are not applied to them (none are set). TLS on lock connections is not tested.

## CLI
    importer migrate up
    importer serve
    importer sources apply [--dir <dir>] [--schema <file>]
    importer run --source <id> [--dry-run]
    importer group plan|apply <group.yaml>
    importer rollback --run <id>
    importer reparse --source <id> [--run <id>]   # re-run the structural pipeline on the stored file
    importer infer <file-or-url> > draft-mapping.yaml
    importer mapping validate [--schema <file>] <file>
    importer mapping materialize <source>

### `importer migrate`
Reads `DATABASE_URL` (required). Applies the embedded SQL in `migrations/` with golang-migrate; the applied version
is kept in the `schema_migrations` table. Each form prints the resulting version.

| Command | Effect |
|---|---|
| `importer migrate up` | apply all pending migrations; safe to repeat |
| `importer migrate down` | roll back **one** migration; no-op at version 0 |
| `importer migrate version` | print the current version (`version 0` on an empty database) |

Output and exit codes, run against an empty Postgres 16:

    $ importer migrate version     -> version 0   (exit 0)
    $ importer migrate up          -> version 1   (exit 0)
    $ importer migrate up          -> version 1   (exit 0, nothing to do)
    $ importer migrate down        -> version 0   (exit 0)
    $ importer migrate down        -> version 0   (exit 0, no-op)
    $ importer migrate up         # DATABASE_URL unset
    importer migrate: DATABASE_URL is not set                                  (exit 1)
    $ importer migrate up         # nothing listening
    importer migrate up: connect to database: failed to open database: ... connection refused   (exit 1)
    $ importer migrate sideways
    importer migrate: unknown direction "sideways" (want up, down or version)  (exit 2)

Missing arguments also exit 2 with `usage: importer migrate up|down|version`.

### `importer sources apply`
Registers sources and mappings from a directory (`--dir`, default `$SOURCES_DIR`). Needs `DATABASE_URL`. `--schema`
defaults to `./canonical-schema.yaml` (current directory, as for `mapping validate`).

Layout (the naming used in `docs/examples/`): `<name>.source.yaml` with an optional `<name>.mapping.yaml`.

- Everything is validated first (sources strictly, mappings against the canonical schema); if anything is wrong, **nothing is written**. The write itself is one transaction.
- These are errors: an empty directory (no `*.source.yaml`), a mapping file with no source file, a duplicate source id, and a mapping whose `source:` differs from its source's `id` (`SOURCE_MISMATCH`).
- A mapping is stored as a **new version only when its content changed** against the active version, and is then activated. Comments, key order and the `version:` key in the file do not count as changes. Unchanged re-apply writes nothing.

Real output (ONS example, `location` pointing at `testdata/ons/l55o.csv`):

    $ importer sources apply --dir src       -> ons-l55o: source created, mapping v1 created     (exit 0)
    $ importer sources apply --dir src       -> ons-l55o: source unchanged, mapping v1 unchanged (exit 0)
    $ # same file plus a comment line        -> ons-l55o: source unchanged, mapping v1 unchanged (exit 0)
    $ # mapping content changed              -> ons-l55o: source unchanged, mapping v2 created   (exit 0)
    $ importer sources apply --dir empty
    importer sources apply: sources dir .../empty: no *.source.yaml files found
    nothing was written                                                          (exit 1)
    $ # mapping says `source: other`
    .../ons-l55o.mapping.yaml: SOURCE_MISMATCH: mapping says source "other" but .../ons-l55o.source.yaml is source "ons-l55o"
    importer sources apply: invalid files; nothing was written                   (exit 1)
    $ # mapping file without source file
    importer sources apply: .../ons-l55o.mapping.yaml: mapping file has no matching ons-l55o.source.yaml
    nothing was written                                                          (exit 1)
    $ env -u SOURCES_DIR importer sources apply
    importer sources apply: no directory: pass --dir or set SOURCES_DIR          (exit 2)

Exit codes: 0 applied; 1 invalid files or database error; 2 usage (no directory, no `DATABASE_URL`, unreadable
directory or schema, unknown flag).

### `importer run`
`importer run --source <id> [--dry-run]` imports one source (CSV so far) from the stored config using `DATABASE_URL`
and prints one summary line on stdout:

    source=<id> status=<status> inserted=N updated=N unchanged=N failed=N run=<run id|dry-run> duration=<d>

Real output, first run, repeat run, and a dry run on an empty database:

    $ importer run --source ons-l55o --dry-run
    source=ons-l55o status=succeeded inserted=0 updated=0 unchanged=0 failed=0 run=dry-run duration=11ms
    $ importer run --source ons-l55o
    source=ons-l55o status=succeeded inserted=642 updated=0 unchanged=0 failed=0 run=1 duration=59ms
    $ importer run --source ons-l55o
    source=ons-l55o status=succeeded inserted=0 updated=0 unchanged=642 failed=0 run=2 duration=59ms

| Exit | Meaning |
|---|---|
| 0 | run succeeded |
| 1 | run `failed` or `aborted` (summary still printed on stdout, error on stderr), unknown source, invalid stored config, database error, no lock connection free within `DB_LOCK_WAIT` ([details](#connection-pool-and-source-locks)) |
| 2 | usage error: missing `--source`, missing `DATABASE_URL`, missing/empty token env var, unknown flag, invalid `DB_MAX_CONNS`, `DB_LOCK_MAX_CONNS` or `DB_LOCK_WAIT` |
| 3 | another run of the source holds the lock (already running) |

    $ importer run                       -> usage: importer run --source <id> [--dry-run]   (exit 2)
    $ importer run --source nope         -> importer run: source "nope": not found           (exit 1)
    $ env -u DATABASE_URL importer run --source ons-l55o
    importer run: DATABASE_URL is not set                                                    (exit 2)

**Difference to `migrate`:** `importer migrate` exits **1** when `DATABASE_URL` is missing (older behaviour, kept); the
newer commands (`sources apply`, `run`) exit **2**.

A real (non-dry) run first sweeps abandoned runs: `running` runs whose source lock is no longer held are marked
`failed` with error `abandoned (process died)` and a line `importer run: marked N abandoned run(s) failed: [ids]` goes
to stderr (real output after inserting a stale `running` row: `importer run: marked 1 abandoned run(s) failed: [3]`).
`--dry-run` does not sweep. Details: [design](design.md#9-failure-handling).

**Secrets.** A source config holds env var **names** only (`auth: {type: bearer|api_key, token_env: NAME, header: H}`);
there is no key for a literal secret, and unknown keys are rejected. `run` reads the variable when it starts, adds the
header only to requests to the source's own host (a redirect to another host gets no token), and never stores or prints
the value. File locations send no HTTP request, so `auth` is not used for them. If the variable is unset or empty,
`run` exits 2 naming the variable.

**Known limitations** (listed in the [roadmap](roadmap.md#hardening-backlog)):
- `--dry-run` prints `inserted=0 updated=0 unchanged=0` and no total, which can mislead: it parses and reports but writes nothing.
- Files in `--dir` have no size cap.
- A YAML error on a garbage file may echo a snippet of its content.
- That a redirect does not leak the token is covered by a unit test only.

### `importer serve`
Runs the gRPC server (`dataimporter.v1.DataService`, `grpc.health.v1`, reflection) until SIGINT or SIGTERM. Takes no
arguments (`importer serve extra` prints `usage: importer serve`, exit 2). Needs `DATABASE_URL`. It serves the
`DataService` methods `GetRecord` and `ListRecords` with mapping on read ([api](api.md#implemented-task-011)).
The other services are not in the proto yet.

| Var | Effect |
|---|---|
| `GRPC_ADDR` | listen address, default `:8443` |
| `DATABASE_URL` | Postgres DSN; pinged for health |
| `TLS_CERT_FILE`, `TLS_KEY_FILE` | both set: serve TLS (minimum TLS 1.2); neither set: plaintext; only one: error |
| `SCAN_LIMIT` | raw rows one `ListRecords` call maps, default `50000`; unset or empty means the default; must be a positive integer |
| `DB_MAX_CONNS`, `DB_LOCK_MAX_CONNS`, `DB_LOCK_WAIT` | pool and lock settings, validated at startup (exit 2 when invalid); see [connection pool and source locks](#connection-pool-and-source-locks). `serve` takes no source locks, but the WARN budget check counts `DB_LOCK_MAX_CONNS` |

`SCAN_LIMIT` is checked at startup, before the database is opened. Real output (built into `bin/`, as `make build`
does), with `DATABASE_URL` pointing at a closed port (`127.0.0.1:1`), so no connection was attempted:

<!-- verify -->
```bash
go build -o bin/importer ./cmd/importer
for v in abc 0; do
  rc=0
  SCAN_LIMIT=$v DATABASE_URL=postgres://postgres@127.0.0.1:1/x bin/importer serve || rc=$?
  echo "exit $rc"
  test "$rc" -eq 2
done
```

    importer serve: SCAN_LIMIT must be a positive integer, got "abc"
    exit 2
    importer serve: SCAN_LIMIT must be a positive integer, got "0"
    exit 2

Tuning: a smaller limit makes each call cheaper and returns short pages with a continuation token sooner; clients
must follow `next_page_token` until it is empty. Real run with `SCAN_LIMIT=100` on the ONS source: the same month
walk returned all 454 records in 10 pages, and a sparse filter (`period_type = year`, 37 records) gave pages of 5,
6, 6, 6, 6, 6 and 2 records. Details: [api](api.md#listrecords) (paging and scan limit).

Startup errors (all exit 1, real output):

    importer serve: DATABASE_URL is not set
    importer serve: TLS_CERT_FILE is set but TLS_KEY_FILE is not: set both or neither
    importer serve: TLS_KEY_FILE is set but TLS_CERT_FILE is not: set both or neither
    importer serve: load TLS key pair: open /x: no such file or directory
    importer serve: listen tcp 127.0.0.1:18443: bind: address already in use

Without TLS the server logs a warning, here with the default address (the listener reports all interfaces):

    time=2026-11-21T07:26:43.576Z level=WARN msg="TLS_CERT_FILE and TLS_KEY_FILE are not set: serving plaintext gRPC" addr=0.0.0.0:8443

**Security.** By default the server speaks **plaintext gRPC on all interfaces** and has no authentication. TLS
enforcement and the auth interceptor are planned for the hardening milestone (M8); do not expose the port publicly
before then.

**Health** (`grpc.health.v1`, service names `""` and `dataimporter.v1.DataService`):
- The status is `NOT_SERVING` until the first database ping succeeds; that first ping runs before the server accepts calls.
- If the database is unreachable at startup the server still starts, logs `health check failed` and reports `NOT_SERVING`.
- The database is pinged every 5 s (5 s timeout per ping). Verified with a real Postgres: `SERVING`, then `NOT_SERVING`
  after the database was stopped, then `SERVING` again after it was restarted (checked within about 6 s each time).
  A failure is logged once when it starts, not on every ping.
- A database failure does not stop the process; only health changes.

**Reflection** is registered, so tools such as `grpcurl` can list the services (UNVERIFIED: no `grpcurl` was available;
only registration in code was checked).

**Graceful shutdown.** On SIGINT or SIGTERM the server stops health checks, sets health to `NOT_SERVING`, ends open
`Health.Watch` streams, and lets in-flight calls finish for up to 10 s; after that it logs
`graceful shutdown timed out; forcing stop` and cuts them off. A clean stop logs `server stopped` and exits 0 (verified
with SIGTERM). A **second signal terminates immediately** (default signal handling is restored after the first):
UNVERIFIED, from reading `cmd/importer/serve.go` only.

Known limitations:
- An unreadable (permission denied) certificate file was not verified: the test environment runs as root.
- Goroutine leaks on shutdown were checked by reading the code only.

### Proto workflow
The API contract is `proto/dataimporter/v1/data.proto`; `gen/` is generated and committed. Generation is offline: `buf`
and the plugins `protoc-gen-go` (v1.36.8) and `protoc-gen-go-grpc` (v1.5.1) run as local `go tool` dependencies, with no
remote plugins. Config: `buf.yaml` (lint `STANDARD` minus the two rules explained in [api](api.md#implemented-task-011)),
`buf.gen.yaml`.

1. Edit `proto/`.
2. Regenerate and lint (no output and exit 0 on success):

<!-- verify -->
```bash
go tool buf generate && go tool buf lint
echo "exit $?"
```

    exit 0

3. Commit `gen/` with the proto change. The gate `scripts/checks.d/15-proto-generated.sh` runs `go tool buf generate` and
   fails with `FAIL: gen/ is stale; run 'go tool buf generate' and commit the result` when `gen/` differs.

### `importer mapping validate`
Parses a mapping file (strict YAML) and checks it against the canonical schema; no database needed. Issue codes are
listed in [mapping-spec](mapping-spec.md#validation-on-save). Each issue is printed to stdout as `path: code: message`.

`--schema` defaults to `./canonical-schema.yaml` relative to the **current directory**, not the binary or the file
being validated. Run the command from the repository root, or pass `--schema <path>`; otherwise it exits 2 with
`canonical schema: open canonical-schema.yaml: no such file or directory`.

| Exit | Meaning |
|---|---|
| 0 | no errors (warnings may be printed) |
| 1 | at least one error issue, or `PARSE_ERROR` (printed as `<file>: PARSE_ERROR: ...`) |
| 2 | unreadable mapping file, schema not found/unreadable, unknown flag, or missing/extra arguments |

Valid mapping (run from the repo root; no output, exit 0):

<!-- verify -->
```bash
go run ./cmd/importer mapping validate docs/examples/ons-l55o.mapping.yaml
echo "exit $?"
```

    exit 0

Invalid mapping (real output for a test file with several mistakes; abridged: several further lines, such as
`UNKNOWN_TRANSFORM`, `CONST_NOT_IN_ENUM`, `TYPE_MISMATCH` and the second `REQUIRED_NOT_COVERED`, are omitted):

    $ importer mapping validate bad.yaml
    fields.bogus: UNKNOWN_FIELD: "bogus" is not a canonical field
    fields.measure.from[0]: BAD_FROM: "$.a..b" is not a field name, JSONPath ($.a.b), @meta.X or @ctx.X
    fields.measure.transform[1]: TRANSFORM_ARGS: transform regex takes 2 argument(s), got 1
    fields.series_id: SOURCE_CONFLICT: from, const and template are mutually exclusive (exactly one is allowed)
    fields.unit.null_markers: NULL_MARKERS_WITHOUT_FLAG: null_markers without flag: the matched marker is discarded
    fields.period_end: REQUIRED_NOT_COVERED: required canonical field "period_end" is not mapped
                                                                           (exit 1)

Scheduling: built in. `importer serve` runs the scheduler for every source with a `schedule`, so deploy at least
one replica (several are safe). Concurrent runs for one source are prevented with row locks plus a Postgres
advisory lock. `importer schedule list` shows each source's next run; `importer run --source <id>` forces one.
Outside `serve`, an external cron can still call `importer run`.

## Observability
- Structured JSON logs with `run_id` and `source_id`.
- Alert on `import_last_success_age_seconds` exceeding the source's expected interval (a missed or failing schedule).
- Prometheus metrics: `import_records_total{source,outcome}`, `import_run_duration_seconds`,
  `grpc_server_handled_total`, `mapping_errors_total{source,code}`.
- Health: gRPC health service; `importer serve` reports `SERVING` only while the database answers a ping ([details](#importer-serve)).

## Security and data protection
- Secrets only from env/secret store. Never logged; payload logging off by default.
- Least-privilege DB roles: importer (write raw), server (read raw, write sources/mappings).
- `GetRawRecord` and `ExportRecords` need elevated roles. Raw payloads may hold personal data, so define
  retention and deletion per source (see open questions).

## Backup and recovery
Standard Postgres backups. Because raw data is retained, derived views can always be rebuilt.
