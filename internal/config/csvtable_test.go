package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const csvT = "id: a\nkind: csv\nlocation: /x.csv\n"

func TestParseCSVTable(t *testing.T) {
	unp := "table: [{unpivot: {id: [d], columns: {a: A, b: B}, name: m, value: v}}]\n"
	tests := []struct {
		name, yaml, wantErr string
	}{
		{"unpivot with header-derived columns", csvT + "natural_key: [d, m]\n" + unp, ""},
		{"unpivot with explicit columns", csvT + "csv: {columns: [d, a, b]}\nnatural_key: [d, m]\n" + unp, ""},
		{"key not an output", csvT + "csv: {columns: [d, a, b]}\nnatural_key: [d, a]\n" + unp, `natural_key[1]: "a" is neither a field after the table pipeline`},
		{"unknown column in step", csvT + "csv: {columns: [d, a]}\nnatural_key: [d]\ntable: [{drop_if_empty: [zz]}]\n", `table[0].drop_if_empty[0]: unknown column "zz"`},
		{"unknown step", csvT + "natural_key: [d]\ntable: [{frobnicate: [d]}]\n", "field frobnicate not found"},
		{"two keys in a step", csvT + "natural_key: [d]\ntable: [{drop_if_empty: [d], strip: {d: [x]}}]\n", "a step must have exactly one of"},
		{"bad transform path", csvT + "natural_key: [d]\ntable: [{normalize: {d: [nope]}}]\n", "table[0].normalize.d[0]"},
		{"context ok and ctx key", csvT + "natural_key: ['@ctx.year', d]\ncontext: {year: 2026}\ntable: [{drop_if_empty: [d]}]\n", ""},
		{"ctx key unknown", csvT + "natural_key: ['@ctx.nope']\ntable: [{drop_if_empty: [d]}]\n", `"@ctx.nope" is not a context variable`},
		{"section variable as key", csvT + "natural_key: [grp, d]\ntable: [{section: {column: d, match: '^G', set: grp}}]\n", ""},
		{"other @ key", csvT + "natural_key: ['@x.y']\ntable: [{drop_if_empty: [d]}]\n", "is not supported for kind csv with table"},
		{"context underscore", csvT + "natural_key: [d]\ncontext: {_x: y}\ntable: [{drop_if_empty: [d]}]\n", "must not start with an underscore"},
		{"table on json kind", "id: a\nkind: json\nlocation: /x\nnatural_key: [k]\ntable: [{drop_if_empty: [k]}]\n", "unsupported kind"},
		{"no table: old csv.columns key rule", csvT + "csv: {columns: [d]}\nnatural_key: [zz]\n", `"zz" is not in csv.columns`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSource([]byte(tc.yaml))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("error %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadHPIExamples(t *testing.T) {
	for _, n := range []string{"uk-hpi-average-prices", "uk-hpi-property-type"} {
		s, err := LoadSource(filepath.Join("..", "..", "docs", "examples", n+".source.yaml"))
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if s.Kind != KindCSV || len(s.Table) != 1 || s.Table[0].Unpivot == nil {
			t.Errorf("%s: unexpected %+v", n, s.Table)
		}
	}
}
