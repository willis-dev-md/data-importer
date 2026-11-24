package importer_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/source"
	"github.com/willis-dev-md/data-importer/internal/store"
)

// ctxAdapter opens a stream that gets the run's context (adapters receive it).
type ctxAdapter struct {
	mk func(ctx context.Context) source.Stream
}

func (a ctxAdapter) Open(ctx context.Context, _ *config.Source) (source.Stream, error) {
	return a.mk(ctx), nil
}

// killLockSession terminates the backend holding the advisory lock of the
// source in THIS database (not our own connection, not other databases).
func killLockSession(t *testing.T, e env, sourceID string) {
	t.Helper()
	var killed int
	err := e.pool.QueryRow(context.Background(), `
SELECT count(*) FROM (
  SELECT pg_terminate_backend(l.pid) FROM pg_locks l
  WHERE l.locktype = 'advisory' AND l.granted AND l.pid <> pg_backend_pid()
    AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND ((l.classid::bigint << 32) | l.objid::bigint) = hashtextextended('data-importer:source:' || $1::text, 0)
) x`, sourceID).Scan(&killed)
	if err != nil || killed != 1 {
		t.Fatalf("terminate lock session: killed=%d err=%v", killed, err)
	}
}

func noMonitorGoroutine(t *testing.T) {
	t.Helper()
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	if strings.Contains(string(buf), "(*Store).monitorLock") {
		t.Fatalf("lock monitor goroutine still running:\n%s", buf)
	}
}

func (e env) runRow(t *testing.T, id int64) (status, errText string) {
	t.Helper()
	var et *string
	if err := e.pool.QueryRow(context.Background(), `SELECT status, error FROM import_runs WHERE id = $1`, id).Scan(&status, &et); err != nil {
		t.Fatal(err)
	}
	if et != nil {
		errText = *et
	}
	return status, errText
}

// The reproduction of the task 019 finding: the lock session is terminated
// mid-run. Before task 020 the run carried on unprotected and a second run of
// the same source could start. Now the run is cancelled and finishes failed.
// A sweep that runs in the gap (the lock is gone, the row still says running)
// may mark the row abandoned, but the run's own final state wins.
func TestRunStopsWhenLockSessionIsTerminated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	e := newSmallEnv(t, 8, store.Options{LockCheckInterval: 20 * time.Millisecond})
	e.addSource(t, testCfg(0))
	const killAt = 12
	var swept []int64
	ad := ctxAdapter{mk: func(rctx context.Context) source.Stream {
		return &keyedStream{n: 1000000, key: func(i int) string {
			if i == killAt {
				killLockSession(t, e, "s1")
				// The terminated session no longer holds the lock: poll until a
				// sweep (what a second `importer run` does first) sees it.
				for len(swept) == 0 && ctx.Err() == nil {
					ids, err := e.st.SweepAbandonedRuns(ctx)
					if err != nil {
						t.Errorf("sweep: %v", err)
						break
					}
					swept = ids
				}
				// The run must be stopped by the monitor, within a polled deadline.
				select {
				case <-rctx.Done():
				case <-ctx.Done():
					t.Error("run was not cancelled after its lock session was terminated")
				}
			}
			return fmt.Sprint("k", i)
		}}
	}}
	res, err := importer.Run(ctx, e.deps(ad), "s1", importer.Options{BatchSize: 5})
	if err == nil || !errors.Is(err, store.ErrLockLost) {
		t.Fatalf("err = %v, want it to wrap ErrLockLost", err)
	}
	if res.Status != importer.StatusFailed || !strings.HasPrefix(res.Error, "lost source lock (session terminated)") {
		t.Fatalf("result %+v", res)
	}
	if len(swept) != 1 || swept[0] != res.RunID {
		t.Fatalf("sweep in the gap closed %v, want [%d]", swept, res.RunID)
	}
	status, errText := e.runRow(t, res.RunID)
	if status != "failed" || !strings.HasPrefix(errText, "lost source lock (session terminated)") {
		t.Fatalf("run row = %q %q, want failed with the lost-lock error (not abandoned)", status, errText)
	}
	// Batches committed before the loss stay, tagged with the run id.
	var tagged int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM raw_records WHERE last_run_id = $1`, res.RunID).Scan(&tagged); err != nil || tagged < 10 {
		t.Fatalf("raw records tagged with run %d = %d (err %v), want at least the 2 committed batches", res.RunID, tagged, err)
	}
	if tagged > killAt+5 {
		t.Fatalf("run kept writing after the loss: %d records", tagged)
	}
	noMonitorGoroutine(t)
	// The lock is free and the next run of the source works and is not swept.
	res2, err := importer.Run(ctx, e.deps(keyedAdapter(30)), "s1", importer.Options{BatchSize: 5})
	if err != nil || res2.Status != importer.StatusSucceeded {
		t.Fatalf("next run: %+v %v", res2, err)
	}
	if n := e.countRunsWithStatus(t, "running"); n != 0 {
		t.Fatalf("%d runs left running", n)
	}
}

// After the kill a second run is either refused (the first still holds a live
// lock: not possible once the session is dead) or starts in the detection
// window; it is never started once the first run has finished failed, and the
// first run still ends failed. With the monitor effectively off (max interval)
// the window is real: the second run is allowed to start while the first still
// believes it holds the lock.
func TestSecondRunDuringDetectionWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	e := newSmallEnv(t, 8, store.Options{LockCheckInterval: store.MaxLockCheckInterval})
	e.addSource(t, testCfg(0))
	var second error
	var secondStatus string
	ad := ctxAdapter{mk: func(context.Context) source.Stream {
		return &keyedStream{n: 20, key: func(i int) string {
			if i == 10 {
				killLockSession(t, e, "s1")
				for { // the terminated session's lock disappears asynchronously
					var held bool
					if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND granted
  AND database = (SELECT oid FROM pg_database WHERE datname = current_database()))`).Scan(&held); err != nil {
						t.Error(err)
						break
					}
					if !held || ctx.Err() != nil {
						break
					}
				}
				other := e.deps(keyedAdapter(5))
				res, err := importer.Run(ctx, other, "s1", importer.Options{})
				second, secondStatus = err, res.Status
			}
			return fmt.Sprint("k", i)
		}}
	}}
	res, err := importer.Run(ctx, e.deps(ad), "s1", importer.Options{BatchSize: 5})
	// The window: the second run started although the first was still running.
	if second != nil || secondStatus != importer.StatusSucceeded {
		t.Fatalf("second run in the window: %q %v", secondStatus, second)
	}
	// The first run is not told (interval 10m); its unlock of a dead session is harmless.
	if err != nil || res.Status != importer.StatusSucceeded {
		t.Fatalf("first run with monitor effectively off: %+v %v", res, err)
	}
}

// Healthy runs never report a lost lock, however often the monitor checks, and
// the sweep (also running all the time) never touches a live run whose lock is
// being re-checked.
func TestHealthyRunNoFalseAlarmAndSweepLeavesLiveRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	e := newSmallEnv(t, 8, store.Options{LockCheckInterval: store.MinLockCheckInterval})
	e.addSource(t, testCfg(0))
	var sweeps int
	ad := ctxAdapter{mk: func(context.Context) source.Stream {
		return &keyedStream{n: 400, key: func(i int) string {
			if i%20 == 0 {
				// A burst of sweeps while the monitor re-checks every 10ms.
				for j := 0; j < 5; j++ {
					ids, err := e.st.SweepAbandonedRuns(ctx)
					if err != nil || len(ids) != 0 {
						t.Errorf("sweep touched a live run: %v %v", ids, err)
					}
					sweeps++
				}
			}
			return fmt.Sprint("k", i)
		}}
	}}
	res, err := importer.Run(ctx, e.deps(ad), "s1", importer.Options{BatchSize: 10})
	if err != nil || res.Status != importer.StatusSucceeded || res.Inserted != 400 || res.Error != "" {
		t.Fatalf("healthy run: %+v %v", res, err)
	}
	if sweeps < 50 {
		t.Fatalf("only %d sweeps ran", sweeps)
	}
	noMonitorGoroutine(t)
}

// The monitor goroutine is joined on every way out of Run: success, failure,
// cancellation and panic.
func TestLockMonitorJoinedOnEveryExit(t *testing.T) {
	ctx := context.Background()
	e := newSmallEnv(t, 8, store.Options{LockCheckInterval: store.MinLockCheckInterval})
	e.addSource(t, testCfg(0))
	if _, err := importer.Run(ctx, e.deps(keyedAdapter(5)), "s1", importer.Options{}); err != nil {
		t.Fatal(err)
	}
	noMonitorGoroutine(t)
	if _, err := importer.Run(ctx, e.deps(fakeAdapter{openErr: errors.New("boom")}), "s1", importer.Options{}); err == nil {
		t.Fatal("want failure")
	}
	noMonitorGoroutine(t)
	cctx, cancel := context.WithCancel(ctx)
	canc := streamAdapter{mk: func() source.Stream {
		return &keyedStream{n: 50, key: func(i int) string { return fmt.Sprint(i) }, last: cancel}
	}}
	if _, err := importer.Run(cctx, e.deps(canc), "s1", importer.Options{BatchSize: 10}); err == nil {
		t.Fatal("want cancelled")
	}
	noMonitorGoroutine(t)
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
	noMonitorGoroutine(t)
	if n := e.countRunsWithStatus(t, "running"); n != 0 {
		t.Fatalf("%d runs left running", n)
	}
}

func (e env) countRunsWithStatus(t *testing.T, status string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM import_runs WHERE status = $1`, status).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
