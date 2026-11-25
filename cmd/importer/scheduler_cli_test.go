package main

import (
	"bytes"
	"context"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/willis-dev-md/data-importer/internal/schedule"
	"github.com/willis-dev-md/data-importer/internal/scheduler"
	"github.com/willis-dev-md/data-importer/internal/store"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

// serveFast runs serve with an unreachable database and a short deadline: a configuration error must return 2
// at once (before any connection); if the check under test is missing serve would run until the deadline and
// the test fails within seconds instead of hanging.
func serveFast(t *testing.T, env map[string]string, errb *bytes.Buffer) int {
	t.Helper()
	env["GRPC_ADDR"] = "127.0.0.1:0"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	code := serve(ctx, nil, func(k string) string { return env[k] }, errb)
	if d := time.Since(start); d >= 2*time.Second {
		t.Errorf("serve took %v to reject the configuration (it must fail before connecting)", d)
	}
	return code
}

func TestServeRejectsBadSchedulerEnv(t *testing.T) {
	for _, tt := range []struct{ k, v string }{
		{"SCHEDULER_ENABLED", "perhaps"},
		{"SCHEDULER_POLL_INTERVAL", "10ms"},
		{"SCHEDULER_POLL_INTERVAL", "2h"},
		{"SCHEDULER_POLL_INTERVAL", "often"},
		{"IMPORT_CONCURRENCY", "0"},
		{"IMPORT_CONCURRENCY", "two"},
	} {
		t.Run(tt.k+"="+tt.v, func(t *testing.T) {
			var errb bytes.Buffer
			env := map[string]string{"DATABASE_URL": "postgres://x", tt.k: tt.v}
			if code := serveFast(t, env, &errb); code != 2 || !strings.Contains(errb.String(), tt.k) {
				t.Errorf("code = %d, stderr %q; want 2 naming %s", code, errb.String(), tt.k)
			}
		})
	}
}

// IMPORT_CONCURRENCY above the lock connection budget is a usage error (slots would be lost), unless the
// scheduler is off.
func TestServeRejectsConcurrencyAboveLockBudget(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  map[string]string
	}{
		{"explicit budget", map[string]string{"DB_LOCK_MAX_CONNS": "1", "IMPORT_CONCURRENCY": "2"}},
		{"default budget", map[string]string{"IMPORT_CONCURRENCY": "9"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var errb bytes.Buffer
			env := map[string]string{"DATABASE_URL": "postgres://x", "FILE_STORE_DIR": t.TempDir()}
			for k, v := range tt.env {
				env[k] = v
			}
			code := serveFast(t, env, &errb)
			if code != 2 || !strings.Contains(errb.String(), "IMPORT_CONCURRENCY") || !strings.Contains(errb.String(), "DB_LOCK_MAX_CONNS") {
				t.Errorf("code = %d, stderr %q", code, errb.String())
			}
		})
	}
}

// A scheduled run whose secret is missing in serve's environment (or whose source is gone) is recorded as a
// failed run of the slot, with the variable name but no secret.
func TestScheduledRunMissingSecretIsRecorded(t *testing.T) {
	absRepo, _ := filepath.Abs(filepath.Join("..", ".."))
	c := &cli{t: t, bin: buildBinary(t), url: testutil.NewDatabaseURL(t)}
	dir := t.TempDir()
	writeFile(t, dir, "partner.source.yaml", "id: partner\nkind: csv\nlocation: https://example.invalid/x.csv\nnatural_key: [id]\n"+
		"auth: { type: bearer, token_env: PARTNER_TOKEN_NOT_SET }\nschedule: { every: daily, at: \"12:00\", timezone: UTC }\n")
	c.expect(nil, 0, "version 3", "migrate", "up")
	c.expect(nil, 0, "next run ", "sources", "apply", "--dir", dir, "--schema", filepath.Join(absRepo, "canonical-schema.yaml"))
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, c.url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	slot := time.Now().UTC().Add(-time.Minute).Truncate(time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE sources SET next_run_at = $1`, slot); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sources SET schedule = jsonb_set(schedule, '{at}', to_jsonb($1::text))`, slot.Format("15:04")); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	files, err := store.NewDirFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := scheduler.New(scheduler.Config{Store: st, Run: scheduledRun(st, files, func(string) string { return "" }),
		Fail: recordFailedRun(st), Clock: nil, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))})
	s.Poll(ctx)
	s.Wait()
	var status, trigger, errText string
	var sched *time.Time
	if err := pool.QueryRow(ctx, `SELECT status, trigger, scheduled_for, error FROM import_runs WHERE source_id='partner'`).Scan(&status, &trigger, &sched, &errText); err != nil {
		t.Fatalf("no failed run row: %v", err)
	}
	if status != "failed" || trigger != "schedule" || sched == nil || !sched.Equal(slot) || !strings.Contains(errText, "PARTNER_TOKEN_NOT_SET") {
		t.Fatalf("row: %s %s %v %q", status, trigger, sched, errText)
	}
	s.Poll(ctx)
	s.Wait()
	if n := count(t, pool, `SELECT count(*) FROM import_runs`); n != 1 {
		t.Fatalf("%d runs, want 1 (never retried)", n)
	}
	out, _ := c.expect(nil, 0, "partner", "schedule", "list")
	if !strings.Contains(out, "failed (schedule)") {
		t.Errorf("schedule list:\n%s", out)
	}
}

func TestScheduleUsage(t *testing.T) {
	for _, args := range [][]string{{"schedule"}, {"schedule", "show"}, {"schedule", "list", "x"}} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage: importer schedule list") {
			t.Errorf("%v: code %d, stderr %q", args, code, errb.String())
		}
	}
}

func TestSummarizeSchedule(t *testing.T) {
	for _, tt := range []struct {
		in   schedule.Input
		want string
	}{
		{schedule.Input{Every: "daily"}, "daily at 00:00"},
		{schedule.Input{Every: "weekly", DayOfWeek: "monday", At: "06:30"}, "weekly on monday at 06:30"},
		{schedule.Input{Every: "monthly", DayOfMonth: 18, At: "09:30"}, "monthly on day 18 at 09:30"},
		{schedule.Input{Every: "monthly", DayOfMonth: schedule.LastDay}, "monthly on the last day at 00:00"},
		{schedule.Input{Every: "daily", Jitter: "5m", OnMissed: "skip"}, "daily at 00:00, jitter 5m0s, skip missed"},
	} {
		sp, err := schedule.New(tt.in)
		if err != nil {
			t.Fatal(err)
		}
		if got := summarizeSchedule(sp); got != tt.want {
			t.Errorf("%+v: %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The real binary: a scheduled source that is due runs by itself against a local TLS server, trigger=schedule,
// 642 inserted; the second slot finds the same file (unchanged_file); serve stays healthy and exits 0 on SIGTERM;
// `schedule list` shows it all and works without serve.
func TestServeRunsScheduledSourceByItself(t *testing.T) {
	absRepo, _ := filepath.Abs(filepath.Join("..", ".."))
	fixture := readFile(t, filepath.Join(absRepo, "testdata", "ons", "l55o.csv"))
	schema := filepath.Join(absRepo, "canonical-schema.yaml")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = w.Write([]byte(fixture))
	}))
	defer srv.Close()
	certFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	tlsEnv := []string{"SSL_CERT_FILE=" + certFile}

	slot := time.Now().UTC().Truncate(time.Minute) // today's slot, a moment ago: "due"
	base := readFile(t, filepath.Join(absRepo, "docs", "examples", "ons-l55o.source.yaml"))
	y := regexp.MustCompile(`(?m)^location:.*$`).ReplaceAllString(base, "location: "+srv.URL+"/l55o.csv")
	y = regexp.MustCompile(`(?s)schedule:.*?\nenabled: true\n`).ReplaceAllString(y, "")
	y += fmt.Sprintf("change_detection: [content_hash]\nschedule: { every: daily, at: %q, timezone: UTC }\nenabled: true\n", slot.Format("15:04"))
	dir := t.TempDir()
	writeFile(t, dir, "ons-l55o.source.yaml", y)
	writeFile(t, dir, "manual.source.yaml", "id: manual\nkind: csv\nlocation: file://"+filepath.Join(absRepo, "testdata", "ons", "l55o.csv")+"\nnatural_key: [id]\n")

	c := &cli{t: t, bin: buildBinary(t), url: testutil.NewDatabaseURL(t)}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, c.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	c.expect(nil, 0, "version 3", "migrate", "up")
	c.expect(nil, 0, "next run ", "sources", "apply", "--dir", dir, "--schema", schema)

	listOut, _ := c.expect(nil, 0, "ID", "schedule", "list") // works without serve
	for _, want := range []string{"ons-l55o", "true", "daily at " + slot.Format("15:04"), "UTC", "manual", "-"} {
		if !strings.Contains(listOut, want) {
			t.Errorf("schedule list lacks %q:\n%s", want, listOut)
		}
	}

	cmd := exec.Command(c.bin, "serve")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+c.url, "GRPC_ADDR=127.0.0.1:0", "TLS_CERT_FILE=", "TLS_KEY_FILE=",
		"SCHEDULER_POLL_INTERVAL=100ms", "FILE_STORE_DIR="+t.TempDir())
	cmd.Env = append(cmd.Env, tlsEnv...)
	logs := &lockedBuf{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-exited:
			if err != nil {
				t.Errorf("importer serve exited with error: %v\n%s", err, logs.String())
			}
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
			t.Errorf("importer serve did not exit after SIGTERM\n%s", logs.String())
		}
	}
	t.Cleanup(stop)

	poll := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for !cond() {
			select {
			case err := <-exited:
				t.Fatalf("serve exited early (%v) while waiting for %s\n%s", err, what, logs.String())
			case <-time.After(20 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s\n%s", what, logs.String())
			}
		}
	}
	addrRe := regexp.MustCompile(`serving (?:plaintext )?gRPC.* addr=(\S+)`)
	var addr string
	poll("the listen address", func() bool {
		if m := addrRe.FindStringSubmatch(logs.String()); m != nil {
			addr = m[1]
		}
		return addr != "" && strings.Contains(logs.String(), "scheduler started")
	})

	runs := func() (n int, status, trigger string, inserted int, scheduled *time.Time) {
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM import_runs WHERE source_id='ons-l55o'`).Scan(&n)
		_ = pool.QueryRow(ctx, `SELECT status, trigger, inserted, scheduled_for FROM import_runs WHERE source_id='ons-l55o' ORDER BY id DESC LIMIT 1`).
			Scan(&status, &trigger, &inserted, &scheduled)
		return
	}
	finished := func(want int) func() bool {
		return func() bool {
			n, status, _, _, _ := runs()
			return n == want && status != "running"
		}
	}

	// Slot 1: made due while serve is running; nobody calls `run`.
	if _, err := pool.Exec(ctx, `UPDATE sources SET next_run_at = $1 WHERE id = 'ons-l55o'`, slot); err != nil {
		t.Fatal(err)
	}
	poll("the first scheduled run", finished(1))
	n, status, trigger, inserted, scheduled := runs()
	if n != 1 || status != "succeeded" || trigger != "schedule" || inserted != 642 || scheduled == nil || !scheduled.Equal(slot) {
		t.Fatalf("first run: n=%d %s %s inserted=%d scheduled=%v", n, status, trigger, inserted, scheduled)
	}
	var next, last *time.Time
	if err := pool.QueryRow(ctx, `SELECT next_run_at, last_scheduled_at FROM sources WHERE id='ons-l55o'`).Scan(&next, &last); err != nil ||
		!next.Equal(slot.AddDate(0, 0, 1)) || !last.Equal(slot) {
		t.Fatalf("next %v last %v err %v", next, last, err)
	}

	// The run row is closed before the source lock is released: wait for the lock to be free, or the second
	// slot would (correctly) be skipped as "previous run still running".
	poll("the source lock to be released", func() bool {
		var held int
		if err := pool.QueryRow(ctx, `
SELECT count(*) FROM pg_locks l
WHERE l.locktype = 'advisory' AND l.granted
  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND ((l.classid::bigint << 32) | l.objid::bigint) = hashtextextended('data-importer:source:ons-l55o', 0)`).Scan(&held); err != nil {
			t.Fatal(err)
		}
		return held == 0
	})

	// Slot 2 (the slot is made due again): the same file, so unchanged_file.
	if _, err := pool.Exec(ctx, `UPDATE sources SET next_run_at = $1 WHERE id = 'ons-l55o'`, slot); err != nil {
		t.Fatal(err)
	}
	poll("the second scheduled run", finished(2))
	if n, status, trigger, inserted, _ = runs(); n != 2 || status != "unchanged_file" || trigger != "schedule" || inserted != 0 {
		t.Fatalf("second run: n=%d %s %s inserted=%d", n, status, trigger, inserted)
	}
	if got := count(t, pool, `SELECT count(*) FROM import_runs WHERE source_id='manual'`); got != 0 {
		t.Errorf("a manual-only source ran %d times", got)
	}

	// Serve is still healthy.
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	h, err := healthpb.NewHealthClient(conn).Check(hctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
	if err != nil || h.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health: %v %v", h, err)
	}
	listOut, _ = c.expect(nil, 0, "ons-l55o", "schedule", "list")
	if !strings.Contains(listOut, "unchanged_file (schedule)") || !strings.Contains(listOut, slot.AddDate(0, 0, 1).Format(time.RFC3339)) {
		t.Errorf("schedule list after the runs:\n%s", listOut)
	}

	conn.Close()
	stop()
	if strings.Contains(logs.String(), "panic") {
		t.Errorf("panic in the log:\n%s", logs.String())
	}
}
