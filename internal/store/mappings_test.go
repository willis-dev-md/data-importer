package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/store"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

func xmins(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var s string
	err := pool.QueryRow(context.Background(), `
SELECT (SELECT coalesce(string_agg(id || ':' || xmin::text, ',' ORDER BY id), '') FROM sources) || '|' ||
       (SELECT coalesce(string_agg(source_id || ':' || version || ':' || xmin::text, ',' ORDER BY source_id, version), '') FROM mappings)`).Scan(&s)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestApply(t *testing.T) {
	ctx := context.Background()
	s, pool := newStore(t)
	item := func(cfg, spec string) store.ApplyItem {
		it := store.ApplyItem{Source: store.Source{ID: "a", Kind: "csv", Config: json.RawMessage(cfg)}, CreatedBy: "cli"}
		if spec != "" {
			it.Spec = json.RawMessage(spec)
		}
		return it
	}
	apply := func(it store.ApplyItem) store.ApplyResult {
		t.Helper()
		r, err := s.Apply(ctx, []store.ApplyItem{it})
		if err != nil {
			t.Fatal(err)
		}
		return r[0]
	}

	r := apply(item(`{"x":1}`, `{"source":"a","version":7,"fields":{"f":{"const":1.0}}}`))
	if !r.SourceCreated || !r.MappingCreated || r.MappingVersion != 1 {
		t.Fatalf("first apply %+v", r)
	}
	m, err := s.ActiveMapping(ctx, "a")
	if err != nil || m.Version != 1 || m.CreatedBy != "cli" {
		t.Fatalf("active %+v %v", m, err)
	}
	var spec map[string]any
	if err := json.Unmarshal(m.Spec, &spec); err != nil || spec["version"] != float64(1) {
		t.Fatalf("stored spec %s (version key must be the storage version)", m.Spec)
	}

	// Same content (different key order, number form, version key): nothing is written.
	before := xmins(t, pool)
	r = apply(item(`{"x": 1}`, `{"fields":{"f":{"const":1}},"source":"a","version":99}`))
	if r.SourceCreated || r.SourceUpdated || r.MappingCreated || r.MappingVersion != 1 {
		t.Fatalf("unchanged apply %+v", r)
	}
	if after := xmins(t, pool); after != before {
		t.Fatalf("unchanged apply wrote rows: %s -> %s", before, after)
	}

	// Changed mapping: version 2, active; source untouched.
	r = apply(item(`{"x":1}`, `{"source":"a","fields":{"f":{"const":2}}}`))
	if r.SourceUpdated || !r.MappingCreated || r.MappingVersion != 2 {
		t.Fatalf("changed mapping %+v", r)
	}
	src, _ := s.GetSource(ctx, "a")
	if src.ActiveMappingVersion == nil || *src.ActiveMappingVersion != 2 {
		t.Fatalf("active version %v", src.ActiveMappingVersion)
	}
	if v1, err := s.GetMapping(ctx, "a", 1); err != nil || !strings.Contains(string(v1.Spec), `"const": 1`) {
		t.Fatalf("v1 kept: %s %v", v1.Spec, err)
	}

	// Changed config only: source updated, mapping untouched; nil spec leaves mappings alone.
	r = apply(item(`{"x":2}`, ""))
	if !r.SourceUpdated || r.MappingCreated || r.MappingVersion != 2 {
		t.Fatalf("config change %+v", r)
	}
	if _, err := s.GetMapping(ctx, "a", 3); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("GetMapping v3 = %v", err)
	}
	if _, err := s.ActiveMapping(ctx, "nope"); !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("ActiveMapping missing = %v", err)
	}
}

func TestApplyIsAtomic(t *testing.T) {
	ctx := context.Background()
	s, pool := newStore(t)
	_, err := s.Apply(ctx, []store.ApplyItem{
		{Source: store.Source{ID: "ok", Kind: "csv", Config: json.RawMessage(`{}`)}, Spec: json.RawMessage(`{"source":"ok"}`)},
		{Source: store.Source{ID: "bad", Kind: "csv", Config: json.RawMessage(`{}`)}, Spec: json.RawMessage(`[1]`)},
	})
	if err == nil {
		t.Fatal("want error for a spec that is not an object")
	}
	if got := xmins(t, pool); got != "|" {
		t.Fatalf("rows left after failed apply: %s", got)
	}
}

func TestSweepAbandonedRuns(t *testing.T) {
	ctx := context.Background()
	// Every held lock pins a pooled connection, so the pool must be big enough.
	url := testutil.NewDatabaseURL(t)
	if _, err := store.Migrate(ctx, url, store.Up); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 24
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := store.New(pool)
	// Several ids so that lock keys of both signs are exercised.
	ids := []string{"s-live", "s-stale", "s-a", "s-b", "s-c", "s-d", "s-e", "s-f", "s-g"}
	for _, id := range ids {
		if err := s.UpsertSource(ctx, store.Source{ID: id, Kind: "csv", Config: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	// Hold the locks of every source except s-stale (and s-a..s-g are "live" too).
	live := map[string]int64{}
	for _, id := range ids {
		if id == "s-stale" {
			continue
		}
		unlock, ok, err := s.TryAdvisoryLock(ctx, id)
		if err != nil || !ok {
			t.Fatalf("lock %s: %v %v", id, ok, err)
		}
		defer unlock()
		runID, err := s.CreateRun(ctx, id, "manual")
		if err != nil {
			t.Fatal(err)
		}
		live[id] = runID
	}
	staleID, err := s.CreateRun(ctx, "s-stale", "manual") // lock not held: its process died
	if err != nil {
		t.Fatal(err)
	}
	doneID, _ := s.CreateRun(ctx, "s-stale", "manual")
	if err := s.FinishRun(ctx, doneID, "succeeded", store.RunCounts{}, nil, nil, ""); err != nil {
		t.Fatal(err)
	}

	got, err := s.SweepAbandonedRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != staleID {
		t.Fatalf("swept %v, want [%d]", got, staleID)
	}
	r, _ := s.GetRun(ctx, staleID)
	if r.Status != "failed" || r.FinishedAt == nil || r.Error == nil || *r.Error != "abandoned (process died)" {
		t.Fatalf("stale run %+v", r)
	}
	for id, runID := range live {
		r, _ := s.GetRun(ctx, runID)
		if r.Status != "running" || r.FinishedAt != nil || r.Error != nil {
			t.Fatalf("live run of %s touched: %+v", id, r)
		}
	}
	if r, _ := s.GetRun(ctx, doneID); r.Status != "succeeded" || r.Error != nil {
		t.Fatalf("finished run touched: %+v", r)
	}
	if again, err := s.SweepAbandonedRuns(ctx); err != nil || len(again) != 0 {
		t.Fatalf("second sweep = %v, %v", again, err)
	}

	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM import_runs WHERE status = 'running'`).Scan(&left); err != nil || left != len(live) {
		t.Fatalf("running rows = %d (%v), want %d", left, err, len(live))
	}
}

// A killed process's session disappears and takes its lock with it: simulate it
// by terminating the backend that holds the lock.
func TestSweepAfterSessionDies(t *testing.T) {
	ctx := context.Background()
	s, pool := newStore(t)
	if err := s.UpsertSource(ctx, store.Source{ID: "k", Kind: "csv", Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	unlock, ok, err := s.TryAdvisoryLock(ctx, "k")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer unlock()
	runID, _ := s.CreateRun(ctx, "k", "manual")
	if ids, _ := s.SweepAbandonedRuns(ctx); len(ids) != 0 {
		t.Fatalf("live run swept: %v", ids)
	}
	var n int
	err = pool.QueryRow(ctx, `
SELECT count(pg_terminate_backend(l.pid)) FROM pg_locks l
WHERE l.locktype = 'advisory' AND l.granted AND l.pid <> pg_backend_pid()
  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&n)
	if err != nil || n != 1 {
		t.Fatalf("terminate: %d %v", n, err)
	}
	// The backend is going away asynchronously; wait until its lock is gone.
	deadline := 50
	for ; deadline > 0; deadline-- {
		var held int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&held)
		if held == 0 {
			break
		}
		sleep()
	}
	ids, err := s.SweepAbandonedRuns(ctx)
	if err != nil || len(ids) != 1 || ids[0] != runID {
		t.Fatalf("swept %v %v, want [%d]", ids, err, runID)
	}
}
