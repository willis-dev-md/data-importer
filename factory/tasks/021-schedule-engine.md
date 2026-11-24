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
validated
## Log
(created by the orchestrator)

### Worker, round 1
Files: `internal/schedule/{doc,schedule,schedule_test}.go` (new, pure); `internal/config/source.go` (+ `schedule_test.go`); `internal/store/mappings.go` (+ `schedule_test.go`, `migrate_test.go`); `migrations/0003_schedule.{up,down}.sql`; `cmd/importer/sources.go` (+ `schedule_cli_test.go`, `main_test.go` and other `version 2` -> `version 3` expectations incl. `internal/e2e`).

What it does:
- `schedule.New(Input) (Spec, error)` validates (all errors, each `schedule.<field>: ...`) and fills defaults (`at` 00:00, `timezone` Europe/London, `on_missed` run_once, `jitter` 0s). `Spec` is plain comparable data and is what is stored in `sources.schedule` (normalised, so equal schedules compare equal). `cron` is rejected: `schedule.cron: not supported yet (use every: daily, weekly or monthly)`.
- `config.DayOfMonth` (int type, `-1` = `last`) reads/writes both `18` and `last`, so existing example files load and `ConfigJSON` round-trips. `config.Schedule` gained `cron`; `Schedule.Spec()` is called from `Source.Validate`, so every source load validates the schedule. `Source.IsEnabled()` added.
- `schedule.Next(spec, after)` returns UTC, strictly after. Gap (spring-forward): the transition instant (first valid instant that day, once). Repeated hour (fall-back): the first occurrence only. Found with offset probing + binary search on the zone database, no reliance on `time.Date` behaviour in gaps.
- `schedule.Plan(spec, lastScheduled, now) (Result{Due, Missed, Next})`: slots in (lastScheduled, now] are due; run_once -> one catch-up (latest), `Missed` counts the rest; skip -> none unless the latest slot is at most `SkipGrace` (5m) old, i.e. on time. Zero `lastScheduled` = no history: nothing due, Next from now. Next never depends on finish time (tested).
- `schedule.Offset(id, jitter)` = FNV-1a 64 of the id mod jitter; 0 for jitter <= 0; pinned in a test.
- Migration 0003 up/down; `Store.Apply`: `ApplyItem.Schedule *schedule.Spec`, `Disabled`, `Now` (clock; zero = time.Now()); `ApplyResult.ScheduleChanged/NextRunAt`. next_run_at is kept when schedule and enabled are unchanged and it is set; recomputed (from Now) on change, re-enable or when null; null when no schedule or disabled. No write at all when nothing changed (xmin test). `last_scheduled_at` never touched by apply. All inside the existing all-or-nothing transaction.
- CLI line: `<id>: source <x>, mapping <y>` is unchanged for unscheduled sources; scheduled ones append `, next run <RFC3339 UTC>`; a cleared schedule appends `, schedule cleared (manual only or disabled)`. An invalid schedule fails in config load (exit 1, all errors, "nothing was written").

Decisions (small, not in the docs):
- `on_missed: skip` needs a notion of "missed" vs "on time": `SkipGrace = 5m` (const in `schedule`). 022 must compare slot time (not slot+jitter offset) against its now, or apply the offset before calling Plan, so a jittered on-time slot is not skipped.
- `day_of_month: 0` is indistinguishable from unset (int type): monthly says "required", other cadences ignore it.
- Stored `sources.schedule` is the normalised Spec JSON: `{"every","at","day_of_week"?,"day_of_month"? (-1 = last),"timezone","on_missed","jitter"}` (not the file text; the file text is still in `sources.config`).
- `import _ "time/tzdata"` in `internal/schedule` (stdlib, +~450 KB) so a minimal image without zoneinfo still resolves IANA names; the dev container does have /usr/share/zoneinfo.
- Disabled sources keep their stored schedule (only `next_run_at` is cleared); `sources_due` already excludes them via `enabled`.
- Group example files are not loadable yet (no group loader); the example test validates the `schedule:` blocks inside them generically, and the http_json example (`partner-api`) by schedule block only.
- No dependency added; go.mod untouched.

Docs/runbooks the documentor / runbook-refiner must say:
- docs/mapping-spec.md#schedule: only daily|weekly|monthly; `cron` rejected ("not supported yet"), remove "or `cron`" from the example comment and the "exactly one of every or cron" rule; `every` required; defaults (`at` 00:00, `timezone` Europe/London, `on_missed` run_once, `jitter` 0); `day_of_week` required for weekly and rejected otherwise; `day_of_month` 1-28 or `last`, required for monthly and rejected otherwise; jitter 0..1h; DST: gap runs at the transition instant once, repeated hour runs once at the first occurrence; `enabled: false` pauses and clears next_run_at; `skip` grace 5m.
- docs/design.md Scheduler: no retry of failed runs (next slot is the retry), cron deferred, "daily, weekly, monthly or cron" in the goals line, jitter is a deterministic per-source offset (FNV-1a, computed in 021, applied in 022).
- docs/data-model.md: columns `schedule` (normalised Spec JSON, shape above), `enabled`, `next_run_at`, `last_scheduled_at`, index `sources_due` now exist (migration 0003); fix the migrations sentence at line 100 (0003_schedule adds them; groups/templates/`group_id` still later).
- docs/operations.md line ~374: scheduling does NOT run yet (loop is task 022); `sources apply` validates, stores the schedule, reports `next run <time>`; `importer schedule list` is task 022, not available.
- docs/open-questions.md 3a: decided (no retry, Europe/London, run_once default, no cron).
- CHANGELOG.md entry; docs/roadmap.md M7 progress if wanted.
- Runbooks: onboarding-a-new-source.md (set a schedule, timezone default, `sources apply` prints `next run ...` / `schedule cleared`, invalid schedule lists all `schedule.<field>` errors); `migrate up` now prints `version 3` and the schema is version 3 in database-migrations.md, reparse-and-file-retention.md, run-the-grpc-server.md, demo-and-smoke-test.md, runbooks/README.md; uk-hpi-monthly-update.md still says the scheduler is not implemented (still true until 022, but `sources apply` now stores the schedule and next_run_at).
- Validator round 1: PASS (51 DST/month cases == Python zoneinfo; migration 0003 up/down; sources apply behaviour). Known gap (documented, ADR-0007): with cron present other schedule errors are not listed. Docs roles ran; docs-check OK; gate OK.
