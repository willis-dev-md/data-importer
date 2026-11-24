-- Scheduling columns on sources (docs/data-model.md): the schedule, the pause flag and the scheduler's bookkeeping.
-- next_run_at / last_scheduled_at are maintained by `sources apply` (task 021) and the scheduler loop (task 022).

ALTER TABLE sources
  ADD COLUMN schedule          jsonb,                               -- normalised schedule; null = manual only
  ADD COLUMN enabled           boolean NOT NULL DEFAULT true,
  ADD COLUMN next_run_at       timestamptz,
  ADD COLUMN last_scheduled_at timestamptz;

CREATE INDEX sources_due ON sources (next_run_at) WHERE enabled AND schedule IS NOT NULL;
