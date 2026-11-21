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
  storage_ref   text NOT NULL,               -- object store key / large object / path
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
- Migrations via `golang-migrate` in `migrations/` (embedded; `store.Migrate`; applied by `importer migrate up|down|version`, `down` rolls back one step). `0001_core` creates only `sources`, `mappings`, `import_runs`, `raw_records`, `import_errors`; groups, templates, `source_files`, and the `group_id`/`schedule`/`enabled`/`next_run_at`/`last_scheduled_at` columns come in later migrations. Until `source_files` exists, `import_runs.file_id` has no foreign key.

Decisions (task 002):
- Runner: golang-migrate v4.18.3 with the pgx5 driver and an `iofs` source over SQL embedded by `migrations/embed.go`. The applied version is stored in `schema_migrations`.
- `down` rolls back one step; at version 0 it is a no-op. `up` is idempotent.
- `sources.kind` CHECK allows `csv`, `xlsx`, `http_json` (as in `0001_core.up.sql`).
- The `sources_due` index and the `import_runs.file_id` foreign key are deliberately left to later migrations; `file_id` is a plain `bigint` for now.

Decisions (task 003):
- **Natural key** (`record.Key(parts...)`): a JSON array of the key parts as strings, e.g. `["L55O","1989"]` (no HTML escaping). Injective: parts containing `|`, quotes or commas cannot collide. `natural_key` is opaque to the store.
- **Content hash** (`record.Hash`): SHA-256 of the canonical JSON of the payload: keys sorted at every level, no whitespace, every number as its exact value in plain decimal (no exponent, no leading or trailing zeros, `-0` is `0`; floats use their shortest round-trip digits, so `9.3e18` is `9300000000000000000`), NaN/Inf rejected. This matches how PostgreSQL numeric renders jsonb numbers, so the hash is identical before and after a jsonb round trip for every number (tested with 9.3e18, 2^64, 1e21, 1e-7, 5e-324, MaxFloat64, ints, `json.Number`). Preserved: numeric value, and for floats the float64 value. Not preserved (by design, not part of the hash): trailing zeros (`1.0` vs `1`), exponent notation, `-0`, key order, whitespace. Limits are not symmetric: `record.Canonical` rejects a number whose plain-decimal expansion needs more than 20000 digits on either side of the point (exponent out of range), but PostgreSQL numeric itself rejects fractions smaller than about 1e-16383, so `UpsertRaw` returns a "value overflows numeric format" error for them (an error, never a silent change). Such values are outside what is guaranteed. Reading back gives `json.Number`, so a value such as `9.3e18` returns as the number `9300000000000000000`, not the original float type. The canonical text is also what is stored in `payload`.
- **Upsert counting** (`store.UpsertRaw`): one transaction, one statement per call. New key = `inserted` (`first_run_id = last_run_id = run`); same key and same hash = `unchanged`; same key and different hash = `updated`. For `unchanged` rows only `last_run_id` is set (so "seen in run N" is queryable); `payload`, `content_hash` and `imported_at` are not rewritten, so `imported_at` means "last time the content changed". A reappearing tombstoned row has `deleted_at` cleared. Duplicate keys inside one batch: last wins, counted once.
- **Advisory lock** (`store.TryAdvisoryLock`): session-level `pg_try_advisory_lock(hashtextextended('data-importer:source:'||id, 0))` on a dedicated pooled connection; released by `unlock()` or by Postgres when the connection is lost. It returns `(unlock, ok, err)`: `err != nil` means the attempt failed (database down, closed pool) and says nothing about who holds the lock; `ok = false` with a nil error means it is held elsewhere; `unlock` is never nil and is safe to call more than once.
