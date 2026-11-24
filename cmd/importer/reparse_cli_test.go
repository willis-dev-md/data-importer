package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/store"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

// TestCLIReparse drives `importer reparse` through the real binary: exit codes
// 0/1/2/3 as for `run`, no network or source file needed, files kept in FILE_STORE_DIR.
func TestCLIReparse(t *testing.T) {
	absRepo, _ := filepath.Abs(filepath.Join("..", ".."))
	schema := filepath.Join(absRepo, "canonical-schema.yaml")

	c := &cli{t: t, bin: buildBinary(t), url: testutil.NewDatabaseURL(t)}
	ctx := context.Background()
	c.expect(nil, 0, "version 3", "migrate", "up")
	pool, err := pgxpool.New(ctx, c.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	dir := t.TempDir()
	data := filepath.Join(dir, "data.csv")
	writeFile(t, dir, "data.csv", "id,name\n1,Ann\n2,Bob\n3,Cy\n")
	writeFile(t, dir, "d.source.yaml", "id: d\nkind: csv\nlocation: "+data+"\nnatural_key: [id]\nchange_detection: [content_hash]\n")
	c.expect(nil, 0, "d: source created", "sources", "apply", "--dir", dir, "--schema", schema)

	// usage errors: exit 2, nothing written
	c.expect(nil, 2, "usage: importer reparse", "reparse")
	c.expect(nil, 2, "usage: importer reparse", "reparse", "--source", "d", "extra")
	c.expect(nil, 2, "usage: importer reparse", "reparse", "--source", "d", "--run", "0")
	c.expect(nil, 2, "usage: importer reparse", "reparse", "--source", "d", "--run", "-3")
	c.expect(nil, 2, "invalid value", "reparse", "--source", "d", "--run", "abc")
	c.expect([]string{"DATABASE_URL="}, 2, "DATABASE_URL is not set", "reparse", "--source", "d")
	// An unusable FILE_STORE_DIR is a usage error before any database work, for run and reparse.
	notDir := filepath.Join(dir, "plain-file")
	writeFile(t, dir, "plain-file", "x")
	c.expect([]string{"FILE_STORE_DIR=" + notDir, "DATABASE_URL=postgres://nobody@127.0.0.1:1/none"}, 2, "FILE_STORE_DIR", "reparse", "--source", "d")
	c.expect([]string{"FILE_STORE_DIR=" + notDir, "DATABASE_URL=postgres://nobody@127.0.0.1:1/none"}, 2, "FILE_STORE_DIR", "run", "--source", "d")
	if n := count(t, pool, `SELECT count(*) FROM import_runs`); n != 0 {
		t.Fatalf("usage errors created runs (%d)", n)
	}

	// exit 1: nothing stored yet / unknown source; no run row
	c.expect(nil, 1, "no run with a stored file", "reparse", "--source", "d")
	c.expect(nil, 1, "not found", "reparse", "--source", "nope")
	if n := count(t, pool, `SELECT count(*) FROM import_runs`); n != 0 {
		t.Fatalf("failed lookups created runs (%d)", n)
	}

	// run once; the file lands in FILE_STORE_DIR under its SHA-256
	_, e := c.expect(nil, 0, "source=d status=succeeded inserted=3 updated=0 unchanged=0 failed=0 run=1", "run", "--source", "d")
	if want := "importer run: file store: " + c.files + "\n"; !strings.Contains(e, want) {
		t.Fatalf("stderr lacks the absolute file store path %q: %q", want, e)
	}
	c.expect(nil, 0, "source=d status=unchanged_file inserted=0 updated=0 unchanged=0 failed=0 run=2", "run", "--source", "d")
	var sha string
	if err := pool.QueryRow(ctx, `SELECT encode(sha256,'hex') FROM source_files`).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.files, sha[:2], sha)); err != nil {
		t.Fatalf("stored file: %v", err)
	}
	// The default location is data/files (relative): not used here, FILE_STORE_DIR wins.

	// reparse needs neither the source file nor the network
	if err := os.Remove(data); err != nil {
		t.Fatal(err)
	}
	c.expect([]string{"HTTPS_PROXY=http://127.0.0.1:1", "HTTP_PROXY=http://127.0.0.1:1"}, 0, "source=d status=succeeded inserted=0 updated=0 unchanged=3 failed=0 run=3", "reparse", "--source", "d")
	c.expect(nil, 0, "unchanged=3 failed=0 run=4", "reparse", "--source", "d", "--run", "1")
	if n := count(t, pool, `SELECT count(*) FROM import_runs WHERE trigger = 'manual' AND file_id IS NOT NULL`); n != 4 {
		t.Fatalf("runs with a file = %d, want 4", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM source_files`); n != 1 {
		t.Fatalf("source_files = %d", n)
	}
	c.expect(nil, 1, "does not exist or has no stored file", "reparse", "--source", "d", "--run", "9999")

	// a changed source config is the "pipeline change": renamed columns update every record
	writeFile(t, dir, "d.source.yaml", "id: d\nkind: csv\nlocation: "+data+"\nnatural_key: [id]\ncsv:\n  columns: [id, label]\n")
	c.expect(nil, 0, "d: source updated", "sources", "apply", "--dir", dir, "--schema", schema)
	c.expect(nil, 0, "inserted=0 updated=3 unchanged=0 failed=0 run=5", "reparse", "--source", "d")

	// a corrupt stored file is refused (exit 1) and nothing changes
	if err := os.WriteFile(filepath.Join(c.files, sha[:2], sha), []byte("id,name\n1,Eve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o, e := c.expect(nil, 1, "SHA-256 mismatch", "reparse", "--source", "d")
	if !strings.Contains(o, "status=failed") || strings.Contains(strings.TrimPrefix(e, "importer reparse: file store: "+c.files+"\n"), c.files) { // the info line names the directory; the error must not
		t.Errorf("corrupt output: %q / %q", o, e)
	}
	if n := count(t, pool, `SELECT count(*) FROM raw_records WHERE payload->>'label' = 'Eve'`); n != 0 {
		t.Fatal("corrupt file was imported")
	}

	// exit 3: the source lock is held elsewhere; no run row
	runs := count(t, pool, `SELECT count(*) FROM import_runs`)
	unlock, ok, err := store.New(pool).TryAdvisoryLock(ctx, "d")
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	c.expect(nil, 3, "import already running for this source", "reparse", "--source", "d")
	unlock()
	if n := count(t, pool, `SELECT count(*) FROM import_runs`); n != runs {
		t.Fatalf("already-running created a run (%d -> %d)", runs, n)
	}

	// a dry run keeps no file and does not need a usable FILE_STORE_DIR
	c.expect([]string{"FILE_STORE_DIR=" + notDir}, 1, "no such file", "run", "--source", "d", "--dry-run") // the source file is gone; the point is: no FILE_STORE_DIR usage error
}

// TestOpenFileStoreReportsAbsolutePath: the cwd-relative default is announced as an absolute path.
func TestOpenFileStoreReportsAbsolutePath(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	old := getenv
	defer func() { getenv = old }()
	for _, env := range []string{"", "rel-files"} {
		getenv = func(k string) string {
			if k == "FILE_STORE_DIR" {
				return env
			}
			return ""
		}
		var errb strings.Builder
		fs, code := openFileStore("run", &errb)
		if code != exitOK || fs == nil {
			t.Fatalf("code %d", code)
		}
		want := filepath.Join(wd, defaultFileStoreDir)
		if env != "" {
			want = filepath.Join(wd, env)
		}
		if r, _ := filepath.EvalSymlinks(want); !strings.Contains(errb.String(), "importer run: file store: ") || !filepath.IsAbs(strings.TrimSpace(strings.TrimPrefix(errb.String(), "importer run: file store: "))) ||
			(!strings.Contains(errb.String(), want) && !strings.Contains(errb.String(), r)) {
			t.Errorf("env %q: stderr %q, want absolute %q", env, errb.String(), want)
		}
	}
}
