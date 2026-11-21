package importer_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/record"
	"github.com/willis-dev-md/data-importer/internal/source"
)

// shiftedBoE writes a copy of the BoE fixture with one column inserted at C
// (before the rate column) into t.TempDir() and returns its path. testdata/ is
// never modified.
func shiftedBoE(t *testing.T) string {
	t.Helper()
	f, err := excelize.OpenFile(filepath.Join("..", "..", "testdata", "boe", "baserate-trimmed.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.InsertCols("HISTORICAL SINCE 1694", "C", 1); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "boe-col.xlsx")
	if err := f.SaveAs(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestBoEColumnShiftReproduction: the config reads C as the month, which is now
// blank, so distinct records share (series, year, "", day). Cause of the old
// "inserted=773 failed=8" (aborted): the 81 collisions were overwritten (last
// wins) and counted once. Now every collision is a RowError.
func TestBoEColumnShiftReproduction(t *testing.T) {
	root := filepath.Join("..", "..")
	cfg, err := config.LoadSource(filepath.Join(root, "docs", "examples", "boe-baserate.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Location = shiftedBoE(t)
	e := newEnv(t)
	e.addSource(t, cfg)
	deps := importer.Deps{Store: e.st, Adapters: map[string]source.Adapter{config.KindXLSX: source.NewXLSX(source.FetchOptions{})}}
	ctx := context.Background()
	res, err := importer.Run(ctx, deps, cfg.ID, importer.Options{})
	if !errors.Is(err, importer.ErrAborted) || res.Status != importer.StatusAborted {
		t.Fatalf("want aborted, got %+v %v", res, err)
	}
	errs, err := e.st.ListErrors(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	dup, other := 0, 0
	for _, x := range errs {
		if strings.HasPrefix(x.Reason, "duplicate natural key (first seen at ") {
			dup++
		} else {
			other++
		}
	}
	var raw int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM raw_records WHERE source_id=$1`, cfg.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	// 862 rows read = 773 written + 81 duplicates + 8 other (the "no changes" rows).
	if res.Total != 862 || res.Inserted != 773 || raw != 773 || dup != 81 || other != 8 || res.Failed != 89 {
		t.Fatalf("res=%+v raw=%d dup=%d other=%d", res, raw, dup, other)
	}
	if res.Inserted+res.Updated+res.Unchanged+dup+other != res.Total {
		t.Fatalf("rows lost: %+v dup=%d other=%d", res, dup, other)
	}
	if res.RunCounts.Failed != dup+other {
		t.Fatalf("failed %d != errors %d", res.Failed, dup+other)
	}
	run, err := e.st.GetRun(ctx, res.RunID)
	if err != nil || run.Failed != 89 || run.Status != importer.StatusAborted {
		t.Fatalf("run row %+v %v", run, err)
	}
}

// keyedStream yields n records with key = key(i) and a payload carrying i; it
// never holds the records in memory.
type keyedStream struct {
	n, i int
	key  func(i int) string
	last func() // called just before the last record is returned
}

func (s *keyedStream) Next() (source.Item, bool) {
	if s.i >= s.n {
		return source.Item{}, false
	}
	i := s.i
	s.i++
	if s.last != nil && s.i == s.n {
		s.last()
	}
	return rec(int64(i+1), s.key(i), map[string]any{"i": int64(i)}), true
}
func (s *keyedStream) Err() error              { return nil }
func (s *keyedStream) Meta() map[string]string { return nil }
func (s *keyedStream) Close() error            { return nil }

type streamAdapter struct{ mk func() source.Stream }

func (a streamAdapter) Open(context.Context, *config.Source) (source.Stream, error) {
	return a.mk(), nil
}

func scriptedAdapter(items ...source.Item) source.Adapter {
	return streamAdapter{mk: func() source.Stream { return &scripted{items: items} }}
}

func payloadI(t *testing.T, e env, sourceID, key string) string {
	t.Helper()
	r, err := e.st.GetRaw(context.Background(), sourceID, key)
	if err != nil {
		t.Fatalf("GetRaw %q: %v", key, err)
	}
	return fmt.Sprint(r.Payload["i"])
}

func TestDuplicateKeysInRun(t *testing.T) {
	ctx := context.Background()
	rows := func(keys ...string) []source.Item {
		out := make([]source.Item, len(keys))
		for i, k := range keys {
			out[i] = rec(int64(10+i), k, map[string]any{"i": int64(i)})
		}
		return out
	}
	tests := []struct {
		name      string
		keys      []string
		batch     int
		wantRaw   int
		wantDup   []string // "pos:first seen at X" per duplicate, in position order
		wantFirst map[string]string
	}{
		{"same batch", []string{"a", "b", "a"}, 100, 2, []string{"12:10"}, map[string]string{"a": "0"}},
		{"three times same batch", []string{"a", "a", "a"}, 100, 1, []string{"11:10", "12:10"}, map[string]string{"a": "0"}},
		{"next batch", []string{"a", "b", "a", "c"}, 2, 3, []string{"12:unknown"}, map[string]string{"a": "0"}},
		{"three times across batches", []string{"a", "b", "a", "c", "a", "d"}, 2, 4, []string{"12:unknown", "14:unknown"}, map[string]string{"a": "0"}},
		{"in-batch repeat of key also in earlier batch", []string{"a", "b", "a", "a"}, 3, 2, nil, nil}, // filled below
		{"case, whitespace and unicode forms are different keys",
			[]string{"a", "A", "a ", " a", "\u00e9", "e\u0301", "a\t"}, 3, 7, nil, map[string]string{"a": "0", "A": "1", "a ": "2", " a": "3", "\u00e9": "4", "e\u0301": "5"}},
	}
	tests[4].wantDup = []string{"12:10", "13:unknown"}
	tests[4].wantFirst = map[string]string{"a": "0"}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			cfg := testCfg(1)
			e.addSource(t, cfg)
			res, err := importer.Run(ctx, e.deps(scriptedAdapter(rows(tc.keys...)...)), cfg.ID, importer.Options{BatchSize: tc.batch})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if got := e.countRaw(t); got != tc.wantRaw {
				t.Errorf("raw rows = %d, want %d", got, tc.wantRaw)
			}
			errs, _ := e.st.ListErrors(ctx, res.RunID)
			var got []string
			for _, x := range errs {
				want := "duplicate natural key (first seen at "
				if !strings.HasPrefix(x.Reason, want) || !strings.HasSuffix(x.Reason, ")") {
					t.Fatalf("reason %q", x.Reason)
				}
				got = append(got, x.Position+":"+strings.TrimSuffix(strings.TrimPrefix(x.Reason, want), ")"))
			}
			if !reflect.DeepEqual(got, tc.wantDup) {
				t.Errorf("duplicates = %v, want %v", got, tc.wantDup)
			}
			if res.Failed != len(tc.wantDup) || res.Total != len(tc.keys) || res.Inserted+res.Failed != res.Total {
				t.Errorf("counts %+v", res)
			}
			for k, v := range tc.wantFirst {
				if g := payloadI(t, e, cfg.ID, record.Key(k)); g != v {
					t.Errorf("key %q holds record %s, want the first (%s)", k, g, v)
				}
			}
		})
	}
}

func TestDuplicateTakesPartInErrorRatio(t *testing.T) {
	ctx := context.Background()
	items := []source.Item{
		rec(1, "a", map[string]any{"i": 1}), rec(2, "a", map[string]any{"i": 2}),
		rec(3, "b", map[string]any{"i": 3}), rec(4, "c", map[string]any{"i": 4}),
	}
	for _, tc := range []struct {
		ratio  float64
		status string
	}{{0.25, importer.StatusSucceeded}, {0.24, importer.StatusAborted}, {0, importer.StatusAborted}} {
		e := newEnv(t)
		cfg := testCfg(tc.ratio)
		e.addSource(t, cfg)
		res, _ := importer.Run(ctx, e.deps(scriptedAdapter(items...)), cfg.ID, importer.Options{})
		if res.Status != tc.status || res.Failed != 1 || res.Inserted != 3 {
			t.Errorf("ratio %v: %+v", tc.ratio, res)
		}
	}
}

func TestDuplicateRerunsAreNotConfused(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	cfg := testCfg(0)
	e.addSource(t, cfg)
	items := []source.Item{rec(1, "a", map[string]any{"i": 1}), rec(2, "b", map[string]any{"i": 2}), rec(3, "c", map[string]any{"i": 3})}
	deps := e.deps(scriptedAdapter(items...))
	for i, want := range []struct{ ins, upd, unch int }{{3, 0, 0}, {0, 0, 3}} {
		res, err := importer.Run(ctx, deps, cfg.ID, importer.Options{BatchSize: 1})
		if err != nil || res.Inserted != want.ins || res.Updated != want.upd || res.Unchanged != want.unch || res.Failed != 0 {
			t.Fatalf("run %d: %+v %v", i+1, res, err)
		}
	}
	// A changed record in a later run is an update, not a duplicate.
	items[1] = rec(2, "b", map[string]any{"i": 22})
	res, err := importer.Run(ctx, deps, cfg.ID, importer.Options{BatchSize: 2})
	if err != nil || res.Updated != 1 || res.Unchanged != 2 || res.Failed != 0 {
		t.Fatalf("run 3: %+v %v", res, err)
	}
	// A tombstoned row from an earlier run that reappears is revived, not a duplicate.
	if _, err := e.pool.Exec(ctx, `UPDATE raw_records SET deleted_at = now() WHERE source_id = 's1' AND natural_key = $1`, record.Key("c")); err != nil {
		t.Fatal(err)
	}
	res, err = importer.Run(ctx, deps, cfg.ID, importer.Options{})
	if err != nil || res.Failed != 0 || res.Unchanged != 3 {
		t.Fatalf("run 4: %+v %v", res, err)
	}
	if r, _ := e.st.GetRaw(ctx, "s1", record.Key("c")); r.DeletedAt != nil {
		t.Error("tombstone not cleared")
	}
	// An aborted run that wrote some rows does not make the next run see duplicates.
	bad := append([]source.Item{}, items...)
	bad = append(bad, rec(4, "a", map[string]any{"i": 9}))
	if res, _ := importer.Run(ctx, e.deps(scriptedAdapter(bad...)), cfg.ID, importer.Options{}); res.Status != importer.StatusAborted || res.Failed != 1 {
		t.Fatalf("run 5: %+v", res)
	}
	res, err = importer.Run(ctx, deps, cfg.ID, importer.Options{})
	if err != nil || res.Failed != 0 || res.Unchanged != 3 {
		t.Fatalf("run 6: %+v %v", res, err)
	}
}

func TestDuplicateKeysWithNULAreRowErrors(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	cfg := testCfg(1)
	e.addSource(t, cfg)
	raw := func(pos int64, key string) source.Item {
		return source.Item{Record: &source.Record{Position: pos, NaturalKey: key, Fields: map[string]any{"i": pos}}}
	}
	items := []source.Item{raw(1, "a\x00b"), raw(2, "ok"), raw(3, "x\xffy")}
	res, err := importer.Run(ctx, e.deps(scriptedAdapter(items...)), cfg.ID, importer.Options{})
	if err != nil || res.Inserted != 1 || res.Failed != 2 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestDuplicatesDifferentSourcesConcurrently(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	ids := []string{"c1", "c2", "c3", "c4"}
	for _, id := range ids {
		cfg := testCfg(0)
		cfg.ID = id
		e.addSource(t, cfg)
	}
	var wg sync.WaitGroup
	results := make([]importer.RunResult, len(ids))
	errs := make([]error, len(ids))
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ad := streamAdapter{mk: func() source.Stream {
				return &keyedStream{n: 3000, key: func(i int) string { return fmt.Sprint("k", i) }}
			}}
			results[i], errs[i] = importer.Run(ctx, e.deps(ad), id, importer.Options{BatchSize: 100})
		}()
	}
	wg.Wait()
	for i := range ids {
		if errs[i] != nil || results[i].Inserted != 3000 || results[i].Failed != 0 {
			t.Errorf("%s: %+v %v", ids[i], results[i], errs[i])
		}
	}
}

// 200k records, every key twice (the second copy always lands in a later batch):
// the first copy wins and nothing but one batch is held by the importer.
func TestDuplicateKeysLargeRun(t *testing.T) {
	if testing.Short() {
		t.Skip("large run")
	}
	ctx := context.Background()
	e := newEnv(t)
	cfg := testCfg(1)
	e.addSource(t, cfg)
	const n = 200_000
	ad := streamAdapter{mk: func() source.Stream {
		return &keyedStream{n: n, key: func(i int) string { return fmt.Sprint("k", i%(n/2)) }}
	}}
	res, err := importer.Run(ctx, e.deps(ad), cfg.ID, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != n || res.Inserted != n/2 || res.Failed != n/2 {
		t.Fatalf("%+v", res)
	}
	var nerr int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM import_errors WHERE run_id = $1 AND reason = 'duplicate natural key (first seen at unknown)'`, res.RunID).Scan(&nerr); err != nil || nerr != n/2 {
		t.Fatalf("errors %d %v", nerr, err)
	}
	if got := payloadI(t, e, cfg.ID, record.Key("k7")); got != "7" {
		t.Errorf("k7 holds record %s, want the first (7)", got)
	}
}

func dryRunBoE(t *testing.T, location string) importer.RunResult {
	t.Helper()
	cfg, err := config.LoadSource(filepath.Join("..", "..", "docs", "examples", "boe-baserate.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Location = location
	e := newEnv(t)
	e.addSource(t, cfg)
	deps := importer.Deps{Store: e.st, Adapters: map[string]source.Adapter{config.KindXLSX: source.NewXLSX(source.FetchOptions{})}}
	res, err := importer.Run(context.Background(), deps, cfg.ID, importer.Options{DryRun: true})
	if res.Status != importer.StatusSucceeded && res.Status != importer.StatusAborted {
		t.Fatalf("dry run: %+v %v", res, err)
	}
	if e.countRaw(t) != 0 || e.countRuns(t) != 0 {
		t.Fatal("dry run wrote something")
	}
	return res
}

func TestDryRunDetectsDuplicates(t *testing.T) {
	good := dryRunBoE(t, filepath.Join("..", "..", "testdata", "boe", "baserate-trimmed.xlsx"))
	if good.Duplicates != 0 || good.Failed != 0 || good.Total != 854 || len(good.DuplicateSamples) != 0 || good.Status != importer.StatusSucceeded {
		t.Fatalf("clean dry run: %+v", good)
	}
	shifted := dryRunBoE(t, shiftedBoE(t))
	if shifted.Duplicates != 81 || shifted.Failed != 89 || shifted.Total != 862 || shifted.Status != importer.StatusAborted {
		t.Fatalf("shifted dry run: %+v", shifted)
	}
	if len(shifted.DuplicateSamples) != importer.MaxDuplicateSamples {
		t.Fatalf("samples: %d", len(shifted.DuplicateSamples))
	}
	for _, s := range shifted.DuplicateSamples {
		if !strings.HasPrefix(s.Reason, "duplicate natural key (first seen at ") || strings.Contains(s.Reason, "unknown") {
			t.Errorf("sample %+v", s)
		}
	}
}

func TestDryRunDuplicateRules(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	cfg := testCfg(1)
	e.addSource(t, cfg)
	rows := []source.Item{rec(1, "a", nil), rec(2, "A", nil), rec(3, "a ", nil), rec(4, "\u00e9", nil), rec(5, "e\u0301", nil), rec(6, "a", nil), rec(7, "a", nil)}
	res, err := importer.Run(ctx, e.deps(scriptedAdapter(rows...)), cfg.ID, importer.Options{DryRun: true, BatchSize: 2})
	if err != nil || res.Duplicates != 2 || res.Failed != 2 || res.Total != 7 {
		t.Fatalf("%+v %v", res, err)
	}
	if res.DuplicateSamples[0].Reason != "duplicate natural key (first seen at 1)" || res.DuplicateSamples[1].Position != "7" {
		t.Errorf("samples %+v", res.DuplicateSamples)
	}
	// Real run agrees on the count.
	real, err := importer.Run(ctx, e.deps(scriptedAdapter(rows...)), cfg.ID, importer.Options{BatchSize: 2})
	if err != nil || real.Duplicates != 2 || real.Failed != 2 {
		t.Fatalf("%+v %v", real, err)
	}
}

func TestDryRunKeyLimit(t *testing.T) {
	old := importer.MaxDryRunKeys
	importer.MaxDryRunKeys = 100
	t.Cleanup(func() { importer.MaxDryRunKeys = old })
	e := newEnv(t)
	cfg := testCfg(1)
	e.addSource(t, cfg)
	ad := streamAdapter{mk: func() source.Stream {
		return &keyedStream{n: 101, key: func(i int) string { return fmt.Sprint("k", i) }}
	}}
	res, err := importer.Run(context.Background(), e.deps(ad), cfg.ID, importer.Options{DryRun: true})
	if err == nil || res.Status != importer.StatusFailed || !strings.Contains(res.Error, "distinct natural keys") {
		t.Fatalf("%+v %v", res, err)
	}
	// Exactly at the limit is fine, and repeated keys do not count against it.
	ad = streamAdapter{mk: func() source.Stream {
		return &keyedStream{n: 300, key: func(i int) string { return fmt.Sprint("k", i%100) }}
	}}
	res, err = importer.Run(context.Background(), e.deps(ad), cfg.ID, importer.Options{DryRun: true})
	if err != nil && !errors.Is(err, importer.ErrAborted) || res.Duplicates != 200 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A 200k-record dry run keeps one small entry per key: well under 64 MB.
func TestDryRunMemory(t *testing.T) {
	e := newEnv(t)
	cfg := testCfg(1)
	e.addSource(t, cfg)
	const n = 200_000
	var before, during runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	ad := streamAdapter{mk: func() source.Stream {
		return &keyedStream{n: n, key: func(i int) string { return fmt.Sprint("key-", i%(n-1000)) }, last: func() {
			runtime.GC()
			runtime.ReadMemStats(&during)
		}}
	}}
	res, err := importer.Run(context.Background(), e.deps(ad), cfg.ID, importer.Options{DryRun: true})
	if err != nil || res.Duplicates != 1000 || res.Total != n {
		t.Fatalf("%+v %v", res, err)
	}
	if grew := int64(during.HeapAlloc) - int64(before.HeapAlloc); grew > 64<<20 {
		t.Fatalf("heap grew by %d MB", grew>>20)
	} else {
		t.Logf("heap growth with %d keys held: %d KB", n-1000, grew>>10)
	}
}
