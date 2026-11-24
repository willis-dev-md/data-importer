package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/willis-dev-md/data-importer/internal/schedule"
	"github.com/willis-dev-md/data-importer/internal/store"
)

func TestApplySchedule(t *testing.T) {
	ctx := context.Background()
	s, pool := newStore(t)
	mk := func(in schedule.Input) *schedule.Spec {
		t.Helper()
		sp, err := schedule.New(in)
		if err != nil {
			t.Fatal(err)
		}
		return &sp
	}
	ts := func(v string) time.Time {
		t.Helper()
		x, err := time.Parse(time.RFC3339, v)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	apply := func(sp *schedule.Spec, disabled bool, now string) store.ApplyResult {
		t.Helper()
		r, err := s.Apply(ctx, []store.ApplyItem{{
			Source: store.Source{ID: "a", Kind: "csv", Config: json.RawMessage(`{}`)}, Schedule: sp, Disabled: disabled, Now: ts(now),
		}})
		if err != nil {
			t.Fatal(err)
		}
		return r[0]
	}
	type row struct {
		sched   *string
		enabled bool
		next    *time.Time
		last    *time.Time
		xmin    string
	}
	get := func() row {
		t.Helper()
		var r row
		err := pool.QueryRow(ctx, `SELECT schedule::text, enabled, next_run_at, last_scheduled_at, xmin::text FROM sources WHERE id='a'`).
			Scan(&r.sched, &r.enabled, &r.next, &r.last, &r.xmin)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	wantNext := func(r row, want string) {
		t.Helper()
		if want == "" {
			if r.next != nil {
				t.Errorf("next_run_at = %v, want null", r.next)
			}
			return
		}
		if r.next == nil || !r.next.Equal(ts(want)) {
			t.Errorf("next_run_at = %v, want %s", r.next, want)
		}
	}

	// No schedule: manual only, enabled, nothing due.
	if r := apply(nil, false, "2026-05-01T10:00:00Z"); r.NextRunAt != nil || r.ScheduleChanged {
		t.Errorf("manual-only apply: %+v", r)
	}
	g := get()
	if g.sched != nil || !g.enabled || g.next != nil {
		t.Errorf("manual-only row: %+v", g)
	}

	// A schedule is stored and next_run_at is computed from now (Europe/London daily 16:00, BST).
	daily := mk(schedule.Input{Every: "daily", At: "16:00"})
	r := apply(daily, false, "2026-05-01T10:00:00Z")
	if !r.ScheduleChanged || r.NextRunAt == nil || !r.NextRunAt.Equal(ts("2026-05-01T15:00:00Z")) {
		t.Fatalf("schedule apply: %+v", r)
	}
	g = get()
	wantNext(g, "2026-05-01T15:00:00Z")
	var stored schedule.Spec
	if err := json.Unmarshal([]byte(*g.sched), &stored); err != nil || stored != *daily {
		t.Errorf("stored schedule %v (%v), want %+v", *g.sched, err, *daily)
	}
	first := g

	// Identical schedule, later now: nothing is rewritten and next_run_at is kept.
	r = apply(mk(schedule.Input{Every: "daily", At: "16:00"}), false, "2026-05-01T14:00:00Z")
	if r.ScheduleChanged || r.NextRunAt == nil || !r.NextRunAt.Equal(ts("2026-05-01T15:00:00Z")) {
		t.Errorf("identical re-apply: %+v", r)
	}
	if g = get(); g.xmin != first.xmin {
		t.Error("identical schedule rewrote the row")
	}
	// Even once next_run_at is in the past (the scheduler has not run yet), an identical apply keeps it.
	apply(daily, false, "2026-05-03T10:00:00Z")
	wantNext(get(), "2026-05-01T15:00:00Z")

	// last_scheduled_at belongs to the scheduler and is never touched by apply.
	if _, err := pool.Exec(ctx, `UPDATE sources SET last_scheduled_at = $1 WHERE id='a'`, ts("2026-05-01T15:00:00Z")); err != nil {
		t.Fatal(err)
	}

	// Changed schedule: recomputed immediately.
	r = apply(mk(schedule.Input{Every: "weekly", DayOfWeek: "monday", At: "07:00"}), false, "2026-05-03T10:00:00Z")
	if !r.ScheduleChanged {
		t.Errorf("changed schedule: %+v", r)
	}
	g = get()
	wantNext(g, "2026-05-04T06:00:00Z")
	if g.last == nil || !g.last.Equal(ts("2026-05-01T15:00:00Z")) {
		t.Errorf("last_scheduled_at changed: %v", g.last)
	}

	// A timezone change is a schedule change.
	apply(mk(schedule.Input{Every: "weekly", DayOfWeek: "monday", At: "07:00", Timezone: "UTC"}), false, "2026-05-03T10:00:00Z")
	wantNext(get(), "2026-05-04T07:00:00Z")

	// enabled: false clears next_run_at but keeps the schedule.
	r = apply(mk(schedule.Input{Every: "weekly", DayOfWeek: "monday", At: "07:00", Timezone: "UTC"}), true, "2026-05-03T10:00:00Z")
	if !r.ScheduleChanged || r.NextRunAt != nil {
		t.Errorf("disable: %+v", r)
	}
	g = get()
	if g.enabled || g.next != nil || g.sched == nil {
		t.Errorf("disabled row: %+v", g)
	}
	// Disabled again: unchanged, nothing written.
	x := g.xmin
	apply(mk(schedule.Input{Every: "weekly", DayOfWeek: "monday", At: "07:00", Timezone: "UTC"}), true, "2026-05-03T11:00:00Z")
	if get().xmin != x {
		t.Error("identical disabled apply rewrote the row")
	}
	// Re-enable: recomputed from now.
	apply(mk(schedule.Input{Every: "weekly", DayOfWeek: "monday", At: "07:00", Timezone: "UTC"}), false, "2026-05-05T10:00:00Z")
	g = get()
	wantNext(g, "2026-05-11T07:00:00Z")
	if !g.enabled {
		t.Error("not re-enabled")
	}

	// Removing the schedule makes the source manual only again.
	r = apply(nil, false, "2026-05-05T10:00:00Z")
	g = get()
	if !r.ScheduleChanged || g.sched != nil || g.next != nil || !g.enabled {
		t.Errorf("schedule removed: %+v %+v", r, g)
	}

	// A source row that already has a schedule but no next_run_at (for example cleared by hand) is repaired.
	apply(daily, false, "2026-06-01T00:00:00Z")
	if _, err := pool.Exec(ctx, `UPDATE sources SET next_run_at = NULL WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	apply(daily, false, "2026-06-01T00:00:00Z")
	wantNext(get(), "2026-06-01T15:00:00Z")

	// The partial index serves the due query.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sources WHERE enabled AND schedule IS NOT NULL AND next_run_at <= $1`, ts("2026-06-02T00:00:00Z")).Scan(&n); err != nil || n != 1 {
		t.Errorf("due count = %d, %v", n, err)
	}
}

// Apply is all or nothing: a failure in a later item leaves earlier schedules unwritten.
func TestApplyScheduleAtomic(t *testing.T) {
	ctx := context.Background()
	s, pool := newStore(t)
	sp, _ := schedule.New(schedule.Input{Every: "daily"})
	bad := schedule.Spec{Every: "daily", Timezone: "Mars/Base"}
	_, err := s.Apply(ctx, []store.ApplyItem{
		{Source: store.Source{ID: "ok", Kind: "csv"}, Schedule: &sp},
		{Source: store.Source{ID: "bad", Kind: "csv"}, Schedule: &bad},
	})
	if err == nil {
		t.Fatal("want error for an invalid spec")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sources`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sources = %d, %v; want nothing written", n, err)
	}
}
