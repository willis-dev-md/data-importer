package importer_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/source"
	"github.com/willis-dev-md/data-importer/internal/store"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

// newSmallEnv is newEnv with a work pool of maxConns connections (the default
// settings of pgxpool.New are max(4, NumCPU)) and explicit lock options.
func newSmallEnv(t *testing.T, maxConns int32, opts store.Options) env {
	t.Helper()
	url := testutil.NewDatabaseURL(t)
	if _, err := store.Migrate(context.Background(), url, store.Up); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pcfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	pcfg.MaxConns = maxConns
	pool, err := pgxpool.NewWithConfig(context.Background(), pcfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return env{pool: pool, st: store.NewWithOptions(pool, opts)}
}

func (e env) addIDSource(t *testing.T, id string) {
	t.Helper()
	c := testCfg(0)
	c.ID = id
	e.addSource(t, c)
}

func keyedAdapter(n int) source.Adapter {
	return streamAdapter{mk: func() source.Stream {
		return &keyedStream{n: n, key: func(i int) string { return fmt.Sprint("k", i) }}
	}}
}

// Before task 019 every run held one WORK pool connection for its lock and
// needed more for its queries: with a pool of N connections, N concurrent runs
// of different sources deadlocked. Runs use a deadline so a regression fails
// instead of hanging the gate.
func TestConcurrentRunsSmallPool(t *testing.T) {
	for _, tc := range []struct {
		name      string
		maxConns  int32
		runs      int
		lockConns int
	}{
		{"1 conn, 1 run", 1, 1, 0},
		{"4 conns, 8 runs", 4, 8, 0},
		{"4 conns, 32 runs, 3 lock slots", 4, 32, 3},
		{"2 conns, 32 runs", 2, 32, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			e := newSmallEnv(t, tc.maxConns, store.Options{LockConns: tc.lockConns, LockWait: 80 * time.Second})
			ids := make([]string, tc.runs)
			for i := range ids {
				ids[i] = fmt.Sprint("c", i)
				e.addIDSource(t, ids[i])
			}
			results := make([]importer.RunResult, len(ids))
			errs := make([]error, len(ids))
			var wg sync.WaitGroup
			for i, id := range ids {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results[i], errs[i] = importer.Run(ctx, e.deps(keyedAdapter(250)), id, importer.Options{BatchSize: 50})
				}()
			}
			wg.Wait()
			for i := range ids {
				if errs[i] != nil || results[i].Status != importer.StatusSucceeded || results[i].Inserted != 250 {
					t.Errorf("%s: %+v %v", ids[i], results[i], errs[i])
				}
			}
			if got := e.countRuns(t); got != tc.runs {
				t.Errorf("run rows = %d, want %d", got, tc.runs)
			}
		})
	}
}

// Many concurrent runs of ONE source on a tiny pool: exactly one wins, all
// others get ErrAlreadyRunning and leave no run row.
func TestConcurrentSameSourceOneWinner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	e := newSmallEnv(t, 2, store.Options{})
	e.addSource(t, testCfg(0))
	const n = 16
	// The winner parks inside its stream until every loser has returned, so the
	// race is decided by the lock alone.
	losersDone := make(chan struct{})
	ad := streamAdapter{mk: func() source.Stream {
		return &keyedStream{n: 10, key: func(i int) string { return fmt.Sprint("k", i) }, last: func() {
			select {
			case <-losersDone:
			case <-ctx.Done():
			}
		}}
	}}
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, err := importer.Run(ctx, e.deps(ad), "s1", importer.Options{})
			errCh <- err
		}()
	}
	for i := 0; i < n-1; i++ {
		if err := <-errCh; !errors.Is(err, importer.ErrAlreadyRunning) {
			t.Fatalf("loser %d: %v, want ErrAlreadyRunning", i, err)
		}
	}
	if got := e.countRuns(t); got != 1 {
		t.Fatalf("run rows while winner runs = %d, want 1", got)
	}
	close(losersDone)
	if err := <-errCh; err != nil {
		t.Fatalf("winner: %v", err)
	}
}

// No lock connection free: the run fails with ErrLockCapacity after the
// documented wait, creates no run row and does not touch the work pool.
func TestLockCapacityBoundedWait(t *testing.T) {
	ctx := context.Background()
	e := newSmallEnv(t, 2, store.Options{LockConns: 1, LockWait: 100 * time.Millisecond})
	e.addIDSource(t, "a")
	e.addIDSource(t, "b")
	unlock, ok, err := e.st.TryAdvisoryLock(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("lock: ok=%v err=%v", ok, err)
	}
	_, err = importer.Run(ctx, e.deps(keyedAdapter(5)), "b", importer.Options{})
	if !errors.Is(err, store.ErrLockCapacity) {
		t.Fatalf("err = %v, want ErrLockCapacity", err)
	}
	if got := e.countRuns(t); got != 0 {
		t.Fatalf("run rows = %d, want 0", got)
	}
	// A cancelled context wakes a waiter immediately (long wait configured).
	e2 := newSmallEnv(t, 2, store.Options{LockConns: 1, LockWait: time.Hour})
	e2.addIDSource(t, "a")
	u2, ok, err := e2.st.TryAdvisoryLock(ctx, "a")
	if err != nil || !ok {
		t.Fatalf("lock: ok=%v err=%v", ok, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	res := make(chan error, 1)
	go func() {
		_, err := importer.Run(cctx, e2.deps(keyedAdapter(5)), "a2", importer.Options{})
		res <- err
	}()
	cancel()
	if err := <-res; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter err = %v, want context.Canceled", err)
	}
	u2()
	// Releasing frees the slot: the same run now works.
	unlock()
	if _, err := importer.Run(ctx, e.deps(keyedAdapter(5)), "b", importer.Options{}); err != nil {
		t.Fatalf("after unlock: %v", err)
	}
}

// Every way a run can end frees its lock slot: with a single slot, a series of
// runs ending in success, failure, abort, cancel and panic must all be followed
// by a run that gets the lock.
func TestLockSlotFreedOnEveryExit(t *testing.T) {
	ctx := context.Background()
	e := newSmallEnv(t, 2, store.Options{LockConns: 1, LockWait: 100 * time.Millisecond})
	e.addSource(t, testCfg(0))
	cfgAbort := testCfg(0.01)
	cfgAbort.ID = "ab"
	e.addSource(t, cfgAbort)
	good := func() {
		t.Helper()
		if _, err := importer.Run(ctx, e.deps(keyedAdapter(3)), "s1", importer.Options{}); err != nil {
			t.Fatalf("lock slot not free: %v", err)
		}
	}
	good()
	// failure: adapter cannot open
	if _, err := importer.Run(ctx, e.deps(fakeAdapter{openErr: errors.New("boom")}), "s1", importer.Options{}); err == nil {
		t.Fatal("want failure")
	}
	good()
	// abort: every row is a duplicate beyond the ratio
	dup := streamAdapter{mk: func() source.Stream {
		return &keyedStream{n: 50, key: func(int) string { return "same" }}
	}}
	if res, err := importer.Run(ctx, e.deps(dup), "ab", importer.Options{BatchSize: 10}); err == nil || res.Status != importer.StatusAborted {
		t.Fatalf("want abort, got %+v %v", res, err)
	}
	good()
	// cancel mid-stream
	cctx, cancel := context.WithCancel(ctx)
	canc := streamAdapter{mk: func() source.Stream {
		return &keyedStream{n: 50, key: func(i int) string { return fmt.Sprint(i) }, last: cancel}
	}}
	if _, err := importer.Run(cctx, e.deps(canc), "s1", importer.Options{BatchSize: 10}); err == nil {
		t.Fatal("want cancelled")
	}
	good()
	// panic inside the adapter stream
	pan := streamAdapter{mk: func() source.Stream {
		return &keyedStream{n: 5, key: func(i int) string { return fmt.Sprint(i) }, last: func() { panic("boom") }}
	}}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		_, _ = importer.Run(ctx, e.deps(pan), "s1", importer.Options{})
	}()
	good()
	var running int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM import_runs WHERE status = 'running'`).Scan(&running); err != nil || running != 0 {
		t.Fatalf("running rows = %d err=%v", running, err)
	}
}
