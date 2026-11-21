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
