package main

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/testutil"
)

// `sources apply` validates the schedule first (all errors, nothing written), stores it, and keeps or recomputes
// next_run_at.
func TestCLISourcesApplySchedule(t *testing.T) {
	repo := filepath.Join("..", "..")
	absRepo, _ := filepath.Abs(repo)
	schema := filepath.Join(absRepo, "canonical-schema.yaml")
	fixture := filepath.Join(absRepo, "testdata", "ons", "l55o.csv")

	c := &cli{t: t, bin: buildBinary(t), url: testutil.NewDatabaseURL(t)}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, c.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	dir := t.TempDir()
	base := readFile(t, filepath.Join(repo, "docs", "examples", "ons-l55o.source.yaml"))
	base = regexp.MustCompile(`(?m)^location:.*$`).ReplaceAllString(base, "location: file://"+fixture)
	withSchedule := func(block string) string {
		s := regexp.MustCompile(`(?s)schedule:.*?\nenabled: true\n`).ReplaceAllString(base, block+"enabled: true\n")
		if s == base && block != "" {
			t.Fatal("schedule block not replaced")
		}
		return s
	}
	writeFile(t, dir, "ons-l55o.mapping.yaml", readFile(t, filepath.Join(repo, "docs", "examples", "ons-l55o.mapping.yaml")))
	state := func() (sched *string, enabled bool, next *time.Time) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT schedule::text, enabled, next_run_at FROM sources WHERE id='ons-l55o'`).Scan(&sched, &enabled, &next); err != nil {
			t.Fatal(err)
		}
		return
	}
	c.expect(nil, 0, "version 3", "migrate", "up")

	// Invalid: every error listed, exit 1, nothing written.
	writeFile(t, dir, "ons-l55o.source.yaml", withSchedule("schedule: { every: monthly, day_of_month: 31, at: \"25:00\", timezone: Nope/Zone, jitter: 3h }\n"))
	_, e := c.expect(nil, 1, "nothing was written", "sources", "apply", "--dir", dir, "--schema", schema)
	for _, want := range []string{"schedule.day_of_month", "schedule.at", "schedule.timezone", "schedule.jitter"} {
		if !regexp.MustCompile(regexp.QuoteMeta(want)).MatchString(e) {
			t.Errorf("stderr lacks %s:\n%s", want, e)
		}
	}
	writeFile(t, dir, "ons-l55o.source.yaml", withSchedule("schedule: { cron: \"0 6 * * 1\" }\n"))
	c.expect(nil, 1, "schedule.cron: not supported yet", "sources", "apply", "--dir", dir, "--schema", schema)
	if n := count(t, pool, `SELECT count(*) FROM sources`); n != 0 {
		t.Fatalf("invalid schedule wrote %d sources", n)
	}

	// Valid: stored with defaults, next_run_at set (in the future).
	writeFile(t, dir, "ons-l55o.source.yaml", withSchedule("schedule: { every: monthly, day_of_month: last, at: \"09:30\" }\n"))
	c.expect(nil, 0, "ons-l55o: source created, mapping v1 created, next run ", "sources", "apply", "--dir", dir, "--schema", schema)
	sched, enabled, next := state()
	if sched == nil || !enabled || next == nil || !next.After(time.Now()) {
		t.Fatalf("after apply: %v %v %v", sched, enabled, next)
	}
	if !regexp.MustCompile(`"timezone": "Europe/London"`).MatchString(*sched) || !regexp.MustCompile(`"day_of_month": -1`).MatchString(*sched) {
		t.Errorf("stored schedule lacks defaults / last: %s", *sched)
	}
	first := *next

	// Same schedule again: next_run_at kept.
	c.expect(nil, 0, "ons-l55o: source unchanged, mapping v1 unchanged, next run "+first.UTC().Format(time.RFC3339), "sources", "apply", "--dir", dir, "--schema", schema)

	// Changed schedule: recomputed immediately.
	writeFile(t, dir, "ons-l55o.source.yaml", withSchedule("schedule: { every: daily, at: \"06:00\", timezone: UTC }\n"))
	c.expect(nil, 0, "next run ", "sources", "apply", "--dir", dir, "--schema", schema)
	if _, _, n := state(); n == nil || n.Equal(first) || n.After(time.Now().Add(25*time.Hour)) {
		t.Errorf("next_run_at not recomputed: %v (was %v)", n, first)
	}

	// enabled: false clears next_run_at.
	writeFile(t, dir, "ons-l55o.source.yaml", regexp.MustCompile(`enabled: true`).ReplaceAllString(withSchedule("schedule: { every: daily, at: \"06:00\", timezone: UTC }\n"), "enabled: false"))
	c.expect(nil, 0, "schedule cleared", "sources", "apply", "--dir", dir, "--schema", schema)
	if s, en, n := state(); s == nil || en || n != nil {
		t.Errorf("disabled: %v %v %v", s, en, n)
	}

	// No schedule: manual only.
	writeFile(t, dir, "ons-l55o.source.yaml", regexp.MustCompile(`(?s)schedule:.*?\nenabled: true\n`).ReplaceAllString(base, ""))
	c.expect(nil, 0, "ons-l55o: source updated, mapping v1 unchanged, schedule cleared", "sources", "apply", "--dir", dir, "--schema", schema)
	if s, en, n := state(); s != nil || !en || n != nil {
		t.Errorf("no schedule: %v %v %v", s, en, n)
	}
}
