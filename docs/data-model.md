# Data model (Postgres)

```sql
CREATE TABLE sources (
  id            text PRIMARY KEY,            -- slug, e.g. "acme-csv"
  kind          text NOT NULL CHECK (kind IN ('csv','http_json')),
  config        jsonb NOT NULL,              -- parsed source config (no secrets)
  active_mapping_version int,
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
  status        text NOT NULL CHECK (status IN ('running','succeeded','failed','aborted')),
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
- `(source_id, natural_key)` scoping stops two setups overwriting each other.
- `content_hash` makes re-imports cheap and gives accurate `unchanged` counts.
- Optional history: `raw_record_versions` (append-only) if audit of changes is needed (open question).
- Migrations via `golang-migrate` in `migrations/`.
