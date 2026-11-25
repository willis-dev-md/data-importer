package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/willis-dev-md/data-importer/internal/schedule"
)

// Claim is one source taken by ClaimDue. Slot is the schedule slot to run (zero when the source has nothing to
// run: with on_missed = skip the missed slots were only passed over). Next is the new next_run_at. Err is set
// (and nothing was written) when the stored schedule cannot be read.
type Claim struct {
	SourceID string
	Slot     time.Time
	Missed   int // slots passed over without a run (run_once keeps the latest and counts the rest)
	Next     time.Time
	Spec     schedule.Spec
	Err      error
}

// ClaimDue claims the sources that are due at now, in one short transaction that ends before any import starts:
// `enabled AND schedule IS NOT NULL AND next_run_at <= now` rows are locked with FOR UPDATE SKIP LOCKED (another
// replica claiming the same row simply does not see it), their next_run_at is advanced to the slot after the
// scheduled one (never from the finish time) and last_scheduled_at is set to the slot that will run.
//
// The jitter offset of a source (schedule.Offset) shifts only the start time: a source is claimed once
// slot+offset <= now, and schedule.Plan is called with now-offset, so the slots stay the raw ones and an
// on_missed = skip source is not treated as late because of its own jitter. At most max sources that have a slot
// to run are claimed (sources whose slots were only skipped do not count).
func (s *Store) ClaimDue(ctx context.Context, now time.Time, max int) ([]Claim, error) {
	if max <= 0 {
		return nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim due sources: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
SELECT id, schedule, next_run_at FROM sources
WHERE enabled AND schedule IS NOT NULL AND next_run_at <= $1
ORDER BY next_run_at, id
FOR UPDATE SKIP LOCKED`, now)
	if err != nil {
		return nil, fmt.Errorf("claim due sources: %w", err)
	}
	type cand struct {
		id   string
		raw  []byte
		next time.Time
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.id, &c.raw, &c.next); err != nil {
			rows.Close()
			return nil, fmt.Errorf("claim due sources: %w", err)
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim due sources: %w", err)
	}

	var out []Claim
	running := 0
	for _, c := range cands {
		cl := Claim{SourceID: c.id}
		if err := json.Unmarshal(c.raw, &cl.Spec); err != nil {
			cl.Err = fmt.Errorf("stored schedule of %q: %w", c.id, err)
			out = append(out, cl)
			continue
		}
		eff := now.Add(-schedule.Offset(c.id, cl.Spec.JitterDuration()))
		if c.next.After(eff) {
			continue // its jittered start is still ahead
		}
		// next_run_at is the first slot not handled yet, so history starts just before it. (Using
		// last_scheduled_at would count slots of an older schedule as missed after a schedule change.)
		res, err := schedule.Plan(cl.Spec, c.next.Add(-time.Nanosecond), eff)
		if err != nil {
			cl.Err = fmt.Errorf("plan %q: %w", c.id, err)
			out = append(out, cl)
			continue
		}
		if len(res.Due) > 0 {
			if running >= max {
				continue // no free run slot: leave the row for the next poll
			}
			running++
			cl.Slot = res.Due[0]
		}
		cl.Missed, cl.Next = res.Missed, res.Next
		var last *time.Time
		if !cl.Slot.IsZero() {
			last = &cl.Slot
		}
		if _, err := tx.Exec(ctx, `UPDATE sources SET next_run_at = $2, last_scheduled_at = COALESCE($3, last_scheduled_at) WHERE id = $1`,
			c.id, cl.Next, last); err != nil {
			return nil, fmt.Errorf("claim %q: %w", c.id, err)
		}
		out = append(out, cl)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim due sources: %w", err)
	}
	return out, nil
}

// ScheduleRow is one line of `importer schedule list`.
type ScheduleRow struct {
	ID              string
	Enabled         bool
	Schedule        *schedule.Spec // nil = manual only
	NextRunAt       *time.Time
	LastScheduledAt *time.Time
	LastRunStatus   string // status of the source's latest run, "" if it never ran
	LastRunTrigger  string
}

// ListSchedules returns every source with its schedule state and the status of its latest run, by id.
func (s *Store) ListSchedules(ctx context.Context) ([]ScheduleRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT s.id, s.enabled, s.schedule, s.next_run_at, s.last_scheduled_at, coalesce(r.status, ''), coalesce(r.trigger, '')
FROM sources s
LEFT JOIN LATERAL (SELECT status, trigger FROM import_runs WHERE source_id = s.id ORDER BY id DESC LIMIT 1) r ON true
ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	defer rows.Close()
	var out []ScheduleRow
	for rows.Next() {
		var r ScheduleRow
		var raw []byte
		if err := rows.Scan(&r.ID, &r.Enabled, &raw, &r.NextRunAt, &r.LastScheduledAt, &r.LastRunStatus, &r.LastRunTrigger); err != nil {
			return nil, fmt.Errorf("list schedules: %w", err)
		}
		if raw != nil {
			var sp schedule.Spec
			if err := json.Unmarshal(raw, &sp); err != nil {
				return nil, fmt.Errorf("list schedules: stored schedule of %q: %w", r.ID, err)
			}
			r.Schedule = &sp
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list schedules: %w", err)
	}
	return out, nil
}
