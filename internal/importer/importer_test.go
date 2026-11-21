package importer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/record"
	"github.com/willis-dev-md/data-importer/internal/source"
	"github.com/willis-dev-md/data-importer/internal/store"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

// scripted is a stream over fixed items.
type scripted struct {
	items  []source.Item
	i      int
	err    error
	meta   map[string]string
	onNext func(i int) // called before returning item i
}

func (s *scripted) Next() (source.Item, bool) {
	if s.i >= len(s.items) {
		return source.Item{}, false
	}
	if s.onNext != nil {
		s.onNext(s.i)
	}
	it := s.items[s.i]
	s.i++
	return it, true
}
func (s *scripted) Err() error              { return s.err }
func (s *scripted) Meta() map[string]string { return s.meta }
func (s *scripted) Close() error            { return nil }
func rec(pos int64, key string, f map[string]any) source.Item {
	return source.Item{Record: &source.Record{Position: pos, NaturalKey: record.Key(key), Fields: f}}
}
func bad(pos int64) source.Item {
	return source.Item{Error: &source.RowError{Position: pos, Raw: "x,y,z", Reason: "bad row"}}
}

type fakeAdapter struct {
	stream  *scripted
	openErr error
}

func (a fakeAdapter) Open(context.Context, *config.Source) (source.Stream, error) {
	if a.openErr != nil {
		return nil, a.openErr
	}
	return a.stream, nil
}

type env struct {
	pool *pgxpool.Pool
	st   *store.Store
}

func newEnv(t *testing.T) env {
	t.Helper()
	url := testutil.NewDatabaseURL(t)
	if _, err := store.Migrate(context.Background(), url, store.Up); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pcfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	// Every running import holds one connection for its source lock and needs
	// another to write: leave room for concurrent runs of different sources.
	pcfg.MaxConns = 32
	pool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return env{pool: pool, st: store.New(pool)}
}

func (e env) addSource(t *testing.T, cfg *config.Source) {
	t.Helper()
	b, err := importer.ConfigJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertSource(context.Background(), store.Source{ID: cfg.ID, Kind: cfg.Kind, Config: b}); err != nil {
		t.Fatal(err)
	}
}

func testCfg(ratio float64) *config.Source {
	return &config.Source{ID: "s1", Kind: "csv", Location: "unused", NaturalKey: []string{"k"}, MaxErrorRatio: ratio}
}

func (e env) deps(a source.Adapter) importer.Deps {
	return importer.Deps{Store: e.st, Adapters: map[string]source.Adapter{"csv": a}}
}

func (e env) countRuns(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM import_runs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e env) countRaw(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM raw_records`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e env) lockFree(t *testing.T) bool {
	t.Helper()
	unlock, ok, err := e.st.TryAdvisoryLock(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	return ok
}

func TestRunBatchesAndShape(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, testCfg(0))
	var items []source.Item
	for i := 0; i < 7; i++ {
		items = append(items, rec(int64(i+1), fmt.Sprint(i), map[string]any{"k": fmt.Sprint(i), "n": json.Number("1"), "_meta": map[string]string{"a": "b"}}))
	}
	items = append(items, rec(8, "x", map[string]any{"k": "x", "n": nil}))
	a := fakeAdapter{stream: &scripted{items: items, meta: map[string]string{"CDID": "Z"}}}
	res, err := importer.Run(context.Background(), e.deps(a), "s1", importer.Options{Trigger: "schedule", BatchSize: 3})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "succeeded" || res.Inserted != 8 || res.Total != 8 {
		t.Fatalf("result %+v", res)
	}
	run, err := e.st.GetRun(context.Background(), res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "succeeded" || run.Trigger != "schedule" || run.FinishedAt == nil || run.Inserted != 8 {
		t.Fatalf("run %+v", run)
	}
	var shape map[string]map[string]int
	if err := json.Unmarshal(run.Shape, &shape); err != nil {
		t.Fatal(err)
	}
	if shape["k"]["string"] != 8 || shape["n"]["number"] != 7 || shape["n"]["null"] != 1 || len(shape) != 2 {
		t.Fatalf("shape %v", shape)
	}
	if !jsonEqual(t, run.SourceMeta, `{"CDID":"Z"}`) {
		t.Fatalf("meta %s", run.SourceMeta)
	}
	if !e.lockFree(t) {
		t.Fatal("lock not released")
	}
}

func TestRowErrorsAndRatio(t *testing.T) {
	cases := []struct {
		name       string
		ratio      float64
		good, badN int
		status     string
	}{
		{"zero ratio, no errors", 0, 5, 0, "succeeded"},
		{"zero ratio, one error aborts", 0, 5, 1, "aborted"},
		{"equal is ok", 0.1, 9, 1, "succeeded"},
		{"above aborts", 0.1, 8, 2, "aborted"},
		{"one percent equal", 0.01, 99, 1, "succeeded"},
		{"all bad, ratio 1", 1, 0, 3, "succeeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.addSource(t, testCfg(tc.ratio))
			var items []source.Item
			for i := 0; i < tc.good; i++ {
				items = append(items, rec(int64(i+1), fmt.Sprint(i), map[string]any{"k": fmt.Sprint(i)}))
			}
			for i := 0; i < tc.badN; i++ {
				items = append(items, bad(int64(100+i)))
			}
			res, err := importer.Run(context.Background(), e.deps(fakeAdapter{stream: &scripted{items: items}}), "s1", importer.Options{BatchSize: 4})
			if res.Status != tc.status {
				t.Fatalf("status %q want %q (err %v)", res.Status, tc.status, err)
			}
			if tc.status == "aborted" {
				if !errors.Is(err, importer.ErrAborted) || res.Error == "" {
					t.Fatalf("err %v res.Error %q", err, res.Error)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			run, _ := e.st.GetRun(context.Background(), res.RunID)
			if run.Status != tc.status || run.Failed != tc.badN || run.Inserted != tc.good {
				t.Fatalf("run %+v", run)
			}
			if tc.status == "aborted" && (run.Error == nil || !strings.Contains(*run.Error, "max_error_ratio")) {
				t.Fatalf("run error %v", run.Error)
			}
			errs, _ := e.st.ListErrors(context.Background(), res.RunID)
			if len(errs) != tc.badN {
				t.Fatalf("errors %d want %d", len(errs), tc.badN)
			}
			if e.countRaw(t) != tc.good { // committed chunks remain even when aborted
				t.Fatalf("raw %d", e.countRaw(t))
			}
		})
	}
}

func TestNULRecordIsRowErrorNotBatchFailure(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, testCfg(1))
	items := []source.Item{
		rec(1, "a", map[string]any{"k": "a"}),
		rec(2, "b", map[string]any{"k": "b", "v": "x\x00y"}),
		rec(3, "c", map[string]any{"k": "c", "n": map[string]any{"deep": []any{"ok", "z\x00"}}}),
		rec(4, "d", map[string]any{"k": "d", "_meta": map[string]string{"m": "\x00"}}),
		rec(5, "e", map[string]any{"k": "e"}),
	}
	res, err := importer.Run(context.Background(), e.deps(fakeAdapter{stream: &scripted{items: items}}), "s1", importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 2 || res.Failed != 3 || res.Status != "succeeded" {
		t.Fatalf("%+v", res)
	}
	errs, _ := e.st.ListErrors(context.Background(), res.RunID)
	if len(errs) != 3 || !strings.Contains(errs[0].Reason, "NUL") || !strings.Contains(errs[1].Reason, "$.n.deep[1]") {
		t.Fatalf("errors %+v", errs)
	}
}

func TestStreamFailure(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, testCfg(1))
	boom := errors.New("connection reset")
	items := []source.Item{rec(1, "a", map[string]any{"k": "a"}), rec(2, "b", map[string]any{"k": "b"})}
	res, err := importer.Run(context.Background(), e.deps(fakeAdapter{stream: &scripted{items: items, err: boom}}), "s1", importer.Options{})
	if !errors.Is(err, boom) || res.Status != "failed" {
		t.Fatalf("%v %+v", err, res)
	}
	run, _ := e.st.GetRun(context.Background(), res.RunID)
	if run.Status != "failed" || run.FinishedAt == nil || run.Error == nil || !strings.Contains(*run.Error, "connection reset") {
		t.Fatalf("run %+v", run)
	}
	if !e.lockFree(t) {
		t.Fatal("lock not released")
	}

	// Open failure.
	res, err = importer.Run(context.Background(), e.deps(fakeAdapter{openErr: boom}), "s1", importer.Options{})
	if !errors.Is(err, boom) || res.Status != "failed" {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestCancellation(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, testCfg(1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var items []source.Item
	for i := 0; i < 50; i++ {
		items = append(items, rec(int64(i+1), fmt.Sprint(i), map[string]any{"k": fmt.Sprint(i)}))
	}
	st := &scripted{items: items, onNext: func(i int) {
		if i == 10 {
			cancel()
		}
	}}
	start := time.Now()
	res, err := importer.Run(ctx, e.deps(fakeAdapter{stream: st}), "s1", importer.Options{BatchSize: 4})
	if !errors.Is(err, context.Canceled) || res.Status != "failed" {
		t.Fatalf("%v %+v", err, res)
	}
	if time.Since(start) > 5*time.Second || st.i > 12 {
		t.Fatalf("did not stop promptly: read %d items", st.i)
	}
	run, _ := e.st.GetRun(context.Background(), res.RunID)
	if run.Status != "failed" || run.FinishedAt == nil || run.Error == nil || !strings.Contains(*run.Error, "cancel") {
		t.Fatalf("run %+v", run)
	}
	if !e.lockFree(t) {
		t.Fatal("lock not released")
	}
}

func TestAlreadyRunning(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, testCfg(1))
	unlock, ok, err := e.st.TryAdvisoryLock(context.Background(), "s1")
	if err != nil || !ok {
		t.Fatalf("lock %v %v", ok, err)
	}
	a := fakeAdapter{stream: &scripted{}}
	_, err = importer.Run(context.Background(), e.deps(a), "s1", importer.Options{})
	if !errors.Is(err, importer.ErrAlreadyRunning) {
		t.Fatalf("err %v", err)
	}
	if e.countRuns(t) != 0 {
		t.Fatal("run row created while locked")
	}
	unlock()
	if _, err = importer.Run(context.Background(), e.deps(a), "s1", importer.Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, testCfg(1))
	items := []source.Item{rec(1, "a", map[string]any{"k": "a"}), bad(2)}
	res, err := importer.Run(context.Background(), e.deps(fakeAdapter{stream: &scripted{items: items}}), "s1", importer.Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.RunID != 0 || res.Total != 2 || res.Failed != 1 || res.Status != "succeeded" {
		t.Fatalf("%+v", res)
	}
	var errN int
	_ = e.pool.QueryRow(context.Background(), `SELECT count(*) FROM import_errors`).Scan(&errN)
	if e.countRuns(t) != 0 || e.countRaw(t) != 0 || errN != 0 {
		t.Fatal("dry run wrote rows")
	}
}

// failingStore reports a lock error; nothing else may be called.
type failingStore struct{ importer.Store }

var errLock = errors.New("database is down")

func (failingStore) TryAdvisoryLock(context.Context, string) (func(), bool, error) {
	return func() {}, false, errLock
}

func TestLockErrorIsNotAlreadyRunning(t *testing.T) {
	_, err := importer.Run(context.Background(), importer.Deps{Store: failingStore{}}, "s1", importer.Options{})
	if !errors.Is(err, errLock) || errors.Is(err, importer.ErrAlreadyRunning) {
		t.Fatalf("err %v", err)
	}
}

func TestONSRuns(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "ons", "l55o.csv"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSource(filepath.Join("..", "..", "docs", "examples", "ons-l55o.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Location = fixture
	e.addSource(t, cfg)
	deps := e.deps(source.NewCSV(source.FetchOptions{}))

	r1, err := importer.Run(ctx, deps, cfg.ID, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r1.Inserted != 642 || r1.Updated != 0 || r1.Unchanged != 0 || r1.Failed != 0 || r1.Status != "succeeded" {
		t.Fatalf("run 1: %+v", r1)
	}
	run1, _ := e.st.GetRun(ctx, r1.RunID)
	var meta map[string]string
	if err := json.Unmarshal(run1.SourceMeta, &meta); err != nil || meta["CDID"] != "L55O" {
		t.Fatalf("meta %s %v", run1.SourceMeta, err)
	}
	if !jsonEqual(t, run1.Shape, `{"period":{"string":642},"value":{"string":642}}`) {
		t.Fatalf("shape %s", run1.Shape)
	}

	r2, err := importer.Run(ctx, deps, cfg.ID, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Inserted != 0 || r2.Updated != 0 || r2.Unchanged != 642 || r2.Failed != 0 {
		t.Fatalf("run 2: %+v", r2)
	}

	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "\"1989\",\"5.7\"", "\"1989\",\"5.8\"", 1)
	if edited == string(data) {
		t.Fatal("fixture line not found")
	}
	tmp := filepath.Join(t.TempDir(), "l55o.csv")
	if err := os.WriteFile(tmp, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Location = tmp
	e.addSource(t, cfg)
	r3, err := importer.Run(ctx, deps, cfg.ID, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r3.Inserted != 0 || r3.Updated != 1 || r3.Unchanged != 641 || r3.Failed != 0 {
		t.Fatalf("run 3: %+v", r3)
	}
}

type panicAdapter struct{}

func (panicAdapter) Open(context.Context, *config.Source) (source.Stream, error) {
	panic("adapter exploded")
}

func TestAdapterPanicFinishesRunAndRepanics(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, testCfg(1))
	var got any
	func() {
		defer func() { got = recover() }()
		_, _ = importer.Run(context.Background(), e.deps(panicAdapter{}), "s1", importer.Options{})
	}()
	if got != "adapter exploded" {
		t.Fatalf("panic not propagated: %v", got)
	}
	var status string
	var errText *string
	var finished *time.Time
	if err := e.pool.QueryRow(context.Background(), `SELECT status, error, finished_at FROM import_runs`).Scan(&status, &errText, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errText == nil || !strings.Contains(*errText, "adapter exploded") || finished == nil {
		t.Fatalf("run %q %v %v", status, errText, finished)
	}
	if !e.lockFree(t) {
		t.Fatal("lock not released")
	}
}

func TestLocksAreIndependentAcrossSources(t *testing.T) {
	e := newEnv(t)
	e.addSource(t, testCfg(1))
	c2 := testCfg(1)
	c2.ID = "s2"
	e.addSource(t, c2)
	unlock, ok, err := e.st.TryAdvisoryLock(context.Background(), "s1")
	if err != nil || !ok {
		t.Fatalf("lock %v %v", ok, err)
	}
	defer unlock()
	a := fakeAdapter{stream: &scripted{items: []source.Item{rec(1, "a", map[string]any{"k": "a"})}}}
	res, err := importer.Run(context.Background(), e.deps(a), "s2", importer.Options{})
	if err != nil || res.Status != "succeeded" || res.Inserted != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if _, err := importer.Run(context.Background(), e.deps(a), "s1", importer.Options{}); !errors.Is(err, importer.ErrAlreadyRunning) {
		t.Fatalf("s1 err %v", err)
	}
}

func TestConfigJSONRoundTripONS(t *testing.T) {
	cfg, err := config.LoadSource(filepath.Join("..", "..", "docs", "examples", "ons-l55o.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := importer.ConfigJSON(cfg)
	if err != nil {
		t.Fatal(err)
	}
	back, err := importer.ParseConfigJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, back) {
		t.Fatalf("round trip differs:\n%+v\n%+v", cfg, back)
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				l := strings.ToLower(k)
				for _, bad := range []string{"secret", "token", "password", "passwd", "apikey", "api_key", "auth", "credential", "env"} {
					if strings.Contains(l, bad) {
						t.Errorf("secret-bearing field %q in stored config", k)
					}
				}
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(generic)
}

func TestONSRerunLeavesPayloadAndImportedAtUntouched(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	fixture, err := filepath.Abs(filepath.Join("..", "..", "testdata", "ons", "l55o.csv"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSource(filepath.Join("..", "..", "docs", "examples", "ons-l55o.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Location = fixture
	e.addSource(t, cfg)
	deps := e.deps(source.NewCSV(source.FetchOptions{}))
	r1, err := importer.Run(ctx, deps, cfg.ID, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := func() map[string]string {
		rows, err := e.pool.Query(ctx, `SELECT natural_key, payload::text || '|' || imported_at::text FROM raw_records`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		m := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			m[k] = v
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return m
	}
	before := snapshot()
	r2, err := importer.Run(ctx, deps, cfg.ID, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if r2.RunID == r1.RunID || r2.Unchanged != 642 {
		t.Fatalf("run 2: %+v", r2)
	}
	after := snapshot()
	if len(before) != 642 || !reflect.DeepEqual(before, after) {
		t.Fatalf("payload/imported_at changed (%d vs %d rows)", len(before), len(after))
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM raw_records WHERE last_run_id = $1`, r2.RunID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 642 {
		t.Fatalf("last_run_id = run 2 for %d rows, want 642", n)
	}
}

// jsonEqual compares JSON semantically (jsonb normalises whitespace and key order).
func jsonEqual(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(a, b)
}
