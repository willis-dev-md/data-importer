-- Core schema: sources, mappings, import runs, raw records, import errors.
-- Deliberately omitted (later migrations): source_groups, mapping_templates, source_files,
-- sources.group_id/schedule/enabled/next_run_at/last_scheduled_at, import_runs.file_id FK.

CREATE TABLE sources (
  id                     text PRIMARY KEY,
  kind                   text NOT NULL CHECK (kind IN ('csv','xlsx','http_json')),
  config                 jsonb NOT NULL,
  active_mapping_version int,
  created_at             timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE mappings (
  source_id  text NOT NULL REFERENCES sources(id),
  version    int  NOT NULL,
  spec       jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  created_by text,
  PRIMARY KEY (source_id, version)
);

CREATE TABLE import_runs (
  id            bigserial PRIMARY KEY,
  source_id     text NOT NULL REFERENCES sources(id),
  status        text NOT NULL CHECK (status IN ('running','succeeded','unchanged_file','failed','aborted')),
  trigger       text NOT NULL DEFAULT 'manual' CHECK (trigger IN ('manual','schedule')),
  scheduled_for timestamptz,
  file_id       bigint,
  source_meta   jsonb,
  started_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz,
  inserted      int NOT NULL DEFAULT 0,
  updated       int NOT NULL DEFAULT 0,
  unchanged     int NOT NULL DEFAULT 0,
  failed        int NOT NULL DEFAULT 0,
  tombstoned    int NOT NULL DEFAULT 0,
  shape         jsonb,
  error         text
);

CREATE TABLE raw_records (
  source_id    text NOT NULL REFERENCES sources(id),
  natural_key  text NOT NULL,
  payload      jsonb NOT NULL,
  content_hash bytea NOT NULL,
  first_run_id bigint NOT NULL REFERENCES import_runs(id),
  last_run_id  bigint NOT NULL REFERENCES import_runs(id),
  imported_at  timestamptz NOT NULL DEFAULT now(),
  deleted_at   timestamptz,
  PRIMARY KEY (source_id, natural_key)
);
CREATE INDEX raw_records_payload_gin ON raw_records USING gin (payload jsonb_path_ops);

CREATE TABLE import_errors (
  id       bigserial PRIMARY KEY,
  run_id   bigint NOT NULL REFERENCES import_runs(id),
  position text,
  raw      text,
  reason   text NOT NULL
);
