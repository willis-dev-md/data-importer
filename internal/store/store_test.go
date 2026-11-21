package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/record"
	"github.com/willis-dev-md/data-importer/internal/store"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

func newStore(t *testing.T) (*store.Store, *pgxpool.Pool) {
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
	return store.New(pool), pool
}

func setup(t *testing.T) (*store.Store, *pgxpool.Pool) {
	t.Helper()
	s, pool := newStore(t)
	if err := s.UpsertSource(context.Background(), store.Source{ID: "src", Kind: "csv", Config: []byte(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	return s, pool
}

func rec(key string, v any) record.Record {
	return record.Record{Key: key, Payload: map[string]any{"v": v}}
}

func TestSourceRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	if _, err := s.GetSource(ctx, "nope"); err == nil || !errorsIs(err, store.ErrNotFound) {
		t.Fatalf("GetSource missing = %v, want ErrNotFound", err)
	}
	if err := s.UpsertSource(ctx, store.Source{ID: "a", Kind: "csv", Config: []byte(`{"x": 1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSource(ctx, store.Source{ID: "a", Kind: "xlsx", Config: []byte(`{"y": 2}`)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSource(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "xlsx" || string(got.Config) != `{"y": 2}` || got.ActiveMappingVersion != nil || got.CreatedAt.IsZero() {
		t.Errorf("got %+v", got)
	}
	if err := s.UpsertSource(ctx, store.Source{ID: "b", Kind: "bogus"}); err == nil {
		t.Error("invalid kind accepted")
	}
}

func TestRunLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)
	id, err := s.CreateRun(ctx, "src", "schedule")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.GetRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "running" || r.Trigger != "schedule" || r.FinishedAt != nil || r.SourceID != "src" || r.Error != nil {
		t.Errorf("new run = %+v", r)
	}
	counts := store.RunCounts{Counts: store.Counts{Inserted: 3, Updated: 2, Unchanged: 1}, Failed: 4, Tombstoned: 5}
	if err := s.FinishRun(ctx, id, "failed", counts, []byte(`{"f":{"string":3}}`), []byte(`{"title":"T"}`), "boom"); err != nil {
		t.Fatal(err)
	}
	r, err = s.GetRun(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "failed" || r.FinishedAt == nil || r.RunCounts != counts || r.Error == nil || *r.Error != "boom" ||
		string(r.Shape) != `{"f": {"string": 3}}` || string(r.SourceMeta) != `{"title": "T"}` {
		t.Errorf("finished run = %+v", r)
	}
	if err := s.FinishRun(ctx, id, "succeeded", store.RunCounts{}, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if r, _ = s.GetRun(ctx, id); r.Error != nil || r.Shape != nil || r.SourceMeta != nil {
		t.Errorf("nil args not stored as NULL: %+v", r)
	}
	if err := s.FinishRun(ctx, id, "bogus", store.RunCounts{}, nil, nil, ""); err == nil {
		t.Error("invalid status accepted")
	}
	if err := s.FinishRun(ctx, 999999, "succeeded", store.RunCounts{}, nil, nil, ""); !errorsIs(err, store.ErrNotFound) {
		t.Errorf("FinishRun missing = %v", err)
	}
	if _, err := s.GetRun(ctx, 999999); !errorsIs(err, store.ErrNotFound) {
		t.Errorf("GetRun missing = %v", err)
	}
	if _, err := s.CreateRun(ctx, "unknown-source", "manual"); err == nil {
		t.Error("run for unknown source accepted")
	}
}

func TestUpsertRawCountsAcrossRuns(t *testing.T) {
	ctx := context.Background()
	s, pool := setup(t)
	run1, _ := s.CreateRun(ctx, "src", "manual")
	c, err := s.UpsertRaw(ctx, run1, "src", []record.Record{rec("a", 1), rec("b", "x"), rec("c", []any{1, 2})})
	if err != nil {
		t.Fatal(err)
	}
	if c != (store.Counts{Inserted: 3}) {
		t.Fatalf("run1 counts = %+v", c)
	}
	before, err := s.GetRaw(ctx, "src", "a")
	if err != nil {
		t.Fatal(err)
	}
	if before.FirstRunID != run1 || before.LastRunID != run1 || before.DeletedAt != nil {
		t.Errorf("after insert: %+v", before)
	}

	run2, _ := s.CreateRun(ctx, "src", "manual")
	c, err = s.UpsertRaw(ctx, run2, "src", []record.Record{
		rec("a", 1),           // unchanged
		rec("b", "y"),         // updated
		rec("c", []any{1, 2}), // unchanged
		rec("d", true),        // inserted
	})
	if err != nil {
		t.Fatal(err)
	}
	if c != (store.Counts{Inserted: 1, Updated: 1, Unchanged: 2}) {
		t.Fatalf("run2 counts = %+v", c)
	}

	after, _ := s.GetRaw(ctx, "src", "a")
	if after.LastRunID != run2 || after.FirstRunID != run1 || !after.ImportedAt.Equal(before.ImportedAt) {
		t.Errorf("unchanged row: first=%d last=%d imported %v -> %v", after.FirstRunID, after.LastRunID, before.ImportedAt, after.ImportedAt)
	}
	var payloadOK bool
	if err := pool.QueryRow(ctx, `SELECT payload = '{"v":1}'::jsonb FROM raw_records WHERE natural_key='a'`).Scan(&payloadOK); err != nil || !payloadOK {
		t.Errorf("payload of unchanged row wrong: %v %v", payloadOK, err)
	}
	b, _ := s.GetRaw(ctx, "src", "b")
	if b.FirstRunID != run1 || b.LastRunID != run2 || b.Payload["v"] != "y" || !b.ImportedAt.After(before.ImportedAt) {
		t.Errorf("updated row: %+v", b)
	}
	d, _ := s.GetRaw(ctx, "src", "d")
	if d.FirstRunID != run2 || d.LastRunID != run2 {
		t.Errorf("inserted row: %+v", d)
	}

	// Third run, identical: everything unchanged.
	run3, _ := s.CreateRun(ctx, "src", "manual")
	c, err = s.UpsertRaw(ctx, run3, "src", []record.Record{rec("a", 1), rec("b", "y"), rec("c", []any{1, 2}), rec("d", true)})
	if err != nil {
		t.Fatal(err)
	}
	if c != (store.Counts{Unchanged: 4}) {
		t.Fatalf("run3 counts = %+v", c)
	}
}

func TestUpsertRawHashMatchesStored(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)
	run, _ := s.CreateRun(ctx, "src", "manual")
	p := map[string]any{"i": 3, "f": 2.5, "big": int64(9007199254740993), "s": "é<", "n": nil, "l": []any{map[string]any{"k": 1.0}}}
	if _, err := s.UpsertRaw(ctx, run, "src", []record.Record{{Key: "k", Payload: p}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRaw(ctx, "src", "k")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.ContentHash) != string(record.Hash(p)) {
		t.Error("stored hash differs from record.Hash")
	}
	if string(record.Hash(got.Payload)) != string(got.ContentHash) {
		t.Error("hash of payload read back differs from stored hash")
	}
	// Re-import of the same payload (with ints as floats) is unchanged.
	run2, _ := s.CreateRun(ctx, "src", "manual")
	p2 := map[string]any{"i": 3.0, "f": 2.5, "big": int64(9007199254740993), "s": "é<", "n": nil, "l": []any{map[string]any{"k": 1}}}
	c, err := s.UpsertRaw(ctx, run2, "src", []record.Record{{Key: "k", Payload: p2}})
	if err != nil || c != (store.Counts{Unchanged: 1}) {
		t.Fatalf("counts = %+v, %v", c, err)
	}
}

func TestUpsertRawEdgeCases(t *testing.T) {
	ctx := context.Background()
	s, pool := setup(t)
	run, _ := s.CreateRun(ctx, "src", "manual")

	if c, err := s.UpsertRaw(ctx, run, "src", nil); err != nil || c != (store.Counts{}) {
		t.Errorf("empty batch = %+v, %v", c, err)
	}
	// Duplicate keys in one batch: last wins, counted once.
	c, err := s.UpsertRaw(ctx, run, "src", []record.Record{rec("d", 1), rec("d", 2)})
	if err != nil || c != (store.Counts{Inserted: 1}) {
		t.Fatalf("dup batch = %+v, %v", c, err)
	}
	if got, _ := s.GetRaw(ctx, "src", "d"); fmt.Sprint(got.Payload["v"]) != "2" {
		t.Errorf("dup: payload = %v", got.Payload)
	}
	// Failures write nothing.
	for name, recs := range map[string][]record.Record{
		"empty key":   {rec("ok", 1), {Key: "", Payload: map[string]any{}}},
		"unencodable": {rec("ok2", 1), rec("nan", nanValue())},
	} {
		if _, err := s.UpsertRaw(ctx, run, "src", recs); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := s.GetRaw(ctx, "src", "ok"); !errorsIs(err, store.ErrNotFound) {
		t.Errorf("record from failed batch was written: %v", err)
	}
	// Unknown source violates the foreign key; the transaction rolls back.
	if _, err := s.UpsertRaw(ctx, run, "nosuch", []record.Record{rec("x", 1)}); err == nil {
		t.Error("unknown source accepted")
	}
	// Tombstoned rows reappear live.
	run2, _ := s.CreateRun(ctx, "src", "manual")
	if _, err := s.UpsertRaw(ctx, run2, "src", []record.Record{rec("t", 1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE raw_records SET deleted_at = now() WHERE natural_key = 't'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertRaw(ctx, run2, "src", []record.Record{rec("t", 1)}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetRaw(ctx, "src", "t"); got.DeletedAt != nil {
		t.Error("deleted_at not cleared")
	}
}

func TestUpsertRawLargeBatch(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)
	const n = 2000
	recs := make([]record.Record, n)
	for i := range recs {
		recs[i] = record.Record{Key: record.Key("K", fmt.Sprintf("%05d", i)), Payload: map[string]any{"i": i, "s": fmt.Sprint("row ", i)}}
	}
	run1, _ := s.CreateRun(ctx, "src", "manual")
	c, err := s.UpsertRaw(ctx, run1, "src", recs)
	if err != nil || c != (store.Counts{Inserted: n}) {
		t.Fatalf("first = %+v, %v", c, err)
	}
	for i := 0; i < n; i += 4 {
		recs[i].Payload["s"] = "changed"
	}
	run2, _ := s.CreateRun(ctx, "src", "manual")
	c, err = s.UpsertRaw(ctx, run2, "src", recs)
	if err != nil || c != (store.Counts{Updated: n / 4, Unchanged: n - n/4}) {
		t.Fatalf("second = %+v, %v", c, err)
	}
}

func TestListRawKeyset(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)
	if err := s.UpsertSource(ctx, store.Source{ID: "other", Kind: "csv"}); err != nil {
		t.Fatal(err)
	}
	const n = 2000
	recs := make([]record.Record, n)
	for i := range recs {
		recs[i] = rec(record.Key(fmt.Sprintf("%05d", (i*7919)%n)), i) // inserted out of order
	}
	run, _ := s.CreateRun(ctx, "src", "manual")
	if _, err := s.UpsertRaw(ctx, run, "src", recs); err != nil {
		t.Fatal(err)
	}
	orun, _ := s.CreateRun(ctx, "other", "manual")
	if _, err := s.UpsertRaw(ctx, orun, "other", []record.Record{rec("zzz", 1)}); err != nil {
		t.Fatal(err)
	}

	var all []string
	after := ""
	for pages := 0; ; pages++ {
		page, err := s.ListRaw(ctx, "src", after, 333)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		if len(page) > 333 || pages > n {
			t.Fatalf("page too large or runaway pagination: %d", len(page))
		}
		for _, r := range page {
			all = append(all, r.Key)
		}
		after = page[len(page)-1].Key
	}
	if len(all) != n {
		t.Fatalf("got %d rows, want %d", len(all), n)
	}
	for i, k := range all {
		if want := record.Key(fmt.Sprintf("%05d", i)); k != want {
			t.Fatalf("row %d = %s, want %s", i, k, want)
		}
	}
	if _, err := s.ListRaw(ctx, "src", "", 0); err == nil {
		t.Error("limit 0 accepted")
	}
	if got, err := s.ListRaw(ctx, "nosuch", "", 10); err != nil || len(got) != 0 {
		t.Errorf("unknown source = %v, %v", got, err)
	}
}

func TestRecordErrors(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)
	run, _ := s.CreateRun(ctx, "src", "manual")
	if err := s.RecordErrors(ctx, run, nil); err != nil {
		t.Fatal(err)
	}
	in := []store.RowError{
		{Position: "row 2", Raw: "a,b,\"c", Reason: "unterminated quote"},
		{Position: "row 5", Raw: "bad\x00\xff bytes", Reason: "invalid"},
	}
	if err := s.RecordErrors(ctx, run, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListErrors(ctx, run)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got[0] != in[0] || got[1].Raw != "bad� bytes" || got[1].Position != "row 5" {
		t.Errorf("got %+v", got)
	}
	if err := s.RecordErrors(ctx, 999999, in); err == nil {
		t.Error("error for unknown run accepted")
	}
}

func TestAdvisoryLockConcurrent(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)
	const workers = 4
	var (
		ready   sync.WaitGroup
		done    sync.WaitGroup
		release = make(chan struct{})
		mu      sync.Mutex
		oks     int
		unlocks []func()
	)
	ready.Add(workers)
	done.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer done.Done()
			unlock, ok, err := s.TryAdvisoryLock(ctx, "src")
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			if ok {
				oks++
				unlocks = append(unlocks, unlock)
			}
			mu.Unlock()
			ready.Done()
			<-release // whoever got the lock keeps it until all have tried
		}()
	}
	ready.Wait()
	mu.Lock()
	got := oks
	mu.Unlock()
	close(release)
	done.Wait()
	if got != 1 {
		t.Fatalf("%d goroutines got the lock, want exactly 1", got)
	}
	unlocks[0]()
	unlocks[0]() // idempotent
}

func TestAdvisoryLockReleaseAndScope(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)
	unlock, ok, err := s.TryAdvisoryLock(ctx, "src")
	if err != nil || !ok {
		t.Fatalf("first lock: ok=%v err=%v", ok, err)
	}
	held, ok2, err := s.TryAdvisoryLock(ctx, "src")
	if ok2 || err != nil || held == nil {
		t.Fatalf("second lock on same source: ok=%v err=%v unlock-nil=%v, want held (false, nil error)", ok2, err, held == nil)
	}
	held()
	other, okOther, err := s.TryAdvisoryLock(ctx, "other-source")
	if err != nil || !okOther {
		t.Fatal("lock on a different source blocked")
	}
	other()
	unlock()
	again, ok3, err := s.TryAdvisoryLock(ctx, "src")
	if err != nil || !ok3 {
		t.Fatal("lock not available after unlock")
	}
	again()
}

func TestAdvisoryLockReleasedOnConnectionLoss(t *testing.T) {
	ctx := context.Background()
	s, pool := setup(t)
	unlock, ok, err := s.TryAdvisoryLock(ctx, "src")
	if err != nil || !ok {
		t.Fatalf("lock not acquired: ok=%v err=%v", ok, err)
	}
	// Kill the backend holding the advisory lock (not our own connection).
	var killed int
	err = pool.QueryRow(ctx, `
SELECT count(*) FROM (
  SELECT pg_terminate_backend(l.pid) FROM pg_locks l
  WHERE l.locktype = 'advisory' AND l.granted AND l.pid <> pg_backend_pid()
) x`).Scan(&killed)
	if err != nil || killed != 1 {
		t.Fatalf("terminate: killed=%d err=%v", killed, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		l, ok, err := s.TryAdvisoryLock(ctx, "src")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			l()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock not released after connection loss")
		}
		time.Sleep(50 * time.Millisecond)
	}
	unlock() // must not panic or hang on the dead connection
}

func TestAdvisoryLockErrorIsNotHeld(t *testing.T) {
	ctx := context.Background()
	s, pool := setup(t)
	// Held elsewhere: no error.
	unlock, ok, err := s.TryAdvisoryLock(ctx, "src")
	if err != nil || !ok {
		t.Fatalf("lock: ok=%v err=%v", ok, err)
	}
	if held, ok, err := s.TryAdvisoryLock(ctx, "src"); ok || err != nil {
		t.Fatalf("held: ok=%v err=%v, want false, nil", ok, err)
	} else {
		held()
	}
	unlock()
	// Closed pool: an error, ok false, unlock still safe.
	pool.Close()
	noop, ok, err := s.TryAdvisoryLock(ctx, "src")
	if err == nil || ok {
		t.Fatalf("closed pool: ok=%v err=%v, want false and an error", ok, err)
	}
	if noop == nil {
		t.Fatal("unlock is nil on error")
	}
	noop()
	noop()
	// Cancelled context is also an error.
	s2, _ := setup(t)
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok, err := s2.TryAdvisoryLock(cctx, "src"); ok || err == nil {
		t.Fatalf("cancelled ctx: ok=%v err=%v", ok, err)
	}
}

func TestJSONBNumberRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, pool := setup(t)
	if _, err := pool.Exec(ctx, `CREATE TABLE numrt (id int PRIMARY KEY, p jsonb)`); err != nil {
		t.Fatal(err)
	}
	vals := map[string]any{
		"9.3e18": 9.3e18, "2^64f": 1.8446744073709552e19, "2^63f": 9.223372036854775808e18,
		"1e21": 1e21, "1e-7": 1e-7, "0.1": 0.1, "negzero": math.Copysign(0, -1),
		"denorm": 5e-324, "max": math.MaxFloat64, "negmax": -math.MaxFloat64, "tiny": 1.5e-300,
		"int": 42, "i64": int64(math.MinInt64), "u64": uint64(math.MaxUint64),
		"numone": json.Number("1.0"), "numexp": json.Number("1.50E+3"), "numbig": json.Number("123456789012345678901234567890.10"),
		"f1": 1.0, "f-": -2.5, "third": 1.0 / 3, "e": []any{9.3e18, map[string]any{"x": 1e21}},
	}
	run, err := s.CreateRun(ctx, "src", "manual")
	if err != nil {
		t.Fatal(err)
	}
	id := 0
	for name, v := range vals {
		id++
		p := map[string]any{"v": v}
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(name, err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO numrt VALUES ($1, $2)`, id, b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT p FROM numrt WHERE id=$1`, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var back map[string]any
		if err := dec.Decode(&back); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(record.Hash(p), record.Hash(back)) {
			c1, _ := record.Canonical(p)
			c2, _ := record.Canonical(back)
			t.Errorf("%s: hash differs after jsonb round trip: %s vs %s", name, c1, c2)
		}
		// Through the store: insert, read back, re-import the read-back payload.
		key := record.Key(name)
		if c, err := s.UpsertRaw(ctx, run, "src", []record.Record{{Key: key, Payload: p}}); err != nil || c.Inserted != 1 {
			t.Fatalf("%s: insert %+v %v", name, c, err)
		}
		got, err := s.GetRaw(ctx, "src", key)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(record.Hash(p), record.Hash(got.Payload)) {
			t.Errorf("%s: stored payload hash differs", name)
		}
		c, err := s.UpsertRaw(ctx, run, "src", []record.Record{{Key: key, Payload: got.Payload}})
		if err != nil || c.Unchanged != 1 || c.Updated != 0 {
			t.Errorf("%s: re-import of read-back payload = %+v, %v; want Unchanged", name, c, err)
		}
	}
}
