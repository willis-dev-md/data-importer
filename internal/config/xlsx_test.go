package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadBoEExample(t *testing.T) {
	s, err := LoadSource(filepath.Join("..", "..", "docs", "examples", "boe-baserate.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	x := s.XLSX
	if s.Kind != KindXLSX || x == nil || x.Sheet != "HISTORICAL SINCE 1694" || x.StartRow != 8 || x.HasHeader() || x.FirstRow() != 8 ||
		x.Columns["A"] != "year" || len(s.Table) != 4 || s.Table[0].Section == nil || s.Table[0].Section.Default == nil || *s.Table[0].Section.Default != "Bank Rate" {
		t.Errorf("unexpected: %+v %+v", s, x)
	}
	if got := x.ColumnList(); len(got) != 4 || got[0].Letter != "A" || got[3].Name != "rate" {
		t.Errorf("columns: %+v", got)
	}
}

const xl = "id: a\nkind: xlsx\nlocation: /x.xlsx\nnatural_key: [k]\n"

func TestParseXLSXSource(t *testing.T) {
	cols := "xlsx: {sheet: S, header: false, columns: {A: k, B: v}}\n"
	tests := []struct {
		name, yaml, wantErr string // wantErr "" = valid
	}{
		{"minimal with header", xl + "xlsx: {sheet: S}\n", ""},
		{"sheet_index", xl + "xlsx: {sheet_index: 0}\n", ""},
		{"sheets list", xl + "xlsx: {sheets: [A, B]}\n", ""},
		{"sheets except", xl + "xlsx: {sheets: {except: [Notes]}}\n", ""},
		{"all options", xl + "xlsx: {sheet: S, header: true, header_row: 2, start_row: 4, end_row: 9, use_cached_values: true}\n", ""},
		{"columns by letter", xl + cols, ""},
		{"lowercase and two-letter columns", xl + "xlsx: {sheet: S, header: false, columns: {a: k, AA: v, XFD: w}}\n", ""},
		{"no xlsx block", xl, "xlsx: required for kind xlsx"},
		{"no sheet", xl + "xlsx: {header: true}\n", "xlsx: set exactly one of sheet, sheet_index and sheets"},
		{"two sheet selectors", xl + "xlsx: {sheet: S, sheet_index: 1}\n", "set exactly one of sheet, sheet_index and sheets (got 2)"},
		{"unknown xlsx key", xl + "xlsx: {sheet: S, sheet_name: x}\n", "field sheet_name not found"},
		{"sheets bad shape", xl + "xlsx: {sheets: {only: [A]}}\n", "sheets must be a list of names or {except"},
		{"sheets scalar", xl + "xlsx: {sheets: A}\n", "sheets must be a list of names"},
		{"empty sheets", xl + "xlsx: {sheets: []}\n", "xlsx.sheets: at least one sheet name"},
		{"negative index", xl + "xlsx: {sheet_index: -1}\n", "xlsx.sheet_index: must not be negative"},
		{"header false needs columns", xl + "xlsx: {sheet: S, header: false}\n", "xlsx.columns: required when header is false"},
		{"bad column letter", xl + "xlsx: {sheet: S, header: false, columns: {A1: k}}\n", `xlsx.columns.A1: "A1" is not a column letter`},
		{"column beyond XFD", xl + "xlsx: {sheet: S, header: false, columns: {XFE: k}}\n", "not a column letter"},
		{"column listed twice", xl + "xlsx: {sheet: S, header: false, columns: {A: k, a: v}}\n", "listed twice"},
		{"duplicate field name", xl + "xlsx: {sheet: S, header: false, columns: {A: k, B: k}}\n", "duplicate field name"},
		{"underscore field name", xl + "xlsx: {sheet: S, header: false, columns: {A: _k}}\n", "must not start with an underscore"},
		{"empty field name", xl + "xlsx: {sheet: S, header: false, columns: {A: ''}}\n", "field name must not be empty"},
		{"use_cached_values false", xl + "xlsx: {sheet: S, use_cached_values: false}\n", "formulas are never evaluated"},
		{"start before header", xl + "xlsx: {sheet: S, header_row: 3, start_row: 3}\n", "must be after the header row 3"},
		{"end before start", xl + "xlsx: {sheet: S, start_row: 5, end_row: 4}\n", "xlsx.end_row"},
		{"row too large", xl + "xlsx: {sheet: S, start_row: 1048577}\n", "xlsx.start_row"},
		{"header_row without header", xl + "xlsx: {sheet: S, header: false, header_row: 2, columns: {A: k}}\n", "xlsx.header_row: only valid with header: true"},
		{"csv block on xlsx", xl + "xlsx: {sheet: S}\ncsv: {delimiter: ';'}\n", "csv: only valid for kind csv"},
		{"xlsx block on csv", "id: a\nkind: csv\nlocation: /x.csv\nnatural_key: [k]\nxlsx: {sheet: S}\n", "xlsx: only valid for kind xlsx"},
		{"unsupported kind lists xlsx", "id: a\nkind: json\nlocation: /x\nnatural_key: [k]\n", "supported: csv, xlsx"},
		// context
		{"context scalars", xl + "xlsx: {sheet: S}\ncontext: {year: 2026, status: provisional, ok: true}\n", ""},
		{"context sheet reserved", xl + "xlsx: {sheet: S}\ncontext: {sheet: x}\n", "context.sheet"},
		{"context underscore", xl + "xlsx: {sheet: S}\ncontext: {_x: y}\n", "must not start with an underscore"},
		{"context nested", xl + "xlsx: {sheet: S}\ncontext: {a: {b: c}}\n", "cannot unmarshal"},
		// natural key
		{"ctx key from context", "id: a\nkind: xlsx\nlocation: /x\nnatural_key: ['@ctx.year', k]\nxlsx: {sheet: S, header: false, columns: {A: k}}\ncontext: {year: 1}\n", ""},
		{"ctx key sheet", "id: a\nkind: xlsx\nlocation: /x\nnatural_key: ['@ctx.sheet', k]\nxlsx: {sheet: S, header: false, columns: {A: k}}\n", ""},
		{"ctx key unknown", "id: a\nkind: xlsx\nlocation: /x\nnatural_key: ['@ctx.nope']\nxlsx: {sheet: S, header: false, columns: {A: k}}\n", `natural_key[0]: "@ctx.nope" is not a context variable (known: sheet)`},
		{"ctx key from section", "id: a\nkind: xlsx\nlocation: /x\nnatural_key: ['@ctx.grp']\nxlsx: {sheet: S, header: false, columns: {A: k}}\ntable: [{section: {column: k, match: x, set: grp}}]\n", ""},
		{"section var as plain key", "id: a\nkind: xlsx\nlocation: /x\nnatural_key: [grp, k]\nxlsx: {sheet: S, header: false, columns: {A: k}}\ntable: [{section: {column: k, match: x, set: grp}}]\n", ""},
		{"meta key", "id: a\nkind: xlsx\nlocation: /x\nnatural_key: ['@meta.x']\nxlsx: {sheet: S, header: false, columns: {A: k}}\n", "not supported for kind xlsx"},
		{"key not a field", "id: a\nkind: xlsx\nlocation: /x\nnatural_key: [zzz]\nxlsx: {sheet: S, header: false, columns: {A: k}}\n", `"zzz" is neither a field`},
		{"key with header is checked at open", xl + "xlsx: {sheet: S}\n", ""},
		{"key must survive unpivot", "id: a\nkind: xlsx\nlocation: /x\nnatural_key: [v]\nxlsx: {sheet: S, header: false, columns: {A: k, B: v}}\ntable: [{unpivot: {id: [k], columns: [v], name: n, value: val}}]\n", `"v" is neither a field`},
		{"key from split group", "id: a\nkind: xlsx\nlocation: /x\nnatural_key: [age]\nxlsx: {sheet: S, header: false, columns: {A: k}}\ntable: [{split: {column: k, regex: '(?P<age>\\d+)'}}]\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSource([]byte(tc.yaml))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q lacks %q", err, tc.wantErr)
			}
		})
	}
}

func TestParseTable(t *testing.T) {
	base := xl + "xlsx: {sheet: S, header: false, columns: {A: k, B: v, C: w}}\ntable:\n"
	tests := []struct {
		name, steps, wantErr string
	}{
		{"every step", `
  - skip_rows: {where: k, match: '^x'}
  - drop_if_empty: [v]
  - section: {column: k, match: '^[A-Z]', set: grp, default: d}
  - fill_down: {columns: [k], reset_on: section}
  - split: {column: k, regex: '(?P<a>\d)(?P<b>\d)', optional: true}
  - strip: {w: ["'"]}
  - normalize: {k: [trim, lower], v: to_int, w: to_number}
  - unpivot: {id: [k, a], columns: {v: V, w: W}, name: n, value: val}
`, ""},
		{"unknown step", "  - sort: {by: k}\n", "field sort not found"},
		{"empty step", "  - {}\n", "table[0]: a step must have exactly one of"},
		{"two keys in a step", "  - {drop_if_empty: [k], strip: {k: [a]}}\n", "table[0]: a step must have exactly one of"},
		{"unknown key in skip_rows", "  - skip_rows: {where: k, match: x, extra: 1}\n", "field extra not found"},
		{"skip_rows unknown column", "  - skip_rows: {where: nope, match: x}\n", `table[0].skip_rows.where: unknown column "nope" (columns here: k, v, w)`},
		{"skip_rows bad regex", "  - skip_rows: {where: k, match: '('}\n", "table[0].skip_rows.match: invalid regular expression"},
		{"skip_rows no match", "  - skip_rows: {where: k}\n", "table[0].skip_rows.match: required"},
		{"drop_if_empty empty", "  - drop_if_empty: []\n", "at least one column"},
		{"drop_if_empty unknown", "  - drop_if_empty: [v, nope]\n", "table[0].drop_if_empty[1]: unknown column"},
		{"section no set", "  - section: {column: k, match: x}\n", "table[0].section.set: required"},
		{"section set sheet", "  - section: {column: k, match: x, set: sheet}\n", "reserved for the sheet name"},
		{"section bad regex", "  - section: {column: k, match: '[', set: g}\n", "table[0].section.match"},
		{"section unknown column", "  - section: {column: nope, match: x, set: g}\n", "table[0].section.column: unknown column"},
		{"fill_down bad reset", "  - fill_down: {columns: [k], reset_on: row}\n", "table[0].fill_down.reset_on: must be section"},
		{"fill_down empty", "  - fill_down: {columns: []}\n", "table[0].fill_down.columns"},
		{"normalize unknown transform", "  - normalize: {k: [trim, shout]}\n", `table[0].normalize.k[1]: unknown transform "shout"`},
		{"normalize scalar", "  - normalize: {k: trim}\n", ""},
		{"normalize unknown column", "  - normalize: {nope: trim}\n", "table[0].normalize.nope: unknown column"},
		{"normalize empty list", "  - normalize: {k: []}\n", "at least one transform"},
		{"unpivot no columns", "  - unpivot: {id: [k], columns: [], name: n, value: v}\n", "table[0].unpivot.columns: at least one column"},
		{"unpivot name clash", "  - unpivot: {id: [k], columns: [v], name: k, value: x}\n", "name and value must not be id columns"},
		{"unpivot same name value", "  - unpivot: {id: [k], columns: [v], name: n, value: n}\n", "must differ from name"},
		{"unpivot column in id", "  - unpivot: {id: [k], columns: [k], name: n, value: x}\n", "also in id"},
		{"unpivot duplicate column", "  - unpivot: {id: [k], columns: [v, v], name: n, value: x}\n", "duplicate column"},
		{"unpivot columns scalar", "  - unpivot: {id: [k], columns: v, name: n, value: x}\n", "unpivot columns must be a list"},
		{"step after unpivot sees only its output", "  - unpivot: {id: [k], columns: [v], name: n, value: x}\n  - drop_if_empty: [w]\n", "table[1].drop_if_empty[0]: unknown column"},
		{"split no named group", "  - split: {column: k, regex: '(\\d)'}\n", "needs at least one named group"},
		{"split bad regex", "  - split: {column: k, regex: '(?P<a'}\n", "table[0].split.regex: invalid regular expression"},
		{"split group visible later", "  - split: {column: k, regex: '(?P<a>\\d)'}\n  - drop_if_empty: [a]\n", ""},
		{"strip empty char", "  - strip: {k: ['']}\n", "must not be empty"},
		{"strip unknown column", "  - strip: {nope: [a]}\n", "table[0].strip.nope: unknown column"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			y := base + tc.steps
			_, err := ParseSource([]byte(y))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q lacks %q", err, tc.wantErr)
			}
		})
	}
}

// The stored config (ConfigJSON: yaml -> generic -> json) must parse back to the same config.
func TestXLSXConfigRoundTrip(t *testing.T) {
	for _, name := range []string{"boe-baserate.source.yaml"} {
		s, err := LoadSource(filepath.Join("..", "..", "docs", "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		roundTrip(t, s)
	}
	y := xl + `xlsx: {sheets: {except: [Notes]}, header: true, header_row: 3, end_row: 99, use_cached_values: true}
context: {year: 2026, status: provisional}
table:
  - skip_rows: {where: k, match: x}
  - normalize: {k: [trim, lower], v: to_int}
  - unpivot: {id: [k], columns: {a: A, b: B}, name: n, value: val}
  - split: {column: k, regex: '(?P<a>.)', optional: true}
`
	s, err := ParseSource([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	roundTrip(t, s)
	// A column list (no labels) round-trips as a list.
	s, err = ParseSource([]byte(xl + "xlsx: {sheet: S}\ntable: [{unpivot: {id: [k], columns: [a, b], name: n, value: val}}]\n"))
	if err == nil {
		roundTrip(t, s)
	} else if !strings.Contains(err.Error(), "unknown column") {
		t.Fatal(err)
	}
}

func roundTrip(t *testing.T, s *Source) {
	t.Helper()
	b, err := yaml.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := yaml.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	j, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseSource(j)
	if err != nil {
		t.Fatalf("stored config does not parse back: %v\n%s", err, j)
	}
	if !reflect.DeepEqual(s, back) {
		t.Errorf("round trip changed the config:\n%+v\n%+v", s, back)
	}
}

func TestColumnIndex(t *testing.T) {
	for in, want := range map[string]int{"A": 1, "Z": 26, "AA": 27, "az": 52, "XFD": 16384, " b ": 2} {
		if got, ok := ColumnIndex(in); !ok || got != want {
			t.Errorf("%q = %d %v, want %d", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "A1", "XFE", "AAAA", "1", "Ä", "A B"} {
		if _, ok := ColumnIndex(in); ok {
			t.Errorf("%q should not be a column", in)
		}
	}
}
