-- Drops only what 0003 added.
DROP INDEX IF EXISTS sources_due;
ALTER TABLE sources
  DROP COLUMN IF EXISTS last_scheduled_at,
  DROP COLUMN IF EXISTS next_run_at,
  DROP COLUMN IF EXISTS enabled,
  DROP COLUMN IF EXISTS schedule;
