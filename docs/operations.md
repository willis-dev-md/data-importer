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
    importer mapping validate <file>
    importer mapping materialize <source>

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
