package importer_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/source"
)

func zipWith(t *testing.T, member, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range []struct{ n, b string }{{"readme.txt", "hello"}, {member, body}} {
		w, err := zw.Create(e.n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, e.b)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// monthServer serves one zip per month path, with HEAD probing, ETags and 304s, and logs each request.
type monthServer struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte // path -> zip
	fail  map[string]int    // path -> forced status
	log   []string          // "METHOD path inm"
}

func newMonthServer(t *testing.T) *monthServer {
	t.Helper()
	m := &monthServer{files: map[string][]byte{}, fail: map[string]int{}}
	m.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.log = append(m.log, r.Method+" "+r.URL.Path+" "+r.Header.Get("If-None-Match"))
		if st, ok := m.fail[r.URL.Path]; ok {
			w.WriteHeader(st)
			return
		}
		z, ok := m.files[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sum := sha256.Sum256(z)
		etag := `"` + hex.EncodeToString(sum[:4]) + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write(z)
		}
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *monthServer) requests() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.log...)
}

func (m *monthServer) gets() (out []string) {
	for _, l := range m.requests() {
		if strings.HasPrefix(l, "GET ") {
			out = append(out, l)
		}
	}
	return out
}

func TestArchiveWithResolveAndChangeDetection(t *testing.T) {
	ctx := context.Background()
	srv := newMonthServer(t)
	now := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC) // newest candidate: 2026-10
	clock := func() time.Time { return now }
	const member = "Data  2026   final.csv"
	sept := zipWith(t, member, csvBody("1,Ann", "2,Bob"))
	srv.files["/f/prices-2026-09.zip"] = sept

	cfg := &config.Source{
		ID: "s1", Kind: "csv", Location: srv.URL + "/f/prices-{ym}.zip", NaturalKey: []string{"id"},
		Archive:         &config.Archive{Type: "zip", Member: `(?i)data\s+2026\s+final\.csv$`},
		Resolve:         map[string]config.Resolve{"ym": {Kind: "month", Layout: "2006-01", Start: "-1m", Back: 4}},
		ChangeDetection: []string{"etag", "content_hash"},
	}
	f := newFileEnv(t, cfg, source.NewCSV(source.FetchOptions{Client: srv.Client(), Now: clock}))

	// 1. Only September exists: it is chosen, the ARCHIVE is kept (its SHA-256), the member is parsed.
	r1 := f.run(t, importer.Options{})
	if r1.Status != "succeeded" || r1.Inserted != 2 {
		t.Fatalf("run 1: %+v", r1)
	}
	loc1 := srv.URL + "/f/prices-2026-09.zip"
	assertLocation(t, f.run1(t, r1.RunID).SourceMeta, loc1)
	var sha string
	var size int64
	if err := f.pool.QueryRow(ctx, `SELECT encode(sha256,'hex'), size_bytes FROM source_files`).Scan(&sha, &size); err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(sept); sha != hex.EncodeToString(sum[:]) || size != int64(len(sept)) {
		t.Errorf("source_files holds %s (%d bytes), want the archive's hash and size", sha, size)
	}
	rc, err := f.fs.Open(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); !bytes.Equal(b, sept) {
		t.Error("the stored file is not the archive")
	}
	_ = rc.Close()

	// 2. Same file again: the validators of the chosen URL give a 304, the run is unchanged_file and still says where it looked.
	r2 := f.run(t, importer.Options{})
	if r2.Status != "unchanged_file" || r2.Inserted+r2.Updated != 0 {
		t.Fatalf("run 2: %+v", r2)
	}
	assertLocation(t, f.run1(t, r2.RunID).SourceMeta, loc1)
	gets := srv.gets()
	if len(gets) != 2 || !strings.HasSuffix(gets[1], `"`) || !strings.Contains(gets[1], "2026-09.zip \"") {
		t.Errorf("run 2 should send the stored ETag to the chosen URL: %v", gets)
	}

	// 3. October appears: a different URL. September's validators must not be sent to it.
	oct := zipWith(t, member, csvBody("1,Ann", "2,Bob", "3,Cy"))
	srv.files["/f/prices-2026-10.zip"] = oct
	r3 := f.run(t, importer.Options{})
	if r3.Status != "succeeded" || r3.Inserted != 1 || r3.Unchanged != 2 {
		t.Fatalf("run 3: %+v", r3)
	}
	loc3 := srv.URL + "/f/prices-2026-10.zip"
	assertLocation(t, f.run1(t, r3.RunID).SourceMeta, loc3)
	gets = srv.gets()
	if last := gets[len(gets)-1]; last != "GET /f/prices-2026-10.zip " {
		t.Errorf("request for the new URL carried a validator: %q", last)
	}
	if f.files(t) != 2 {
		t.Errorf("source_files rows = %d, want 2 (one per archive)", f.files(t))
	}

	// 4. Reparse from the store with no network at all.
	srv.Close()
	rp := f.run(t, importer.Options{Reparse: &importer.ReparseOptions{}})
	if rp.Status != "succeeded" || rp.Total != 3 {
		t.Fatalf("reparse: %+v", rp)
	}
}

func TestResolveFailuresFailTheRun(t *testing.T) {
	srv := newMonthServer(t)
	clock := func() time.Time { return time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC) }
	srv.files["/f/prices-2026-09.zip"] = zipWith(t, "a.csv", csvBody("1,Ann"))
	cfg := &config.Source{
		ID: "s1", Kind: "csv", Location: srv.URL + "/f/prices-{ym}.zip", NaturalKey: []string{"id"},
		Archive: &config.Archive{Type: "zip", Member: `a\.csv$`},
		Resolve: map[string]config.Resolve{"ym": {Kind: "month", Layout: "2006-01", Start: "-1m", Back: 3}},
	}
	f := newFileEnv(t, cfg, source.NewCSV(source.FetchOptions{Client: srv.Client(), Now: clock}))

	// A 5xx on the newest candidate must not fall back to the older file.
	srv.fail["/f/prices-2026-10.zip"] = http.StatusServiceUnavailable
	res, err := importer.Run(context.Background(), f.deps, "s1", importer.Options{})
	if !errors.Is(err, source.ErrResolveProbe) || res.Status != "failed" || res.Inserted != 0 {
		t.Fatalf("res %+v, err %v", res, err)
	}
	if run := f.run1(t, res.RunID); run.Status != "failed" || run.Error == nil || !strings.Contains(*run.Error, "prices-2026-10.zip") {
		t.Errorf("run row: %+v", run)
	}

	// Nothing exists: the failed run names every URL tried.
	delete(srv.fail, "/f/prices-2026-10.zip")
	delete(srv.files, "/f/prices-2026-09.zip")
	res, err = importer.Run(context.Background(), f.deps, "s1", importer.Options{})
	if !errors.Is(err, source.ErrNoCandidate) || res.Status != "failed" {
		t.Fatalf("res %+v, err %v", res, err)
	}
	for _, ym := range []string{"2026-10", "2026-09", "2026-08"} {
		if !strings.Contains(res.Error, "prices-"+ym+".zip") {
			t.Errorf("error does not list %s: %s", ym, res.Error)
		}
	}
}

func assertLocation(t *testing.T, meta json.RawMessage, want string) {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(meta, &m); err != nil || m[source.ResolveKey] != want {
		t.Errorf("source_meta = %s (%v), want _location %q", meta, err, want)
	}
}
