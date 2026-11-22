package source

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/willis-dev-md/data-importer/internal/config"
)

func fakeClock(y int, m time.Month, d int) func() time.Time {
	return func() time.Time { return time.Date(y, m, d, 23, 59, 0, 0, time.UTC) }
}

func resolveCfg(location string, back int) *config.Source {
	return &config.Source{
		ID: "r", Kind: config.KindCSV, Location: location, NaturalKey: []string{"id"},
		Resolve: map[string]config.Resolve{"ym": {Kind: "month", Layout: "2006-01", Start: "-1m", Back: back}},
	}
}

func TestCandidates(t *testing.T) {
	tests := []struct {
		name   string
		now    time.Time
		layout string
		start  string
		back   int
		want   []string
	}{
		{"start -1m, six back", time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), "2006-01", "-1m", 6,
			[]string{"2026-10", "2026-09", "2026-08", "2026-07", "2026-06", "2026-05"}},
		{"this month", time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), "2006-01", "0m", 2, []string{"2026-11", "2026-10"}},
		{"across a year", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), "2006-01", "-1m", 3, []string{"2025-12", "2025-11", "2025-10"}},
		{"31st of a month: no overflow", time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC), "2006-01", "-1m", 2, []string{"2026-02", "2026-01"}},
		{"clock in another zone is read as UTC", time.Date(2026, 4, 1, 0, 30, 0, 0, time.FixedZone("x", 3600)), "2006-01", "-1m", 1, []string{"2026-02"}},
		{"name layout", time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), "Jan-2006", "-1m", 2, []string{"Oct-2026", "Sep-2026"}},
		{"far back", time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), "200601", "-12m", 2, []string{"202511", "202510"}},
	}
	for _, tc := range tests {
		cfg := resolveCfg("https://h/f-{ym}.csv", tc.back)
		cfg.Resolve["ym"] = config.Resolve{Kind: "month", Layout: tc.layout, Start: tc.start, Back: tc.back}
		got, err := Candidates(cfg, tc.now)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for i := range tc.want {
			tc.want[i] = "https://h/f-" + tc.want[i] + ".csv"
		}
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	// No resolve: the location itself.
	got, _ := Candidates(&config.Source{Location: "x"}, time.Now())
	if len(got) != 1 || got[0] != "x" {
		t.Errorf("no resolve: %v", got)
	}
	// Invalid values that slipped past validation are errors, not panics.
	bad := resolveCfg("https://h/{ym}", 3)
	bad.Resolve["ym"] = config.Resolve{Kind: "week", Layout: "2006-01", Start: "-1m", Back: 3}
	if _, err := Candidates(bad, time.Now()); err == nil {
		t.Error("kind week must fail")
	}
	bad.Resolve["ym"] = config.Resolve{Kind: "month", Layout: "2006-01", Start: "soon", Back: 3}
	if _, err := Candidates(bad, time.Now()); err == nil {
		t.Error("start soon must fail")
	}
	bad.Resolve["ym"] = config.Resolve{Kind: "month", Layout: "2006-01", Start: "-1m", Back: 100000}
	if _, err := Candidates(bad, time.Now()); err == nil {
		t.Error("back 100000 must fail")
	}
}

type probeSrv struct {
	*httptest.Server
	mu      sync.Mutex
	status  map[string]int // path -> status for HEAD (and ranged GET unless getStatus has it)
	get     map[string]int // path -> status of the ranged GET
	log     []string       // "METHOD path [range]"
	headNot map[string]bool
}

func newProbeSrv(t *testing.T) *probeSrv {
	t.Helper()
	p := &probeSrv{status: map[string]int{}, get: map[string]int{}, headNot: map[string]bool{}}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		entry := r.Method + " " + r.URL.Path
		if rg := r.Header.Get("Range"); rg != "" {
			entry += " " + rg
		}
		p.log = append(p.log, entry)
		st, known := p.status[r.URL.Path]
		if !known {
			st = http.StatusNotFound
		}
		switch {
		case r.Method == http.MethodHead && p.headNot[r.URL.Path]:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.Method == http.MethodGet:
			if g, ok := p.get[r.URL.Path]; ok {
				st = g
			}
			w.WriteHeader(st)
			if st/100 == 2 {
				_, _ = io.WriteString(w, "a")
			}
		default:
			w.WriteHeader(st)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *probeSrv) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.log...)
}

func TestResolveLocation(t *testing.T) {
	ctx := context.Background()
	now := fakeClock(2026, 11, 22) // candidates: 2026-10 ... 2026-05
	path := func(ym string) string { return "/f/Average-prices-" + ym + ".csv" }

	t.Run("newest exists: only it is probed, by HEAD", func(t *testing.T) {
		p := newProbeSrv(t)
		p.status[path("2026-10")] = 200
		p.status[path("2026-09")] = 200
		got, err := ResolveLocation(ctx, resolveCfg(p.URL+"/f/Average-prices-{ym}.csv", 6), FetchOptions{Client: p.Client(), Now: now})
		if err != nil || got != p.URL+path("2026-10") {
			t.Fatalf("got %q, %v", got, err)
		}
		if r := p.requests(); len(r) != 1 || r[0] != "HEAD "+path("2026-10") {
			t.Errorf("requests %v", r)
		}
	})

	t.Run("only an older one exists: tried newest first", func(t *testing.T) {
		p := newProbeSrv(t)
		p.status[path("2026-08")] = 200
		p.status[path("2026-06")] = 200
		got, err := ResolveLocation(ctx, resolveCfg(p.URL+"/f/Average-prices-{ym}.csv", 6), FetchOptions{Client: p.Client(), Now: now})
		if err != nil || got != p.URL+path("2026-08") {
			t.Fatalf("got %q, %v", got, err)
		}
		want := []string{"HEAD " + path("2026-10"), "HEAD " + path("2026-09"), "HEAD " + path("2026-08")}
		if r := p.requests(); strings.Join(r, "|") != strings.Join(want, "|") {
			t.Errorf("requests %v, want %v", r, want)
		}
	})

	t.Run("none exist: error lists every URL tried", func(t *testing.T) {
		p := newProbeSrv(t)
		_, err := ResolveLocation(ctx, resolveCfg(p.URL+"/f/Average-prices-{ym}.csv", 6), FetchOptions{Client: p.Client(), Now: now})
		if !errors.Is(err, ErrNoCandidate) || errors.Is(err, ErrResolveProbe) {
			t.Fatalf("err = %v", err)
		}
		for _, ym := range []string{"2026-10", "2026-09", "2026-08", "2026-07", "2026-06", "2026-05"} {
			if !strings.Contains(err.Error(), p.URL+path(ym)) {
				t.Errorf("error lacks %s: %v", ym, err)
			}
		}
		if strings.Contains(err.Error(), "2026-04") || strings.Contains(err.Error(), "2026-11") {
			t.Errorf("error lists a URL that was not a candidate: %v", err)
		}
		if len(p.requests()) != 6 {
			t.Errorf("requests %v", p.requests())
		}
	})

	t.Run("410 is not found too", func(t *testing.T) {
		p := newProbeSrv(t)
		p.status[path("2026-10")] = 410
		p.status[path("2026-09")] = 200
		got, err := ResolveLocation(ctx, resolveCfg(p.URL+"/f/Average-prices-{ym}.csv", 3), FetchOptions{Client: p.Client(), Now: now})
		if err != nil || got != p.URL+path("2026-09") {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	for _, code := range []int{500, 502, 503, 401, 403, 429, 400} {
		t.Run("status "+http.StatusText(code)+" is an error, not a 404", func(t *testing.T) {
			p := newProbeSrv(t)
			p.status[path("2026-10")] = code
			p.status[path("2026-09")] = 200 // an older file exists, but must NOT be picked after a failure
			_, err := ResolveLocation(ctx, resolveCfg(p.URL+"/f/Average-prices-{ym}.csv", 6), FetchOptions{Client: p.Client(), Now: now})
			if !errors.Is(err, ErrResolveProbe) || errors.Is(err, ErrNoCandidate) {
				t.Fatalf("err = %v", err)
			}
			if !strings.Contains(err.Error(), p.URL+path("2026-10")) {
				t.Errorf("error does not name the URL: %v", err)
			}
			if r := p.requests(); len(r) != 1 {
				t.Errorf("the next candidate was probed after a failure: %v", r)
			}
		})
	}

	t.Run("5xx on an older candidate after 404s is still an error", func(t *testing.T) {
		p := newProbeSrv(t)
		p.status[path("2026-09")] = 503
		_, err := ResolveLocation(ctx, resolveCfg(p.URL+"/f/Average-prices-{ym}.csv", 6), FetchOptions{Client: p.Client(), Now: now})
		if !errors.Is(err, ErrResolveProbe) || !strings.Contains(err.Error(), path("2026-10")) {
			t.Fatalf("err = %v (the URLs tried so far should be listed)", err)
		}
	})

	t.Run("HEAD not allowed: ranged GET", func(t *testing.T) {
		p := newProbeSrv(t)
		p.headNot[path("2026-10")], p.headNot[path("2026-09")] = true, true
		p.status[path("2026-10")] = 404
		p.get[path("2026-09")] = 206
		p.status[path("2026-09")] = 206
		got, err := ResolveLocation(ctx, resolveCfg(p.URL+"/f/Average-prices-{ym}.csv", 6), FetchOptions{Client: p.Client(), Now: now})
		if err != nil || got != p.URL+path("2026-09") {
			t.Fatalf("got %q, %v", got, err)
		}
		want := []string{"HEAD " + path("2026-10"), "GET " + path("2026-10") + " bytes=0-0", "HEAD " + path("2026-09"), "GET " + path("2026-09") + " bytes=0-0"}
		if r := p.requests(); strings.Join(r, "|") != strings.Join(want, "|") {
			t.Errorf("requests %v, want %v", r, want)
		}
	})

	t.Run("HEAD not allowed and the GET fails with 500", func(t *testing.T) {
		p := newProbeSrv(t)
		p.headNot[path("2026-10")] = true
		p.get[path("2026-10")] = 500
		_, err := ResolveLocation(ctx, resolveCfg(p.URL+"/f/Average-prices-{ym}.csv", 6), FetchOptions{Client: p.Client(), Now: now})
		if !errors.Is(err, ErrResolveProbe) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("network error is an error", func(t *testing.T) {
		p := newProbeSrv(t)
		url := p.URL
		p.Close()
		_, err := ResolveLocation(ctx, resolveCfg(url+"/f/Average-prices-{ym}.csv", 2), FetchOptions{Client: p.Client(), Now: now})
		if !errors.Is(err, ErrResolveProbe) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("no resolve: the location as it is, no request", func(t *testing.T) {
		got, err := ResolveLocation(ctx, &config.Source{Location: "https://example.invalid/x.csv"}, FetchOptions{})
		if err != nil || got != "https://example.invalid/x.csv" {
			t.Fatalf("got %q, %v", got, err)
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		p := newProbeSrv(t)
		c, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := ResolveLocation(c, resolveCfg(p.URL+"/f/{ym}.csv", 2), FetchOptions{Client: p.Client(), Now: now}); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("local files are checked with stat", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f-2026-08.csv"), []byte("id\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := ResolveLocation(ctx, resolveCfg(filepath.Join(dir, "f-{ym}.csv"), 6), FetchOptions{Now: now})
		if err != nil || got != filepath.Join(dir, "f-2026-08.csv") {
			t.Fatalf("got %q, %v", got, err)
		}
		_, err = ResolveLocation(ctx, resolveCfg(filepath.Join(dir, "none-{ym}.csv"), 2), FetchOptions{Now: now})
		if !errors.Is(err, ErrNoCandidate) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCSVAdapterOpenResolves(t *testing.T) {
	p := newProbeSrv(t)
	srvCSV := p.Server.Config.Handler
	p.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.Header.Get("Range") == "" && strings.HasSuffix(r.URL.Path, "2026-09.csv") {
			_, _ = io.WriteString(w, "id,v\n1,a\n")
			return
		}
		srvCSV.ServeHTTP(w, r)
	})
	p.status["/Average-prices-2026-09.csv"] = 200
	cfg := resolveCfg(p.URL+"/Average-prices-{ym}.csv", 6)
	st, err := NewCSV(FetchOptions{Client: p.Client(), Now: fakeClock(2026, 11, 22)}).Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	recs, _ := drain(t, st)
	if len(recs) != 1 {
		t.Fatalf("records %d", len(recs))
	}
	if got := st.Meta()[ResolveKey]; got != p.URL+"/Average-prices-2026-09.csv" {
		t.Errorf("Meta[_location] = %q", got)
	}
	if h, ok := st.(Hasher); !ok || h.SHA256() != sha([]byte("id,v\n1,a\n")) {
		t.Errorf("Hasher lost through the location wrapper")
	}
	_ = st.Close()

	// Without resolve there is no _location.
	plain := &config.Source{ID: "p", Kind: config.KindCSV, Location: p.URL + "/Average-prices-2026-09.csv", NaturalKey: []string{"id"}}
	st, err = NewCSV(FetchOptions{Client: p.Client()}).Open(context.Background(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Meta()[ResolveKey]; ok {
		t.Error("_location without resolve")
	}
	_ = st.Close()

	// None exist: Open fails with the list.
	_, err = NewCSV(FetchOptions{Client: p.Client(), Now: fakeClock(2020, 1, 1)}).Open(context.Background(), cfg)
	if !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchRefusesRedirectDowngrade(t *testing.T) {
	var plainHits int
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits++
		_, _ = io.WriteString(w, "secret over plain http")
	}))
	defer plain.Close()
	var final *httptest.Server
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/down":
			http.Redirect(w, r, plain.URL+"/f.csv", http.StatusFound)
		case "/up":
			http.Redirect(w, r, final.URL+"/ok.csv", http.StatusFound)
		default:
			_, _ = io.WriteString(w, "id\n1\n")
		}
	}))
	defer tlsSrv.Close()
	final = tlsSrv

	_, err := Fetch(context.Background(), tlsSrv.URL+"/down", FetchOptions{Client: tlsSrv.Client()})
	if !errors.Is(err, ErrRedirectDowngrade) {
		t.Fatalf("err = %v, want ErrRedirectDowngrade", err)
	}
	if !strings.Contains(err.Error(), "https") || !strings.Contains(err.Error(), plain.URL) {
		t.Errorf("error should name both ends: %v", err)
	}
	if plainHits != 0 {
		t.Errorf("the plain http server was contacted %d times", plainHits)
	}
	// The probe refuses it too.
	if _, _, err := probe(context.Background(), httpsOnly(tlsSrv.Client()), tlsSrv.URL+"/down"); !errors.Is(err, ErrRedirectDowngrade) {
		t.Errorf("probe err = %v", err)
	}
	// https to https is fine, and the caller's client keeps its own policy.
	b, err := Fetch(context.Background(), tlsSrv.URL+"/up", FetchOptions{Client: tlsSrv.Client()})
	if err != nil {
		t.Fatalf("https redirect: %v", err)
	}
	_ = b.Close()
	if tlsSrv.Client().CheckRedirect != nil {
		t.Error("the caller's client was modified")
	}
	// A custom CheckRedirect still runs.
	custom := &http.Client{Transport: tlsSrv.Client().Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("custom stop") }}
	if _, err := Fetch(context.Background(), tlsSrv.URL+"/up", FetchOptions{Client: custom}); err == nil || !strings.Contains(err.Error(), "custom stop") {
		t.Errorf("custom policy ignored: %v", err)
	}
}
