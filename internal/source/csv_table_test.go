package source

import (
	"context"
	"fmt"
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

// openCSVCfg opens csv text through the configured-source path (the pipeline runs).
func openCSVCfg(t *testing.T, yaml, data string) (Stream, error) {
	t.Helper()
	cfg, err := config.ParseSource([]byte("id: t\nkind: csv\nlocation: /x.csv\n" + yaml))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return NewCSV(FetchOptions{}).OpenReader(context.Background(), cfg, strings.NewReader(data))
}

func TestCSVTableSteps(t *testing.T) {
	tests := []struct {
		name, yaml, data string
		wantFields       []map[string]string // string view of the output fields (underscore keys skipped)
		wantErrs         int
		wantKeys         []string
	}{
		{
			name:       "skip_rows",
			yaml:       "natural_key: [k]\ntable: [{skip_rows: {where: k, match: '^#'}}]\n",
			data:       "k,v\n#note,1\na,2\n",
			wantFields: []map[string]string{{"k": "a", "v": "2"}},
			wantKeys:   []string{`["a"]`},
		},
		{
			name:       "drop_if_empty",
			yaml:       "natural_key: [k]\ntable: [{drop_if_empty: [v, w]}]\n",
			data:       "k,v,w\na,,\nb,1,\nc,,2\n",
			wantFields: []map[string]string{{"k": "b", "v": "1", "w": ""}, {"k": "c", "v": "", "w": "2"}},
			wantKeys:   []string{`["b"]`, `["c"]`},
		},
		{
			name:       "section and fill_down",
			yaml:       "natural_key: ['@ctx.grp', k]\ntable:\n  - section: {column: k, match: '^G:', set: grp}\n  - fill_down: {columns: [v], reset_on: section}\n",
			data:       "k,v\nG:one,\na,1\nb,\nG:two,\nc,\nd,4\n",
			wantFields: []map[string]string{{"k": "a", "v": "1"}, {"k": "b", "v": "1"}, {"k": "c", "v": ""}, {"k": "d", "v": "4"}},
			wantKeys:   []string{`["G:one","a"]`, `["G:one","b"]`, `["G:two","c"]`, `["G:two","d"]`},
		},
		{
			name:       "normalize",
			yaml:       "natural_key: [k]\ntable: [{normalize: {k: [trim, upper]}}]\n",
			data:       "k,v\n  ab ,1\n",
			wantFields: []map[string]string{{"k": "AB", "v": "1"}},
			wantKeys:   []string{`["AB"]`},
		},
		{
			name:       "normalize to_int error is a RowError",
			yaml:       "natural_key: [k]\ntable: [{normalize: {v: to_int}}]\n",
			data:       "k,v\na,1\nb,x\nc,3\n",
			wantFields: []map[string]string{{"k": "a", "v": "1"}, {"k": "c", "v": "3"}},
			wantErrs:   1,
			wantKeys:   []string{`["a"]`, `["c"]`},
		},
		{
			name: "unpivot map form drops empty cells",
			yaml: "natural_key: [id, m]\ntable: [{unpivot: {id: [id], columns: {a: alpha, b: beta}, name: m, value: val}}]\n",
			data: "id,a,b,ignored\n1,x,,z\n2,,y,z\n3,p,q,z\n4,,,z\n",
			wantFields: []map[string]string{
				{"id": "1", "m": "alpha", "val": "x"}, {"id": "2", "m": "beta", "val": "y"},
				{"id": "3", "m": "alpha", "val": "p"}, {"id": "3", "m": "beta", "val": "q"}},
			wantKeys: []string{`["1","alpha"]`, `["2","beta"]`, `["3","alpha"]`, `["3","beta"]`},
		},
		{
			name:       "unpivot list form uses column names",
			yaml:       "natural_key: [id, m]\ntable: [{unpivot: {id: [id], columns: [a, b], name: m, value: val}}]\n",
			data:       "id,a,b\n1,x,y\n",
			wantFields: []map[string]string{{"id": "1", "m": "a", "val": "x"}, {"id": "1", "m": "b", "val": "y"}},
			wantKeys:   []string{`["1","a"]`, `["1","b"]`},
		},
		{
			name:       "split",
			yaml:       "natural_key: [code]\ntable: [{split: {column: k, regex: '^(?P<code>[A-Z]+)-(?P<n>\\d+)$'}}]\n",
			data:       "k,v\nAB-12,1\nbad,2\n",
			wantFields: []map[string]string{{"k": "AB-12", "v": "1", "code": "AB", "n": "12"}},
			wantErrs:   1,
			wantKeys:   []string{`["AB"]`},
		},
		{
			name:       "strip",
			yaml:       "natural_key: [k]\ntable: [{strip: {k: [\"'\"]}}]\n",
			data:       "k,v\n'01,1\n",
			wantFields: []map[string]string{{"k": "01", "v": "1"}},
			wantKeys:   []string{`["01"]`},
		},
		{
			name:       "static context in key",
			yaml:       "natural_key: ['@ctx.year', k]\ncontext: {year: 2026}\ntable: [{drop_if_empty: [k]}]\n",
			data:       "k,v\na,1\n",
			wantFields: []map[string]string{{"k": "a", "v": "1"}},
			wantKeys:   []string{`["2026","a"]`},
		},
		{
			name:       "empty key part is a RowError",
			yaml:       "natural_key: [k, v]\ntable: [{drop_if_empty: [k]}]\n",
			data:       "k,v\na,\nb,2\n",
			wantFields: []map[string]string{{"k": "b", "v": "2"}},
			wantErrs:   1,
			wantKeys:   []string{`["b","2"]`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := openCSVCfg(t, tt.yaml, tt.data)
			if err != nil {
				t.Fatal(err)
			}
			recs, errs := drain(t, s)
			if len(errs) != tt.wantErrs {
				t.Errorf("row errors = %d (%v), want %d", len(errs), errs, tt.wantErrs)
			}
			if len(recs) != len(tt.wantFields) {
				t.Fatalf("records = %d, want %d", len(recs), len(tt.wantFields))
			}
			for i, r := range recs {
				got := map[string]string{}
				for k, v := range r.Fields {
					if strings.HasPrefix(k, "_") {
						continue
					}
					got[k] = fmt.Sprint(v)
				}
				for k, w := range tt.wantFields[i] {
					if got[k] != w {
						t.Errorf("rec %d field %s = %q, want %q", i, k, got[k], w)
					}
				}
				if len(got) != len(tt.wantFields[i]) {
					t.Errorf("rec %d fields = %v, want %v", i, got, tt.wantFields[i])
				}
				if r.NaturalKey != tt.wantKeys[i] {
					t.Errorf("rec %d key = %s, want %s", i, r.NaturalKey, tt.wantKeys[i])
				}
				if _, ok := r.Fields["_source"].(map[string]any)["line"]; !ok {
					t.Errorf("rec %d has no _source.line: %v", i, r.Fields["_source"])
				}
			}
		})
	}
}

func TestCSVTableOpenErrors(t *testing.T) {
	tests := []struct{ name, yaml, data, want string }{
		{"unknown column in step", "natural_key: [k]\ntable: [{drop_if_empty: [nope]}]\n", "k,v\na,1\n", `table[0].drop_if_empty[0]: unknown column "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := openCSVCfg(t, tt.yaml, tt.data)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// A csv source without table: behaves exactly as before (no _ctx, plain row, key of any empty part is an error).
func TestCSVWithoutTableUnchanged(t *testing.T) {
	s, err := openCSVCfg(t, "natural_key: [k]\n", "k,v\na,1\n,2\n")
	if err != nil {
		t.Fatal(err)
	}
	recs, errs := drain(t, s)
	if len(recs) != 1 || len(errs) != 1 {
		t.Fatalf("records=%d errors=%d", len(recs), len(errs))
	}
	if _, ok := recs[0].Fields["_ctx"]; ok {
		t.Errorf("a plain csv record must not get _ctx")
	}
}

func hpiStream(t *testing.T, example, fixture string) []*Record {
	t.Helper()
	root := filepath.Join("..", "..")
	cfg, err := config.LoadSource(filepath.Join(root, "docs", "examples", example+".source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(root, "testdata", "hpi", fixture))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := NewCSV(FetchOptions{}).OpenReader(context.Background(), cfg, f)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recs, errs := drain(t, s)
	if len(errs) != 0 {
		t.Fatalf("row errors: %v", errs[0])
	}
	return recs
}

func TestHPIUnpivotCounts(t *testing.T) {
	tests := []struct {
		example, fixture, col string
		want                  map[string]int
	}{
		{"uk-hpi-average-prices", "average-prices-trimmed.csv", "measure_col", map[string]int{"average_price": 2487, "average_price_sa": 915}},
		{"uk-hpi-property-type", "average-prices-property-type-trimmed.csv", "property_type", map[string]int{"detached": 915, "semi_detached": 915, "terraced": 1026, "flat": 1296}},
	}
	for _, tt := range tests {
		t.Run(tt.example, func(t *testing.T) {
			recs := hpiStream(t, tt.example, tt.fixture)
			got := map[string]int{}
			keys := map[string]bool{}
			total := 0
			for _, r := range recs {
				got[r.Fields[tt.col].(string)]++
				if keys[r.NaturalKey] {
					t.Fatalf("duplicate natural key %s", r.NaturalKey)
				}
				keys[r.NaturalKey] = true
				total++
			}
			sum := 0
			for k, w := range tt.want {
				sum += w
				if got[k] != w {
					t.Errorf("%s = %d, want %d", k, got[k], w)
				}
			}
			if total != sum {
				t.Errorf("total = %d, want %d (%v)", total, sum, got)
			}
		})
	}
}

// The HPI example resolves `Average-prices-{ym}.csv`: with the clock at 2026-11-20 the candidates are 2026-10 (404,
// not published yet) and then 2026-09 (200), which is fetched and imported (https via an httptest TLS server).
func TestHPIResolveMonth(t *testing.T) {
	root := filepath.Join("..", "..")
	body, err := os.ReadFile(filepath.Join(root, "testdata", "hpi", "average-prices-trimmed.csv"))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var log []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		log = append(log, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/market-trend-data/house-price-index-data/Average-prices-2026-09.csv" {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	cfg, err := config.LoadSource(filepath.Join(root, "docs", "examples", "uk-hpi-average-prices.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const tail = "/market-trend-data/house-price-index-data/Average-prices-{ym}.csv"
	if !strings.HasSuffix(cfg.Location, tail) || cfg.Resolve["ym"].Kind != "month" {
		t.Fatalf("example location/resolve changed: %q %+v", cfg.Location, cfg.Resolve)
	}
	cfg.Location = srv.URL + tail // everything else (resolve, table) is the example as written
	now := func() time.Time { return time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC) }
	s, err := NewCSV(FetchOptions{Client: srv.Client(), Now: now}).Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recs, errs := drain(t, s)
	if len(recs) != 3402 || len(errs) != 0 {
		t.Fatalf("records=%d errors=%d", len(recs), len(errs))
	}
	mu.Lock()
	defer mu.Unlock()
	p := "/market-trend-data/house-price-index-data/Average-prices-"
	want := []string{"HEAD " + p + "2026-10.csv", "HEAD " + p + "2026-09.csv", "GET " + p + "2026-09.csv"}
	if strings.Join(log, "|") != strings.Join(want, "|") {
		t.Errorf("requests %v, want %v", log, want)
	}
}
