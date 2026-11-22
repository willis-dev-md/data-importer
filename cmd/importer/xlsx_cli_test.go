package main

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/testutil"
)

// An xlsx source is applied and run through the real binary: kind xlsx is accepted by `sources apply`
// and the adapter registry used by `run` includes it.
func TestCLIXLSXSource(t *testing.T) {
	repo := filepath.Join("..", "..")
	absRepo, _ := filepath.Abs(repo)
	schema := filepath.Join(absRepo, "canonical-schema.yaml")
	fixture := filepath.Join(absRepo, "testdata", "boe", "baserate-trimmed.xlsx")

	c := &cli{t: t, bin: buildBinary(t), url: testutil.NewDatabaseURL(t)}
	pool, err := pgxpool.New(context.Background(), c.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	dir := t.TempDir()
	src := readFile(t, filepath.Join(repo, "docs", "examples", "boe-baserate.source.yaml"))
	src = regexp.MustCompile(`(?m)^location:.*$`).ReplaceAllString(src, "location: file://"+fixture)
	writeFile(t, dir, "boe-bank-rate.source.yaml", src)
	writeFile(t, dir, "boe-bank-rate.mapping.yaml", readFile(t, filepath.Join(repo, "docs", "examples", "boe-baserate.mapping.yaml")))

	c.expect(nil, 0, "version 2", "migrate", "up")
	c.expect(nil, 0, "boe-bank-rate: source created, mapping v1 created", "sources", "apply", "--dir", dir, "--schema", schema)
	c.expect(nil, 0, "source=boe-bank-rate status=succeeded inserted=854 updated=0 unchanged=0 failed=0 run=1", "run", "--source", "boe-bank-rate")
	// The BoE example lists content_hash: the same file closes the run as unchanged_file (task 014).
	c.expect(nil, 0, "status=unchanged_file inserted=0 updated=0 unchanged=0 failed=0 run=2", "run", "--source", "boe-bank-rate")
	c.expect(nil, 0, "status=succeeded inserted=0 updated=0 unchanged=854 failed=0 run=3", "reparse", "--source", "boe-bank-rate")
	if n := count(t, pool, `SELECT count(*) FROM raw_records WHERE source_id = 'boe-bank-rate'`); n != 854 {
		t.Fatalf("raw_records = %d, want 854", n)
	}

	// A broken xlsx config is reported with its field path, not as "unsupported kind".
	writeFile(t, dir, "boe-bank-rate.source.yaml", regexp.MustCompile(`(?m)^  header: false.*$`).ReplaceAllString(src, "  header: false\n  sheet_index: 1"))
	c.expect(nil, 1, "xlsx: set exactly one of sheet, sheet_index and sheets", "sources", "apply", "--dir", dir, "--schema", schema)
}
