package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/willis-dev-md/data-importer/internal/store"
)

func shaOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func newDirStore(t *testing.T) (*store.DirFileStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "files")
	fs, err := store.NewDirFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fs, dir
}

// allFiles lists every regular file under dir, relative.
func allFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDirFileStorePutOpen(t *testing.T) {
	ctx := context.Background()
	fs, dir := newDirStore(t)
	data := []byte("a,b\n1,2\n")
	info, err := fs.Put(ctx, bytes.NewReader(data), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if info.SHA256 != shaOf(data) || info.Size != int64(len(data)) {
		t.Fatalf("info = %+v", info)
	}
	rc, err := fs.Open(ctx, info.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("read back %q, %v", got, err)
	}
	// Same bytes again: still one file, no leftovers (temporary files included).
	if _, err := fs.Put(ctx, bytes.NewReader(data), 1<<20); err != nil {
		t.Fatal(err)
	}
	files := allFiles(t, dir)
	if want := filepath.Join(info.SHA256[:2], info.SHA256); len(files) != 1 || files[0] != want {
		t.Fatalf("files = %v, want only %s", files, want)
	}
	// Empty file is a valid file.
	e, err := fs.Put(ctx, strings.NewReader(""), 10)
	if err != nil || e.Size != 0 || e.SHA256 != shaOf(nil) {
		t.Fatalf("empty put = %+v, %v", e, err)
	}
}

func TestDirFileStoreSizeLimit(t *testing.T) {
	ctx := context.Background()
	fs, dir := newDirStore(t)
	for _, tt := range []struct {
		name string
		n    int
		max  int64
		ok   bool
	}{
		{"at the limit", 100, 100, true},
		{"one over", 101, 100, false},
		{"far over", 1 << 20, 1000, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := fs.Put(ctx, bytes.NewReader(bytes.Repeat([]byte("x"), tt.n)), tt.max)
			if tt.ok != (err == nil) || (!tt.ok && !errors.Is(err, store.ErrFileTooLarge)) {
				t.Fatalf("err = %v, ok want %v", err, tt.ok)
			}
		})
	}
	// Only the accepted file remains: a rejected one leaves nothing behind.
	if files := allFiles(t, dir); len(files) != 1 {
		t.Fatalf("files = %v, want exactly the accepted one", files)
	}
	if _, err := fs.Put(ctx, strings.NewReader("x"), 0); err == nil {
		t.Error("maxBytes 0 accepted")
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestDirFileStorePutReadError(t *testing.T) {
	fs, dir := newDirStore(t)
	boom := errors.New("boom")
	_, err := fs.Put(context.Background(), io.MultiReader(strings.NewReader("abc"), errReader{boom}), 100)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if files := allFiles(t, dir); len(files) != 0 {
		t.Fatalf("files left after failed put: %v", files)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fs.Put(ctx, strings.NewReader("abc"), 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled put = %v", err)
	}
}

func TestDirFileStoreNoTraversal(t *testing.T) {
	ctx := context.Background()
	fs, dir := newDirStore(t)
	outside := filepath.Join(filepath.Dir(dir), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := shaOf([]byte("x"))
	for _, ref := range []string{
		"", "../secret", "../../etc/passwd", "/etc/passwd", "ab/" + valid, valid + "/../secret",
		strings.ToUpper(valid), valid[:63], valid + "0", "dir:" + valid[:2] + "/" + valid, valid[:62] + "..",
		strings.Repeat("0", 63) + "/", "..\\secret",
	} {
		if rc, err := fs.Open(ctx, ref); err == nil {
			rc.Close()
			t.Errorf("Open(%q) accepted", ref)
		}
	}
	if _, err := fs.Open(ctx, valid); !errors.Is(err, store.ErrFileMissing) {
		t.Errorf("Open of a missing file = %v, want ErrFileMissing", err)
	}
}

func TestDirFileStoreDetectsCorruption(t *testing.T) {
	ctx := context.Background()
	fs, dir := newDirStore(t)
	data := bytes.Repeat([]byte("abcdefghij"), 1000)
	info, err := fs.Put(ctx, bytes.NewReader(data), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, info.SHA256[:2], info.SHA256)
	for name, mutate := range map[string]func([]byte) []byte{
		"flipped byte": func(b []byte) []byte { c := append([]byte(nil), b...); c[500] ^= 1; return c },
		"truncated":    func(b []byte) []byte { return b[:len(b)-1] },
		"appended":     func(b []byte) []byte { return append(append([]byte(nil), b...), 'x') },
		"empty":        func([]byte) []byte { return nil },
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, mutate(data), 0o600); err != nil {
				t.Fatal(err)
			}
			rc, err := fs.Open(ctx, info.SHA256)
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			if _, err := io.ReadAll(rc); !errors.Is(err, store.ErrFileCorrupt) {
				t.Fatalf("ReadAll = %v, want ErrFileCorrupt", err)
			}
		})
	}
	// Putting the good bytes again heals the copy.
	if _, err := fs.Put(ctx, bytes.NewReader(data), 1<<20); err != nil {
		t.Fatal(err)
	}
	rc, err := fs.Open(ctx, info.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if got, err := io.ReadAll(rc); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("after heal: %v", err)
	}
}

func TestDirFileStoreConcurrent(t *testing.T) {
	ctx := context.Background()
	fs, dir := newDirStore(t)
	const workers, kinds = 24, 4
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := bytes.Repeat([]byte(fmt.Sprintf("content-%d;", i%kinds)), 50_000) // several MB-ish chunks
			info, err := fs.Put(ctx, bytes.NewReader(data), 1<<30)
			if err != nil {
				errs <- err
				return
			}
			rc, err := fs.Open(ctx, info.SHA256)
			if err != nil {
				errs <- err
				return
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil || !bytes.Equal(got, data) {
				errs <- fmt.Errorf("read back mismatch: %v", err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if files := allFiles(t, dir); len(files) != kinds {
		t.Errorf("files = %v, want %d distinct", files, kinds)
	}
}

func TestNewDirFileStoreErrors(t *testing.T) {
	if _, err := store.NewDirFileStore(""); err == nil {
		t.Error("empty dir accepted")
	}
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewDirFileStore(file); err == nil {
		t.Error("a regular file accepted as the directory")
	}
	if _, err := store.NewDirFileStore(filepath.Join(file, "sub")); err == nil {
		t.Error("a directory below a regular file accepted")
	}
}

func TestSourceFilesDB(t *testing.T) {
	ctx := context.Background()
	s, pool := setup(t)
	if err := s.UpsertSource(ctx, store.Source{ID: "other", Kind: "csv", Config: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	run := func(src, status string) int64 {
		id, err := s.CreateRun(ctx, src, "manual")
		if err != nil {
			t.Fatal(err)
		}
		if status != "running" {
			if err := s.FinishRun(ctx, id, status, store.RunCounts{}, nil, nil, ""); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	sha1, sha2 := shaOf([]byte("one")), shaOf([]byte("two"))
	lm := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)

	if _, err := s.LastFile(ctx, "src"); !errors.Is(err, store.ErrNoFile) {
		t.Fatalf("LastFile with no runs = %v", err)
	}
	if _, err := s.FileForReparse(ctx, "src", 0); !errors.Is(err, store.ErrNoFile) {
		t.Fatalf("FileForReparse with no runs = %v", err)
	}

	r1 := run("src", "running")
	id1, created, err := s.RecordFile(ctx, store.File{SourceID: "src", RunID: r1, SHA256: sha1, Size: 3, ETag: `W/"abc"`, LastModified: &lm, StorageRef: "ref1"})
	if err != nil || !created {
		t.Fatalf("RecordFile = %d, %v, %v", id1, created, err)
	}
	if err := s.SetRunFile(ctx, r1, id1); err != nil {
		t.Fatal(err)
	}
	// A running (or failed) run is not a successful run.
	if _, err := s.LastFile(ctx, "src"); !errors.Is(err, store.ErrNoFile) {
		t.Fatalf("LastFile with only a running run = %v", err)
	}
	if err := s.FinishRun(ctx, r1, "failed", store.RunCounts{}, nil, nil, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LastFile(ctx, "src"); !errors.Is(err, store.ErrNoFile) {
		t.Fatalf("LastFile with only a failed run = %v", err)
	}
	// ... but a failed run's file can be reparsed.
	if f, err := s.FileForReparse(ctx, "src", 0); err != nil || f.ID != id1 {
		t.Fatalf("FileForReparse = %+v, %v", f, err)
	}

	r2 := run("src", "running")
	id1b, created, err := s.RecordFile(ctx, store.File{SourceID: "src", RunID: r2, SHA256: sha1, Size: 3, ETag: `"new"`, StorageRef: "ref1"})
	if err != nil || created || id1b != id1 {
		t.Fatalf("second RecordFile = %d, %v, %v; want the same row, not created", id1b, created, err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_files`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("source_files rows = %d, %v; want 1", rows, err)
	}
	if err := s.SetRunFile(ctx, r2, id1); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, r2, "succeeded", store.RunCounts{}, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	f, err := s.LastFile(ctx, "src")
	if err != nil {
		t.Fatal(err)
	}
	// run_id is the run that stored it first; validators come from the latest response.
	if f.ID != id1 || f.RunID != r1 || f.SHA256 != sha1 || f.Size != 3 || f.ETag != `"new"` || f.LastModified != nil {
		t.Fatalf("LastFile = %+v", f)
	}

	// A new file, and a file of another source with the same bytes.
	r3 := run("src", "running")
	id2, _, err := s.RecordFile(ctx, store.File{SourceID: "src", RunID: r3, SHA256: sha2, Size: 3, ETag: `W/"abc"`, LastModified: &lm, StorageRef: "ref2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRunFile(ctx, r3, id2); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, r3, "unchanged_file", store.RunCounts{}, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	ro := run("other", "running")
	ido, created, err := s.RecordFile(ctx, store.File{SourceID: "other", RunID: ro, SHA256: sha2, Size: 3, StorageRef: "ref2"})
	if err != nil || !created || ido == id2 {
		t.Fatalf("other source's file = %d, %v, %v", ido, created, err)
	}
	f, err = s.LastFile(ctx, "src")
	if err != nil || f.ID != id2 || f.ETag != `W/"abc"` || f.LastModifiedHeader() != "Mon, 06 May 2024 07:08:09 GMT" {
		t.Fatalf("LastFile after unchanged_file = %+v, %v", f, err)
	}

	// Reparse lookups: latest, given, foreign and unknown runs.
	if f, err := s.FileForReparse(ctx, "src", 0); err != nil || f.ID != id2 {
		t.Fatalf("latest = %+v, %v", f, err)
	}
	if f, err := s.FileForReparse(ctx, "src", r1); err != nil || f.ID != id1 {
		t.Fatalf("run r1 = %+v, %v", f, err)
	}
	if _, err := s.FileForReparse(ctx, "src", ro); !errors.Is(err, store.ErrNoFile) {
		t.Fatalf("run of another source = %v", err)
	}
	if _, err := s.FileForReparse(ctx, "src", 987654); !errors.Is(err, store.ErrNoFile) {
		t.Fatalf("unknown run = %v", err)
	}
	if _, err := s.FileForReparse(ctx, "nope", 0); !errors.Is(err, store.ErrNoFile) {
		t.Fatalf("unknown source = %v", err)
	}
	// Bad input is rejected / foreign keys hold.
	if _, _, err := s.RecordFile(ctx, store.File{SourceID: "src", RunID: r1, SHA256: "zz", StorageRef: "r"}); err == nil {
		t.Error("non-hex sha accepted")
	}
	if err := s.SetRunFile(ctx, r1, 99999); err == nil {
		t.Error("SetRunFile to an unknown file accepted")
	}
	if err := s.SetRunFile(ctx, 99999, id1); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetRunFile on an unknown run = %v", err)
	}
	run1, err := s.GetRun(ctx, r2)
	if err != nil || run1.FileID == nil || *run1.FileID != id1 {
		t.Fatalf("GetRun file id = %+v, %v", run1.FileID, err)
	}
}
