package source

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type seenReq struct{ inm, ims string }

func TestFetchConditional(t *testing.T) {
	const etag = `W/"v1;odd \x"` // sent back byte for byte
	const lm = "Mon, 06 May 2024 07:08:09 GMT"
	var mu sync.Mutex
	var seen []seenReq
	bodies := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, seenReq{r.Header.Get("If-None-Match"), r.Header.Get("If-Modified-Since")})
		mu.Unlock()
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		mu.Lock()
		bodies++
		mu.Unlock()
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", lm)
		_, _ = io.WriteString(w, "a,b\n1,2\n")
	}))
	defer srv.Close()
	ctx := context.Background()

	// A plain fetch reports the validators exactly as received.
	b, err := Fetch(ctx, srv.URL, FetchOptions{Client: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, b)
	_ = b.Close()
	if b.ETag != etag || b.LastModified != lm {
		t.Fatalf("validators = %q, %q", b.ETag, b.LastModified)
	}
	if seen[0] != (seenReq{}) {
		t.Fatalf("unconditional fetch sent conditional headers: %+v", seen[0])
	}

	// A conditional fetch sends them verbatim, and a 304 yields ErrNotModified with no body.
	b, err = Fetch(ctx, srv.URL, FetchOptions{Client: srv.Client(), IfNoneMatch: etag, IfModifiedSince: lm})
	if !errors.Is(err, ErrNotModified) || b != nil {
		t.Fatalf("Fetch = %v, %v; want ErrNotModified", b, err)
	}
	if bodies != 1 {
		t.Errorf("bodies served = %d, want 1 (a 304 carries none)", bodies)
	}

	// A different validator gets the full body again.
	b, err = Fetch(ctx, srv.URL, FetchOptions{Client: srv.Client(), IfNoneMatch: `"other"`})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(b)
	_ = b.Close()
	if string(data) != "a,b\n1,2\n" {
		t.Errorf("body = %q", data)
	}
	last := seen[len(seen)-1]
	if last.inm != `"other"` || last.ims != "" {
		t.Errorf("sent %+v", last)
	}
	for _, s := range seen[1 : len(seen)-1] {
		if s.inm != etag || s.ims != lm {
			t.Errorf("conditional request sent %+v, want the stored validators verbatim", s)
		}
	}
}

func TestFetchUnexpected304(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()
	// Not conditional: a 304 is a plain error, never "unchanged".
	_, err := Fetch(context.Background(), srv.URL, FetchOptions{Client: srv.Client()})
	if err == nil || errors.Is(err, ErrNotModified) || !strings.Contains(err.Error(), "304") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchConditionalIgnoredForLocalFiles(t *testing.T) {
	b, err := Fetch(context.Background(), fixture("comma.csv"), FetchOptions{IfNoneMatch: `"x"`, IfModifiedSince: "y"})
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	if b.ETag != "" || b.LastModified != "" {
		t.Errorf("local validators %q %q", b.ETag, b.LastModified)
	}
}

func TestFileAdapterOpenReader(t *testing.T) {
	var _ FileAdapter = (*CSVAdapter)(nil)
	var _ FileAdapter = (*XLSXAdapter)(nil)
	if got := NewCSV(FetchOptions{}).FetchOptions(nil).MaxBytes; got != DefaultMaxBytes {
		t.Errorf("csv max = %d", got)
	}
	if got := NewXLSX(FetchOptions{MaxBytes: 1 << 40}).FetchOptions(nil).MaxBytes; got != DefaultXLSXMaxFileBytes {
		t.Errorf("xlsx max = %d, want the workbook limit", got)
	}
}
