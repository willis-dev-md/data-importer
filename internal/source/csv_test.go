package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/record"
)

func fixture(name string) string { return filepath.Join("..", "..", "testdata", "csv", name) }

func boolp(b bool) *bool { return &b }

func drain(t *testing.T, s Stream) (recs []*Record, errs []*RowError) {
	t.Helper()
	for {
		it, ok := s.Next()
		if !ok {
			break
		}
		switch {
		case it.Record != nil && it.Error == nil:
			recs = append(recs, it.Record)
		case it.Error != nil && it.Record == nil:
			errs = append(errs, it.Error)
		default:
			t.Fatalf("item must be exactly one of record/error: %+v", it)
		}
	}
	if err := s.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	return recs, errs
}

func openFixture(t *testing.T, name string, c config.CSV, key []string) Stream {
	t.Helper()
	cfg := &config.Source{ID: "t", Kind: config.KindCSV, Location: fixture(name), CSV: &c, NaturalKey: key}
	s, err := NewCSV(FetchOptions{}).Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCSVFormats(t *testing.T) {
	std := [][3]string{{"1", "Ann", "Leeds"}, {"2", "Bob", "York"}}
	tests := []struct {
		name string
		file string
		cfg  config.CSV
		want [][3]string
	}{
		{"comma", "comma.csv", config.CSV{}, std},
		{"semicolon", "semicolon.csv", config.CSV{Delimiter: ";"}, std},
		{"tab", "tab.tsv", config.CSV{Delimiter: "\t"}, std},
		{"tab word", "tab.tsv", config.CSV{Delimiter: "tab"}, std},
		{"pipe", "pipe.csv", config.CSV{Delimiter: "|"}, std},
		{"bom", "bom.csv", config.CSV{}, std},
		{"crlf", "crlf.csv", config.CSV{}, std},
		{"blank lines", "blank.csv", config.CSV{}, std},
		{"windows-1252", "windows1252.csv", config.CSV{Encoding: "windows-1252"}, [][3]string{{"1", "Zoé", "Zürich"}, {"2", "Bob", "€uro"}}},
		{"utf-16", "utf16.csv", config.CSV{Encoding: "utf-16"}, [][3]string{{"1", "Zoë", "Zürich"}, {"2", "Bob", "€uro"}}},
		{"explicit columns override header", "comma.csv", config.CSV{Columns: []string{"id", "name", "city"}}, std},
		{"headerless with columns", "headerless.csv", config.CSV{Header: boolp(false), Columns: []string{"id", "name", "city"}}, std},
		{"headerless default names", "headerless.csv", config.CSV{Header: boolp(false)}, std},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := []string{"id"}
			if tt.name == "headerless default names" {
				key = []string{"c1"}
			}
			recs, errs := drain(t, openFixture(t, tt.file, tt.cfg, key))
			if len(errs) != 0 {
				t.Fatalf("unexpected row errors: %+v", errs[0])
			}
			if len(recs) != len(tt.want) {
				t.Fatalf("got %d records, want %d", len(recs), len(tt.want))
			}
			for i, w := range tt.want {
				f := recs[i].Fields
				id, nm, ct := "id", "name", "city"
				if tt.name == "headerless default names" {
					id, nm, ct = "c1", "c2", "c3"
				}
				if f[id] != w[0] || f[nm] != w[1] || f[ct] != w[2] {
					t.Errorf("record %d = %v, want %v", i, f, w)
				}
				if recs[i].NaturalKey != record.Key(w[0]) {
					t.Errorf("key %q", recs[i].NaturalKey)
				}
			}
		})
	}
}

func TestCSVPositionsWithBlankLines(t *testing.T) {
	recs, _ := drain(t, openFixture(t, "blank.csv", config.CSV{}, []string{"id"}))
	if recs[0].Position != 3 || recs[1].Position != 6 {
		t.Errorf("positions = %d, %d; want 3, 6", recs[0].Position, recs[1].Position)
	}
}

func TestCSVQuoted(t *testing.T) {
	recs, errs := drain(t, openFixture(t, "quoted.csv", config.CSV{}, []string{"id"}))
	if len(errs) != 0 || len(recs) != 2 {
		t.Fatalf("recs=%d errs=%v", len(recs), errs)
	}
	if recs[0].Fields["name"] != "Smith, Ann" || recs[0].Fields["city"] != `say "hi"` {
		t.Errorf("row 1: %v", recs[0].Fields)
	}
	if recs[1].Fields["city"] != "line one\nline two" {
		t.Errorf("row 2: %q", recs[1].Fields["city"])
	}
	if recs[1].Position != 3 {
		t.Errorf("position = %d", recs[1].Position)
	}
}

func TestCSVBadRowsContinue(t *testing.T) {
	recs, errs := drain(t, openFixture(t, "badrows.csv", config.CSV{}, []string{"id"}))
	var ids []string
	for _, r := range recs {
		ids = append(ids, r.Fields["id"].(string))
	}
	if strings.Join(ids, ",") != "1,5" {
		t.Errorf("good ids = %v", ids)
	}
	want := []struct {
		pos    int64
		raw    string
		reason string
	}{
		{3, "2,Bob", "expected 3 fields, got 2"},
		{4, "3,Cy,Hull,extra", "expected 3 fields, got 4"},
		{5, `x"y,Di,Rome`, "bare quote"},
		{6, `4,"Eve"x,Bath`, "text after closing quote"},
		{7, ",Fay,Ely", `natural key field "id" is empty`},
		{9, `6,"open,Hal,Rye`, "unterminated"},
	}
	if len(errs) != len(want) {
		t.Fatalf("got %d row errors: %+v", len(errs), errs)
	}
	for i, w := range want {
		e := errs[i]
		if e.Position != w.pos || e.Raw != w.raw || !strings.Contains(e.Reason, w.reason) {
			t.Errorf("error %d = %+v, want %+v", i, *e, w)
		}
	}
}

func TestCSVHeaderErrors(t *testing.T) {
	tests := []struct {
		name, in, want string
		cfg            config.CSV
		key            []string
	}{
		{"empty", "", "no header", config.CSV{}, []string{"id"}},
		{"duplicate", "a,a\n1,2\n", "duplicate column", config.CSV{}, []string{"a"}},
		{"empty name", "a,\n1,2\n", "empty name", config.CSV{}, []string{"a"}},
		{"bad quote", "a,b\"c\n", "bare quote", config.CSV{}, []string{"a"}},
		{"key not a column", "a,b\n1,2\n", `field "z" is not a column`, config.CSV{}, []string{"z"}},
		{"bad encoding", "a\n", "unsupported encoding", config.CSV{Encoding: "ebcdic"}, []string{"a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCSVReader(context.Background(), strings.NewReader(tt.in), tt.cfg, tt.key)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCSVInvalidUTF8AndNoFinalNewline(t *testing.T) {
	s, err := NewCSVReader(context.Background(), strings.NewReader("id,v\n1,a\xffb"), config.CSV{}, []string{"id"})
	if err != nil {
		t.Fatal(err)
	}
	recs, errs := drain(t, s)
	if len(errs) != 0 || len(recs) != 1 || recs[0].Fields["v"] != "a�b" {
		t.Fatalf("recs=%v errs=%v", recs, errs)
	}
}

func TestCSVContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s, err := NewCSVReader(ctx, strings.NewReader("id\n1\n2\n"), config.CSV{}, []string{"id"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, ok := s.Next(); ok {
		t.Fatal("expected end")
	}
	if s.Err() == nil {
		t.Fatal("expected context error")
	}
}

func TestCSVAdapterSHA256(t *testing.T) {
	s := openFixture(t, "comma.csv", config.CSV{}, []string{"id"})
	drain(t, s)
	data, _ := os.ReadFile(fixture("comma.csv"))
	if got := s.(Hasher).SHA256(); got != sha(data) {
		t.Errorf("sha = %s", got)
	}
}

func TestOpenRejectsOtherKinds(t *testing.T) {
	if _, err := NewCSV(FetchOptions{}).Open(context.Background(), &config.Source{Kind: "xlsx"}); err == nil {
		t.Fatal("expected error")
	}
}

// FuzzCSVReader: the reader must never panic and always terminate, whatever the bytes.
func FuzzCSVReader(f *testing.F) {
	for _, name := range []string{"comma.csv", "quoted.csv", "badrows.csv", "bom.csv", "utf16.csv", "windows1252.csv", "blank.csv", "crlf.csv"} {
		if b, err := os.ReadFile(fixture(name)); err == nil {
			f.Add(b, uint8(0))
		}
	}
	f.Add([]byte("\"\"\"\"\n\"a"), uint8(1))
	f.Add([]byte("a;b\n\"x\"y;z\n"), uint8(2))
	f.Add([]byte{0xff, 0xfe, 0x00, 0x22}, uint8(3))
	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		cfg := config.CSV{Header: boolp(mode&1 == 0)}
		cfg.Delimiter = []string{",", ";", "tab", "|"}[(mode>>1)&3]
		cfg.Encoding = []string{"utf-8", "windows-1252", "utf-16", "utf-16be"}[(mode>>3)&3]
		s, err := NewCSVReader(context.Background(), strings.NewReader(string(data)), cfg, nil)
		if err != nil {
			return
		}
		for {
			if _, ok := s.Next(); !ok {
				break
			}
		}
	})
}

func onsConfig(t *testing.T) *config.Source {
	t.Helper()
	cfg, err := config.LoadSource(filepath.Join("..", "..", "docs", "examples", "ons-l55o.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Location = filepath.Join("..", "..", "testdata", "ons", "l55o.csv")
	return cfg
}

func TestONSGolden(t *testing.T) {
	s, err := NewCSV(FetchOptions{}).Open(context.Background(), onsConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	meta := s.Meta()
	if len(meta) != 8 || meta["CDID"] != "L55O" || meta["Unit"] != "%" || meta["Release date"] != "18-11-2026" || meta["Important notes"] != "" {
		t.Errorf("meta = %v", meta)
	}
	recs, errs := drain(t, s)
	if len(recs) != 642 || len(errs) != 0 {
		t.Fatalf("records=%d errors=%d, want 642/0", len(recs), len(errs))
	}
	if recs[0].NaturalKey != `["L55O","1989"]` || recs[0].Fields["period"] != "1989" || recs[0].Fields["value"] != "5.7" {
		t.Errorf("first = %+v", recs[0])
	}
	if recs[0].Position != 9 {
		t.Errorf("first position = %d", recs[0].Position)
	}
	if src, _ := recs[0].Fields["_source"].(map[string]any); src["line"] != int64(9) {
		t.Errorf("_source = %v", recs[0].Fields["_source"])
	}
	if m, _ := recs[0].Fields["_meta"].(map[string]string); m["CDID"] != "L55O" {
		t.Errorf("_meta = %v", recs[0].Fields["_meta"])
	}
	if last := recs[len(recs)-1]; last.NaturalKey != `["L55O","2026 OCT"]` {
		t.Errorf("last key = %s", last.NaturalKey)
	}
	seen := map[string]bool{}
	for _, r := range recs {
		if seen[r.NaturalKey] {
			t.Fatalf("duplicate key %s", r.NaturalKey)
		}
		seen[r.NaturalKey] = true
	}
}

func openString(t *testing.T, data string, c config.CSV, key []string) (Stream, error) {
	t.Helper()
	return NewCSVReader(context.Background(), strings.NewReader(data), c, key)
}

func TestPreamble(t *testing.T) {
	cols := []string{"a", "b"}
	tests := []struct {
		name, data string
		pre        config.Preamble
		header     bool
		key        []string
		wantErr    string
		wantRecs   int
		wantErrs   int
		wantKey0   string
	}{
		{"lines", "k,v\nx,\n1,2\n3,4\n", config.Preamble{Lines: 2, As: "metadata"}, false, []string{"@meta.k", "a"}, "", 2, 0, `["v","1"]`},
		{"until", "k,v\nx\n\"1\",2\n3,4\n", config.Preamble{Until: `^"?\d`, As: "metadata"}, false, []string{"@meta.k", "a"}, "", 2, 0, `["v","1"]`},
		{"until with header", "k,v\nh1,h2\n1,2\n", config.Preamble{Until: `^h1`, As: "metadata"}, true, []string{"h1"}, "", 1, 0, `["1"]`},
		{"lines too many", "k,v\n1,2\n", config.Preamble{Lines: 5, As: "metadata"}, false, []string{"a"}, "expected 5 lines, file has only 2", 0, 0, ""},
		{"until never matches", "k,v\n", config.Preamble{Until: `zzz`, As: "metadata"}, false, []string{"a"}, "no line matches", 0, 0, ""},
		{"unknown meta key", "k,v\n1,2\n", config.Preamble{Lines: 1, As: "metadata"}, false, []string{"@meta.nope"}, "unknown preamble key \"nope\"", 0, 0, ""},
		{"too many fields", "k,v,w\n1,2\n", config.Preamble{Lines: 1, As: "metadata"}, false, []string{"a"}, "expected key,value", 0, 0, ""},
		{"duplicate key", "k,v\nk,w\n1,2\n", config.Preamble{Lines: 2, As: "metadata"}, false, []string{"a"}, "duplicate key", 0, 0, ""},
		{"wrong column count", "k,v\n1,2\n3\n4,5,6\n7,8\n", config.Preamble{Lines: 1, As: "metadata"}, false, []string{"a"}, "", 2, 2, `["1"]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := config.CSV{Header: boolp(tt.header), Preamble: &tt.pre}
			if !tt.header {
				c.Columns = cols
			}
			s, err := openString(t, tt.data, c, tt.key)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			recs, errs := drain(t, s)
			if len(recs) != tt.wantRecs || len(errs) != tt.wantErrs {
				t.Fatalf("records=%d errors=%d", len(recs), len(errs))
			}
			if recs[0].NaturalKey != tt.wantKey0 {
				t.Errorf("key0 = %s, want %s", recs[0].NaturalKey, tt.wantKey0)
			}
		})
	}
}

func TestHeaderlessWithoutPreambleHasNoMeta(t *testing.T) {
	s, err := openString(t, "1,2\n", config.CSV{Header: boolp(false), Columns: []string{"a", "b"}}, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Meta() != nil {
		t.Errorf("meta = %v", s.Meta())
	}
	recs, _ := drain(t, s)
	if m, ok := recs[0].Fields["_meta"].(map[string]string); !ok || len(m) != 0 {
		t.Errorf("_meta = %v", recs[0].Fields["_meta"])
	}
}
