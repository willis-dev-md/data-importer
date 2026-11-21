# Operations

## Configuration (env)
| Var | Purpose |
|---|---|
| `DATABASE_URL` | Postgres DSN |
| `GRPC_ADDR` | listen address, default `:8443` |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | server TLS |
| `AUTH_TOKENS_FILE` | token -> roles/sources map |
| `IMPORT_CONCURRENCY` | max simultaneous import runs (default 4) |
| `SOURCES_DIR` | directory of source + mapping YAML |

## CLI
    importer migrate up
    importer serve
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
- Health: gRPC health service; `importer serve` readiness checks DB.

## Security and data protection
- Secrets only from env/secret store. Never logged; payload logging off by default.
- Least-privilege DB roles: importer (write raw), server (read raw, write sources/mappings).
- `GetRawRecord` and `ExportRecords` need elevated roles. Raw payloads may hold personal data, so define
  retention and deletion per source (see open questions).

## Backup and recovery
Standard Postgres backups. Because raw data is retained, derived views can always be rebuilt.
