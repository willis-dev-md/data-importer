# ADR-0007: Schedule engine decisions (task 021)

Status: accepted (task 021)

## Context
A source's `schedule` is validated when it is applied, and the next run time is computed in the source's timezone, with
daylight saving changes and downtime handled the same way every time. Task 021 builds the pure computation
(`internal/schedule`) and its persistence (`sources apply`, migration `0003_schedule`). The polling loop that starts due
runs is task 022.

## Decision
1. No retry. A failed run is not retried; the next scheduled slot is the retry. (user decision)
2. Scope: `every` is `daily`, `weekly` or `monthly`. `cron:` is rejected with `schedule.cron: not supported yet (use every: daily, weekly or monthly)`. (user decision)
3. Defaults: `timezone` `Europe/London` (user decision), `at` `00:00`, `on_missed` `run_once`, `jitter` `0s`.
4. Spring-forward gap: a local time that does not exist runs once, at the transition instant. Autumn repeated hour: a local time that occurs twice runs once, at its first occurrence.
5. Catch-up: `run_once` runs one catch-up for the latest missed slot. `skip` runs no catch-up, except a slot at most `SkipGrace` (5 minutes) old, so on-time slots are not skipped. Task 022 must compare the slot time, not slot plus jitter, with now, or apply the offset before calling `Plan`.
6. Cadence is computed from the scheduled slot, never from the finish time of a run.
7. Jitter: `schedule.Offset(sourceID, jitter)` is the FNV-1a 64-bit hash of the source id, modulo the jitter; it is deterministic, lies in [0, jitter) and is 0 when jitter is zero. It is computed in task 021 and applied in task 022.
8. `day_of_month` is an int in Go with `-1` meaning `last`. `0` is indistinguishable from unset, so monthly reports it as required.
9. The stored `sources.schedule` is the normalised spec as JSON (`every`, `at`, `day_of_week`, `day_of_month`, `timezone`, `on_missed`, `jitter`). The file text stays in `sources.config`. Equal schedules compare equal, so an identical re-apply keeps `next_run_at`.
10. A disabled source keeps its stored schedule; `enabled: false` clears `next_run_at`, and `sources_due` excludes it.
11. The binary embeds the IANA timezone database (`import _ "time/tzdata"`), so a minimal image resolves timezone names. Size cost: UNVERIFIED (the validator measured about 410 KB; the task 021 log says about 450 KB).

## Consequences
- `sources apply` writes nothing when any schedule field is invalid, and lists every error it found.
- Task 022 added the scheduler loop: `importer serve` runs due scheduled sources ([ADR-0008](0008-scheduler-loop.md)). Before it, `importer run --source <id>` was the only way to run them.
- Known gap: when `cron` is present, `every` errors are not listed (the `cron` check returns first). The task asked for all errors together. Not fixed in task 021.
- The schedule maths was checked against Python `zoneinfo` on 51 cases with 0 mismatches (validator report, task 021).
