# Design

## 1. Problem

Several setups supply data in different structures: some as CSV files (varying columns, delimiters, encodings),
some via HTTP APIs (varying JSON shapes, auth, pagination). We need to ingest them without writing new code per
setup, store the data in a database, and expose it through a gRPC API.

## 2. Goals

- Add a new setup by writing **configuration**, not code, and add **many datasets** (hundreds) through a shared
  definition ([source groups](source-groups.md)).
- **Never lose source data**; keep what the source actually sent.
- **Cadence is part of a source's setup** (daily, weekly or monthly; `cron` is deferred), run by a built-in scheduler.
- Idempotent, observable imports (re-running a file does not duplicate data).
- Serve a **stable, typed gRPC contract** regardless of source shape.
- Change a mapping **without re-importing**.

## 3. Non-goals (v1)

- A UI for authoring mappings.
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
   natural key. Adapters do **no** business mapping. v1: `csv`, `xlsx`, `http_json`. (An `mcp` adapter is [parked](parked/mcp-source.md).) Each adapter can apply a declarative
   [row pipeline](mapping-spec.md#row-pipeline-table-for-csv-and-xlsx) (sections, fill-down, unpivot) for structure
   that needs row order. Fetched files are kept by hash (`source_files`) so a pipeline change is a **reparse**,
   not a re-download ([ADR-0004](adr/0004-structural-parsing-at-import.md)).
2. **Importer** (`internal/importer`): orchestrates a run: opens an `import_runs` row, streams records from the
   adapter, upserts into `raw_records` in chunked transactions, records counts and the observed field shape,
   closes the run.
3. **Store** (`internal/store`): Postgres access and migrations.
4. **Mapping engine** (`internal/mapping`): parses/validates mapping YAML, evaluates it against a raw payload to
   produce a canonical `Record` (or per-record errors).
5. **gRPC server** (`internal/server`): `DataService` (read), `SourceService` (sources/mappings),
   `ImportService` (control). Applies the active mapping on read.

### Source groups
A group (template + member list) expands to one ordinary source per dataset, so scheduling, runs, errors and
health are tracked per dataset while configuration is written once. See [source-groups](source-groups.md).

### Scheduler
Each source declares its own `schedule` (see [mapping-spec](mapping-spec.md#schedule)). The scheduler runs inside
`importer serve` and:
1. Computes `sources.next_run_at` from the schedule and timezone (`schedule.Next`, task 021); polls for due sources every `SCHEDULER_POLL_INTERVAL` (default 30s, task 022).
2. Applies the jitter offset (`schedule.Offset`: deterministic FNV-1a of the source id, in [0, jitter)) to the start time only, and a global concurrency limit (`IMPORT_CONCURRENCY`) on scheduled runs. Per-host rate limiting is **not implemented**.
3. Claims due sources in the database: `SELECT ... FOR UPDATE SKIP LOCKED` in one short transaction, so several server replicas never start the same slot twice and no leader election is needed. The claim holds no lock during the import. A per-source advisory lock still stops a second run of a source that is running (overlap).
4. The claim computes the next `next_run_at` from the **scheduled** time, not the finish time, so cadence does not drift; then the import starts with `trigger = 'schedule'` and `scheduled_for` = the slot.
5. Applies `on_missed` after downtime: `run_once` (default: one catch-up run) or `skip`.
6. Overlap policy is always skip: a source already running is not started again.
Manual runs (`importer run`, `StartImport`) use the same path with `trigger = 'manual'`. Changing a schedule
recomputes `next_run_at` immediately (`sources apply`, task 021). A source can be paused with `enabled: false`, which clears `next_run_at`.

**Decision (task 021): no retry.** A failed run is not retried; the next scheduled slot is the retry. `cron` is deferred. See [ADR-0007](adr/0007-schedule-engine.md).

**As built (task 022)**, in `internal/scheduler`, started by `importer serve` ([operations](operations.md#scheduler); decisions in [ADR-0008](adr/0008-scheduler-loop.md)):
- **Poll:** `Run` polls once at start and then every `SCHEDULER_POLL_INTERVAL`. Each poll first sweeps abandoned runs (`store.SweepAbandonedRuns`), then claims due sources, up to the free slots of `IMPORT_CONCURRENCY`.
- **Claim** (`store.ClaimDue`): one short transaction selects `enabled AND schedule IS NOT NULL AND next_run_at <= now`, ordered by `next_run_at, id`, `FOR UPDATE SKIP LOCKED`. For each row `schedule.Plan(spec, next_run_at - 1ns, now - offset)` gives the slot; `next_run_at` moves to the slot after it and `last_scheduled_at` is set to the slot. The transaction commits, then the import starts. Slots that are only passed over (`skip`) are advanced and use no run slot.
- **Jitter:** a source is claimed once `next_run_at + offset <= now`. `Plan` gets the raw slots, so an offset above `SkipGrace` cannot make a slot count as missed. The offset moves the start only, never the slot.
- **Missed slots:** `run_once` runs once for the latest missed slot (`scheduled_for` = that slot); `skip` runs none ([ADR-0007](adr/0007-schedule-engine.md)).
- **Overlap:** if the source lock is held, the slot is skipped and logged at WARN. No run row is written.
- **Failures:** a failed or aborted scheduled run is not retried. A run that fails before it starts (lock capacity or wait, missing token variable, unreadable source) leaves a `failed` row with `trigger = 'schedule'`, `scheduled_for` = the slot and the error text, so the lost slot is visible. A panic in a run is recovered and logged; the other sources continue.
- **Shutdown:** SIGTERM stops claiming. In-flight runs get the shutdown grace (10 s, the serve `ShutdownTimeout`), then their context is cancelled and they finish `failed` with `cancelled: ...`, as a manual run does. `Run` returns after every goroutine has been joined.
- **Not implemented:** cron expressions, retries, per-host rate limiting, source groups (M7b), a metrics endpoint (separate task). `http_json` sources cannot run yet (no adapter, same as `run`).

## 5a. Import and read flows

### Import
1. `importer run --source <id>` (or `ImportService.StartImport`).
2. Load source config; create `import_runs(status=running)`.
2a. **Change detection:** conditional fetch: `If-None-Match` / `If-Modified-Since` from the last successful run's stored ETag / Last-Modified, when `change_detection` lists them. A `304` closes the run as `unchanged_file` without parsing. After a `200` the SHA-256 is computed and the file stored once per source and hash ([data-model](data-model.md#decisions-task-014)); the run closes as `unchanged_file` without parsing only when `change_detection` lists `content_hash` and the SHA-256 equals the last successful run's file.
3. Adapter yields records; for each compute `natural_key` (JSON array of key parts) and `content_hash`
   (SHA-256 of canonical JSON); see [data-model](data-model.md#decisions-task-003).
4. Upsert `raw_records` on `(source_id, natural_key)` (`store.UpsertRaw`); when `content_hash` is unchanged the
   payload is not rewritten, only `last_run_id` is set (counts as `unchanged`). Duplicate keys in one run: the first record wins; a later record with the same key is a row error, not a merge ([data-model](data-model.md#decisions-task-018)).
   A per-source advisory lock (`store.TryAdvisoryLockMonitored`, see failure handling) keeps two runs of one source from overlapping.
5. Track field names/types seen (shape summary) on the run.
6. Optional `missing_policy: tombstone`: after a **complete** run, mark rows not seen as `deleted_at = now()`.
7. Close run with counts (`inserted`, `updated`, `unchanged`, `failed`, `tombstoned`) and status.

Parse-level failures (malformed CSV row, non-JSON response) are written to `import_errors`; they never abort the
whole run unless `max_error_ratio` is exceeded.

#### As implemented (task 008, `internal/importer`)
`importer.Run(ctx, Deps{Store, Adapters}, sourceID, Options{Trigger, DryRun, BatchSize})` returns a `RunResult`
(`RunID`, `Status`, counts, `Total`, `Shape`, `SourceMeta`, `Error`, `DryRun`). `Adapters` is keyed by source kind.
`BatchSize` defaults to 500 records per `UpsertRaw` call; `Trigger` is `manual` or `schedule`.
- **Lock:** a real run first takes the per-source advisory lock. `ErrAlreadyRunning` is returned (and no run row is
  created) only when the lock is held elsewhere; an error from the lock attempt itself fails the call with that error.
  `DryRun` takes no lock, creates no run row (`RunID` 0) and writes nothing; only `Total`, `Failed`, `Shape` and
  `SourceMeta` are filled.
- **Statuses:** `running` -> `succeeded`, `failed` or `aborted`. `unchanged_file` is set by change detection (task 014): no parse work, zero counts.
- **Counts:** `inserted`, `updated`, `unchanged` from `UpsertRaw`; `failed` counts rows that became `import_errors`.
- **Row errors:** adapter `RowError`s, records with an empty natural key, and records whose strings (values or keys)
  contain a NUL byte (`\u0000`, not storable as jsonb) are written to `import_errors` and counted in `failed`; the
  rest of the batch is still written.
- **Duplicate natural keys:** a record whose key was already written earlier in the same run (same or earlier batch) is
  rejected with reason `duplicate natural key (first seen at <position>)`, recorded and counted in `failed` like any row
  error, so it also counts toward `max_error_ratio`. A dry run detects them too (`RunResult.Duplicates`). Task 018 made
  this the rule: before it, a later record silently overwrote the earlier one and was counted once.
- **`max_error_ratio`:** after the stream ends, `failed/total > ratio` ends the run `aborted`. Equal is fine; ratio 0 means
  any error aborts; `total` 0 never aborts. Batches already committed stay in `raw_records`, tagged with the run id.
- **Shape and metadata:** `shape` is per top-level field the observed value types with counts, e.g.
  `{"value":{"string":642}}`; fields starting with `_` are excluded. `source_meta` is the adapter's preamble metadata.
- **Failures:** an adapter open or read error ends the run `failed` (rows parsed before it are kept). Context
  cancellation ends it `failed` with a `cancelled:` error; the run row is closed with a detached 10 s context and the
  lock is released. A panic in the adapter closes the run as `failed` with the text `panic: ...`, releases the lock,
  then re-panics.
- **Return value:** for `failed` and `aborted` runs `Run` returns the filled `RunResult` **and** a non-nil error
  (`aborted` wraps `importer.ErrAborted`); check `RunResult.Status`.
- **Source config:** `ConfigJSON` / `ParseConfigJSON` convert a validated source config to and from the JSON in
  `sources.config`. It holds env var names only, never secrets.
- **Hard kill:** a hard kill or OOM leaves the run in `running`; `store.SweepAbandonedRuns` closes it (see Failure handling).

### Read
1. Client calls `ListRecords(source, filter, page)`.
2. Server loads the active mapping version for the source on every call (no cache; [ADR-0005](adr/0005-data-service-read-path.md)).
3. The server walks the raw rows in natural-key order (keyset) and maps each payload; filters on canonical fields are
   applied in Go to the mapped values, not translated to SQL (only `source_id` and `include_deleted` are pushed down).
4. Each raw payload is mapped to `Record{id, source_id, fields, extra, mapping_errors}`.
5. A record failing mapping is returned with `mapping_errors` rather than failing the call; it never matches a filter on a failed field.
6. A call maps at most `SCAN_LIMIT` rows; then it returns a short page with a continuation token. Details in [api](api.md#listrecords).

## 6. Why ELT

Nuance: only **structural** parsing happens at import; **semantic** mapping stays on read (ADR-0004).

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

The canonical field set is declared in `canonical-schema.yaml` (name, type, required). It is a generic
**observation**: a measured `value` for a `measure` (optionally a `statistic`), described by free-form
`dimensions` (area, age band, occupation, property type, ...), at a time period. Version 2 of the schema (after
the ONS, Bank of England, HM Land Registry and ASHE sources) is:

    series_id      string  required   stable id of the series (built with `template:`)
    series_title   string
    measure        string  required   e.g. cpih_annual_rate, bank_rate, average_price, annual_pay_gross
    statistic      string             e.g. median, mean, jobs (empty when the measure is the statistic)
    dimensions     map<string,string> e.g. {area_code: E92000001, age_band: 22-29, soc_code: 11}
    period_type    enum    required   year | tax_year | quarter | month | day
    period_start   date    required
    period_end     date    required
    value          decimal            null when the publisher suppresses it (see quality_flag)
    unit           string             GBP, %, thousand_jobs, index, ...
    status         string             provisional | revised | final
    quality_flag   string             publisher marker for suppressed/unreliable values (x, .., :, -)

The schema file is loaded by `mapping.LoadCanonical`. Each field has a `type` (`string int decimal bool date
timestamp enum map`), optional `required`, `nullable` (`value`) and, for enums, `values`. Mappings are validated against
it by `importer mapping validate` (rules in [mapping-spec](mapping-spec.md#validation-on-save)); `dimensions` is a map
and is filled only from the mapping's `dimensions:` block.

Original summary kept for orientation: All four sources so far (ONS
[CPIH](sources/ons-l55o.md), [Bank Rate](sources/boe-baserate.md), [UK HPI](sources/uk-hpi.md) and
[ASHE Table 20](sources/ons-ashe-table20.md)) are time-series observations, so one canonical entity serves them.
Other setups with a different entity shape would need their own canonical schema. Mappings are
validated against it on save.

## 8. Performance path

Read-time mapping and JSONB filtering are fine for moderate volume. Escape hatches, in order:
1. Expression/GIN indexes on hot payload paths.
2. Materialized, typed view per source generated from its mapping (`mapping materialize <source>`).
3. Move to typed tables for sources that have stabilised (the ETL end state for that source only).

## 9. Failure handling

- Runs are resumable per chunk; a run killed without cleanup is marked `failed` with error `abandoned (process died)` by `store.SweepAbandonedRuns`, called at the start of every real `importer run` (not in `--dry-run`; the future server calls it at start). It only touches `running` runs whose source advisory lock is not held by any live session, so a live run is never swept; a stale row of a source that has a live run right now stays until that run ends. A run whose lock is lost may be swept in the gap; its own final state then overwrites that row ([operations](operations.md#lost-source-lock)).
- Upserts are idempotent, so retries are safe.
- API sources: retry with exponential backoff and jitter, honour `Retry-After`, per-source rate limit.
- `max_error_ratio` aborts before committing a mostly-bad file (chunks already committed are tagged with
  the run id and can be rolled back with `importer rollback --run <id>`).
- A bad file with many collisions (for example a layout change that blanks a key column) therefore shows up as many
  `duplicate natural key` row errors and an `aborted` run, not as a silently smaller import. Reading the errors is in
  [import failures](runbooks/import-failures.md).

- **Concurrency and locks (task 019).** The per-source advisory lock is held on a dedicated connection outside the work
  pool, not on a work connection. Before, a run held one work connection for its lock, so N concurrent runs with a pool
  of N or fewer connections each held a lock and waited forever for a second connection (reproduced with pools of 1 to 4
  connections). Now a run waits for a free lock connection up to `DB_LOCK_WAIT`, then fails with `store.ErrLockCapacity`
  before a run row is created. The lock is released on success, failure, abort, cancel and panic (unit test
  `TestLockSlotFreedOnEveryExit`). Settings: [operations](operations.md#connection-pool-and-source-locks); decisions:
  [data model, task 019](data-model.md#decisions-task-019).
- **Lost source lock (task 020).** A real run's source lock comes from `store.TryAdvisoryLockMonitored`, which runs one goroutine that every `DB_LOCK_CHECK_INTERVAL` (default 5 s, range 10 ms to 10 m) makes one read-only `pg_locks` lookup on the run's own lock connection: is the session alive and does it still hold the key? The check never releases the lock and times out after 10 s; a failed or timed-out check counts as lost. On loss the run is cancelled and finishes `failed` with `lost source lock (session terminated): ...`, which outranks abort or success at the last moment. Batches already committed stay tagged with the run id. The monitor is joined before the lock is released, on every exit (success, failure, cancel, panic). Settings: [operations](operations.md#lost-source-lock); decisions: [data model, task 020](data-model.md#decisions-task-020).
- **Detection window (honest):** between the loss and its detection (at most one interval plus the check round trip) the run still writes, and another run of the same source can start. After detection the first run ends `failed` within one batch; a run started after that is legitimate.
- **Sweep rule:** `SweepAbandonedRuns` fails a `running` row only when its source lock is not held, so a live run, including one being re-checked, is never swept. Since task 022 the scheduler also sweeps at the start of every poll, so a killed replica's row is closed by the next poll of any replica.
- **Unlock waits for the monitor (task 022):** unlock waits for a lock check in flight (normally milliseconds, at most 10 s on a hung connection) and does not cancel it. An earlier cancel could break the connection and leave the lock held. A SIGTERM during a hung check can therefore delay exit up to 10 s past the shutdown grace.
- **Evidence (validator, not re-run here):** with the real binary, `pg_terminate_backend` on the lock session mid-run left 160000 rows kept with `first_run_id=5`, and the rerun succeeded.

## 10. Security

- Secrets (API tokens) referenced by env var name in source config, never stored in config or DB.
- gRPC over TLS; bearer-token interceptor; tokens scoped to a list of source ids.
- Raw payloads may contain personal data: retention and access to `GetRawRecord` are restricted
  (see [operations](operations.md)).
