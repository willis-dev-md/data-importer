package scheduler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/schedule"
	"github.com/willis-dev-md/data-importer/internal/scheduler"
	"github.com/willis-dev-md/data-importer/internal/source"
	"github.com/willis-dev-md/data-importer/internal/store"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

// ---- fake clock ----

type fakeClock struct {
	mu   sync.Mutex
	now  time.Time
	subs []chan time.Time
}

func newClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Set(t time.Time) { c.mu.Lock(); c.now = t; c.mu.Unlock() }
func (c *fakeClock) NewTicker(time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.subs = append(c.subs, ch)
	c.mu.Unlock()
	return ch, func() {}
}

// Tick fires every ticker once (never blocks: a pending tick is enough).
func (c *fakeClock) Tick() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.subs {
		select {
		case ch <- c.now:
		default:
		}
	}
}

// eventually polls cond until a deadline (no sleep-and-hope).
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// ---- environment ----

type env struct {
	pool *pgxpool.Pool
	st   *store.Store
	deps importer.Deps
	logs *lockedBuf
	log  *slog.Logger
}

var t0 = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC) // a Monday

func newEnv(t *testing.T) env {
	t.Helper()
	url := testutil.NewDatabaseURL(t)
	if _, err := store.Migrate(context.Background(), url, store.Up); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	files, err := store.NewDirFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st := store.NewWithOptions(pool, store.Options{LockCheckInterval: 50 * time.Millisecond})
	logs := &lockedBuf{}
	return env{
		pool: pool, st: st, logs: logs, log: slog.New(slog.NewTextHandler(logs, nil)),
		deps: importer.Deps{Store: st, Adapters: map[string]source.Adapter{config.KindCSV: source.NewCSV(source.FetchOptions{})}, Files: files},
	}
}

func repoFile(t *testing.T, parts ...string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join(append([]string{"..", ".."}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// addSource stores an ONS csv source (location = the fixture, or loc when given) with the schedule, as
// `sources apply` at time now would.
func (e env) addSource(t *testing.T, id, loc string, spec *schedule.Spec, disabled bool, now time.Time) {
	t.Helper()
	b, err := os.ReadFile(repoFile(t, "docs", "examples", "ons-l55o.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if loc == "" {
		loc = "file://" + repoFile(t, "testdata", "ons", "l55o.csv")
	}
	y := regexp.MustCompile(`(?m)^location:.*$`).ReplaceAllString(string(b), "location: "+loc)
	y = regexp.MustCompile(`(?m)^id:.*$`).ReplaceAllString(y, "id: "+id)
	y = regexp.MustCompile(`(?s)schedule:.*?\nenabled: true\n`).ReplaceAllString(y, "") + "change_detection: [content_hash]\n"
	cfg, err := config.ParseSource([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := importer.ConfigJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Apply(context.Background(), []store.ApplyItem{{
		Source: store.Source{ID: id, Kind: cfg.Kind, Config: raw}, Schedule: spec, Disabled: disabled, Now: now,
	}}); err != nil {
		t.Fatal(err)
	}
}

func mustSpec(t *testing.T, in schedule.Input) *schedule.Spec {
	t.Helper()
	s, err := schedule.New(in)
	if err != nil {
		t.Fatal(err)
	}
	return &s
}

// daily12 is "every day at 12:00 UTC".
func daily12(t *testing.T) *schedule.Spec {
	return mustSpec(t, schedule.Input{Every: "daily", At: "12:00", Timezone: "UTC"})
}

// realRun is the production RunFunc shape: importer.Run with trigger schedule.
func (e env) realRun(ctx context.Context, id string, slot time.Time) (importer.RunResult, error) {
	return importer.Run(ctx, e.deps, id, importer.Options{Trigger: "schedule", ScheduledFor: slot})
}

func (e env) sched(clock scheduler.Clock, run scheduler.RunFunc, conc int) *scheduler.Scheduler {
	return e.schedWith(e.st, clock, run, conc)
}

func (e env) schedWith(st *store.Store, clock scheduler.Clock, run scheduler.RunFunc, conc int) *scheduler.Scheduler {
	return scheduler.New(scheduler.Config{
		Store: st, Run: run, Clock: clock, Concurrency: conc, Logger: e.log, ShutdownGrace: 5 * time.Second,
		Sweep: st.SweepAbandonedRuns,
		Fail: func(ctx context.Context, id string, slot time.Time, cause error) error {
			_, err := st.RecordFailedRun(ctx, id, "schedule", slot, cause.Error())
			return err
		},
	})
}

type runRow struct {
	ID        int64
	Status    string
	Trigger   string
	Scheduled *time.Time
	Inserted  int
	Error     string
}

func (e env) runs(t *testing.T, id string) []runRow {
	t.Helper()
	rows, err := e.pool.Query(context.Background(),
		`SELECT id, status, trigger, scheduled_for, inserted, coalesce(error,'') FROM import_runs WHERE source_id=$1 ORDER BY id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []runRow
	for rows.Next() {
		var r runRow
		if err := rows.Scan(&r.ID, &r.Status, &r.Trigger, &r.Scheduled, &r.Inserted, &r.Error); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (e env) nextAndLast(t *testing.T, id string) (next, last *time.Time) {
	t.Helper()
	if err := e.pool.QueryRow(context.Background(), `SELECT next_run_at, last_scheduled_at FROM sources WHERE id=$1`, id).Scan(&next, &last); err != nil {
		t.Fatal(err)
	}
	return
}

func at(day int, h, m, s int) time.Time { return time.Date(2026, 1, day, h, m, s, 0, time.UTC) }

// ---- tests ----

func TestSettingsFromEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want scheduler.Settings
		err  string
	}{
		{"defaults", nil, scheduler.Settings{Enabled: true, PollInterval: 30 * time.Second, Concurrency: 2}, ""},
		{"all set", map[string]string{"SCHEDULER_ENABLED": "false", "SCHEDULER_POLL_INTERVAL": "100ms", "IMPORT_CONCURRENCY": "5"},
			scheduler.Settings{Enabled: false, PollInterval: 100 * time.Millisecond, Concurrency: 5}, ""},
		{"max poll", map[string]string{"SCHEDULER_POLL_INTERVAL": "1h"}, scheduler.Settings{Enabled: true, PollInterval: time.Hour, Concurrency: 2}, ""},
		{"enabled junk", map[string]string{"SCHEDULER_ENABLED": "maybe"}, scheduler.Settings{}, "SCHEDULER_ENABLED"},
		{"poll too small", map[string]string{"SCHEDULER_POLL_INTERVAL": "99ms"}, scheduler.Settings{}, "SCHEDULER_POLL_INTERVAL"},
		{"poll too big", map[string]string{"SCHEDULER_POLL_INTERVAL": "61m"}, scheduler.Settings{}, "SCHEDULER_POLL_INTERVAL"},
		{"poll junk", map[string]string{"SCHEDULER_POLL_INTERVAL": "soon"}, scheduler.Settings{}, "SCHEDULER_POLL_INTERVAL"},
		{"conc zero", map[string]string{"IMPORT_CONCURRENCY": "0"}, scheduler.Settings{}, "IMPORT_CONCURRENCY"},
		{"conc negative", map[string]string{"IMPORT_CONCURRENCY": "-1"}, scheduler.Settings{}, "IMPORT_CONCURRENCY"},
		{"conc junk", map[string]string{"IMPORT_CONCURRENCY": "two"}, scheduler.Settings{}, "IMPORT_CONCURRENCY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := scheduler.SettingsFromEnv(func(k string) string { return tt.env[k] })
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("err = %v, want it to name %s", err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

// A due source runs once with trigger=schedule and scheduled_for = the slot; next_run_at advances from the slot,
// not from when the run finished; the second slot finds the same file.
func TestDueSourceRunsOncePerSlot(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, "ons", "", daily12(t), false, t0)
	clock := newClock(at(5, 11, 59, 59))
	s := e.sched(clock, e.realRun, 2)
	ctx := context.Background()

	s.Poll(ctx)
	s.Wait()
	if n := len(e.runs(t, "ons")); n != 0 {
		t.Fatalf("ran before the slot: %d runs", n)
	}

	clock.Set(at(5, 12, 0, 30)) // the run starts late; the next slot must still be tomorrow 12:00
	s.Poll(ctx)
	s.Wait()
	runs := e.runs(t, "ons")
	if len(runs) != 1 || runs[0].Status != "succeeded" || runs[0].Trigger != "schedule" || runs[0].Inserted != 642 ||
		runs[0].Scheduled == nil || !runs[0].Scheduled.Equal(at(5, 12, 0, 0)) {
		t.Fatalf("runs = %+v", runs)
	}
	next, last := e.nextAndLast(t, "ons")
	if !next.Equal(at(6, 12, 0, 0)) || !last.Equal(at(5, 12, 0, 0)) {
		t.Fatalf("next %v last %v", next, last)
	}

	s.Poll(ctx) // nothing new is due
	s.Wait()
	if n := len(e.runs(t, "ons")); n != 1 {
		t.Fatalf("second poll in the same slot ran again: %d", n)
	}

	clock.Set(at(6, 12, 0, 1))
	s.Poll(ctx)
	s.Wait()
	runs = e.runs(t, "ons")
	if len(runs) != 2 || runs[1].Status != "unchanged_file" || !runs[1].Scheduled.Equal(at(6, 12, 0, 0)) {
		t.Fatalf("runs = %+v", runs)
	}
	if st := s.Stats(); st.Claimed != 2 || st.Failed != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestNotClaimed(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, "paused", "", daily12(t), true, t0)
	e.addSource(t, "manual", "", nil, false, t0)
	e.addSource(t, "later", "", daily12(t), false, t0)
	s := e.sched(newClock(at(5, 12, 0, 1)), e.realRun, 2)
	s.Poll(context.Background())
	s.Wait()
	for _, id := range []string{"paused", "manual"} {
		if n := len(e.runs(t, id)); n != 0 {
			t.Errorf("%s ran %d times", id, n)
		}
	}
	if n := len(e.runs(t, "later")); n != 1 {
		t.Errorf("later ran %d times, want 1", n)
	}
}

// A failed run is not retried: next_run_at already points at the next slot, and the next slot runs again.
func TestFailedRunNotRetried(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, "bad", "file:///nonexistent/nowhere.csv", daily12(t), false, t0)
	e.addSource(t, "good", "", daily12(t), false, t0)
	clock := newClock(at(5, 12, 0, 1))
	s := e.sched(clock, e.realRun, 2)
	ctx := context.Background()
	s.Poll(ctx)
	s.Wait()
	s.Poll(ctx)
	s.Poll(ctx)
	s.Wait()
	bad := e.runs(t, "bad")
	if len(bad) != 1 || bad[0].Status != "failed" || bad[0].Error == "" {
		t.Fatalf("bad runs = %+v", bad)
	}
	if g := e.runs(t, "good"); len(g) != 1 || g[0].Status != "succeeded" {
		t.Fatalf("one bad source stopped the others: %+v", g)
	}
	if next, _ := e.nextAndLast(t, "bad"); !next.Equal(at(6, 12, 0, 0)) {
		t.Fatalf("next_run_at = %v", next)
	}
	clock.Set(at(6, 12, 0, 1))
	s.Poll(ctx)
	s.Wait()
	if bad = e.runs(t, "bad"); len(bad) != 2 || bad[1].Status != "failed" || !bad[1].Scheduled.Equal(at(6, 12, 0, 0)) {
		t.Fatalf("next slot is the retry: %+v", bad)
	}
	if st := s.Stats(); st.Failed != 2 {
		t.Fatalf("stats %+v", st)
	}
}

// After downtime run_once runs ONCE for the latest missed slot; skip runs none. The scheduler is stopped and
// started again around the downtime.
func TestMissedSlotsAfterDowntime(t *testing.T) {
	tests := []struct {
		onMissed   string
		wantRuns   int
		wantSlot   time.Time
		wantPassed int
	}{
		{"run_once", 1, at(8, 12, 0, 0), 3}, // slots 5, 6, 7 missed and 8 due: the latest runs, three are passed over
		{"skip", 0, time.Time{}, 4},
	}
	for _, tt := range tests {
		t.Run(tt.onMissed, func(t *testing.T) {
			e := newEnv(t)
			e.addSource(t, "ons", "", mustSpec(t, schedule.Input{Every: "daily", At: "12:00", Timezone: "UTC", OnMissed: tt.onMissed}), false, t0)
			clock := newClock(at(5, 11, 0, 0))

			// Run 1 of the scheduler: up before the first slot, then stopped for the downtime.
			s1 := e.sched(clock, e.realRun, 2)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); s1.Run(ctx) }()
			cancel()
			<-done
			if n := len(e.runs(t, "ons")); n != 0 {
				t.Fatalf("ran early: %d", n)
			}

			clock.Set(at(8, 12, 30, 0))
			s2 := e.sched(clock, e.realRun, 2)
			ctx2, cancel2 := context.WithCancel(context.Background())
			done2 := make(chan struct{})
			go func() { defer close(done2); s2.Run(ctx2) }()
			eventually(t, "the first poll of the restarted scheduler", func() bool {
				next, _ := e.nextAndLast(t, "ons")
				return next.After(at(8, 12, 30, 0))
			})
			clock.Tick() // a further poll must not run anything more
			cancel2()
			<-done2

			runs := e.runs(t, "ons")
			if len(runs) != tt.wantRuns {
				t.Fatalf("runs = %+v, want %d", runs, tt.wantRuns)
			}
			if tt.wantRuns == 1 && (!runs[0].Scheduled.Equal(tt.wantSlot) || runs[0].Status != "succeeded") {
				t.Fatalf("run = %+v, want slot %v", runs[0], tt.wantSlot)
			}
			if next, _ := e.nextAndLast(t, "ons"); !next.Equal(at(9, 12, 0, 0)) {
				t.Fatalf("next_run_at = %v", next)
			}
			if got := s2.Stats().PassedOver; got != tt.wantPassed {
				t.Fatalf("passed over = %d, want %d", got, tt.wantPassed)
			}
			if !strings.Contains(e.logs.String(), "passed over") {
				t.Fatalf("passed-over slots not logged:\n%s", e.logs)
			}
		})
	}
}

// Jitter shifts the start time only: nothing is claimed before slot+offset, and an on_missed=skip source whose
// offset is larger than SkipGrace is still on time (Plan sees the raw slot).
func TestJitterShiftsStartNotTheSlot(t *testing.T) {
	e := newEnv(t)
	spec := mustSpec(t, schedule.Input{Every: "daily", At: "12:00", Timezone: "UTC", OnMissed: "skip", Jitter: "1h"})
	id := "jittered"
	off := schedule.Offset(id, time.Hour)
	if off <= schedule.SkipGrace+time.Minute || off > 50*time.Minute {
		t.Fatalf("test needs an offset between SkipGrace and 50m, got %v (pick another id)", off)
	}
	e.addSource(t, id, "", spec, false, t0)
	clock := newClock(at(5, 12, 0, 0).Add(off - time.Second))
	s := e.sched(clock, e.realRun, 2)
	ctx := context.Background()
	s.Poll(ctx)
	s.Wait()
	if n := len(e.runs(t, id)); n != 0 {
		t.Fatalf("started before slot+offset: %d", n)
	}
	if next, _ := e.nextAndLast(t, id); !next.Equal(at(5, 12, 0, 0)) {
		t.Fatalf("a not-yet-due source was modified: next_run_at %v", next)
	}
	clock.Set(at(5, 12, 0, 0).Add(off))
	s.Poll(ctx)
	s.Wait()
	runs := e.runs(t, id)
	if len(runs) != 1 || !runs[0].Scheduled.Equal(at(5, 12, 0, 0)) {
		t.Fatalf("runs = %+v (skip must not drop a jittered on-time slot)", runs)
	}
	if next, _ := e.nextAndLast(t, id); !next.Equal(at(6, 12, 0, 0)) {
		t.Fatalf("next_run_at = %v", next)
	}
}

// Three schedulers over one database and one due source start exactly one run per slot.
func TestThreeReplicasOneRunPerSlot(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, "ons", "", daily12(t), false, t0)
	clock := newClock(at(5, 11, 0, 0))
	var scheds []*scheduler.Scheduler
	for i := 0; i < 3; i++ {
		scheds = append(scheds, e.sched(clock, e.realRun, 2))
	}
	for day := 5; day <= 9; day++ {
		clock.Set(at(day, 12, 0, 1))
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, s := range scheds {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; s.Poll(context.Background()); s.Wait() }()
		}
		close(start)
		wg.Wait()
		runs := e.runs(t, "ons")
		if len(runs) != day-4 {
			next, last := e.nextAndLast(t, "ons")
			t.Fatalf("day %d: %d runs, want %d\nruns: %+v\nnext_run_at %v last_scheduled_at %v\nstats: %+v %+v %+v\nlogs:\n%s",
				day, len(runs), day-4, runs, next, last, scheds[0].Stats(), scheds[1].Stats(), scheds[2].Stats(), e.logs)
		}
		if last := runs[len(runs)-1]; last.Trigger != "schedule" || !last.Scheduled.Equal(at(day, 12, 0, 0)) {
			t.Fatalf("day %d: %+v", day, last)
		}
	}
	total := 0
	for _, s := range scheds {
		total += s.Stats().Claimed
	}
	if total != 5 {
		t.Fatalf("claimed %d times over 5 slots", total)
	}
}

// A source whose previous run still holds its lock is not started again; the skipped slot is logged and no run
// row is created.
func TestOverlapSkipped(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, "ons", "", daily12(t), false, t0)
	clock := newClock(at(5, 12, 0, 1))
	s := e.sched(clock, e.realRun, 2)
	unlock, ok, err := e.st.TryAdvisoryLock(context.Background(), "ons")
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	s.Poll(context.Background())
	s.Wait()
	if st := s.Stats(); st.SkippedRunning != 1 || st.Started != 0 {
		t.Fatalf("stats %+v", st)
	}
	if n := len(e.runs(t, "ons")); n != 0 {
		t.Fatalf("overlap created %d run rows", n)
	}
	if l := e.logs.String(); !strings.Contains(l, "still running") || !strings.Contains(l, "source=ons") || !strings.Contains(l, "2026-01-05T12:00:00") {
		t.Fatalf("skip not logged:\n%s", l)
	}
	unlock()
	clock.Set(at(6, 12, 0, 1))
	s.Poll(context.Background())
	s.Wait()
	if runs := e.runs(t, "ons"); len(runs) != 1 || !runs[0].Scheduled.Equal(at(6, 12, 0, 0)) {
		t.Fatalf("runs = %+v", runs)
	}
}

// gateRun is a RunFunc that blocks until released and records concurrency.
type gateRun struct {
	mu      sync.Mutex
	started []string
	cur     int32
	max     int32
	release chan struct{}
}

func newGate() *gateRun { return &gateRun{release: make(chan struct{}, 100)} }

func (g *gateRun) run(ctx context.Context, id string, slot time.Time) (importer.RunResult, error) {
	n := atomic.AddInt32(&g.cur, 1)
	for {
		m := atomic.LoadInt32(&g.max)
		if n <= m || atomic.CompareAndSwapInt32(&g.max, m, n) {
			break
		}
	}
	g.mu.Lock()
	g.started = append(g.started, id)
	g.mu.Unlock()
	defer atomic.AddInt32(&g.cur, -1)
	select {
	case <-g.release:
		return importer.RunResult{Status: importer.StatusSucceeded}, nil
	case <-ctx.Done():
		return importer.RunResult{Status: importer.StatusFailed}, fmt.Errorf("cancelled: %w", ctx.Err())
	}
}

func (g *gateRun) count() int { g.mu.Lock(); defer g.mu.Unlock(); return len(g.started) }

// At most IMPORT_CONCURRENCY runs at once; the sources that did not get a slot stay due (not advanced).
func TestConcurrencyLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.addSource(t, fmt.Sprintf("s%d", i), "", daily12(t), false, t0)
	}
	clock := newClock(at(5, 12, 0, 1))
	g := newGate()
	s := e.sched(clock, g.run, 2)
	ctx := context.Background()
	s.Poll(ctx)
	eventually(t, "two runs started", func() bool { return g.count() == 2 })
	s.Poll(ctx) // full: claims nothing
	unadvanced := func() int {
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM sources WHERE next_run_at <= $1`, at(5, 12, 0, 1)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := unadvanced(); n != 3 || g.count() != 2 {
		t.Fatalf("unadvanced %d, started %d; want 3 and 2", n, g.count())
	}
	for done := 2; done < 5; done++ {
		g.release <- struct{}{}
		want := done + 1
		if want > 5 {
			want = 5
		}
		eventually(t, "a free slot to be used", func() bool {
			s.Poll(ctx)
			return g.count() >= want || done == 4
		})
	}
	for i := 0; i < 5; i++ {
		g.release <- struct{}{}
	}
	eventually(t, "all five started", func() bool { s.Poll(ctx); return g.count() == 5 })
	s.Wait()
	if m := atomic.LoadInt32(&g.max); m > 2 {
		t.Fatalf("max concurrent runs = %d, want <= 2", m)
	}
	if n := unadvanced(); n != 0 {
		t.Fatalf("%d sources still due", n)
	}
}

// A panic in a run is recovered: the loop continues and the next slot still runs.
func TestPanicInRunDoesNotStopLoop(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, "boom", "", daily12(t), false, t0)
	clock := newClock(at(5, 12, 0, 1))
	var calls int32
	run := func(ctx context.Context, id string, slot time.Time) (importer.RunResult, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			panic("kaboom")
		}
		return e.realRun(ctx, id, slot)
	}
	s := e.sched(clock, run, 2)
	s.Poll(context.Background())
	s.Wait()
	if st := s.Stats(); st.Panics != 1 {
		t.Fatalf("stats %+v", st)
	}
	if !strings.Contains(e.logs.String(), "kaboom") {
		t.Fatalf("panic not logged:\n%s", e.logs)
	}
	clock.Set(at(6, 12, 0, 1))
	s.Poll(context.Background())
	s.Wait()
	if runs := e.runs(t, "boom"); len(runs) != 1 || runs[0].Status != "succeeded" {
		t.Fatalf("runs = %+v", runs)
	}
}

func schedulerGoroutines() string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var out []string
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, "internal/scheduler.") && !strings.Contains(g, "testing.tRunner") {
			out = append(out, g)
		}
	}
	return strings.Join(out, "\n\n")
}

// Shutdown: claiming stops, a run that finishes within the grace period is left alone, one that does not is
// cancelled, and every goroutine is joined.
func TestShutdownJoinsEverything(t *testing.T) {
	for _, tc := range []struct {
		name      string
		finishing bool
	}{{"run finishes in grace", true}, {"run cancelled after grace", false}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.addSource(t, "slow", "", daily12(t), false, t0)
			e.addSource(t, "later", "", daily12(t), false, t0)
			clock := newClock(at(5, 12, 0, 1))
			g := newGate()
			grace := 100 * time.Millisecond
			if tc.finishing {
				grace = 30 * time.Second
			}
			s := scheduler.New(scheduler.Config{Store: e.st, Run: g.run, Clock: clock, Concurrency: 1, Logger: e.log, ShutdownGrace: grace})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); s.Run(ctx) }()
			eventually(t, "the run to start", func() bool { return g.count() == 1 })
			cancel()
			if tc.finishing {
				select {
				case <-done:
					t.Fatal("Run returned while a run was in flight and the grace period was open")
				case <-time.After(50 * time.Millisecond):
				}
				g.release <- struct{}{}
			}
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatalf("Run did not return:\n%s", schedulerGoroutines())
			}
			// Run has returned, but its helper goroutine may still be inside its last instructions: poll.
			eventually(t, "every scheduler goroutine to end", func() bool { return schedulerGoroutines() == "" })
			if g.count() != 1 {
				t.Fatalf("claimed after shutdown: %d runs started", g.count())
			}
			var due int
			if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM sources WHERE next_run_at <= $1`, at(5, 12, 0, 1)).Scan(&due); err != nil || due != 1 {
				t.Fatalf("sources still due = %d (%v), want 1 (left for another replica)", due, err)
			}
		})
	}
}

// slowAdapter's stream blocks until the run's context ends.
type slowAdapter struct{ started chan struct{} }

type slowStream struct {
	ctx     context.Context
	started chan struct{}
	once    sync.Once
}

func (a slowAdapter) Open(ctx context.Context, _ *config.Source) (source.Stream, error) {
	return &slowStream{ctx: ctx, started: a.started}, nil
}
func (s *slowStream) Next() (source.Item, bool) {
	s.once.Do(func() { close(s.started) })
	<-s.ctx.Done()
	return source.Item{}, false
}
func (s *slowStream) Err() error              { return s.ctx.Err() }
func (s *slowStream) Meta() map[string]string { return nil }
func (s *slowStream) Close() error            { return nil }

// A real import still running when the grace period ends is cancelled and its run row is closed (never left
// 'running'), as for a manual run that gets SIGTERM.
func TestShutdownCancelsRealRun(t *testing.T) {
	e := newEnv(t)
	started := make(chan struct{})
	e.deps.Adapters = map[string]source.Adapter{config.KindCSV: slowAdapter{started: started}}
	e.deps.Files = nil
	e.addSource(t, "ons", "", daily12(t), false, t0)
	s := scheduler.New(scheduler.Config{Store: e.st, Run: e.realRun, Clock: newClock(at(5, 12, 0, 1)), Logger: e.log, ShutdownGrace: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	<-started
	cancel()
	<-done
	runs := e.runs(t, "ons")
	if len(runs) != 1 || runs[0].Status != "failed" || !strings.Contains(runs[0].Error, "cancelled") {
		t.Fatalf("runs = %+v", runs)
	}
}

// A source with an unreadable stored schedule is reported and does not stop the others.
func TestBadStoredScheduleDoesNotBlockOthers(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, "broken", "", daily12(t), false, t0)
	e.addSource(t, "good", "", daily12(t), false, t0)
	if _, err := e.pool.Exec(context.Background(), `UPDATE sources SET schedule = '"nonsense"'::jsonb WHERE id = 'broken'`); err != nil {
		t.Fatal(err)
	}
	s := e.sched(newClock(at(5, 12, 0, 1)), e.realRun, 2)
	s.Poll(context.Background())
	s.Wait()
	if g := e.runs(t, "good"); len(g) != 1 {
		t.Fatalf("good runs = %+v", g)
	}
	if !strings.Contains(e.logs.String(), `source=broken`) {
		t.Fatalf("broken schedule not reported:\n%s", e.logs)
	}
	var raw []byte
	_ = e.pool.QueryRow(context.Background(), `SELECT schedule FROM sources WHERE id='broken'`).Scan(&raw)
	var v string
	if json.Unmarshal(raw, &v) != nil || v != "nonsense" {
		t.Fatalf("broken row was modified: %s", raw)
	}
}

// A run row left 'running' by a process that died (its lock session is gone) is closed by the next poll
// (abandoned, failed); a live holder's row is never touched; the slot then runs exactly once.
func TestSweepsRunOfKilledReplicaThenSlotRuns(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, "ons", "", daily12(t), false, t0)
	ctx := context.Background()
	unlock, ok, err := e.st.TryAdvisoryLock(ctx, "ons") // the dead replica's run: its row and its lock session
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	defer unlock()
	deadRun, err := e.st.CreateRunFor(ctx, "ons", "schedule", at(4, 12, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	clock := newClock(at(5, 11, 0, 0)) // before the slot: polls only sweep
	s := e.sched(clock, e.realRun, 2)
	s.Poll(ctx)
	if r := e.runs(t, "ons"); len(r) != 1 || r[0].Status != "running" {
		t.Fatalf("live holder's run was touched: %+v", r)
	}

	// kill -9 of the replica: its lock session dies
	var killed int
	if err := e.pool.QueryRow(ctx, `
SELECT count(*) FROM (
  SELECT pg_terminate_backend(l.pid) FROM pg_locks l
  WHERE l.locktype = 'advisory' AND l.granted AND l.pid <> pg_backend_pid()
    AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND ((l.classid::bigint << 32) | l.objid::bigint) = hashtextextended('data-importer:source:' || $1::text, 0)
) x`, "ons").Scan(&killed); err != nil || killed != 1 {
		t.Fatalf("terminate: %d %v", killed, err)
	}
	eventually(t, "the sweep to close the dead run", func() bool {
		s.Poll(ctx)
		r := e.runs(t, "ons")
		return len(r) == 1 && r[0].Status == "failed"
	})
	if r := e.runs(t, "ons"); r[0].ID != deadRun || r[0].Error != store.AbandonedError {
		t.Fatalf("dead run = %+v", r[0])
	}

	clock.Set(at(5, 12, 0, 1))
	s.Poll(ctx)
	s.Wait()
	s.Poll(ctx)
	s.Wait()
	r := e.runs(t, "ons")
	if len(r) != 2 || r[1].Status != "succeeded" || !r[1].Scheduled.Equal(at(5, 12, 0, 0)) {
		t.Fatalf("runs = %+v", r)
	}
}

// A scheduled run that fails before it has a run row (no lock connection, unknown source, ...) is recorded as a
// failed run of the slot, never retried; the schedule list shows it.
func TestPreRunFailuresAreRecorded(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	tiny := store.NewWithOptions(e.pool, store.Options{LockConns: 1, LockWait: 50 * time.Millisecond})
	e.addSource(t, "cap", "", daily12(t), false, t0)
	e.addSource(t, "gone", "", daily12(t), false, t0)
	unlock, ok, err := tiny.TryAdvisoryLock(ctx, "other") // takes the only lock connection
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer unlock()
	run := func(ctx context.Context, id string, slot time.Time) (importer.RunResult, error) {
		if id == "gone" {
			return importer.RunResult{}, fmt.Errorf("source %q: stored config: boom", id)
		}
		return importer.Run(ctx, importer.Deps{Store: tiny, Adapters: e.deps.Adapters, Files: e.deps.Files}, id,
			importer.Options{Trigger: "schedule", ScheduledFor: slot})
	}
	clock := newClock(at(5, 12, 0, 1))
	s := e.schedWith(tiny, clock, run, 2)
	s.Poll(ctx)
	s.Wait()
	s.Poll(ctx)
	s.Wait()
	for id, want := range map[string]string{"cap": "no lock connection available", "gone": "stored config: boom"} {
		r := e.runs(t, id)
		if len(r) != 1 || r[0].Status != "failed" || r[0].Trigger != "schedule" || r[0].Scheduled == nil ||
			!r[0].Scheduled.Equal(at(5, 12, 0, 0)) || !strings.Contains(r[0].Error, want) {
			t.Errorf("%s: runs = %+v, want one failed row containing %q", id, r, want)
		}
		if next, last := e.nextAndLast(t, id); !next.Equal(at(6, 12, 0, 0)) || !last.Equal(at(5, 12, 0, 0)) {
			t.Errorf("%s: next %v last %v (never retried)", id, next, last)
		}
	}
	rows, err := tiny.ListSchedules(ctx)
	if err != nil || len(rows) != 2 || rows[0].LastRunStatus != "failed" || rows[1].LastRunStatus != "failed" {
		t.Fatalf("schedule list: %+v %v", rows, err)
	}
	if st := s.Stats(); st.Failed != 2 {
		t.Fatalf("stats %+v", st)
	}
}
