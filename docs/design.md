# Design

## 1. Problem

Several setups supply data in different structures: some as CSV files (varying columns, delimiters, encodings),
some via HTTP APIs (varying JSON shapes, auth, pagination). We need to ingest them without writing new code per
setup, store the data in a database, and expose it through a gRPC API.

## 2. Goals

- Add a new setup by writing **configuration**, not code.
- **Never lose source data**; keep what the source actually sent.
- Idempotent, observable imports (re-running a file does not duplicate data).
- Serve a **stable, typed gRPC contract** regardless of source shape.
- Change a mapping **without re-importing**.

## 3. Non-goals (v1)

- A UI for authoring mappings.
- Built-in scheduler (use cron or the platform).
- Streaming/CDC sources, GraphQL, XML, OAuth flows beyond bearer/API-key.
- Multi-tenant authorisation beyond per-token source scoping.

## 4. Architecture

    +-----------+     +--------------+     +-----------------+
    | CSV file  |---->|              |     |                 |
    +-----------+     |  Source      |     |   Postgres      |
                      |  adapters    |---->|   raw_records   |
    +-----------+     |  (parse only)|     |   import_runs   |
    | HTTP API  |---->|              |     |   sources       |
    +-----------+     +--------------+     |   mappings      |
                            ^              +--------+--------+
                            | source config         |
                       +----+-----+                 | read
                       | Importer |                 v
                       |  (CLI /  |          +--------------+      gRPC
                       |  gRPC)   |          | Mapping      |<---- clients
                       +----------+          | evaluator in |
                                             | gRPC server  |
                                             +--------------+

### Components

1. **Source adapters** (`internal/source`): turn a source into a stream of `map[string]any` records plus a
   natural key. Adapters do **no** business mapping. v1: `csv`, `http_json`.
2. **Importer** (`internal/importer`): orchestrates a run: opens an `import_runs` row, streams records from the
   adapter, upserts into `raw_records` in chunked transactions, records counts and the observed field shape,
   closes the run.
3. **Store** (`internal/store`): Postgres access and migrations.
4. **Mapping engine** (`internal/mapping`): parses/validates mapping YAML, evaluates it against a raw payload to
   produce a canonical `Record` (or per-record errors).
5. **gRPC server** (`internal/server`): `DataService` (read), `SourceService` (sources/mappings),
   `ImportService` (control). Applies the active mapping on read.

## 5. Key flows

### Import
1. `importer run --source <id>` (or `ImportService.StartImport`).
2. Load source config; create `import_runs(status=running)`.
3. Adapter yields records; for each compute `natural_key` and `content_hash`.
4. Upsert `raw_records` on `(source_id, natural_key)`; skip write when `content_hash` unchanged
   (counts as `unchanged`).
5. Track field names/types seen (shape summary) on the run.
6. Optional `missing_policy: tombstone`: after a **complete** run, mark rows not seen as `deleted_at = now()`.
7. Close run with counts (`inserted`, `updated`, `unchanged`, `failed`, `tombstoned`) and status.

Parse-level failures (malformed CSV row, non-JSON response) are written to `import_errors`; they never abort the
whole run unless `max_error_ratio` is exceeded.

### Read
1. Client calls `ListRecords(source, filter, page)`.
2. Server loads the active mapping version for the source (cached).
3. Filters on canonical fields are translated to JSONB expressions using the mapping's field paths.
4. Each raw payload is mapped to `Record{id, source_id, fields, extra, mapping_errors}`.
5. A record failing mapping is returned with `mapping_errors` rather than failing the call.

## 6. Why ELT

See [ADR-0001](adr/0001-elt-over-etl.md). In short: structure varies and is not fully known up front, so the
variable part (the mapping) must be cheap to change and must not gate loading.

## 7. Canonical record

    Record {
      id            string   // stable: source_id + natural_key
      source_id     string
      fields        Struct   // mapped, typed canonical fields
      extra         Struct   // unmapped source fields
      mapping_errors []FieldError
      source_updated_at Timestamp
      imported_at   Timestamp
    }

The canonical field set is declared in `canonical-schema.yaml` (name, type, required). The sample schema ships
with the repo; the domain is intentionally undecided (see [open-questions](open-questions.md)). Mappings are
validated against it on save.

## 8. Performance path

Read-time mapping and JSONB filtering are fine for moderate volume. Escape hatches, in order:
1. Expression/GIN indexes on hot payload paths.
2. Materialized, typed view per source generated from its mapping (`mapping materialize <source>`).
3. Move to typed tables for sources that have stabilised (the ETL end state for that source only).

## 9. Failure handling

- Runs are resumable per chunk; a crashed run is marked `failed` by a startup reaper.
- Upserts are idempotent, so retries are safe.
- API sources: retry with exponential backoff and jitter, honour `Retry-After`, per-source rate limit.
- `max_error_ratio` aborts before committing a mostly-bad file (chunks already committed are tagged with
  the run id and can be rolled back with `importer rollback --run <id>`).

## 10. Security

- Secrets (API tokens) referenced by env var name in source config, never stored in config or DB.
- gRPC over TLS; bearer-token interceptor; tokens scoped to a list of source ids.
- Raw payloads may contain personal data: retention and access to `GetRawRecord` are restricted
  (see [operations](operations.md)).
