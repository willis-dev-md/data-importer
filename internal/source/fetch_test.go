package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/willis-dev-md/data-importer/internal/config"
)

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func readAll(t *testing.T, b *Body) []byte {
	t.Helper()
	data, err := io.ReadAll(b)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return data
}

func TestFetchLocal(t *testing.T) {
	want, err := os.ReadFile(fixture("comma.csv"))
	if err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(fixture("comma.csv"))
	for _, loc := range []string{fixture("comma.csv"), "file://" + abs} {
		b, err := Fetch(context.Background(), loc, FetchOptions{})
		if err != nil {
			t.Fatalf("%s: %v", loc, err)
		}
		got := readAll(t, b)
		_ = b.Close()
		if string(got) != string(want) || b.SHA256() != sha(want) || b.Size() != int64(len(want)) {
			t.Errorf("%s: mismatch (sha %s)", loc, b.SHA256())
		}
	}
}

func TestFetchErrors(t *testing.T) {
	for _, loc := range []string{"/nonexistent/x.csv", "ftp://example.org/x", "http://example.org/x", "s3://bucket/x"} {
		if _, err := Fetch(context.Background(), loc, FetchOptions{}); err == nil {
			t.Errorf("%s: expected error", loc)
		}
	}
}

func TestFetchHTTPS(t *testing.T) {
	body := "id,name\n1,Ann\n"
	var gotUA string
	mux := http.NewServeMux()
	mux.HandleFunc("/data.csv", func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/data.csv", http.StatusFound)
	})
	mux.HandleFunc("/missing", http.NotFound)
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	opts := FetchOptions{Client: srv.Client()}

	t.Run("ok with redirect and user agent", func(t *testing.T) {
		b, err := Fetch(context.Background(), srv.URL+"/redirect", opts)
		if err != nil {
			t.Fatal(err)
		}
		defer b.Close()
		if got := string(readAll(t, b)); got != body {
			t.Errorf("body = %q", got)
		}
		if b.SHA256() != sha([]byte(body)) {
			t.Error("sha mismatch")
		}
		if !strings.HasPrefix(gotUA, "data-importer/") {
			t.Errorf("User-Agent = %q", gotUA)
		}
	})
	t.Run("status error", func(t *testing.T) {
		if _, err := Fetch(context.Background(), srv.URL+"/missing", opts); err == nil || !strings.Contains(err.Error(), "404") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("max size", func(t *testing.T) {
		o := opts
		o.MaxBytes = 5
		b, err := Fetch(context.Background(), srv.URL+"/data.csv", o)
		if err == nil {
			defer b.Close()
			_, err = io.ReadAll(b)
		}
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("err = %v, want ErrTooLarge", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		o := opts
		o.Timeout = 100 * time.Millisecond
		if _, err := Fetch(context.Background(), srv.URL+"/slow", o); err == nil {
			t.Error("expected timeout error")
		}
	})
	t.Run("adapter over https", func(t *testing.T) {
		cfg := &config.Source{ID: "h", Kind: config.KindCSV, Location: srv.URL + "/data.csv", NaturalKey: []string{"id"}}
		s, err := NewCSV(opts).Open(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		recs, errs := drain(t, s)
		if len(recs) != 1 || len(errs) != 0 {
			t.Errorf("recs=%d errs=%d", len(recs), len(errs))
		}
	})
}

func TestMaxSizeLocal(t *testing.T) {
	b, err := Fetch(context.Background(), fixture("comma.csv"), FetchOptions{MaxBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err := io.ReadAll(b); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v", err)
	}
}
