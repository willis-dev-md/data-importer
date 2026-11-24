package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"runtime"
	"strings"
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
	// pg_locks is cluster-wide and tests in other packages (and other tests in
	// this one) hold the same advisory key in their own databases. A second
	// database holding the same source's lock acts as a bystander that must
	// survive the termination below.
	bystander, _ := setup(t)
	bUnlock, bOK, err := bystander.TryAdvisoryLock(ctx, "src")
	if err != nil || !bOK {
		t.Fatalf("bystander lock not acquired: ok=%v err=%v", bOK, err)
	}
	defer bUnlock()
	// Kill only the backend holding this database's lock for "src" (not our own connection).
	var killed int
	err = pool.QueryRow(ctx, `
SELECT count(*) FROM (
  SELECT pg_terminate_backend(l.pid) FROM pg_locks l
  WHERE l.locktype = 'advisory' AND l.granted AND l.pid <> pg_backend_pid()
    AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND ((l.classid::bigint << 32) | l.objid::bigint) = hashtextextended('data-importer:source:src', 0)
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
	// The bystander database's session must not have been terminated.
	if l, ok, err := bystander.TryAdvisoryLock(ctx, "src"); err != nil || ok {
		if ok {
			l()
		}
		t.Fatalf("bystander lock lost: ok=%v err=%v, want still held", ok, err)
	}
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
	// Database unreachable: an error, ok false, unlock still safe, slot freed.
	badCfg := pool.Config().Copy()
	badCfg.ConnConfig.Port = 1
	badCfg.ConnConfig.ConnectTimeout = 2 * time.Second
	badPool, err := pgxpool.NewWithConfig(ctx, badCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer badPool.Close()
	bad := store.NewWithOptions(badPool, store.Options{LockConns: 1, LockWait: time.Second})
	for i := 0; i < 3; i++ { // a leaked slot would turn the 2nd attempt into ErrLockCapacity
		noop, ok, err := bad.TryAdvisoryLock(ctx, "src")
		if err == nil || ok || errors.Is(err, store.ErrLockCapacity) {
			t.Fatalf("unreachable db: ok=%v err=%v, want false and a connect error", ok, err)
		}
		if noop == nil {
			t.Fatal("unlock is nil on error")
		}
		noop()
		noop()
	}
	pool.Close()
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

func TestListRawPageDeleted(t *testing.T) {
	ctx := context.Background()
	s, pool := setup(t)
	run, _ := s.CreateRun(ctx, "src", "manual")
	if _, err := s.UpsertRaw(ctx, run, "src", []record.Record{rec("a", 1), rec("b", 2), rec("c", 3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE raw_records SET deleted_at = now() WHERE source_id = 'src' AND natural_key = $1`, "b"); err != nil {
		t.Fatal(err)
	}
	keys := func(includeDeleted bool, after string, limit int) []string {
		t.Helper()
		page, err := s.ListRawPage(ctx, "src", after, limit, includeDeleted)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range page {
			out = append(out, r.Key)
		}
		return out
	}
	tests := []struct {
		deleted bool
		after   string
		limit   int
		want    string
	}{
		{false, "", 10, "a,c"}, {true, "", 10, "a,b,c"}, {false, "a", 10, "c"}, {false, "", 1, "a"}, {true, "a", 1, "b"},
	}
	for _, tt := range tests {
		if got := fmt.Sprint(keys(tt.deleted, tt.after, tt.limit)); got != "["+replaceComma(tt.want)+"]" {
			t.Errorf("deleted=%v after=%q limit=%d: got %s, want %s", tt.deleted, tt.after, tt.limit, got, tt.want)
		}
	}
	if _, err := s.ListRawPage(ctx, "src", "", 0, true); err == nil {
		t.Error("limit 0 must fail")
	}
}

func replaceComma(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c == ',' {
			out[i] = ' '
		}
	}
	return string(out)
}

func TestUpsertRawUnique(t *testing.T) {
	ctx := context.Background()
	s, _ := setup(t)
	run1, _ := s.CreateRun(ctx, "src", "manual")

	// Same call: first occurrence wins, later ones are reported with the index of the first.
	c, dups, err := s.UpsertRawUnique(ctx, run1, "src", []record.Record{rec("a", 1), rec("b", 2), rec("a", 3), rec("a", 4), rec("B", 5)})
	if err != nil {
		t.Fatal(err)
	}
	if c != (store.Counts{Inserted: 3}) || !reflect.DeepEqual(dups, []store.Dup{{Index: 2, First: 0}, {Index: 3, First: 0}}) {
		t.Fatalf("call 1: %+v %+v", c, dups)
	}
	if got, _ := s.GetRaw(ctx, "src", "a"); fmt.Sprint(got.Payload["v"]) != "1" {
		t.Errorf("a = %v, want the first record", got.Payload)
	}

	// A later call of the same run: rows are left untouched, even if the content differs.
	before, _ := s.GetRaw(ctx, "src", "b")
	c, dups, err = s.UpsertRawUnique(ctx, run1, "src", []record.Record{rec("c", 1), rec("a", 9), rec("b", 2), rec("b", 7), rec("c", 2)})
	if err != nil {
		t.Fatal(err)
	}
	want := []store.Dup{{Index: 1, First: -1}, {Index: 2, First: -1}, {Index: 3, First: -1}, {Index: 4, First: 0}}
	if c != (store.Counts{Inserted: 1}) || !reflect.DeepEqual(dups, want) {
		t.Fatalf("call 2: %+v %+v", c, dups)
	}
	after, _ := s.GetRaw(ctx, "src", "b")
	if !reflect.DeepEqual(before, after) {
		t.Errorf("row touched by a duplicate: %+v -> %+v", before, after)
	}
	if got, _ := s.GetRaw(ctx, "src", "a"); fmt.Sprint(got.Payload["v"]) != "1" {
		t.Errorf("a = %v, want the first record", got.Payload)
	}

	// The next run is not confused by run 1: update and unchanged.
	run2, _ := s.CreateRun(ctx, "src", "manual")
	c, dups, err = s.UpsertRawUnique(ctx, run2, "src", []record.Record{rec("a", 1), rec("b", 20), rec("c", 1)})
	if err != nil || len(dups) != 0 || c != (store.Counts{Updated: 1, Unchanged: 2}) {
		t.Fatalf("run 2: %+v %+v %v", c, dups, err)
	}
	// Validation failures write nothing.
	if _, _, err := s.UpsertRawUnique(ctx, run2, "src", []record.Record{rec("z", 1), {Key: ""}}); err == nil {
		t.Error("empty key accepted")
	}
	if _, err := s.GetRaw(ctx, "src", "z"); !errorsIs(err, store.ErrNotFound) {
		t.Errorf("z written: %v", err)
	}
	if c, d, err := s.UpsertRawUnique(ctx, run2, "src", nil); err != nil || len(d) != 0 || c != (store.Counts{}) {
		t.Errorf("empty = %+v %+v %v", c, d, err)
	}
}

func TestAdvisoryLockSlotsBounded(t *testing.T) {
	ctx := context.Background()
	_, pool := setup(t)
	s := store.NewWithOptions(pool, store.Options{LockConns: 2, LockWait: 50 * time.Millisecond})
	u1, ok, err := s.TryAdvisoryLock(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("a: ok=%v err=%v", ok, err)
	}
	u2, ok, err := s.TryAdvisoryLock(ctx, "b")
	if err != nil || !ok {
		t.Fatalf("b: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.TryAdvisoryLock(ctx, "c"); ok || !errors.Is(err, store.ErrLockCapacity) {
		t.Fatalf("c: ok=%v err=%v, want ErrLockCapacity", ok, err)
	}
	// A source held elsewhere also needs (and frees) a slot: not leaked.
	u1()
	u1()
	held, ok, err := s.TryAdvisoryLock(ctx, "b") // held by u2's session
	if ok || err != nil {
		t.Fatalf("b again: ok=%v err=%v, want held", ok, err)
	}
	held()
	for i := 0; i < 5; i++ { // repeated held results must not leak slots
		h, ok, err := s.TryAdvisoryLock(ctx, "b")
		if ok || err != nil {
			t.Fatalf("b again: ok=%v err=%v", ok, err)
		}
		h()
	}
	u3, ok, err := s.TryAdvisoryLock(ctx, "c")
	if err != nil || !ok {
		t.Fatalf("c after release: ok=%v err=%v", ok, err)
	}
	u3()
	u2()
}

func TestOpenPoolEnv(t *testing.T) {
	url := testutil.NewDatabaseURL(t)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	ctx := context.Background()
	pool, opts, err := store.OpenPool(ctx, url, env(map[string]string{"DB_MAX_CONNS": "3", "DB_LOCK_MAX_CONNS": "5", "DB_LOCK_WAIT": "7s", "DB_LOCK_CHECK_INTERVAL": "250ms"}))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if pool.Config().MaxConns != 3 || opts.LockConns != 5 || opts.LockWait != 7*time.Second || opts.LockCheckInterval != 250*time.Millisecond {
		t.Fatalf("got max=%d opts=%+v", pool.Config().MaxConns, opts)
	}
	pool2, opts2, err := store.OpenPool(ctx, url, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer pool2.Close()
	if opts2 != (store.Options{}) || pool2.Config().MaxConns < 4 {
		t.Fatalf("defaults: max=%d opts=%+v", pool2.Config().MaxConns, opts2)
	}
	for _, bad := range []map[string]string{
		{"DB_MAX_CONNS": "0"}, {"DB_MAX_CONNS": "x"}, {"DB_LOCK_MAX_CONNS": "-1"},
		{"DB_LOCK_WAIT": "0s"}, {"DB_LOCK_WAIT": "soon"},
		{"DB_LOCK_CHECK_INTERVAL": "0s"}, {"DB_LOCK_CHECK_INTERVAL": "1ms"}, {"DB_LOCK_CHECK_INTERVAL": "11m"},
		{"DB_LOCK_CHECK_INTERVAL": "-5s"}, {"DB_LOCK_CHECK_INTERVAL": "often"}, {"DB_LOCK_CHECK_INTERVAL": "5"},
	} {
		if p, _, err := store.OpenPool(ctx, url, env(bad)); err == nil {
			p.Close()
			t.Errorf("%v accepted", bad)
		} else if !errors.Is(err, store.ErrConfig) {
			t.Errorf("%v: err %v does not wrap ErrConfig", bad, err)
		}
	}
}

func TestConnectionBudgetWarning(t *testing.T) {
	for _, tc := range []struct {
		work, lock, max int
		warn            bool
	}{
		{4, 8, 100, false}, {92, 8, 100, false}, {93, 8, 100, true}, {100, 1, 100, true},
	} {
		msg, warn := store.ConnectionBudgetWarning(tc.work, tc.lock, tc.max)
		if warn != tc.warn {
			t.Errorf("%+v: warn=%v", tc, warn)
		}
		if warn && (!strings.Contains(msg, fmt.Sprint(tc.work)) || !strings.Contains(msg, fmt.Sprint(tc.lock)) || !strings.Contains(msg, "DB_MAX_CONNS") || !strings.Contains(msg, "DB_LOCK_MAX_CONNS")) {
			t.Errorf("%+v: message %q", tc, msg)
		}
	}
}

func goroutinesInMonitor() bool {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	return strings.Contains(string(buf), "(*Store).monitorLock")
}

// A monitored lock reports a terminated session once, via onLost, with
// ErrLockLost; unlock afterwards is safe and leaves no goroutine.
func TestMonitoredLockReportsLoss(t *testing.T) {
	ctx := context.Background()
	_, pool := newStore(t)
	s := store.NewWithOptions(pool, store.Options{LockCheckInterval: store.MinLockCheckInterval})
	lost := make(chan error, 4)
	unlock, ok, err := s.TryAdvisoryLockMonitored(ctx, "src", func(e error) { lost <- e })
	if err != nil || !ok {
		t.Fatalf("lock: ok=%v err=%v", ok, err)
	}
	var killed int
	err = pool.QueryRow(ctx, `
SELECT count(*) FROM (
  SELECT pg_terminate_backend(l.pid) FROM pg_locks l
  WHERE l.locktype = 'advisory' AND l.granted AND l.pid <> pg_backend_pid()
    AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
) x`).Scan(&killed)
	if err != nil || killed != 1 {
		t.Fatalf("terminate: killed=%d err=%v", killed, err)
	}
	select {
	case e := <-lost:
		if !errors.Is(e, store.ErrLockLost) || !strings.HasPrefix(e.Error(), "lost source lock (session terminated)") {
			t.Fatalf("lost error = %v", e)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("loss not reported")
	}
	unlock()
	unlock()
	if goroutinesInMonitor() {
		t.Fatal("monitor goroutine still running after unlock")
	}
	if len(lost) != 0 {
		t.Fatalf("onLost called %d more times", len(lost))
	}
	// Healthy lock released normally: no report, no goroutine, lock free again.
	u2, ok, err := s.TryAdvisoryLockMonitored(ctx, "src", func(e error) { lost <- e })
	if err != nil || !ok {
		t.Fatalf("relock: ok=%v err=%v", ok, err)
	}
	u2()
	if goroutinesInMonitor() || len(lost) != 0 {
		t.Fatalf("after normal unlock: monitor running=%v reports=%d", goroutinesInMonitor(), len(lost))
	}
}
