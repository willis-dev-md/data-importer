# Data model (Postgres)

```sql
CREATE TABLE source_groups (
  id          text PRIMARY KEY,
  spec        jsonb NOT NULL,              -- template, rate_limit, members
  created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mapping_templates (            -- shared mappings referenced as name@version
  name        text NOT NULL,
  version     int  NOT NULL,
  spec        jsonb NOT NULL,
  PRIMARY KEY (name, version)
);

CREATE TABLE sources (
  id            text PRIMARY KEY,            -- slug, e.g. "acme-csv"
  group_id      text REFERENCES source_groups(id),   -- null for standalone sources
  kind          text NOT NULL CHECK (kind IN ('csv','xlsx','http_json')),
  config        jsonb NOT NULL,              -- parsed source config (no secrets)
  active_mapping_version int,
  schedule      jsonb,                       -- null = manual only
  enabled       boolean NOT NULL DEFAULT true,
  next_run_at   timestamptz,                 -- maintained by the scheduler
  last_scheduled_at timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mappings (
  source_id     text NOT NULL REFERENCES sources(id),
  version       int  NOT NULL,
  spec          jsonb NOT NULL,              -- validated mapping
  created_at    timestamptz NOT NULL DEFAULT now(),
  created_by    text,
  PRIMARY KEY (source_id, version)
);

CREATE TABLE import_runs (
  id            bigserial PRIMARY KEY,
  source_id     text NOT NULL REFERENCES sources(id),
  status        text NOT NULL CHECK (status IN ('running','succeeded','unchanged_file','failed','aborted')),
  trigger       text NOT NULL DEFAULT 'manual' CHECK (trigger IN ('manual','schedule')),
  scheduled_for timestamptz,                 -- the slot this run satisfies
  file_id       bigint,                      -- source_files.id parsed by this run
  source_meta   jsonb,                       -- parsed preamble metadata
  started_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz,
  inserted      int NOT NULL DEFAULT 0,
  updated       int NOT NULL DEFAULT 0,
  unchanged     int NOT NULL DEFAULT 0,
  failed        int NOT NULL DEFAULT 0,
  tombstoned    int NOT NULL DEFAULT 0,
  shape         jsonb,                       -- field name -> observed types/counts
  error         text
);

CREATE INDEX sources_due ON sources (next_run_at) WHERE enabled AND schedule IS NOT NULL;

CREATE TABLE source_files (                 -- fetched files kept for reparse and audit
  id            bigserial PRIMARY KEY,
  source_id     text NOT NULL REFERENCES sources(id),
  run_id        bigint NOT NULL REFERENCES import_runs(id),
  sha256        bytea NOT NULL,
  size_bytes    bigint NOT NULL,
  etag          text,
  last_modified timestamptz,
  storage_ref   text NOT NULL,               -- dir:<aa>/<sha256>, informational only (task 014)
  fetched_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (source_id, sha256)
);

CREATE TABLE raw_records (
  source_id     text NOT NULL REFERENCES sources(id),
  natural_key   text NOT NULL,
  payload       jsonb NOT NULL,
  content_hash  bytea NOT NULL,              -- sha256 of canonical-JSON payload
  first_run_id  bigint NOT NULL REFERENCES import_runs(id),
  last_run_id   bigint NOT NULL REFERENCES import_runs(id),
  imported_at   timestamptz NOT NULL DEFAULT now(),
  deleted_at    timestamptz,
  PRIMARY KEY (source_id, natural_key)
);
CREATE INDEX raw_records_payload_gin ON raw_records USING gin (payload jsonb_path_ops);

CREATE TABLE import_errors (
  id            bigserial PRIMARY KEY,
  run_id        bigint NOT NULL REFERENCES import_runs(id),
  position      text,                        -- csv row number / api page+index
  raw           text,                        -- offending raw input
  reason        text NOT NULL
);
```

Notes:
- Stored payloads for pipelined sources include `_source` (sheet/row) and `_ctx` (section context).
- `(source_id, natural_key)` scoping stops two setups overwriting each other.
- `content_hash` makes re-imports cheap and gives accurate `unchanged` counts.
- Optional history: `raw_record_versions` (append-only) if audit of changes is needed (open question).
- Migrations via `golang-migrate` in `migrations/` (embedded; `store.Migrate`; applied by `importer migrate up|down|version`, `down` rolls back one step). `0001_core` creates only `sources`, `mappings`, `import_runs`, `raw_records`, `import_errors`; `0002_source_files` adds `source_files` and the foreign key `import_runs.file_id -> source_files(id)`; groups, templates, and the `group_id`/`schedule`/`enabled`/`next_run_at`/`last_scheduled_at` columns come in later migrations.

Decisions (task 002):
- Runner: golang-migrate v4.18.3 with the pgx5 driver and an `iofs` source over SQL embedded by `migrations/embed.go`. The applied version is stored in `schema_migrations`.
- `down` rolls back one step; at version 0 it is a no-op. `up` is idempotent.
- `sources.kind` CHECK allows `csv`, `xlsx`, `http_json` (as in `0001_core.up.sql`).
- The `sources_due` index and the `import_runs.file_id` foreign key are deliberately left to later migrations; `file_id` was a plain `bigint` until task 014, which added the foreign key in `0002_source_files`.

Decisions (task 003):
- **Natural key** (`record.Key(parts...)`): a JSON array of the key parts as strings, e.g. `["L55O","1989"]` (no HTML escaping). Injective: parts containing `|`, quotes or commas cannot collide. `natural_key` is opaque to the store.
- **Content hash** (`record.Hash`): SHA-256 of the canonical JSON of the payload: keys sorted at every level, no whitespace, every number as its exact value in plain decimal (no exponent, no leading or trailing zeros, `-0` is `0`; floats use their shortest round-trip digits, so `9.3e18` is `9300000000000000000`), NaN/Inf rejected. This matches how PostgreSQL numeric renders jsonb numbers, so the hash is identical before and after a jsonb round trip for every number (tested with 9.3e18, 2^64, 1e21, 1e-7, 5e-324, MaxFloat64, ints, `json.Number`). Preserved: numeric value, and for floats the float64 value. Not preserved (by design, not part of the hash): trailing zeros (`1.0` vs `1`), exponent notation, `-0`, key order, whitespace. Limits are not symmetric: `record.Canonical` rejects a number whose plain-decimal expansion needs more than 20000 digits on either side of the point (exponent out of range), but PostgreSQL numeric itself rejects fractions smaller than about 1e-16383, so `UpsertRaw` returns a "value overflows numeric format" error for them (an error, never a silent change). Such values are outside what is guaranteed. Reading back gives `json.Number`, so a value such as `9.3e18` returns as the number `9300000000000000000`, not the original float type. The canonical text is also what is stored in `payload`.
- **Upsert counting** (`store.UpsertRaw`): one transaction, one statement per call. New key = `inserted` (`first_run_id = last_run_id = run`); same key and same hash = `unchanged`; same key and different hash = `updated`. For `unchanged` rows only `last_run_id` is set (so "seen in run N" is queryable); `payload`, `content_hash` and `imported_at` are not rewritten, so `imported_at` means "last time the content changed". A reappearing tombstoned row has `deleted_at` cleared. Duplicate keys inside one call: last wins, counted once. This is the store-level behaviour only; the importer does not use it for duplicate keys in a run (superseded by the task 018 rule below).
- **Advisory lock** (`store.TryAdvisoryLock`): session-level `pg_try_advisory_lock(hashtextextended('data-importer:source:'||id, 0))` on a dedicated connection outside the work pool (task 019, see [decisions](#decisions-task-019)); released by `unlock()` or by Postgres when the connection is lost. It returns `(unlock, ok, err)`: `err != nil` means the attempt failed (database down, closed pool) and says nothing about who holds the lock; `ok = false` with a nil error means it is held elsewhere; `unlock` is never nil and is safe to call more than once.

Decisions (task 008):
- **Run lifecycle** (`importer.Run`): a real run inserts `import_runs(status=running)` and always closes it with `succeeded`, `failed` or `aborted`, counts, `shape`, `source_meta` and `error`. `unchanged_file` is written by change detection (task 014, see below). A hard kill/OOM leaves `running` until the sweep below.
- **Counts:** `inserted`/`updated`/`unchanged` come from `UpsertRaw`; `failed` is the number of rows written to `import_errors` (adapter row errors, empty natural key, NUL byte in a string). Failed rows never reach `raw_records`.
- **`shape`:** JSON object `field -> {type -> count}` over top-level fields of accepted records, types `string`, `number`, `boolean`, `null`, `object`, `array`; fields starting with `_` are excluded. Example: `{"value":{"string":642}}`.
- **`source_meta`:** the adapter's preamble metadata as JSON; null when the source has none.
- **`sources.config`:** the validated source config as JSON (`importer.ConfigJSON`), read back with `ParseConfigJSON`; env var names only, no secrets.
- **NUL bytes:** jsonb rejects `\u0000`, so a record with one in any string value or key is recorded as a row error rather than altered or failing the batch.
- **Abort:** batches committed before a `max_error_ratio` abort stay in `raw_records` with `first_run_id`/`last_run_id` set to the run id.

Decisions (task 009):
- **Mapping versions** (`store.Apply`, used by `importer sources apply`): `mappings.spec` is the file's YAML converted to JSON as written; its `version:` key is ignored and the stored `version` key is the storage version (max + 1). A new version is written only when the content differs from the ACTIVE version, ignoring `version`, key order, number form and comments; the new version is then activated (`sources.active_mapping_version`). Going back to older content creates a new version. `created_by` is `cli`. The whole apply is one transaction.
- **Unchanged re-apply writes nothing:** the source upsert only updates when `kind` or `config` differ, so row versions (`xmin`) of unchanged sources and mappings do not move.
- **Abandoned-run sweep** (`store.SweepAbandonedRuns`): one UPDATE that sets `status='failed'`, `finished_at=now()`, `error='abandoned (process died)'` on `running` runs whose source advisory lock (same key as `TryAdvisoryLock`, looked up in `pg_locks` for the current database) is not held. Called at the start of a real `importer run`; dry runs do not sweep. A live run is never touched.

Decisions (task 018):
- **Duplicate natural key within a run** (`importer.Run`, real run and `--dry-run`): within one run the **first** record for a natural key wins. A later record with the same key is never merged into it: whether it is in the same batch or in an earlier batch of that run, it becomes a row error with reason `duplicate natural key (first seen at <position>)`, is written to `import_errors` (`raw` holds the natural key), counts in `failed` and takes part in `max_error_ratio`. `<position>` is the first record's position when it is in the same batch, and the literal `unknown` when it was written by an earlier batch (positions are not stored).
- **Store:** `store.UpsertRawUnique` applies this rule and returns the rejected records as `Dup` entries. A key is a duplicate when its stored row has `last_run_id` equal to the current run id. `store.UpsertRaw` is unchanged (last wins within one call).
- **Across runs** nothing changes: a key last seen by a previous run is updated or unchanged as before; a tombstoned row is revived once and its repeat in the run is a duplicate; other sources with the same keys are not affected. Re-runs are unchanged: ONS `ons-l55o` inserted 642, then `unchanged=642`, then `updated=1 unchanged=641` (third run); BoE 854 then `unchanged=854`.
- **Key equality is byte for byte:** case, trailing spaces, NFC/NFD forms, NUL and JSON-quote characters give different keys (2,349 near-duplicate keys in a test: 0 false duplicates).
- **Memory:** a real run keeps nothing between batches (memory is one batch). A dry run keeps a 128-bit hash of each distinct key (SHA-256, first 16 bytes) in memory, bounded by `importer.MaxDryRunKeys` = 10,000,000 distinct keys; beyond it the dry run fails with `dry run: more than <N> distinct natural keys; duplicate detection is bounded, use a real run...` and writes nothing. Measured: a 200k-key dry run used about +6.7 MB heap; a real 200k-key run about +247 KB heap (12.6 s).
- **Dry run** writes nothing but reports `Duplicates` and the first 10 `DuplicateSamples` in `RunResult`. The `importer run` CLI prints only the summary (`failed=` includes duplicates); printing the samples is a backlog item in [roadmap](roadmap.md#hardening-backlog).
- **Evidence (BoE column-shift copy, [sources/boe-baserate](sources/boe-baserate.md)):** a column inserted before the rate column left the `month` key part blank, so 854 valid records collapsed onto 773 natural keys. Before this rule: `status=aborted inserted=773 failed=8` (862 rows read), the 81 collisions were overwritten and counted once, silently. After it: `inserted=773 failed=89` (81 duplicate errors + 8 normalize errors), aborted at `max_error_ratio: 0`; 773 + 81 + 8 = 862, nothing lost silently. Dry run: `failed=89`.

## Decisions (task 014)
- **Schema:** migration `0002_source_files` creates `source_files` as in the table above (`UNIQUE (source_id, sha256)`, FK `run_id -> import_runs(id)`, `source_id -> sources(id)`) and the FK `import_runs.file_id -> source_files(id)`, with indexes on `import_runs(file_id)` and `(source_id, id)`. Down clears `file_id`, drops the FK and the table. `importer migrate up` prints `version 2`; one `migrate down` goes from 2 to 1.
- **File store** (`store.FileStore` with `Put` and `Open`; implementation `store.DirFileStore`): files live in a local directory, chosen over large objects and object storage for now. Path `<dir>/<aa>/<sha256>` (`<aa>` = first two hex digits of the SHA-256). The only input to a path is the 64-hex SHA-256, so no name can leave the directory. Directories are 0700, files 0600. A write goes to a temp file, is fsynced and renamed (atomic). The same bytes stored twice are one file; each source keeps its own `source_files` row.
- **Verification:** `Open` re-hashes the bytes. A corrupt file fails with `stored file is corrupt (SHA-256 mismatch)`, a missing one with `stored file is missing`; both exit 1 from the CLI.
- **Size:** the fetch limit applies (256 MB; the xlsx workbook limit is 100 MB, task 013). CSV is streamed (fetch, store, verify, parse), never buffered.
- **Location:** env `FILE_STORE_DIR`, default `data/files` relative to the working directory. It is created if missing; a path that is not a directory exits 2 before any database work. `data/` is git-ignored. Running from another directory starts an empty store (see [operations](operations.md#configuration-env)).
- **Retention:** none. Files are kept forever; no cleanup exists (backlog: [roadmap](roadmap.md#hardening-backlog)).
- **Conditional fetch** (`http`/`https` only; `file://` and local paths send no conditional headers): `If-None-Match` and `If-Modified-Since` are sent only when `change_detection` lists `etag` or `last_modified`, using the values of the last SUCCESSFUL run (`succeeded` or `unchanged_file`; an aborted or failed run is never the baseline). Last-Modified is stored as a timestamp and resent in canonical HTTP-date form; an unparsable date is stored NULL and only the ETag is sent. Weak ETags and long ETags work. Servers that ignore validators, send random ETags or redirect give normal runs.
- **`unchanged_file`:** a `304` closes the run as `unchanged_file`: zero parse work, no `raw_records` or `import_errors` writes, counts 0, `file_id` set, `shape`, `source_meta` and `error` NULL. A `304` on a first run (no stored validators) fails the run (exit 1). A `200` whose SHA-256 equals the last successful run's file also closes the run as `unchanged_file`, but **only when `change_detection` lists `content_hash`**. Without it an identical file is parsed and gives `unchanged` counts. (The earlier design text said an identical SHA-256 always closes the run; the condition is the decision taken in task 014, see [design](design.md#import).)
- **Reparse** (`importer reparse --source <id> [--run <id>]`): parses the stored file of the latest run that has one, or of run `<id>`, again with the current `sources.config`. No network. The run goes through `importer.Run` (source lock, duplicate-key rule, `max_error_ratio`), gets `trigger=manual` and `file_id` of that file. With an unchanged pipeline `unchanged` equals the record count (BoE 854). After a pipeline change only records whose canonical payload changed are `updated`; keys no longer produced are left alone (no tombstoning yet). Operator guide: [operations](operations.md#importer-reparse).
- **Evidence** (validator, not re-run for this document): ONS `ons-l55o` 642 then `unchanged=642`; BoE 854 then `unchanged_file`; `reparse --run 1` after a pipeline change gave `updated=1 unchanged=1`; reparse with the source server killed succeeded.
- **Not verified:** read-only or full disk (tests ran as root); kill during a write (code reading only); the 256 MB limit beyond the existing tests.

## Decisions (task 019)
- **Lock connections** (`store.TryAdvisoryLock`): a source lock is held on a dedicated `pgx.Conn` outside the work pool, so a run never holds a work connection while it waits for other work connections. The connection is a copy of the work pool's connection config (`application_name`, `search_path`, `sslmode` inherited; pool connect hooks are not applied, none are set). It is closed on `unlock()`, so the session and its lock go away together. The number of concurrent locks per process is bounded by `Options.LockConns` (`DB_LOCK_MAX_CONNS`, default 8).
- **Waiting for a lock connection:** a run waits up to `Options.LockWait` (`DB_LOCK_WAIT`, default 30s) or until its context is cancelled, then fails with `store.ErrLockCapacity` (`no lock connection available: all N are in use after waiting 30s`). This happens before any `import_runs` row is created, so nothing is written. The CLI exits 1 (generic failure), not 3. Runs beyond the free slots queue instead of failing.
- **Same-source runs are unchanged:** a second run of a source whose lock is held still gets `ErrAlreadyRunning` (CLI exit 3), with no run row.
- **Configuration:** `DB_MAX_CONNS` (work pool size, default `max(4, NumCPU)`), `DB_LOCK_MAX_CONNS` and `DB_LOCK_WAIT` are read by `store.OpenPool`. An invalid value wraps `store.ErrConfig` and is rejected before any connection is made (CLI exit 2). `store.New(pool)` keeps its signature with the defaults; `store.NewWithOptions` takes the lock options. Operator guidance: [operations](operations.md#connection-pool-and-source-locks).
- **Connection budget:** `DB_MAX_CONNS + DB_LOCK_MAX_CONNS` is compared with Postgres `max_connections` once per process, after connecting. A larger sum logs one WARN and never fails the process.
- **Test change:** `TestAdvisoryLockErrorIsNotHeld` no longer fails lock acquisition by closing the work pool, because the lock does not use it. Its error case is now an unreachable database, and it checks that no lock slot is leaked.
- **Verified by the validator** (not re-run for this document): 8 and 32 concurrent runs with a work pool of 2 and 4 all complete (0.1 to 0.4 s each); 50 runs with 8 lock slots; after 200 sequential runs the test database's sessions return to the baseline; two concurrent runs of the same source give one winner and one exit 3 with no run row; different sources are not blocked; the lock scopes to its own database; ONS `ons-l55o` 642 then unchanged 642 and BoE 854 then unchanged 854.
- **Known limit (pre-existing, not fixed here):** if the lock session is killed mid-run, the lock is released and the run keeps going, so a second run of the same source can start and succeed, and its abandoned-run sweep marks the first run `failed` while both write. Task 020 makes a run notice the loss and stop. Listed in the [roadmap backlog](roadmap.md#hardening-backlog).
