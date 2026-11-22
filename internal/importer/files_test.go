package importer_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/source"
	"github.com/willis-dev-md/data-importer/internal/store"
)

const lastMod = "Mon, 06 May 2024 07:08:09 GMT"

// fileServer is an https server that counts what it is asked and what it sends.
type fileServer struct {
	*httptest.Server
	mu          sync.Mutex
	body        string
	etag        string
	honor       bool // answer 304 to a matching If-None-Match
	requests    int
	bodies      int
	conditional []string // "If-None-Match|If-Modified-Since" of each request
}

func newFileServer(t *testing.T, body string) *fileServer {
	t.Helper()
	fsrv := &fileServer{body: body, etag: `W/"v1"`, honor: true}
	fsrv.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fsrv.mu.Lock()
		defer fsrv.mu.Unlock()
		fsrv.requests++
		fsrv.conditional = append(fsrv.conditional, r.Header.Get("If-None-Match")+"|"+r.Header.Get("If-Modified-Since"))
		if fsrv.honor && r.Header.Get("If-None-Match") == fsrv.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fsrv.bodies++
		w.Header().Set("ETag", fsrv.etag)
		w.Header().Set("Last-Modified", lastMod)
		_, _ = io.WriteString(w, fsrv.body)
	}))
	t.Cleanup(fsrv.Close)
	return fsrv
}

func (s *fileServer) set(body, etag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body, s.etag = body, etag
}

func (s *fileServer) counts() (requests, bodies int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests, s.bodies
}

func csvBody(rows ...string) string { return "id,name\n" + strings.Join(rows, "\n") + "\n" }

type fileEnv struct {
	env
	dir  string
	fs   *store.DirFileStore
	deps importer.Deps
}

func newFileEnv(t *testing.T, cfg *config.Source, a source.Adapter) fileEnv {
	t.Helper()
	e := newEnv(t)
	e.addSource(t, cfg)
	dir := filepath.Join(t.TempDir(), "files")
	fs, err := store.NewDirFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fileEnv{env: e, dir: dir, fs: fs, deps: importer.Deps{Store: e.st, Adapters: map[string]source.Adapter{"csv": a}, Files: fs}}
}

func fileCfg(location string, detection ...string) *config.Source {
	return &config.Source{ID: "s1", Kind: "csv", Location: location, NaturalKey: []string{"id"}, ChangeDetection: detection}
}

func (f fileEnv) run(t *testing.T, opts importer.Options) importer.RunResult {
	t.Helper()
	res, err := importer.Run(context.Background(), f.deps, "s1", opts)
	if err != nil {
		t.Fatalf("run: %v (result %+v)", err, res)
	}
	return res
}

func (f fileEnv) files(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM source_files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f fileEnv) onDisk(t *testing.T) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(f.dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func (f fileEnv) run1(t *testing.T, id int64) store.Run {
	t.Helper()
	r, err := f.st.GetRun(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestChangeDetectionConditionalFetch(t *testing.T) {
	ctx := context.Background()
	body1 := csvBody("1,Ann", "2,Bob", "3,Cy")
	srv := newFileServer(t, body1)
	f := newFileEnv(t, fileCfg(srv.URL, "etag", "last_modified", "content_hash"),
		source.NewCSV(source.FetchOptions{Client: srv.Client()}))

	// 1. First run: no validators yet, full download, file kept.
	r1 := f.run(t, importer.Options{})
	if r1.Status != "succeeded" || r1.Inserted != 3 {
		t.Fatalf("run 1 = %+v", r1)
	}
	run1 := f.run1(t, r1.RunID)
	if run1.FileID == nil || f.files(t) != 1 || f.onDisk(t) != 1 {
		t.Fatalf("file id %v, rows %d, on disk %d", run1.FileID, f.files(t), f.onDisk(t))
	}
	if srv.conditional[0] != "|" {
		t.Fatalf("first request was conditional: %q", srv.conditional[0])
	}
	var etag, ref string
	var size int64
	var lm *string
	if err := f.pool.QueryRow(ctx, `SELECT etag, size_bytes, to_char(last_modified AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS'), storage_ref FROM source_files`).Scan(&etag, &size, &lm, &ref); err != nil {
		t.Fatal(err)
	}
	if etag != `W/"v1"` || size != int64(len(body1)) || lm == nil || *lm != "2024-05-06T07:08:09" || !strings.HasPrefix(ref, "dir:") {
		t.Fatalf("source_files row: %q %d %v %q", etag, size, lm, ref)
	}

	// 2. Server answers 304: unchanged_file, nothing parsed, nothing written, no body sent.
	var rawBefore string
	if err := f.pool.QueryRow(ctx, `SELECT string_agg(natural_key||last_run_id::text||imported_at::text, ',' ORDER BY natural_key) FROM raw_records`).Scan(&rawBefore); err != nil {
		t.Fatal(err)
	}
	reqBefore, bodiesBefore := srv.counts()
	r2 := f.run(t, importer.Options{})
	if r2.Status != "unchanged_file" || r2.Inserted+r2.Updated+r2.Unchanged+r2.Failed+r2.Total != 0 || r2.Shape != nil || r2.SourceMeta != nil {
		t.Fatalf("run 2 = %+v", r2)
	}
	req, bodies := srv.counts()
	if req != reqBefore+1 || bodies != bodiesBefore {
		t.Fatalf("requests %d->%d bodies %d->%d", reqBefore, req, bodiesBefore, bodies)
	}
	if got, want := srv.conditional[req-1], `W/"v1"|`+lastMod; got != want {
		t.Fatalf("conditional headers = %q, want %q (exactly as received)", got, want)
	}
	run2 := f.run1(t, r2.RunID)
	if run2.Status != "unchanged_file" || run2.FileID == nil || *run2.FileID != *run1.FileID || run2.FinishedAt == nil ||
		run2.Inserted+run2.Updated+run2.Unchanged+run2.Failed+run2.Tombstoned != 0 || run2.Error != nil {
		t.Fatalf("run 2 row = %+v", run2)
	}
	var rawAfter string
	if err := f.pool.QueryRow(ctx, `SELECT string_agg(natural_key||last_run_id::text||imported_at::text, ',' ORDER BY natural_key) FROM raw_records`).Scan(&rawAfter); err != nil {
		t.Fatal(err)
	}
	if rawAfter != rawBefore || f.files(t) != 1 || f.onDisk(t) != 1 {
		t.Fatalf("records or files changed by an unchanged run: %q -> %q", rawBefore, rawAfter)
	}
	var nerr int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM import_errors`).Scan(&nerr); err != nil || nerr != 0 {
		t.Fatalf("import_errors = %d, %v", nerr, err)
	}

	// 3. Server ignores the conditional request and sends the same bytes with a new ETag:
	// the SHA-256 is identical, so the run is unchanged_file and the file is not stored twice.
	srv.mu.Lock()
	srv.honor, srv.etag = false, `"v2"`
	srv.mu.Unlock()
	r3 := f.run(t, importer.Options{})
	if r3.Status != "unchanged_file" || r3.Total != 0 || f.files(t) != 1 || f.onDisk(t) != 1 {
		t.Fatalf("run 3 = %+v, files %d/%d", r3, f.files(t), f.onDisk(t))
	}
	if _, bodies := srv.counts(); bodies != bodiesBefore+1 {
		t.Fatalf("bodies = %d", bodies)
	}
	// ... and the validators of the latest response are the ones used next.
	if err := f.pool.QueryRow(ctx, `SELECT etag FROM source_files`).Scan(&etag); err != nil || etag != `"v2"` {
		t.Fatalf("etag = %q, %v", etag, err)
	}
	srv.mu.Lock()
	srv.honor = true
	srv.mu.Unlock()
	if r := f.run(t, importer.Options{}); r.Status != "unchanged_file" {
		t.Fatalf("run 3b = %+v", r)
	}
	if got := srv.conditional[len(srv.conditional)-1]; got != `"v2"|`+lastMod {
		t.Fatalf("conditional headers after validator refresh = %q", got)
	}

	// 4. Changed content: parsed, new file kept.
	body2 := csvBody("1,Ann", "2,Bobby", "3,Cy", "4,Di")
	srv.set(body2, `"v3"`)
	r4 := f.run(t, importer.Options{})
	if r4.Status != "succeeded" || r4.Inserted != 1 || r4.Updated != 1 || r4.Unchanged != 2 || f.files(t) != 2 || f.onDisk(t) != 2 {
		t.Fatalf("run 4 = %+v, files %d/%d", r4, f.files(t), f.onDisk(t))
	}

	// 5. Back to the first bytes: differs from the LAST successful run's file, so it is parsed;
	// the file of run 1 is reused (still two rows).
	srv.set(body1, `"v4"`)
	r5 := f.run(t, importer.Options{})
	if r5.Status != "succeeded" || r5.Updated != 1 || r5.Unchanged != 2 || f.files(t) != 2 || f.onDisk(t) != 2 {
		t.Fatalf("run 5 = %+v, files %d/%d", r5, f.files(t), f.onDisk(t))
	}
	if run5 := f.run1(t, r5.RunID); run5.FileID == nil || *run5.FileID != *run1.FileID {
		t.Fatalf("run 5 file = %v, want the first file %d", run5.FileID, *run1.FileID)
	}
}

func TestChangeDetectionOnlyWhenListed(t *testing.T) {
	body := csvBody("1,Ann", "2,Bob")
	srv := newFileServer(t, body)
	f := newFileEnv(t, fileCfg(srv.URL), source.NewCSV(source.FetchOptions{Client: srv.Client()}))
	f.run(t, importer.Options{})
	r2 := f.run(t, importer.Options{})
	// Not listed: no conditional request, and an identical file is parsed (and counted unchanged).
	if r2.Status != "succeeded" || r2.Unchanged != 2 || r2.Inserted != 0 {
		t.Fatalf("run 2 = %+v", r2)
	}
	for i, c := range srv.conditional {
		if c != "|" {
			t.Errorf("request %d was conditional: %q", i, c)
		}
	}
	if f.files(t) != 1 || f.onDisk(t) != 1 {
		t.Fatalf("files %d/%d, want one (identical bytes are kept once)", f.files(t), f.onDisk(t))
	}

	// Only content_hash listed: always downloads, no conditional headers, skips by hash.
	f2 := newFileEnv(t, fileCfg(srv.URL, "content_hash"), source.NewCSV(source.FetchOptions{Client: srv.Client()}))
	f2.run(t, importer.Options{})
	if r := f2.run(t, importer.Options{}); r.Status != "unchanged_file" {
		t.Fatalf("content_hash only: %+v", r)
	}
	if c := srv.conditional[len(srv.conditional)-1]; c != "|" {
		t.Errorf("content_hash only sent %q", c)
	}
}

func TestChangeDetectionFailedRunDoesNotCount(t *testing.T) {
	good := csvBody("1,Ann")
	srv := newFileServer(t, good)
	cfg := fileCfg(srv.URL, "etag", "content_hash")
	f := newFileEnv(t, cfg, source.NewCSV(source.FetchOptions{Client: srv.Client()}))
	f.run(t, importer.Options{})

	// A changed file whose run aborts (bad rows) is not the "last successful" file.
	srv.set(csvBody("1,Ann", "2"), `W/"v9"`)
	cfg.MaxErrorRatio = 0
	f.addSource(t, cfg)
	res, err := importer.Run(context.Background(), f.deps, "s1", importer.Options{})
	if err == nil || res.Status != "aborted" {
		t.Fatalf("want aborted run, got %+v, %v", res, err)
	}
	// Same bad bytes again: still not unchanged (the last SUCCESSFUL file is the first one).
	srv.mu.Lock()
	srv.honor = false
	srv.mu.Unlock()
	res, err = importer.Run(context.Background(), f.deps, "s1", importer.Options{})
	if err == nil || res.Status != "aborted" {
		t.Fatalf("second bad run = %+v, %v", res, err)
	}
	if last := srv.conditional[len(srv.conditional)-1]; last != `W/"v1"|` {
		t.Fatalf("conditional headers = %q, want the successful run's etag", last)
	}
}

func TestFetchErrorsAreRunFailures(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", 500) }))
	defer srv.Close()
	f := newFileEnv(t, fileCfg(srv.URL, "etag"), source.NewCSV(source.FetchOptions{Client: srv.Client()}))
	res, err := importer.Run(context.Background(), f.deps, "s1", importer.Options{})
	if err == nil || res.Status != "failed" || !strings.Contains(res.Error, "500") {
		t.Fatalf("res %+v err %v", res, err)
	}
	if f.files(t) != 0 || f.countRaw(t) != 0 {
		t.Fatal("something was stored for a failed fetch")
	}
	if !f.lockFree(t) {
		t.Fatal("lock not released")
	}
}

func TestFileSizeLimitStillApplies(t *testing.T) {
	big := csvBody(strings.Repeat("1,Ann\n", 5000))
	srv := newFileServer(t, big)
	f := newFileEnv(t, fileCfg(srv.URL), source.NewCSV(source.FetchOptions{Client: srv.Client(), MaxBytes: 1000}))
	res, err := importer.Run(context.Background(), f.deps, "s1", importer.Options{})
	if err == nil || res.Status != "failed" || !errors.Is(err, source.ErrTooLarge) {
		t.Fatalf("res %+v err %v", res, err)
	}
	if f.files(t) != 0 || f.onDisk(t) != 0 || f.countRaw(t) != 0 {
		t.Fatalf("oversized file left traces: rows %d, disk %d, raw %d", f.files(t), f.onDisk(t), f.countRaw(t))
	}
	// A large file within the limit streams through: many rows, stored once, parsed.
	var rows []string
	for i := 0; i < 20000; i++ {
		rows = append(rows, fmt.Sprintf("%d,name%d", i, i))
	}
	srv.set(csvBody(rows...), `"big"`)
	f.deps.Adapters["csv"] = source.NewCSV(source.FetchOptions{Client: srv.Client()})
	res = f.run(t, importer.Options{BatchSize: 1000})
	if res.Inserted != 20000 || f.files(t) != 1 {
		t.Fatalf("res %+v files %d", res, f.files(t))
	}
}

// reparseAdapter wraps the csv adapter and lets a test change "the pipeline": edit stands
// in for a new adapter version or config that changes some records.
type reparseAdapter struct {
	*source.CSVAdapter
	edit    func(*source.Record)
	opens   int
	readers int
}

func (a *reparseAdapter) Open(ctx context.Context, cfg *config.Source) (source.Stream, error) {
	a.opens++
	return a.CSVAdapter.Open(ctx, cfg)
}

func (a *reparseAdapter) OpenReader(ctx context.Context, cfg *config.Source, r io.Reader) (source.Stream, error) {
	a.readers++
	s, err := a.CSVAdapter.OpenReader(ctx, cfg, r)
	if err != nil || a.edit == nil {
		return s, err
	}
	return editStream{s, a.edit}, nil
}

type editStream struct {
	source.Stream
	edit func(*source.Record)
}

func (e editStream) Next() (source.Item, bool) {
	it, ok := e.Stream.Next()
	if ok && it.Record != nil {
		e.edit(it.Record)
	}
	return it, ok
}

func TestReparse(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	loc := filepath.Join(dir, "data.csv")
	body := csvBody("1,Ann", "2,Bob", "3,Cy", "4,Di")
	if err := os.WriteFile(loc, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ad := &reparseAdapter{CSVAdapter: source.NewCSV(source.FetchOptions{})}
	f := newFileEnv(t, fileCfg(loc, "content_hash"), ad)
	r1 := f.run(t, importer.Options{})
	if r1.Inserted != 4 {
		t.Fatalf("run 1 = %+v", r1)
	}
	// The source is gone: a reparse must not need it (no network, no original file).
	if err := os.Remove(loc); err != nil {
		t.Fatal(err)
	}
	opens := ad.opens

	// Unchanged pipeline: every record is unchanged; a new manual run; same file; no change detection.
	r2 := f.run(t, importer.Options{Reparse: &importer.ReparseOptions{}})
	if r2.Status != "succeeded" || r2.Unchanged != 4 || r2.Inserted != 0 || r2.Updated != 0 || r2.Total != 4 || r2.RunID == r1.RunID {
		t.Fatalf("reparse = %+v", r2)
	}
	run2 := f.run1(t, r2.RunID)
	run1 := f.run1(t, r1.RunID)
	if run2.Trigger != "manual" || run2.FileID == nil || *run2.FileID != *run1.FileID {
		t.Fatalf("reparse run row = %+v", run2)
	}
	if ad.opens != opens {
		t.Fatal("reparse used Open (which fetches)")
	}
	if f.files(t) != 1 {
		t.Fatalf("source_files rows = %d", f.files(t))
	}

	// Pipeline change touching two records: exactly those are updated, nothing else.
	ad.edit = func(r *source.Record) {
		if r.Fields["id"] == "2" || r.Fields["id"] == "4" {
			r.Fields["name"] = fmt.Sprint(r.Fields["name"], "!")
		}
	}
	r3 := f.run(t, importer.Options{Reparse: &importer.ReparseOptions{RunID: r1.RunID}})
	if r3.Updated != 2 || r3.Unchanged != 2 || r3.Inserted != 0 || r3.Failed != 0 {
		t.Fatalf("changed reparse = %+v", r3)
	}
	rec, err := f.st.GetRaw(ctx, "s1", `["2"]`)
	if err != nil || rec.Payload["name"] != "Bob!" || rec.LastRunID != r3.RunID {
		t.Fatalf("record 2 = %+v, %v", rec, err)
	}
	// A new field on some records is the same story; keys that are no longer produced stay (no tombstoning here).
	ad.edit = func(r *source.Record) {
		if r.Fields["id"] == "1" {
			r.NaturalKey = `["one"]`
		}
	}
	r4 := f.run(t, importer.Options{Reparse: &importer.ReparseOptions{}})
	if r4.Inserted != 1 || r4.Updated != 2 || r4.Unchanged != 1 || f.countRaw(t) != 5 { // 2 and 4 lose the "!" again; 3 is untouched
		t.Fatalf("key change reparse = %+v, raw %d", r4, f.countRaw(t))
	}
}

func TestReparseDuplicateKeysAndLock(t *testing.T) {
	dir := t.TempDir()
	loc := filepath.Join(dir, "d.csv")
	if err := os.WriteFile(loc, []byte(csvBody("1,Ann", "2,Bob", "1,Again")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := fileCfg(loc)
	cfg.MaxErrorRatio = 1
	f := newFileEnv(t, cfg, source.NewCSV(source.FetchOptions{}))
	r1 := f.run(t, importer.Options{})
	if r1.Inserted != 2 || r1.Duplicates != 1 || r1.Failed != 1 {
		t.Fatalf("run 1 = %+v", r1)
	}
	// Reparse goes through the same path: the duplicate is reported again, not merged.
	r2 := f.run(t, importer.Options{Reparse: &importer.ReparseOptions{}})
	if r2.Unchanged != 2 || r2.Duplicates != 1 || r2.Failed != 1 || len(r2.DuplicateSamples) != 1 {
		t.Fatalf("reparse = %+v", r2)
	}
	rec, err := f.st.GetRaw(context.Background(), "s1", `["1"]`)
	if err != nil || rec.Payload["name"] != "Ann" {
		t.Fatalf("first record must win: %+v, %v", rec, err)
	}
	// The source lock is taken: a held lock means ErrAlreadyRunning and no run row.
	unlock, ok, err := f.st.TryAdvisoryLock(context.Background(), "s1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer unlock()
	runs := f.countRuns(t)
	_, err = importer.Run(context.Background(), f.deps, "s1", importer.Options{Reparse: &importer.ReparseOptions{}})
	if !errors.Is(err, importer.ErrAlreadyRunning) || f.countRuns(t) != runs {
		t.Fatalf("err = %v, runs %d -> %d", err, runs, f.countRuns(t))
	}
}

func TestReparseErrors(t *testing.T) {
	dir := t.TempDir()
	loc := filepath.Join(dir, "d.csv")
	if err := os.WriteFile(loc, []byte(csvBody("1,Ann")), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFileEnv(t, fileCfg(loc), source.NewCSV(source.FetchOptions{}))
	reparse := func(id int64) (importer.RunResult, error) {
		return importer.Run(context.Background(), f.deps, "s1", importer.Options{Reparse: &importer.ReparseOptions{RunID: id}})
	}
	// Nothing stored yet: an error and no run row.
	if res, err := reparse(0); !errors.Is(err, store.ErrNoFile) || res.RunID != 0 || f.countRuns(t) != 0 {
		t.Fatalf("no file: %+v, %v, runs %d", res, err, f.countRuns(t))
	}
	r1 := f.run(t, importer.Options{})
	for _, id := range []int64{999, r1.RunID + 100} {
		if _, err := reparse(id); !errors.Is(err, store.ErrNoFile) {
			t.Errorf("run %d: %v", id, err)
		}
	}
	// A run of another source cannot be used.
	f.addSource(t, &config.Source{ID: "s2", Kind: "csv", Location: loc, NaturalKey: []string{"id"}})
	other, err := importer.Run(context.Background(), f.deps, "s2", importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reparse(other.RunID); !errors.Is(err, store.ErrNoFile) {
		t.Errorf("foreign run: %v", err)
	}
	// Reparse needs a real run, a file store and a file adapter.
	if _, err := importer.Run(context.Background(), f.deps, "s1", importer.Options{DryRun: true, Reparse: &importer.ReparseOptions{}}); !errors.Is(err, importer.ErrNoFileStore) {
		t.Errorf("dry run: %v", err)
	}
	noFiles := f.deps
	noFiles.Files = nil
	if _, err := importer.Run(context.Background(), noFiles, "s1", importer.Options{Reparse: &importer.ReparseOptions{}}); !errors.Is(err, importer.ErrNoFileStore) {
		t.Errorf("no file store: %v", err)
	}
	if !f.lockFree(t) {
		t.Fatal("lock not released")
	}
}

func TestReparseCorruptOrMissingFile(t *testing.T) {
	dir := t.TempDir()
	loc := filepath.Join(dir, "d.csv")
	if err := os.WriteFile(loc, []byte(csvBody("1,Ann", "2,Bob")), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFileEnv(t, fileCfg(loc), source.NewCSV(source.FetchOptions{}))
	f.run(t, importer.Options{})
	var sha string
	if err := f.pool.QueryRow(context.Background(), `SELECT encode(sha256,'hex') FROM source_files`).Scan(&sha); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.dir, sha[:2], sha)
	rawBefore := f.countRaw(t)

	// Corrupt in a way that still parses as CSV: it must be refused, not imported.
	if err := os.WriteFile(path, []byte(csvBody("1,Ann", "2,Eve")), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := importer.Run(context.Background(), f.deps, "s1", importer.Options{Reparse: &importer.ReparseOptions{}})
	if !errors.Is(err, store.ErrFileCorrupt) || res.Status != "failed" || res.Updated != 0 || res.Inserted != 0 {
		t.Fatalf("corrupt: %+v, %v", res, err)
	}
	rec, err := f.st.GetRaw(context.Background(), "s1", `["2"]`)
	if err != nil || rec.Payload["name"] != "Bob" || f.countRaw(t) != rawBefore {
		t.Fatalf("corrupt file changed records: %+v %v", rec, err)
	}
	if strings.Contains(res.Error, "postgres") || strings.Contains(res.Error, f.dir) {
		t.Errorf("error leaks internals: %q", res.Error)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	res, err = importer.Run(context.Background(), f.deps, "s1", importer.Options{Reparse: &importer.ReparseOptions{}})
	if !errors.Is(err, store.ErrFileMissing) || res.Status != "failed" {
		t.Fatalf("missing: %+v, %v", res, err)
	}
	if strings.Contains(res.Error, f.dir) {
		t.Errorf("error leaks the directory: %q", res.Error)
	}
}

func TestFileRetentionConcurrentSources(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	fs, err := store.NewDirFileStore(filepath.Join(dir, "files"))
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	deps := importer.Deps{Store: e.st, Adapters: map[string]source.Adapter{"csv": source.NewCSV(source.FetchOptions{})}, Files: fs}
	for i := 0; i < n; i++ {
		loc := filepath.Join(dir, fmt.Sprintf("s%d.csv", i))
		// Half of the sources have identical bytes: one blob on disk, one source_files row each.
		content := csvBody(fmt.Sprintf("1,name%d", i%2), "2,x")
		if err := os.WriteFile(loc, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		e.addSource(t, &config.Source{ID: fmt.Sprintf("s%d", i), Kind: "csv", Location: loc, NaturalKey: []string{"id"}})
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := importer.Run(context.Background(), deps, fmt.Sprintf("s%d", i), importer.Options{})
			if err != nil || res.Inserted != 2 {
				errs <- fmt.Errorf("source s%d: %+v, %v", i, res, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var rows int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM source_files`).Scan(&rows); err != nil || rows != n {
		t.Fatalf("source_files rows = %d, %v; want %d", rows, err, n)
	}
	blobs := 0
	_ = filepath.WalkDir(filepath.Join(dir, "files"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			blobs++
		}
		return nil
	})
	if blobs != 2 {
		t.Fatalf("blobs on disk = %d, want 2", blobs)
	}
}

func TestDryRunKeepsNoFile(t *testing.T) {
	dir := t.TempDir()
	loc := filepath.Join(dir, "d.csv")
	if err := os.WriteFile(loc, []byte(csvBody("1,Ann")), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFileEnv(t, fileCfg(loc, "content_hash"), source.NewCSV(source.FetchOptions{}))
	res := f.run(t, importer.Options{DryRun: true})
	if res.Total != 1 || f.files(t) != 0 || f.onDisk(t) != 0 || f.countRuns(t) != 0 {
		t.Fatalf("dry run: %+v files %d/%d runs %d", res, f.files(t), f.onDisk(t), f.countRuns(t))
	}
}
