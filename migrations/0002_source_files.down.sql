-- file_id values point into source_files, which is dropped: clear them so a later `up` can add the foreign key again.
UPDATE import_runs SET file_id = NULL WHERE file_id IS NOT NULL;
DROP INDEX IF EXISTS import_runs_source_id_idx;
DROP INDEX IF EXISTS import_runs_file_id_idx;
ALTER TABLE import_runs DROP CONSTRAINT IF EXISTS import_runs_file_id_fkey;
DROP TABLE IF EXISTS source_files;
