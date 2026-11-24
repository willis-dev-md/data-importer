<!-- factory-kit: customized (Go) -->
# 021 – Schedule engine: parsing, validation and next-run computation

## Goal
A source's `schedule` is validated at `sources apply` and a pure function computes the next run time (and the missed-slot decision) from it, in the source's timezone, with the new `sources` columns persisted.

## Depends on
none (020 validated)

## Design refs
`docs/design.md` (Scheduler), `docs/mapping-spec.md#schedule`, `docs/data-model.md` (sources columns `schedule`, `enabled`, `next_run_at`, `last_scheduled_at`, index `sources_due`; `import_runs.trigger`, `scheduled_for`), `docs/open-questions.md` 3a

## Decisions already made by the user
- A failed run is NOT retried; the next scheduled slot is the retry.
- Default timezone is `Europe/London` (used when `timezone` is omitted).
- `cron:` is NOT in scope: only `daily | weekly | monthly` (a `cron:` key is rejected with a clear "not supported yet" error).

## Scope
`internal/schedule/` (new, pure, no DB), `internal/config/` (schedule validation wired into source validation, `enabled`), `internal/store/` (+ migration `0003_schedule` adding `sources.schedule jsonb`, `enabled bool not null default true`, `next_run_at timestamptz`, `last_scheduled_at timestamptz`, partial index `sources_due`; `sources apply` persists schedule/enabled and recomputes `next_run_at` immediately when the schedule changes), tests, task Log

## Out of scope
The polling loop, claiming, concurrency limits, `importer schedule list` (task 022); cron expressions; jitter beyond a deterministic offset function (the offset is computed and tested here, applied in 022).

## Acceptance criteria (each must be checkable)
- [ ] Schedule parsed/validated: `every` required and one of daily|weekly|monthly; `at` `HH:MM` 24h (default `00:00`); `day_of_week` (monday..sunday, required for weekly, rejected otherwise); `day_of_month` 1-28 or `last` (required for monthly, rejected otherwise; note the current Go field is an int, so `last` needs a type change that keeps existing example files loading); `timezone` an IANA name (default Europe/London; unknown name is an error); `on_missed` run_once|skip (default run_once); `jitter` a Go duration 0..1h; `cron` rejected. Each error names the field; all errors reported together.
- [ ] `schedule.Next(spec, after time.Time) (time.Time, error)` returns the first slot strictly after `after`, in the spec's timezone, for daily/weekly/monthly. Table-driven tests with fixed instants: DST spring-forward (a slot in the skipped hour runs at the next valid instant that day, exactly once) and fall-back (a slot in the repeated hour runs once, the first occurrence); month ends (`last`, 28th, February leap/non-leap); weekly across year boundaries; timezone other than London; result always UTC-comparable and strictly increasing over 1,000 consecutive calls.
- [ ] `schedule.Plan(spec, lastScheduled, now)` returns the slots due and the next slot: after downtime `run_once` yields ONE catch-up slot (the latest missed) and `skip` yields none; cadence is computed from the scheduled slot, never the finish time (test: a run finishing late does not shift the next slot).
- [ ] `schedule.Offset(sourceID, jitter)` is deterministic per source id, in [0, jitter), stable across processes; zero jitter gives 0.
- [ ] Migration 0003 up and down work (down drops only what up added; version command shows 3); existing data and the ONS 642 / BoE 854 / HPI / ASHE e2e counts unchanged.
- [ ] `sources apply` on an invalid schedule exits 1 listing every error and changes nothing; on a valid one it stores the schedule and sets `next_run_at` (from now); re-applying an identical schedule keeps `next_run_at`; changing it recomputes immediately; `enabled: false` clears `next_run_at`; a source without `schedule` has `next_run_at` null (manual only).
- [ ] All docs/examples with a `schedule:` still validate (a test loads every example); a source with no schedule behaves exactly as before.
- [ ] Tests with real Postgres for the persistence parts, `-race`, deterministic (fake clock, no sleeps); `scripts/validate.sh` passes

## Docs impact
docs/mapping-spec.md#schedule (final rules, defaults, `cron` not supported); docs/data-model.md (columns, decisions); docs/design.md (Scheduler: no retry decision, cron deferred); docs/operations.md (`sources apply` behaviour; remove any claim that scheduling already runs); docs/open-questions.md (3a decided); CHANGELOG.md

## Runbook impact
docs/runbooks/onboarding-a-new-source.md (how to set a schedule, timezone default, what `sources apply` reports)

## Status
pending
## Log
(created by the orchestrator)
