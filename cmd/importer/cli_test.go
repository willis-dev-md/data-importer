package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/store"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

const secret = "s3cr3t-token-VALUE-4711"

type cli struct {
	t    *testing.T
	bin  string
	url  string
	env  []string
	logs strings.Builder // everything the binary printed
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "importer")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// do runs the real binary; extra env entries override the defaults.
func (c *cli) do(extraEnv []string, args ...string) (stdout, stderr string, code int) {
	c.t.Helper()
	cmd := exec.Command(c.bin, args...)
	cmd.Env = append(append(os.Environ(), "DATABASE_URL="+c.url, "SOURCES_DIR="), c.env...)
	cmd.Env = append(cmd.Env, extraEnv...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code = 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			c.t.Fatalf("run %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	c.logs.WriteString(out.String() + errb.String())
	return out.String(), errb.String(), code
}

func (c *cli) expect(extraEnv []string, wantCode int, wantOut string, args ...string) (string, string) {
	c.t.Helper()
	o, e, code := c.do(extraEnv, args...)
	if code != wantCode {
		c.t.Fatalf("importer %v: exit %d, want %d\nstdout: %s\nstderr: %s", args, code, wantCode, o, e)
	}
	if !strings.Contains(o+e, wantOut) {
		c.t.Fatalf("importer %v: output lacks %q\nstdout: %s\nstderr: %s", args, wantOut, o, e)
	}
	return o, e
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func count(t *testing.T, pool *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCLIEndToEnd(t *testing.T) {
	repo := filepath.Join("..", "..")
	absRepo, _ := filepath.Abs(repo)
	schema := filepath.Join(absRepo, "canonical-schema.yaml")
	fixture := filepath.Join(absRepo, "testdata", "ons", "l55o.csv")

	c := &cli{t: t, bin: buildBinary(t), url: testutil.NewDatabaseURL(t), env: []string{"ONS_TOKEN=" + secret}}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, c.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	dir := t.TempDir()
	src := readFile(t, filepath.Join(repo, "docs", "examples", "ons-l55o.source.yaml"))
	src = regexp.MustCompile(`(?m)^location:.*$`).ReplaceAllString(src, "location: file://"+fixture)
	src += "auth: { type: bearer, token_env: ONS_TOKEN }\n"
	writeFile(t, dir, "ons-l55o.source.yaml", src)
	writeFile(t, dir, "ons-l55o.mapping.yaml", readFile(t, filepath.Join(repo, "docs", "examples", "ons-l55o.mapping.yaml")))

	c.expect(nil, 0, "version 1", "migrate", "up")

	// apply: creates the source and mapping v1
	c.expect(nil, 0, "ons-l55o: source created, mapping v1 created", "sources", "apply", "--dir", dir, "--schema", schema)
	snapshot := func() string {
		var s string
		err := pool.QueryRow(ctx, `SELECT (SELECT string_agg(id||':'||xmin::text||':'||coalesce(active_mapping_version,0), ',') FROM sources) || '|' ||
		  (SELECT string_agg(source_id||':'||version||':'||xmin::text, ',') FROM mappings)`).Scan(&s)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()

	// re-apply with unchanged files (dir via SOURCES_DIR): nothing changes
	c.expect([]string{"SOURCES_DIR=" + dir}, 0, "ons-l55o: source unchanged, mapping v1 unchanged", "sources", "apply", "--schema", schema)
	if after := snapshot(); after != before {
		t.Fatalf("unchanged re-apply changed the database: %s -> %s", before, after)
	}
	if n := count(t, pool, `SELECT count(*) FROM mappings`); n != 1 {
		t.Fatalf("mappings = %d", n)
	}

	// run twice: 642 inserted, then 642 unchanged
	o, _ := c.expect(nil, 0, "source=ons-l55o status=succeeded inserted=642 updated=0 unchanged=0 failed=0 run=1 duration=", "run", "--source", "ons-l55o")
	if strings.Count(o, "\n") != 1 {
		t.Errorf("summary must be one line, got %q", o)
	}
	c.expect(nil, 0, "status=succeeded inserted=0 updated=0 unchanged=642 failed=0 run=2 duration=", "run", "--source", "ons-l55o")
	if n := count(t, pool, `SELECT count(*) FROM raw_records WHERE source_id = 'ons-l55o'`); n != 642 {
		t.Fatalf("raw_records = %d, want 642", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM import_runs WHERE status = 'succeeded'`); n != 2 {
		t.Fatalf("succeeded runs = %d", n)
	}

	// dry-run writes nothing
	c.expect(nil, 0, "status=succeeded inserted=0 updated=0 unchanged=0 failed=0 run=dry-run", "run", "--source", "ons-l55o", "--dry-run")
	if n := count(t, pool, `SELECT count(*) FROM import_runs`); n != 2 {
		t.Fatalf("dry run wrote a run row (%d runs)", n)
	}

	// usage errors: exit 2, no stack trace
	c.expect(nil, 2, "usage: importer run", "run")
	c.expect([]string{"DATABASE_URL="}, 2, "DATABASE_URL is not set", "run", "--source", "ons-l55o")
	c.expect([]string{"DATABASE_URL="}, 2, "DATABASE_URL is not set", "sources", "apply", "--dir", dir, "--schema", schema)
	c.expect(nil, 2, "no directory: pass --dir or set SOURCES_DIR", "sources", "apply", "--schema", schema)
	c.expect(nil, 2, "not a readable directory", "sources", "apply", "--dir", filepath.Join(dir, "nope"), "--schema", schema)
	c.expect(nil, 2, "canonical schema", "sources", "apply", "--dir", dir, "--schema", filepath.Join(dir, "missing.yaml"))
	c.expect(nil, 2, "usage: importer sources apply", "sources")
	c.expect(nil, 2, "usage: importer run", "run", "--source", "x", "extra")
	c.expect([]string{"ONS_TOKEN="}, 2, "environment variable ONS_TOKEN (auth.token_env) is not set or empty", "run", "--source", "ons-l55o")
	if n := count(t, pool, `SELECT count(*) FROM import_runs`); n != 2 {
		t.Fatalf("usage errors created runs (%d)", n)
	}

	// exit 1: unknown source
	c.expect(nil, 1, "not found", "run", "--source", "no-such-source")

	// exit 3: another session holds the source lock
	st := store.New(pool)
	unlock, ok, err := st.TryAdvisoryLock(ctx, "ons-l55o")
	if err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	c.expect(nil, 3, "import already running for this source", "run", "--source", "ons-l55o")
	unlock()
	if n := count(t, pool, `SELECT count(*) FROM import_runs`); n != 2 {
		t.Fatalf("already-running created a run (%d runs)", n)
	}

	// abandoned run: a stale 'running' row is closed at the start of the next run
	var stale int64
	if err := pool.QueryRow(ctx, `INSERT INTO import_runs (source_id, status) VALUES ('ons-l55o','running') RETURNING id`).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	c.expect(nil, 0, "marked 1 abandoned run(s) failed", "run", "--source", "ons-l55o")
	var status, errText string
	var finished bool
	if err := pool.QueryRow(ctx, `SELECT status, error, finished_at IS NOT NULL FROM import_runs WHERE id = $1`, stale).Scan(&status, &errText, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errText != "abandoned (process died)" || !finished {
		t.Fatalf("stale run: %s %q %v", status, errText, finished)
	}

	// exit 1: a failing run (source file missing) still prints its summary
	writeFile(t, dir, "gone.source.yaml", "id: gone\nkind: csv\nlocation: "+filepath.Join(dir, "missing.csv")+"\nnatural_key: [id]\n")
	c.expect(nil, 0, "gone: source created, mapping none", "sources", "apply", "--dir", dir, "--schema", schema)
	o, _ = c.expect(nil, 1, "no such file", "run", "--source", "gone")
	if !strings.Contains(o, "source=gone status=failed inserted=0") {
		t.Errorf("failed run summary missing: %q", o)
	}

	// changed mapping: new version 2, activated; source row not rewritten
	writeFile(t, dir, "ons-l55o.mapping.yaml", readFile(t, filepath.Join(dir, "ons-l55o.mapping.yaml"))+"# comment only\n")
	c.expect(nil, 0, "mapping v1 unchanged", "sources", "apply", "--dir", dir, "--schema", schema) // comments are not content
	changed := strings.Replace(readFile(t, filepath.Join(dir, "ons-l55o.mapping.yaml")), "cpih_annual_rate", "cpih_rate", 1)
	writeFile(t, dir, "ons-l55o.mapping.yaml", changed)
	c.expect(nil, 0, "ons-l55o: source unchanged, mapping v2 created", "sources", "apply", "--dir", dir, "--schema", schema)
	if v := count(t, pool, `SELECT active_mapping_version FROM sources WHERE id = 'ons-l55o'`); v != 2 {
		t.Fatalf("active version = %d", v)
	}

	// invalid files abort with nothing written
	bad := t.TempDir()
	writeFile(t, bad, "fine.source.yaml", "id: fine\nkind: csv\nlocation: /x.csv\nnatural_key: [id]\n")
	writeFile(t, bad, "fine.mapping.yaml", "source: fine\nfields:\n  nope: {const: 1}\n")
	o, _ = c.expect(nil, 1, "nothing was written", "sources", "apply", "--dir", bad, "--schema", schema)
	if !strings.Contains(o, "fine.mapping.yaml: fields.nope: UNKNOWN_FIELD") {
		t.Errorf("stdout %q", o)
	}
	writeFile(t, bad, "fine.mapping.yaml", readFile(t, filepath.Join(dir, "ons-l55o.mapping.yaml")))
	o, _ = c.expect(nil, 1, "nothing was written", "sources", "apply", "--dir", bad, "--schema", schema)
	if !strings.Contains(o, "SOURCE_MISMATCH") {
		t.Errorf("stdout %q", o)
	}
	writeFile(t, bad, "broken.source.yaml", "id: broken\n")
	c.expect(nil, 1, "broken.source.yaml", "sources", "apply", "--dir", bad, "--schema", schema)
	if n := count(t, pool, `SELECT count(*) FROM sources WHERE id IN ('fine','broken')`); n != 0 {
		t.Fatalf("invalid apply wrote %d sources", n)
	}

	// secrets: never printed, never stored
	if strings.Contains(c.logs.String(), secret) {
		t.Errorf("secret appears in the CLI output:\n%s", c.logs.String())
	}
	var cfg string
	if err := pool.QueryRow(ctx, `SELECT config::text FROM sources WHERE id = 'ons-l55o'`).Scan(&cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg, secret) || !strings.Contains(cfg, "ONS_TOKEN") {
		t.Errorf("stored config must hold the variable name only: %s", cfg)
	}
	var everything string
	err = pool.QueryRow(ctx, `SELECT (SELECT string_agg(t::text, '') FROM sources t) || (SELECT string_agg(t::text, '') FROM mappings t) ||
	  (SELECT string_agg(t::text, '') FROM import_runs t) || (SELECT coalesce(string_agg(t::text, ''), '') FROM import_errors t)`).Scan(&everything)
	if err != nil || strings.Contains(everything, secret) {
		t.Errorf("secret stored in the database (err %v)", err)
	}
}

func TestAuthClientSendsHeaderToSourceHostOnly(t *testing.T) {
	var got http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer srv.Close()
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer other.Close()

	for _, tc := range []struct{ header, value string }{{"Authorization", "Bearer abc"}, {"X-Api-Key", "abc"}} {
		c := authClient(srv.Client().Transport, srv.URL+"/data.csv", tc.header, tc.value)
		got = nil
		if _, err := c.Get(srv.URL + "/data.csv"); err != nil {
			t.Fatal(err)
		}
		if got.Get(tc.header) != tc.value {
			t.Errorf("%s = %q, want %q", tc.header, got.Get(tc.header), tc.value)
		}
		// A request to a different host (e.g. after a redirect) must not carry the secret.
		got = nil
		oc := authClient(other.Client().Transport, srv.URL+"/data.csv", tc.header, tc.value)
		if _, err := oc.Get(other.URL + "/x"); err != nil {
			t.Fatal(err)
		}
		if got.Get(tc.header) != "" {
			t.Errorf("%s leaked to another host: %q", tc.header, got.Get(tc.header))
		}
	}
}
