package testutil

import (
	"context"
	"runtime"
	"testing"
)

func TestDBConnects(t *testing.T) {
	pool := DB(t)
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&n); err != nil || n != 1 {
		t.Fatalf("SELECT 1 = %d, %v", n, err)
	}
}

func TestDBIsolated(t *testing.T) {
	ctx := context.Background()
	a, b := DB(t), DB(t)
	if _, err := a.Exec(ctx, "CREATE TABLE only_in_a (id int)"); err != nil {
		t.Fatal(err)
	}
	var reg *string
	if err := b.QueryRow(ctx, "SELECT to_regclass('only_in_a')::text").Scan(&reg); err != nil {
		t.Fatal(err)
	}
	if reg != nil {
		t.Fatalf("table from database a visible in b: %s", *reg)
	}
	var an, bn string
	_ = a.QueryRow(ctx, "SELECT current_database()").Scan(&an)
	_ = b.QueryRow(ctx, "SELECT current_database()").Scan(&bn)
	if an == bn {
		t.Fatalf("both tests share database %s", an)
	}
}

func TestDatabaseURLPolicy(t *testing.T) {
	tests := []struct {
		name, dburl, require string
		want                 string // "value", "skip" or "fatal"
	}{
		{"set", "postgres://x", "", "value"},
		{"unset skips", "", "", "skip"},
		{"unset with CI_REQUIRE_DB fails", "", "1", "fatal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DATABASE_URL", tt.dburl)
			t.Setenv("CI_REQUIRE_DB", tt.require)
			if tt.want == "value" {
				if got := DatabaseURL(t); got != tt.dburl {
					t.Fatalf("got %q", got)
				}
				return
			}
			ft := &fakeTB{TB: t}
			done := make(chan struct{})
			go func() { // Skip/Fatal call runtime.Goexit, so run in a goroutine
				defer close(done)
				DatabaseURL(ft)
			}()
			<-done
			if tt.want == "skip" && !ft.skipped || tt.want == "fatal" && !ft.failed {
				t.Fatalf("want %s: skipped=%v failed=%v", tt.want, ft.skipped, ft.failed)
			}
		})
	}
}

type fakeTB struct {
	testing.TB
	skipped, failed bool
}

func (f *fakeTB) Helper()      {}
func (f *fakeTB) Skip(...any)  { f.skipped = true; runtimeGoexit() }
func (f *fakeTB) Fatal(...any) { f.failed = true; runtimeGoexit() }

func runtimeGoexit() { runtime.Goexit() }
