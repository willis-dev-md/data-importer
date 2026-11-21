package source

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/willis-dev-md/data-importer/internal/config"
)

const asheYAML = `xlsx:
  sheets: { except: [Notes] }
  header: false
  start_row: 6
  columns: { A: description, B: soc_code, C: jobs, D: median, F: mean }
  use_cached_values: true
table:
  - skip_rows: { where: description, match: '^\s*(a  |b  |KEY|The quality|Source|Not Classified|Key|Statistical|CV|Estimates|x =|\.\. =|: =|- =)' }
  - skip_rows: { where: description, match: '^\s*(18-21|22-29|30-39|40-49|50-59|60\+)\s*$' }
  - split:
      column: description
      regex: '^\s*(?P<age_band>18-21|22-29|30-39|40-49|50-59|60\+)\s+(?P<soc_name>.*?)\s*$'
      optional: true
  - strip: { soc_code: ["'"] }
  - normalize: { soc_name: [collapse_ws, trim], soc_code: [trim], jobs: to_number, median: to_number, mean: to_number }
natural_key: ["@ctx.sheet", description]
`

func TestXLSXBoEGolden(t *testing.T) {
	cfg, err := config.LoadSource(filepath.Join("..", "..", "docs", "examples", "boe-baserate.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Location = filepath.Join("..", "..", "testdata", "boe", "baserate-trimmed.xlsx") // the example points at the BoE website
	st, err := NewXLSX(FetchOptions{}).Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := st.(Hasher); !ok || len(h.SHA256()) != 64 {
		t.Errorf("stream should report the sha256 of the workbook")
	}
	g := drainX(t, st)
	if g.err != nil || len(g.errs) != 0 {
		t.Fatalf("err=%v rowerrors=%v", g.err, g.errs)
	}
	if len(g.recs) != 854 {
		t.Fatalf("records = %d, want 854", len(g.recs))
	}
	series := map[string]int{}
	noDay := 0
	keys := map[string]bool{}
	for _, r := range g.recs {
		series[ctxOf(r)["series_name"].(string)]++
		if r.Fields["day"] == "" {
			noDay++
		}
		if keys[r.NaturalKey] {
			t.Errorf("duplicate natural key %s", r.NaturalKey)
		}
		keys[r.NaturalKey] = true
		m, _ := r.Fields["month"].(string)
		if m == "" || m != strings.ToLower(strings.TrimSpace(m)) {
			t.Errorf("month not normalised: %q", r.Fields["month"])
		}
		if _, ok := r.Fields["year"].(int64); !ok {
			t.Fatalf("year %v (%T) is not an int64", r.Fields["year"], r.Fields["year"])
		}
		src := r.Fields["_source"].(map[string]any)
		if src["sheet"] != "HISTORICAL SINCE 1694" || src["row"].(int64) < 8 || ctxOf(r)["sheet"] != "HISTORICAL SINCE 1694" {
			t.Errorf("bad _source/_ctx.sheet: %v", r.Fields)
		}
	}
	if len(series) != 5 {
		t.Errorf("series = %v, want 5 distinct", series)
	}
	if noDay != 17 {
		t.Errorf("records without a day = %d, want 17", noDay)
	}
	first := g.recs[0]
	if ctxOf(first)["series_name"] != "Bank Rate" || first.Fields["year"] != int64(1694) || first.Fields["month"] != "oct" || first.Fields["day"] != "" || first.Fields["rate"] != "6" {
		t.Errorf("first record: %v", first.Fields)
	}
	found := false
	for _, r := range g.recs {
		if r.Fields["year"] == int64(1695) && r.Fields["month"] == "may" && ctxOf(r)["series_name"] == "Bank Rate" {
			found = true
		}
	}
	if !found {
		t.Error("no record for 1695 may (year not filled down)")
	}
	// The workbook has text years ('1694') and numeric years (2025): both were normalised to int64 above;
	// the 1933-1938 note row is a text row without a rate and must be gone.
	for _, r := range g.recs {
		if r.Fields["year"] == int64(1933) && r.Fields["rate"] == "" {
			t.Error("'1933-1938 no changes' leaked")
		}
	}
}

func TestXLSXASHEGolden(t *testing.T) {
	g := readXLSX(t, fixtureXLSX(t, "ashe/t20-7a-trimmed.xlsx"), asheYAML)
	if g.err != nil || len(g.errs) != 0 {
		t.Fatalf("err=%v rowerrors=%v", g.err, g.errs)
	}
	per := map[string][]*Record{}
	keys := map[string]bool{}
	for _, r := range g.recs {
		s := ctxOf(r)["sheet"].(string)
		per[s] = append(per[s], r)
		if keys[r.NaturalKey] {
			t.Errorf("duplicate key %s", r.NaturalKey)
		}
		keys[r.NaturalKey] = true
	}
	if len(per) != 2 || len(per["All"]) != 217 || len(per["Male Full-Time"]) != 217 {
		t.Fatalf("rows per sheet: All=%d Male Full-Time=%d (sheets %d)", len(per["All"]), len(per["Male Full-Time"]), len(per))
	}
	if f := per["All"][0].Fields; f["age_band"] != "" || f["description"] != "All employees" {
		t.Errorf("first row: %v", f)
	}
	var sawAll, sawTwoDigit bool
	for _, r := range per["All"] {
		f := r.Fields
		if f["description"] == "18-21 ALL OCCUPATIONS" {
			sawAll = true
			if f["age_band"] != "18-21" || f["soc_name"] != "ALL OCCUPATIONS" || f["soc_code"] != "" {
				t.Errorf("18-21 ALL OCCUPATIONS: %v", f)
			}
		}
		if c, _ := f["soc_code"].(string); len(c) == 2 {
			sawTwoDigit = true
			if strings.Contains(c, "'") {
				t.Errorf("apostrophe left in %q", c)
			}
		}
		if strings.Contains(f["soc_code"].(string), "'") {
			t.Errorf("apostrophe left in %v", f)
		}
	}
	if !sawAll || !sawTwoDigit {
		t.Errorf("sawAll=%v sawTwoDigit=%v", sawAll, sawTwoDigit)
	}
}

func TestXLSXSheetSelection(t *testing.T) {
	data := buildXLSX(t,
		sheetSpec{name: "Notes", rows: [][]string{{"s:k"}, {"s:note"}}},
		sheetSpec{name: "Data A", rows: [][]string{{"s:k"}, {"s:a1"}, {"s:a2"}}},
		sheetSpec{name: "Hidden", hidden: true, rows: [][]string{{"s:k"}, {"s:h1"}}},
	)
	tests := []struct {
		name    string
		sheets  string
		want    []string // "sheet:value"
		wantErr []string // substrings of the Open error
	}{
		{"sheet by name", "sheet: Data A", []string{"Data A:a1", "Data A:a2"}, nil},
		{"hidden sheet by name", "sheet: Hidden", []string{"Hidden:h1"}, nil},
		{"sheet_index", "sheet_index: 1", []string{"Data A:a1", "Data A:a2"}, nil},
		{"sheet_index hidden", "sheet_index: 2", []string{"Hidden:h1"}, nil},
		{"list", `sheets: ["Hidden", "Data A"]`, []string{"Hidden:h1", "Data A:a1", "Data A:a2"}, nil},
		{"except", "sheets: { except: [Notes] }", []string{"Data A:a1", "Data A:a2", "Hidden:h1"}, nil},
		{"unknown sheet", "sheet: Nope", nil, []string{`"Nope" not found`, `"Notes"`, `"Data A"`, `"Hidden" (hidden)`}},
		{"unknown in list", `sheets: [Notes, Nope]`, nil, []string{`"Nope" not found`, "available sheets"}},
		{"unknown in except", `sheets: {except: [Nope]}`, nil, []string{`"Nope" not found`}},
		{"index out of range", "sheet_index: 3", nil, []string{"out of range", `"Notes"`}},
		{"all excluded", "sheets: {except: [Notes, Data A, Hidden]}", nil, []string{"every sheet is excluded"}},
		{"duplicate in list", `sheets: [Notes, Notes]`, nil, []string{"listed twice"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := xcfg(t, "xlsx: { "+strings.ReplaceAll(strings.ReplaceAll(tc.sheets, "\n", " "), "{ except", "{ except")+" }\nnatural_key: [\"@ctx.sheet\", k]\n")
			st, err := NewXLSXReader(context.Background(), data, cfg, XLSXLimits{})
			if tc.wantErr != nil {
				if err == nil {
					_ = st.Close()
					t.Fatal("want an error")
				}
				for _, w := range tc.wantErr {
					if !strings.Contains(err.Error(), w) {
						t.Errorf("error %q lacks %q", err, w)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			g := drainX(t, st)
			var have []string
			for _, r := range g.recs {
				have = append(have, ctxOf(r)["sheet"].(string)+":"+r.Fields["k"].(string))
				if r.Fields["_source"].(map[string]any)["sheet"] != ctxOf(r)["sheet"] {
					t.Errorf("_source.sheet != _ctx.sheet: %v", r.Fields)
				}
			}
			if strings.Join(have, ",") != strings.Join(tc.want, ",") || g.err != nil || len(g.errs) != 0 {
				t.Errorf("got %v err=%v errs=%v, want %v", have, g.err, g.errs, tc.want)
			}
		})
	}
}

func TestXLSXFormulasAreNeverEvaluated(t *testing.T) {
	data := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{
		{"s:id", "s:v"},
		{"n:1", "f:1+1|999"},    // cached value differs from what evaluation would give: the cache wins
		{"n:2", "f:SUM(A2:A3)"}, // no cached value: nothing to read, never computed
		{"n:3", `f:HYPERLINK("http://example.invalid/x","y")|link`},
		{"n:4", "f:A2+A3|"},
	}})
	g := readXLSX(t, data, "xlsx: {sheet: S}\nnatural_key: [id]\n")
	if g.err != nil || len(g.errs) != 0 || len(g.recs) != 4 {
		t.Fatalf("%v %v %d", g.err, g.errs, len(g.recs))
	}
	want := []string{"999", "", "link", ""}
	for i, r := range g.recs {
		if r.Fields["v"] != want[i] {
			t.Errorf("row %d: v = %q, want %q", i, r.Fields["v"], want[i])
		}
	}
}

func TestXLSXCellValues(t *testing.T) {
	// Every cell arrives as text exactly as stored (shared/inline text, number as saved, boolean 1/0, error
	// literal); numbers are not reformatted by display formats.
	data := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{
		{"s:id", "s:v"},
		{"n:1", "s:1694"},
		{"n:2", "n:2025"},
		{"n:3", "n:13.8438"},
		{"n:4", "b:1"},
		{"n:5", "e:#N/A"},
		{"n:6", "s:  padded  "},
		{"n:7", "s:"},
		{"n:8", "n:1E-3"},
	}})
	g := readXLSX(t, data, "xlsx: {sheet: S}\nnatural_key: [id]\n")
	if g.err != nil || len(g.errs) != 0 {
		t.Fatalf("%v %v", g.err, g.errs)
	}
	want := []string{"1694", "2025", "13.8438", "1", "#N/A", "  padded  ", "", "1E-3"}
	if len(g.recs) != len(want) {
		t.Fatalf("records = %d", len(g.recs))
	}
	for i, r := range g.recs {
		if r.Fields["v"] != want[i] {
			t.Errorf("row %d: %q, want %q", i+1, r.Fields["v"], want[i])
		}
	}
}

func TestXLSXMixedTypesNormalise(t *testing.T) {
	data := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{
		{"s:year", "s:rate"},
		{"s:1694", "n:6"},
		{"n:2025", "s:x"},
		{"s:abc", "n:1"},
		{"n:1.5", "n:1"},
		{"s:1e3", "s:4.5"},
	}})
	g := readXLSX(t, data, "xlsx: {sheet: S}\ntable:\n  - normalize: {year: to_int, rate: to_number}\nnatural_key: [year, rate]\n")
	if g.err != nil {
		t.Fatal(g.err)
	}
	if len(g.recs) != 3 || len(g.errs) != 2 {
		t.Fatalf("records=%d errors=%d (%v)", len(g.recs), len(g.errs), g.errs)
	}
	if g.recs[0].Fields["year"] != int64(1694) || g.recs[0].Fields["rate"] != int64(6) {
		t.Errorf("row 2: %v", g.recs[0].Fields)
	}
	if g.recs[1].Fields["year"] != int64(2025) || g.recs[1].Fields["rate"] != "x" {
		t.Errorf("row 3 (marker left untouched): %v", g.recs[1].Fields)
	}
	if g.recs[2].Fields["year"] != int64(1000) || g.recs[2].Fields["rate"] != 4.5 {
		t.Errorf("row 6: %v", g.recs[2].Fields)
	}
	for _, e := range g.errs {
		if !strings.Contains(e.Reason, `normalize: column "year"`) || e.Position == 0 || !strings.HasPrefix(e.Raw, "S!") {
			t.Errorf("row error: %+v", e)
		}
	}
}

func TestXLSXMergedCellsAndBlankRows(t *testing.T) {
	data := buildXLSX(t, sheetSpec{name: "S",
		rows:   [][]string{{"s:grp", "s:v"}, {"s:G1", "n:1"}, {"", "n:2"}, {}, {"s:G2", "n:3"}},
		merges: []string{"A2:A3"}})
	g := readXLSX(t, data, "xlsx: {sheet: S}\nnatural_key: [v]\n")
	if g.err != nil || len(g.errs) != 0 || len(g.recs) != 3 {
		t.Fatalf("%v %v %d", g.err, g.errs, len(g.recs))
	}
	// A merged range is read as its top-left cell only; the covered cell is empty (fill_down can carry it).
	if g.recs[0].Fields["grp"] != "G1" || g.recs[1].Fields["grp"] != "" || g.recs[2].Fields["grp"] != "G2" {
		t.Errorf("groups: %v %v %v", g.recs[0].Fields["grp"], g.recs[1].Fields["grp"], g.recs[2].Fields["grp"])
	}
	if g.recs[2].Position != 5 {
		t.Errorf("row numbers must survive blank rows: %d", g.recs[2].Position)
	}
	g = readXLSX(t, data, "xlsx: {sheet: S}\ntable:\n  - fill_down: {columns: [grp]}\nnatural_key: [v]\n")
	if g.recs[1].Fields["grp"] != "G1" {
		t.Errorf("fill_down over merged cell: %v", g.recs[1].Fields)
	}
}

func TestXLSXHeaderAndRowRange(t *testing.T) {
	data := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{
		{"s:Title"},
		{"s:id", "s:Name", "", "s:other"},
		{"n:1", "s:a", "s:ignored", "s:p"},
		{"n:2", "s:b", "s:ignored", "s:q"},
		{"n:3", "s:c", "s:ignored", "s:r"},
	}})
	g := readXLSX(t, data, "xlsx: {sheet: S, header_row: 2, end_row: 4}\nnatural_key: [id]\n")
	if g.err != nil || len(g.errs) != 0 || len(g.recs) != 2 {
		t.Fatalf("%v %v %d", g.err, g.errs, len(g.recs))
	}
	f := g.recs[0].Fields
	if f["Name"] != "a" || f["other"] != "p" || f["id"] != "1" {
		t.Errorf("fields: %v", f)
	}
	if _, ok := f[""]; ok || len(f) != 3+2 { // id, Name, other + _ctx, _source
		t.Errorf("a column without a header must not be read: %v", f)
	}
	// header + columns by letter renames and selects.
	g = readXLSX(t, data, "xlsx: {sheet: S, header_row: 2, columns: {A: n, B: label}}\nnatural_key: [n]\n")
	if len(g.recs) != 3 || g.recs[0].Fields["label"] != "a" {
		t.Errorf("%v", g.recs)
	}
}

func TestXLSXHeaderProblems(t *testing.T) {
	dup := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{"s:a", "s:a"}, {"n:1", "n:2"}}})
	us := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{"s:a", "s:_x"}, {"n:1", "n:2"}}})
	empty := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{}, {"n:1"}}})
	ok := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{"s:a"}, {"n:1"}}})
	tests := []struct {
		name string
		data []byte
		yaml string
		want string
	}{
		{"duplicate header", dup, "xlsx: {sheet: S}\nnatural_key: [a]\n", `duplicate column name "a"`},
		{"underscore header", us, "xlsx: {sheet: S}\nnatural_key: [a]\n", "starts with an underscore"},
		{"no header", empty, "xlsx: {sheet: S}\nnatural_key: [a]\n", "no header names found in row 1"},
		{"natural key not a column", ok, "xlsx: {sheet: S}\nnatural_key: [zzz]\n", `"zzz" is neither a field`},
		{"step column not a column", ok, "xlsx: {sheet: S}\ntable: [{drop_if_empty: [zzz]}]\nnatural_key: [a]\n", "unknown column"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, err := NewXLSXReader(context.Background(), tc.data, xcfg(t, tc.yaml), XLSXLimits{})
			if err == nil {
				_ = st.Close()
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q lacks %q", err, tc.want)
			}
		})
	}
}

func TestXLSXContextAndNaturalKey(t *testing.T) {
	data := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{"s:id"}, {"n:1"}, {"n:2"}}})
	g := readXLSX(t, data, "xlsx: {sheet: S}\ncontext: {year: 2026, status: provisional}\nnatural_key: [\"@ctx.status\", \"@ctx.year\", \"@ctx.sheet\", id]\n")
	if g.err != nil || len(g.errs) != 0 || len(g.recs) != 2 {
		t.Fatalf("%v %v %d", g.err, g.errs, len(g.recs))
	}
	c := ctxOf(g.recs[0])
	if c["year"] != "2026" || c["status"] != "provisional" || c["sheet"] != "S" || len(c) != 3 {
		t.Errorf("_ctx = %v", c)
	}
	if g.recs[0].NaturalKey != `["provisional","2026","S","1"]` {
		t.Errorf("key = %s", g.recs[0].NaturalKey)
	}
	// An empty natural key part is allowed (BoE events have no day) but not when every part is empty.
	data = buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{"s:a", "s:b"}, {"s:x", ""}, {"n:1", "n:2"}, {"s: ", "s:"}, {"", "n:5"}}})
	g = readXLSX(t, data, "xlsx: {sheet: S}\nnatural_key: [a]\n")
	if len(g.recs) != 2 || len(g.errs) != 1 || !strings.Contains(g.errs[0].Reason, "natural key is empty") || g.errs[0].Position != 5 {
		t.Errorf("recs=%d errs=%+v", len(g.recs), g.errs)
	}
}

func TestXLSXSectionsPerSheet(t *testing.T) {
	// Section state does not leak from one sheet to the next.
	data := buildXLSX(t,
		sheetSpec{name: "S1", rows: [][]string{{"s:Alpha", ""}, {"s:2020", "n:1"}, {"", "n:2"}}},
		sheetSpec{name: "S2", rows: [][]string{{"s:2021", "n:3"}, {"s:Beta", ""}, {"", "n:4"}}},
	)
	g := readXLSX(t, data, `xlsx: {sheets: [S1, S2], header: false, columns: {A: year, B: v}}
table:
  - section: {column: year, match: '^[A-Za-z]', set: grp, default: none}
  - fill_down: {columns: [year], reset_on: section}
natural_key: ["@ctx.sheet", grp, v]
`)
	if g.err != nil || len(g.errs) != 0 || len(g.recs) != 4 {
		t.Fatalf("%v %v %d", g.err, g.errs, len(g.recs))
	}
	want := []string{"S1/Alpha/2020", "S1/Alpha/2020", "S2/none/2021", "S2/Beta/"}
	for i, r := range g.recs {
		have := ctxOf(r)["sheet"].(string) + "/" + ctxOf(r)["grp"].(string) + "/" + r.Fields["year"].(string)
		if have != want[i] {
			t.Errorf("rec %d = %s, want %s", i, have, want[i])
		}
	}
}

func TestXLSXLimits(t *testing.T) {
	good := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{"s:a"}, {"n:1"}, {"n:2"}, {"n:3"}}})
	cfg := xcfg(t, "xlsx: {sheet: S}\nnatural_key: [a]\n")

	t.Run("too many rows", func(t *testing.T) {
		st, err := NewXLSXReader(context.Background(), good, cfg, XLSXLimits{MaxRows: 3})
		if err != nil {
			t.Fatal(err)
		}
		g := drainX(t, st)
		if !errors.Is(g.err, ErrXLSXTooManyRows) {
			t.Errorf("err = %v", g.err)
		}
	})
	t.Run("row limit counts rows before start_row", func(t *testing.T) {
		st, err := NewXLSXReader(context.Background(), good, xcfg(t, "xlsx: {sheet: S, header: false, start_row: 4, columns: {A: a}}\nnatural_key: [a]\n"), XLSXLimits{MaxRows: 3})
		if err != nil {
			t.Fatal(err)
		}
		if g := drainX(t, st); !errors.Is(g.err, ErrXLSXTooManyRows) {
			t.Errorf("err = %v", g.err)
		}
	})
	t.Run("file too large", func(t *testing.T) {
		_, err := NewXLSXReader(context.Background(), good, cfg, XLSXLimits{MaxFileBytes: 100})
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("err = %v", err)
		}
		dir := t.TempDir()
		p := filepath.Join(dir, "w.xlsx")
		if err := os.WriteFile(p, good, 0o600); err != nil {
			t.Fatal(err)
		}
		c := xcfg(t, "xlsx: {sheet: S}\nnatural_key: [a]\n")
		c.Location = p
		_, err = (&XLSXAdapter{Limits: XLSXLimits{MaxFileBytes: 100}}).Open(context.Background(), c)
		if !errors.Is(err, ErrTooLarge) {
			t.Errorf("adapter err = %v", err)
		}
	})
	t.Run("too many sheets", func(t *testing.T) {
		_, err := NewXLSXReader(context.Background(), buildXLSX(t, sheetSpec{name: "A"}, sheetSpec{name: "B"}, sheetSpec{name: "C"}), xcfg(t, "xlsx: {sheet: A, header: false, columns: {A: a}}\nnatural_key: [a]\n"), XLSXLimits{MaxSheets: 2})
		if err == nil || !strings.Contains(err.Error(), "3 sheets") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("too many entries", func(t *testing.T) {
		_, err := NewXLSXReader(context.Background(), good, cfg, XLSXLimits{MaxZipEntries: 2})
		if err == nil || !strings.Contains(err.Error(), "archive entries") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("zip bomb", func(t *testing.T) {
		// A valid workbook whose sheet part is 20 MB of spaces (a few KB compressed), against a 1 MB limit.
		bomb := bombXLSX(t, 20<<20)
		if len(bomb) > 200<<10 {
			t.Fatalf("test bomb is not small: %d bytes", len(bomb))
		}
		_, err := NewXLSXReader(context.Background(), bomb, cfg, XLSXLimits{MaxUnzipBytes: 1 << 20})
		if !errors.Is(err, ErrXLSXUnzipLimit) {
			t.Errorf("err = %v", err)
		}
		// Many parts, each small, together over the limit.
		_, err = NewXLSXReader(context.Background(), bombXLSX(t, 600<<10), cfg, XLSXLimits{MaxUnzipBytes: 512 << 10})
		if !errors.Is(err, ErrXLSXUnzipLimit) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("header lies about the size", func(t *testing.T) {
		// The central directory says 10 bytes, the data inflates to 4 MB: the real size is checked, not the header.
		lying := lyingXLSX(t, 4<<20, 10)
		_, err := NewXLSXReader(context.Background(), lying, cfg, XLSXLimits{MaxUnzipBytes: 1 << 20})
		if err == nil {
			t.Fatal("want an error")
		}
		if !errors.Is(err, ErrXLSXUnzipLimit) && !errors.Is(err, ErrXLSXBadArchive) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("default limits", func(t *testing.T) {
		d := XLSXLimits{}.withDefaults()
		if d.MaxFileBytes != 100<<20 || d.MaxRows != 2_000_000 || d.MaxUnzipBytes != 1<<30 || d.MaxSheets != 256 || d.MaxZipEntries != 10_000 {
			t.Errorf("defaults: %+v", d)
		}
	})
}

// bombXLSX returns a structurally valid workbook whose sheet XML contains n bytes of padding.
func bombXLSX(t testing.TB, n int) []byte {
	t.Helper()
	base := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{"s:a"}, {"n:1"}}})
	zr, err := zip.NewReader(bytes.NewReader(base), int64(len(base)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		rc, _ := f.Open()
		var b bytes.Buffer
		_, _ = b.ReadFrom(rc)
		_ = rc.Close()
		body := b.String()
		if strings.HasPrefix(f.Name, "xl/worksheets/") {
			body = strings.Replace(body, "<sheetData>", "<sheetData>"+strings.Repeat(" ", n), 1)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// lyingXLSX builds a zip whose one entry inflates to realSize bytes but declares declared bytes.
func lyingXLSX(t testing.TB, realSize int, declared uint32) []byte {
	t.Helper()
	var raw bytes.Buffer
	fw, _ := flate.NewWriter(&raw, flate.BestCompression)
	_, _ = fw.Write(bytes.Repeat([]byte{' '}, realSize))
	_ = fw.Close()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name: "xl/worksheets/sheet1.xml", Method: zip.Deflate,
		CRC32:            crc32.ChecksumIEEE(bytes.Repeat([]byte{' '}, declared32(declared))),
		CompressedSize64: uint64(raw.Len()), UncompressedSize64: uint64(declared),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(raw.Bytes())
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func declared32(n uint32) int { return int(n) }

func TestXLSXNotAWorkbook(t *testing.T) {
	good := fixtureXLSX(t, "boe/baserate-trimmed.xlsx")
	var emptyZip bytes.Buffer
	_ = zip.NewWriter(&emptyZip).Close()
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"text", []byte("id,name\n1,a\n")},
		{"truncated", good[:len(good)/2]},
		{"truncated tail", good[:len(good)-30]},
		{"empty zip", emptyZip.Bytes()},
		{"ole header", append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 600)...)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, err := NewXLSXReader(context.Background(), tc.data, xcfg(t, "xlsx: {sheet: S}\nnatural_key: [a]\n"), XLSXLimits{})
			if err == nil {
				g := drainX(t, st)
				t.Fatalf("want an error, got %d records, stream err %v", len(g.recs), g.err)
			}
			if !errors.Is(err, ErrXLSXBadArchive) {
				t.Errorf("err = %v, want ErrXLSXBadArchive", err)
			}
		})
	}
}

func TestXLSXCorruptSheetXMLIsAnErrorNeverSilent(t *testing.T) {
	base := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{"s:a"}, {"n:1"}, {"n:2"}}})
	tests := []struct {
		name   string
		mutate func(string) string
		clean  int // a clean end is acceptable only with exactly this many records (nothing lost); 0 = must be an error
	}{
		{name: "bad row and cell refs", mutate: func(s string) string {
			return strings.Replace(s, "<sheetData>", "<sheetData><row r=\"x\"><c r=\"ZZZZZ99999999\"><v>1</v></c></row>", 1)
		}},
		{name: "missing sheetData end", mutate: func(s string) string { return strings.Replace(s, "</sheetData>", "", 1) }},
		{name: "negative row number", mutate: func(s string) string { return strings.Replace(s, `r="2"`, `r="-5"`, 1) }, clean: 2},
		{name: "row beyond the sheet limit", mutate: func(s string) string { return strings.Replace(s, `<row r="1">`, `<row r="1048577">`, 1) }},
		{name: "truncated", mutate: func(s string) string { return s[:len(s)/2] }},
		{name: "truncated mid-row", mutate: func(s string) string { return s[:strings.Index(s, `r="3"`)+3] }},
		{name: "control character", mutate: func(s string) string { return strings.Replace(s, "<v>2</v>", "<v>2\x01</v>", 1) }},
		{name: "unclosed element", mutate: func(s string) string { return strings.Replace(s, "</row></sheetData>", "</sheetData>", 1) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := rewriteXLSX(t, base, func(n, b string) string {
				if strings.HasPrefix(n, "xl/worksheets/") {
					return tc.mutate(b)
				}
				return b
			}, nil)
			st, err := NewXLSXReader(context.Background(), data, xcfg(t, "xlsx: {sheet: S}\nnatural_key: [a]\n"), XLSXLimits{})
			if err != nil {
				return // rejected at open with an error: fine
			}
			g := drainX(t, st) // must not panic
			if g.err == nil && len(g.errs) == 0 && len(g.recs) != tc.clean {
				t.Fatalf("silent success: %d records, no error", len(g.recs))
			}
		})
	}
}

func TestXLSXCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	st, err := NewXLSXReader(ctx, fixtureXLSX(t, "boe/baserate-trimmed.xlsx"), xcfg(t, boeSheetYAML), XLSXLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Next(); !ok {
		t.Fatal("first item")
	}
	cancel()
	for {
		if _, ok := st.Next(); !ok {
			break
		}
	}
	if !errors.Is(st.Err(), context.Canceled) {
		t.Errorf("err = %v", st.Err())
	}
	_ = st.Close()
	_ = st.Close() // idempotent
}

const boeSheetYAML = `xlsx: {sheet: "HISTORICAL SINCE 1694", start_row: 8, header: false, columns: {A: year, B: day, C: month, D: rate}}
natural_key: [year, day, month]
`

func TestXLSXOpenWrongKind(t *testing.T) {
	if _, err := NewXLSX(FetchOptions{}).Open(context.Background(), &config.Source{Kind: config.KindCSV}); err == nil {
		t.Error("want an error for a csv config")
	}
	if _, err := NewXLSX(FetchOptions{}).Open(context.Background(), nil); err == nil {
		t.Error("want an error for nil")
	}
}

func FuzzXLSXReader(f *testing.F) {
	f.Add(fixtureXLSX(f, "boe/baserate-trimmed.xlsx"))
	f.Add(buildXLSX(f, sheetSpec{name: "S", rows: [][]string{{"s:a", "s:b"}, {"n:1", "f:1+1|2"}}}))
	f.Add([]byte("PK\x03\x04"))
	f.Add([]byte{})
	cfg := xcfg(f, "xlsx: {sheet_index: 0}\ntable:\n  - drop_if_empty: [a]\n  - normalize: {a: to_int}\nnatural_key: [a]\n")
	lim := XLSXLimits{MaxRows: 5000, MaxUnzipBytes: 8 << 20, MaxFileBytes: 4 << 20}
	f.Fuzz(func(t *testing.T, data []byte) {
		st, err := NewXLSXReader(context.Background(), data, cfg, lim)
		if err != nil {
			return
		}
		for {
			it, ok := st.Next()
			if !ok {
				break
			}
			if (it.Record == nil) == (it.Error == nil) {
				t.Fatalf("item must be exactly one of record and error: %+v", it)
			}
		}
		_ = st.Close()
	})
}

// rewriteXLSX copies a workbook, letting fn change the body of every part (fn may return the same body).
// extra parts are appended.
func rewriteXLSX(t testing.TB, data []byte, fn func(name, body string) string, extra map[string]string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	put := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	for _, f := range zr.File {
		rc, _ := f.Open()
		var b bytes.Buffer
		_, _ = b.ReadFrom(rc)
		_ = rc.Close()
		put(f.Name, fn(f.Name, b.String()))
	}
	for n, b := range extra {
		put(n, b)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// A raw control character in the XML makes the standard decoder (and so excelize) stop reading; that must be
// an error, never a short successful stream.
func TestXLSXIllegalXMLCharactersAreErrors(t *testing.T) {
	rows := [][]string{{"s:a"}, {"s:first"}, {"s:second"}, {"s:third"}, {"s:fourth"}}
	base := buildXLSX(t, sheetSpec{name: "S", rows: rows})
	cfg := "xlsx: {sheet: S}\nnatural_key: [a]\n"
	bads := map[string]string{"raw SOH": "a\x01b", "raw VT": "a\x0bb", "raw NUL": "a\x00b", "char ref SOH": "a&#1;b", "char ref NUL": "a&#0;b", "raw FFFE": "a\xef\xbf\xbeb"}
	for _, target := range []string{"first", "second", "fourth"} { // first, middle and last data row
		for name, bad := range bads {
			t.Run("inline "+target+" "+name, func(t *testing.T) {
				data := rewriteXLSX(t, base, func(n, b string) string { return strings.Replace(b, ">"+target+"<", ">"+bad+"<", 1) }, nil)
				st, err := NewXLSXReader(context.Background(), data, xcfg(t, cfg), XLSXLimits{})
				if err == nil {
					g := drainX(t, st)
					t.Fatalf("want an error; got %d records, %d row errors, stream err %v", len(g.recs), len(g.errs), g.err)
				}
				if !errors.Is(err, ErrXLSXBadArchive) || !strings.Contains(err.Error(), "xl/worksheets/sheet1.xml") || !strings.Contains(err.Error(), "not well-formed XML") {
					t.Errorf("unclear error: %v", err)
				}
			})
			t.Run("shared "+target+" "+name, func(t *testing.T) {
				// Move the cell text into xl/sharedStrings.xml.
				idx := map[string]int{"first": 0, "second": 1, "fourth": 3}[target]
				sst := `<?xml version="1.0" encoding="UTF-8"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="4" uniqueCount="4"><si><t>first</t></si><si><t>second</t></si><si><t>third</t></si><si><t>fourth</t></si></sst>`
				sst = strings.Replace(sst, "<t>"+target+"</t>", "<t>"+bad+"</t>", 1)
				_ = idx
				data := rewriteXLSX(t, base, func(n, b string) string {
					if strings.HasPrefix(n, "xl/worksheets/") {
						for i, w := range []string{"first", "second", "third", "fourth"} {
							b = strings.Replace(b, `t="inlineStr"><is><t xml:space="preserve">`+w+`</t></is>`, `t="s"><v>`+string(rune('0'+i))+`</v>`, 1)
						}
					}
					return b
				}, map[string]string{"xl/sharedStrings.xml": sst})
				st, err := NewXLSXReader(context.Background(), data, xcfg(t, cfg), XLSXLimits{})
				if err == nil {
					g := drainX(t, st)
					t.Fatalf("want an error; got %d records, %d row errors, stream err %v", len(g.recs), len(g.errs), g.err)
				}
				if !errors.Is(err, ErrXLSXBadArchive) || !strings.Contains(err.Error(), "xl/sharedStrings.xml") {
					t.Errorf("unclear error: %v", err)
				}
			})
		}
	}
	t.Run("escaped _x0000_ form still works", func(t *testing.T) {
		data := rewriteXLSX(t, base, func(n, b string) string { return strings.Replace(b, ">second<", ">a_x0000_b<", 1) }, nil)
		g := readXLSX(t, data, cfg)
		if g.err != nil || len(g.errs) != 0 || len(g.recs) != 4 {
			t.Fatalf("%v %v %d", g.err, g.errs, len(g.recs))
		}
	})
	t.Run("shared strings in a normal workbook", func(t *testing.T) {
		sst := `<?xml version="1.0" encoding="UTF-8"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>first</t></si><si><t>second</t></si><si><t>third</t></si><si><t>fourth</t></si></sst>`
		data := rewriteXLSX(t, base, func(n, b string) string {
			if strings.HasPrefix(n, "xl/worksheets/") {
				for i, w := range []string{"first", "second", "third", "fourth"} {
					b = strings.Replace(b, `t="inlineStr"><is><t xml:space="preserve">`+w+`</t></is>`, `t="s"><v>`+string(rune('0'+i))+`</v>`, 1)
				}
			}
			return b
		}, map[string]string{"xl/sharedStrings.xml": sst})
		g := readXLSX(t, data, cfg)
		if g.err != nil || len(g.recs) != 4 || g.recs[3].Fields["a"] != "fourth" {
			t.Fatalf("%v %d", g.err, len(g.recs))
		}
	})
}

// The reader must notice when the row stream ends before the last row the archive pass saw.
func TestXLSXEarlyEndOfRowsIsAnError(t *testing.T) {
	data := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{"s:a"}, {"s:x"}, {"s:y"}, {"s:z"}}})
	info, err := checkZip(data, XLSXLimits{}.withDefaults())
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := info.lastRowOf("S"); !ok || n != 4 {
		t.Fatalf("lastRowOf = %d %v", n, ok)
	}
	if _, ok := info.lastRowOf("nope"); ok {
		t.Error("unknown sheet")
	}
	// Pretend the pass saw more rows than the reader delivers.
	st, err := NewXLSXReader(context.Background(), data, xcfg(t, "xlsx: {sheet: S}\nnatural_key: [a]\n"), XLSXLimits{})
	if err != nil {
		t.Fatal(err)
	}
	xs := st.(*xlsxStream)
	xs.info.lastRow[xs.info.rels[xs.info.sheets[0].rid]] = 9
	g := drainX(t, st)
	if !errors.Is(g.err, ErrXLSXBadArchive) || !strings.Contains(g.err.Error(), "ended at row 4") || len(g.recs) != 3 {
		t.Errorf("err = %v, records %d", g.err, len(g.recs))
	}
	// end_row stops early on purpose: that is not an error.
	st, _ = NewXLSXReader(context.Background(), data, xcfg(t, "xlsx: {sheet: S, end_row: 2}\nnatural_key: [a]\n"), XLSXLimits{})
	if g := drainX(t, st); g.err != nil || len(g.recs) != 1 {
		t.Errorf("end_row: %v %d", g.err, len(g.recs))
	}
}

func TestXLSXLyingHeaderMessage(t *testing.T) {
	cfg := xcfg(t, "xlsx: {sheet: S}\nnatural_key: [a]\n")
	for name, data := range map[string][]byte{
		"declares less": lyingXLSX(t, 4096, 10),
		"declares more": lyingXLSX(t, 10, 4096),
	} {
		_, err := NewXLSXReader(context.Background(), data, cfg, XLSXLimits{})
		if err == nil || !errors.Is(err, ErrXLSXBadArchive) && !errors.Is(err, ErrXLSXUnzipLimit) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if !strings.Contains(err.Error(), "corrupt or its header contradicts its contents") || strings.Contains(err.Error(), "not a valid zip file") {
			t.Errorf("%s: unclear message: %v", name, err)
		}
	}
}

// structBase is a small workbook with explicit row and cell references: rows 1-5, columns A and B.
func structBase(t testing.TB) []byte {
	t.Helper()
	return buildXLSX(t, sheetSpec{name: "S", rows: [][]string{
		{"s:a", "s:b"}, {"s:r2", "n:2"}, {"s:r3", "n:3"}, {"s:r4", "n:4"}, {"s:r5", "n:5"},
	}})
}

func mutateSheet(t testing.TB, data []byte, fn func(string) string) []byte {
	t.Helper()
	return rewriteXLSX(t, data, func(n, b string) string {
		if strings.HasPrefix(n, "xl/worksheets/") {
			return fn(b)
		}
		return b
	}, nil)
}

func TestXLSXStructuralInconsistenciesAreErrors(t *testing.T) {
	rep := func(old, new string) func(string) string {
		return func(s string) string {
			if !strings.Contains(s, old) {
				panic("mutation does not apply: " + old)
			}
			return strings.Replace(s, old, new, 1)
		}
	}
	tests := []struct {
		name string
		fn   func(string) string
		want string // substring of the error
	}{
		{"duplicate row number", rep(`<row r="3">`, `<row r="2">`), "not greater than the previous row"},
		{"duplicate of the last row", rep(`<row r="5">`, `<row r="4">`), "not greater than the previous row"},
		{"last row jumps back to 1", rep(`<row r="5">`, `<row r="1">`), "not greater than the previous row"},
		{"out of order", func(s string) string {
			s = strings.Replace(s, `<row r="2">`, `<row r="X">`, 1)
			s = strings.Replace(s, `<row r="3">`, `<row r="2">`, 1)
			return strings.Replace(s, `<row r="X">`, `<row r="3">`, 1)
		}, "inconsistent"},
		{"row jumps forward then back", rep(`<row r="3">`, `<row r="9">`), "inconsistent"},
		{"negative row number", rep(`<row r="3">`, `<row r="-3">`), "not an integer"},
		{"zero row number", rep(`<row r="3">`, `<row r="0">`), "not an integer"},
		{"non-numeric row number", rep(`<row r="3">`, `<row r="x">`), "not an integer"},
		{"exponent row number", rep(`<row r="3">`, `<row r="1e1">`), "not an integer"},
		{"spaced row number", rep(`<row r="3">`, `<row r=" 3">`), "not an integer"},
		{"empty row number", rep(`<row r="3">`, `<row r="">`), "not an integer"},
		{"row beyond the sheet", rep(`<row r="5">`, `<row r="1048577">`), "not an integer"},
		{"cell moved to another row", rep(`r="A3"`, `r="A4"`), "does not match"},
		{"cell moved to a later column", rep(`r="A3"`, `r="C3"`), "not to the right"},
		{"cell moved before the previous", func(s string) string {
			s = strings.Replace(s, `r="A3"`, `r="X"`, 1)
			s = strings.Replace(s, `r="B3"`, `r="A3"`, 1)
			return strings.Replace(s, `r="X"`, `r="B3"`, 1)
		}, "not to the right"},
		{"duplicate cell", rep(`r="B3"`, `r="A3"`), "not to the right"},
		{"lowercase cell ref", rep(`r="A3"`, `r="a3"`), "not like A1"},
		{"row-only cell ref", rep(`r="A3"`, `r="3"`), "not like A1"},
		{"column-only cell ref", rep(`r="A3"`, `r="A"`), "not like A1"},
		{"cell ref with junk", rep(`r="A3"`, `r="A3B"`), "not like A1"},
		{"cell ref past XFD", rep(`r="B3"`, `r="XFE3"`), "outside the worksheet"},
		{"style with a space", rep(`<c r="B3">`, `<c r="B3" s="1 2">`), "not a non-negative integer"},
		{"negative style", rep(`<c r="B3">`, `<c r="B3" s="-1">`), "not a non-negative integer"},
		{"empty style", rep(`<c r="B3">`, `<c r="B3" s="">`), "not a non-negative integer"},
		{"fractional style", rep(`<c r="B3">`, `<c r="B3" s="1.5">`), "not a non-negative integer"},
		{"unknown cell type", rep(`<c r="B3">`, `<c r="B3" t="zz">`), "unsupported cell type"},
		{"upper-case cell type", rep(`<c r="B3">`, `<c r="B3" t="N">`), "unsupported cell type"},
		{"repeated r attribute", rep(`<c r="B3">`, `<c r="B3" r="B9">`), "repeated"},
		{"repeated t attribute", rep(`<c r="B3">`, `<c r="B3" t="n" t="zz">`), "repeated"},
		{"repeated row r attribute", rep(`<row r="3">`, `<row r="3" r="9">`), "repeated"},
		{"cell inside a cell", rep(`<c r="B3"><v>3</v>`, `<c r="B3"><c r="C3"/><v>3</v>`), "inside a cell"},
		{"cell outside a row", rep(`<sheetData>`, `<sheetData><c r="A1"/>`), "outside a row"},
		{"nested row", rep(`<row r="3">`, `<row r="3"><row r="3">`), "inside a row"},
	}
	base := structBase(t)
	cfg := "xlsx: {sheet: S}\nnatural_key: [a]\n"
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data := mutateSheet(t, base, tc.fn)
			st, err := NewXLSXReader(context.Background(), data, xcfg(t, cfg), XLSXLimits{})
			if err == nil {
				g := drainX(t, st)
				t.Fatalf("want an error; got %d records, %d row errors, stream err %v", len(g.recs), len(g.errs), g.err)
			}
			if !errors.Is(err, ErrXLSXBadArchive) || !strings.Contains(err.Error(), "xl/worksheets/sheet1.xml") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("unclear error (want part name and %q): %v", tc.want, err)
			}
		})
	}
	t.Run("shared string index without a table", func(t *testing.T) {
		data := mutateSheet(t, base, rep(`<c r="B3">`, `<c r="B3" t="s">`))
		_, err := NewXLSXReader(context.Background(), data, xcfg(t, cfg), XLSXLimits{})
		if !errors.Is(err, ErrXLSXBadArchive) || !strings.Contains(err.Error(), "shared string") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("shared string index out of range", func(t *testing.T) {
		sst := `<?xml version="1.0" encoding="UTF-8"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>x</t></si></sst>`
		data := rewriteXLSX(t, base, func(n, b string) string {
			if strings.HasPrefix(n, "xl/worksheets/") {
				b = strings.Replace(b, `<c r="B3"><v>3</v>`, `<c r="B3" t="s"><v>7</v>`, 1)
			}
			return b
		}, map[string]string{"xl/sharedStrings.xml": sst})
		_, err := NewXLSXReader(context.Background(), data, xcfg(t, cfg), XLSXLimits{})
		if !errors.Is(err, ErrXLSXBadArchive) || !strings.Contains(err.Error(), "shared string 7") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("shared string index not a number", func(t *testing.T) {
		data := mutateSheet(t, base, rep(`<c r="B3"><v>3</v>`, `<c r="B3" t="s"><v>x</v>`))
		_, err := NewXLSXReader(context.Background(), data, xcfg(t, cfg), XLSXLimits{})
		if !errors.Is(err, ErrXLSXBadArchive) || !strings.Contains(err.Error(), "shared string index") {
			t.Errorf("err = %v", err)
		}
	})
}

// Sparse rows and columns (strictly increasing) and files without r attributes are legal and read as before.
func TestXLSXLegalSparseAndReferenceFreeSheets(t *testing.T) {
	// Rows 1, 3, 7 and columns A, C, F only.
	sparse := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{
		{"s:a", "", "s:c", "", "", "s:f"}, {}, {"s:x1", "", "s:x3", "", "", "s:x6"}, {}, {}, {}, {"s:y1", "", "", "", "", "s:y6"},
	}})
	cfg := "xlsx: {sheet: S, header: false, start_row: 3, columns: {A: a, C: c, F: f}}\nnatural_key: [a]\n"
	g := readXLSX(t, sparse, cfg)
	if g.err != nil || len(g.errs) != 0 || len(g.recs) != 2 {
		t.Fatalf("%v %v %d", g.err, g.errs, len(g.recs))
	}
	want := func(r *Record, a, c, f string, row int64) {
		t.Helper()
		if r.Fields["a"] != a || r.Fields["c"] != c || r.Fields["f"] != f || r.Position != row {
			t.Errorf("row %d: %v", row, r.Fields)
		}
	}
	want(g.recs[0], "x1", "x3", "x6", 3)
	want(g.recs[1], "y1", "", "y6", 7)

	// The same workbook without any r attribute on rows (dense, so numbering is implicit) and cells.
	dense := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{{"s:a", "s:b"}, {"s:r2", "n:2"}, {"s:r3", "n:3"}}})
	strip := regexp.MustCompile(`(<(?:row|c)) r="[A-Z0-9]+"`)
	free := mutateSheet(t, dense, func(s string) string { return strip.ReplaceAllString(s, "$1") })
	if strings.Contains(string(mutateSheet(t, free, func(s string) string { return s })), ` r="`) {
		// inline-string rich text uses <r> elements only; attribute r must be gone
		t.Log("note: r attribute still present somewhere")
	}
	a := readXLSX(t, dense, "xlsx: {sheet: S}\nnatural_key: [a]\n")
	b := readXLSX(t, free, "xlsx: {sheet: S}\nnatural_key: [a]\n")
	if a.err != nil || b.err != nil || len(a.recs) != 2 || len(b.recs) != 2 {
		t.Fatalf("%v %v %d %d", a.err, b.err, len(a.recs), len(b.recs))
	}
	for i := range a.recs {
		if !reflect.DeepEqual(a.recs[i], b.recs[i]) {
			t.Errorf("record %d differs without r attributes: %v vs %v", i, a.recs[i], b.recs[i])
		}
	}
	// Mixed: rows without r follow the previous number; a later explicit number must still increase.
	mixed := mutateSheet(t, dense, func(s string) string { return strings.Replace(s, `<row r="2">`, `<row>`, 1) })
	if g := readXLSX(t, mixed, "xlsx: {sheet: S}\nnatural_key: [a]\n"); g.err != nil || len(g.recs) != 2 {
		t.Errorf("mixed: %v %d", g.err, len(g.recs))
	}
	bad := mutateSheet(t, dense, func(s string) string { return strings.Replace(s, `<row r="2">`, `<row>`, 1) })
	bad = mutateSheet(t, bad, func(s string) string { return strings.Replace(s, `<row r="3">`, `<row r="2">`, 1) })
	if _, err := NewXLSXReader(context.Background(), bad, xcfg(t, "xlsx: {sheet: S}\nnatural_key: [a]\n"), XLSXLimits{}); err == nil {
		t.Error("an explicit number equal to the implicit previous one must be an error")
	}
}

var (
	rowAttrRe  = regexp.MustCompile(`<row r="(\d+)"`)
	cellAttrRe = regexp.MustCompile(`<c r="([A-Z]+)(\d+)"`)
	cellTagRe  = regexp.MustCompile(`<c ([^>]*)>`)
)

var tagRe = regexp.MustCompile(`<(row|c)\b([^>]*)>`)
var attrRe = regexp.MustCompile(`([A-Za-z:]+)="([^"]*)"`)

// structurallyValid is an independent (regexp based) statement of the rules: strictly increasing row numbers,
// cells strictly increasing within their row and on the row they claim, sane s and t attributes.
func structurallyValid(body string) bool {
	prevRow, prevCol, row := int64(0), 0, int64(0)
	for _, m := range tagRe.FindAllStringSubmatch(body, -1) {
		attrs := map[string]string{}
		for _, a := range attrRe.FindAllStringSubmatch(m[2], -1) {
			if _, dup := attrs[a[1]]; dup {
				return false
			}
			attrs[a[1]] = a[2]
		}
		if m[1] == "row" {
			r, has := attrs["r"]
			n := prevRow + 1
			if has {
				v, err := strconv.ParseInt(r, 10, 64)
				if err != nil || v < 1 || v > 1048576 || r != strconv.FormatInt(v, 10) {
					return false
				}
				n = v
			}
			if n <= prevRow || n > 1048576 {
				return false
			}
			prevRow, row, prevCol = n, n, 0
			continue
		}
		col := prevCol + 1
		if ref, has := attrs["r"]; has {
			mm := regexp.MustCompile(`^([A-Z]{1,3})([0-9]+)$`).FindStringSubmatch(ref)
			if mm == nil {
				return false
			}
			c := 0
			for _, ch := range mm[1] {
				c = c*26 + int(ch-'A') + 1
			}
			rn, _ := strconv.ParseInt(mm[2], 10, 64)
			if c > 16384 || rn != row {
				return false
			}
			col = c
		}
		if col <= prevCol {
			return false
		}
		prevCol = col
		if v, has := attrs["s"]; has {
			if n, err := strconv.ParseInt(v, 10, 64); err != nil || n < 0 || v != strconv.FormatInt(n, 10) {
				return false
			}
		}
		switch attrs["t"] {
		case "", "b", "d", "e", "inlineStr", "n", "s", "str":
		default:
			return false
		}
	}
	return true
}

// Seeded property: whatever structural mutation is applied to a worksheet, the outcome is an error or exactly
// the original records, never a shorter or different successful stream.
func TestXLSXStructuralMutationProperty(t *testing.T) {
	sig := func(g xres) string {
		var b strings.Builder
		for _, r := range g.recs {
			fmt.Fprintf(&b, "%d %s %v|", r.Position, r.NaturalKey, r.Fields)
		}
		return b.String()
	}
	run := func(t *testing.T, base []byte, sheet, yaml string, n int, seed int64) {
		cfg := xcfg(t, yaml)
		st0, err := NewXLSXReader(context.Background(), base, cfg, XLSXLimits{})
		if err != nil {
			t.Fatal(err)
		}
		orig := drainX(t, st0)
		if orig.err != nil || len(orig.errs) != 0 || len(orig.recs) < 3 {
			t.Fatalf("original: %v %d %d", orig.err, len(orig.errs), len(orig.recs))
		}
		want := sig(orig)
		info, err := checkZip(base, XLSXLimits{}.withDefaults())
		if err != nil {
			t.Fatal(err)
		}
		part := ""
		for _, sh := range info.sheets {
			if sh.name == sheet {
				part = info.rels[sh.rid]
			}
		}
		if part == "" {
			t.Fatal("sheet part not found")
		}
		rng := rand.New(rand.NewSource(seed))
		garbage := []string{"1 2", "-1", "", "x", "1.5", "99999999999999999999", " 1", "0x1"}
		badTypes := []string{"zz", "N", "S", "string", "inline", "1"}
		letters := "ABCDEFGHIJ"
		applied, errored, same, valid := 0, 0, 0, 0
		mutateOne := func(kind int, body string) string {
			rows := rowAttrRe.FindAllStringSubmatchIndex(body, -1)
			cells := cellAttrRe.FindAllStringSubmatchIndex(body, -1)
			tags := cellTagRe.FindAllStringSubmatchIndex(body, -1)
			splice := func(from, to int, s string) string { return body[:from] + s + body[to:] }
			switch kind {
			case 0, 1: // row number: shifted, or copied from another row
				m := rows[rng.Intn(len(rows))]
				cur, _ := strconv.Atoi(body[m[2]:m[3]])
				nv := cur + rng.Intn(9) - 4
				if kind == 1 {
					o := rows[rng.Intn(len(rows))]
					nv, _ = strconv.Atoi(body[o[2]:o[3]])
				}
				return splice(m[2], m[3], strconv.Itoa(nv))
			case 2: // row number replaced by junk
				m := rows[rng.Intn(len(rows))]
				return splice(m[2], m[3], []string{"0", "-1", "x", "", "9", "1048577", "1e1"}[rng.Intn(7)])
			case 3: // cell column letter
				m := cells[rng.Intn(len(cells))]
				return splice(m[2], m[3], string(letters[rng.Intn(len(letters))]))
			case 4: // cell row part
				m := cells[rng.Intn(len(cells))]
				cur, _ := strconv.Atoi(body[m[4]:m[5]])
				return splice(m[4], m[5], strconv.Itoa(cur+rng.Intn(7)-3))
			case 5: // style attribute
				m := tags[rng.Intn(len(tags))]
				return splice(m[2], m[3], body[m[2]:m[3]]+` s="`+garbage[rng.Intn(len(garbage))]+`"`)
			default: // unsupported type
				m := tags[rng.Intn(len(tags))]
				return splice(m[2], m[3], body[m[2]:m[3]]+` t="`+badTypes[rng.Intn(len(badTypes))]+`"`)
			}
		}
		for i := 0; i < n; i++ {
			kind := rng.Intn(7)
			var mutated string
			data := rewriteXLSX(t, base, func(name, body string) string {
				if strings.ToLower(name) != part {
					return body
				}
				mutated = mutateOne(kind, body)
				return mutated
			}, nil)
			applied++
			st, err := NewXLSXReader(context.Background(), data, cfg, XLSXLimits{})
			if err != nil {
				errored++
				continue
			}
			g := drainX(t, st)
			switch {
			case g.err != nil || len(g.errs) > 0:
				errored++
			case sig(g) == want:
				same++ // a harmless mutation (e.g. the same value, or a column that is not read)
			case structurallyValid(mutated):
				valid++ // a different but well-formed sheet (e.g. a cell in another column): not corruption
			default:
				t.Fatalf("mutation %d (kind %d, seed %d): silent change: %d records instead of %d, no error", i, kind, seed, len(g.recs), len(orig.recs))
			}
		}
		if errored < applied/2 {
			t.Errorf("only %d of %d mutations were detected (%d identical, %d valid): the mutator is too weak", errored, applied, same, valid)
		}
	}
	t.Run("small workbook", func(t *testing.T) {
		base := buildXLSX(t, sheetSpec{name: "S", rows: [][]string{
			{"s:a", "s:b", "s:c"}, {"s:r2", "n:2", "s:x"}, {"s:r3", "n:3", "s:y"}, {"s:r4", "n:4", "s:z"}, {"s:r5", "n:5", "s:w"}, {"s:r6", "n:6", "s:v"},
		}})
		run(t, base, "S", "xlsx: {sheet: S}\nnatural_key: [a]\n", 300, 20261121)
	})
	t.Run("BoE fixture", func(t *testing.T) {
		if testing.Short() {
			t.Skip("short")
		}
		run(t, fixtureXLSX(t, "boe/baserate-trimmed.xlsx"), "HISTORICAL SINCE 1694", boeSheetYAML, 20, 1694)
	})
}
