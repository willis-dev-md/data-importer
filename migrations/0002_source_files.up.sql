-- Fetched files kept for reparse and audit (docs/data-model.md, source_files), and the
-- foreign key from import_runs.file_id that 0001 left out.

CREATE TABLE source_files (
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

ALTER TABLE import_runs
  ADD CONSTRAINT import_runs_file_id_fkey FOREIGN KEY (file_id) REFERENCES source_files(id);
CREATE INDEX import_runs_file_id_idx ON import_runs (file_id) WHERE file_id IS NOT NULL;
CREATE INDEX import_runs_source_id_idx ON import_runs (source_id, id);
