package mapping

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadCanon(t *testing.T) *Canonical {
	t.Helper()
	c, err := LoadCanonical(filepath.Join("..", "..", "canonical-schema.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoadCanonical(t *testing.T) {
	c := loadCanon(t)
	if c.Version != 2 {
		t.Errorf("version = %d", c.Version)
	}
	for _, name := range []string{"series_id", "series_title", "measure", "statistic", "dimensions", "period_type",
		"period_start", "period_end", "value", "unit", "status", "quality_flag"} {
		if c.Fields[name] == nil {
			t.Errorf("missing canonical field %s", name)
		}
	}
	if !c.Fields["value"].Nullable || c.Fields["value"].Type != "decimal" {
		t.Errorf("value must be a nullable decimal: %+v", c.Fields["value"])
	}
	if got := strings.Join(c.Fields["period_type"].Values, ","); got != "year,tax_year,quarter,month,day" {
		t.Errorf("period_type values = %s", got)
	}
	for _, name := range []string{"series_id", "measure", "period_type", "period_start", "period_end"} {
		if !c.Fields[name].Required {
			t.Errorf("%s must be required", name)
		}
	}
	if _, err := ParseCanonical([]byte("fields: {a: {type: bogus}}")); err == nil {
		t.Error("unknown canonical type accepted")
	}
	if _, err := ParseCanonical([]byte("fields: {a: {type: enum}}")); err == nil {
		t.Error("enum without values accepted")
	}
	if _, err := LoadCanonical("nonexistent.yaml"); err == nil {
		t.Error("missing file accepted")
	}
}

func TestParse(t *testing.T) {
	good := `
source: s
version: 2
fields:
  a: { from: [x, y], transform: trim, format: ["a", "b"] }
  b: { from: "@meta.X", transform: [trim, lower], format: f }
dimensions:
  d: { from: "$.a.b" }
extra: keep
`
	m, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 2 || len(m.Fields["a"].From) != 2 || len(m.Fields["a"].Transform) != 1 ||
		len(m.Fields["b"].Transform) != 2 || len(m.Fields["a"].Format) != 2 || m.Dimensions["d"].From[0] != "$.a.b" {
		t.Errorf("unexpected parse: %+v", m)
	}
	bad := map[string]string{
		"unknown top key":   "source: s\nbogus: 1\n",
		"unknown field key": "source: s\nfields:\n  a: {from: x, bogus: 1}\n",
		"empty":             "",
		"from is a map":     "source: s\nfields:\n  a: {from: {x: y}}\n",
		"two documents":     "source: s\n---\nsource: t\n",
		"not yaml":          "source: [",
	}
	for name, doc := range bad {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// base is a valid mapping; tests mutate a copy of its YAML via replacement.
const base = `
source: s
version: 1
fields:
  series_id:    { const: x }
  measure:      { const: m }
  period_type:  { from: p, type: period, output: period_type }
  period_start: { from: p, type: period, output: period_start }
  period_end:   { from: p, type: period, output: period_end }
  value:        { from: v, type: decimal }
`

func validate(t *testing.T, doc string) []Issue {
	t.Helper()
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return Validate(m, loadCanon(t))
}

func hasIssue(issues []Issue, path, code string, sev Severity) bool {
	for _, i := range issues {
		if i.Path == path && i.Code == code && i.Severity == sev {
			return true
		}
	}
	return false
}

func TestValidateBase(t *testing.T) {
	if issues := validate(t, base); len(issues) != 0 {
		t.Fatalf("base mapping has issues: %v", issues)
	}
}

func TestValidateRules(t *testing.T) {
	tests := []struct {
		name    string
		extra   string // appended to base (YAML, later keys under fields/dimensions need care, so full blocks)
		path    string
		code    string
		sev     Severity
		replace [2]string // optional: replace in base
	}{
		{name: "unknown canonical field", replace: [2]string{"  value:", "  nope: { const: 1 }\n  value:"}, path: "fields.nope", code: CodeUnknownField, sev: SeverityError},
		{name: "incompatible type", replace: [2]string{"from: v, type: decimal", "from: v, type: bool"}, path: "fields.value.type", code: CodeTypeMismatch, sev: SeverityError},
		{name: "unknown type", replace: [2]string{"from: v, type: decimal", "from: v, type: number"}, path: "fields.value.type", code: CodeUnknownType, sev: SeverityError},
		{name: "required not covered", replace: [2]string{"  measure:      { const: m }\n", ""}, path: "fields.measure", code: CodeRequiredNotCovered, sev: SeverityError},
		{name: "from and const", replace: [2]string{"{ const: x }", "{ const: x, from: y }"}, path: "fields.series_id", code: CodeSourceConflict, sev: SeverityError},
		{name: "neither from nor const", replace: [2]string{"{ const: x }", "{ type: string }"}, path: "fields.series_id", code: CodeSourceMissing, sev: SeverityError},
		{name: "from const template", replace: [2]string{"{ const: x }", `{ const: x, from: y, template: "{a}" }`}, path: "fields.series_id", code: CodeSourceConflict, sev: SeverityError},
		{name: "unknown transform", replace: [2]string{"{ const: x }", "{ from: y, transform: reverse }"}, path: "fields.series_id.transform[0]", code: CodeUnknownTransform, sev: SeverityError},
		{name: "transform arity", replace: [2]string{"{ const: x }", "{ from: y, transform: concat }"}, path: "fields.series_id.transform[0]", code: CodeTransformArgs, sev: SeverityError},
		{name: "unknown validator", replace: [2]string{"{ const: x }", "{ from: y, validate: iban }"}, path: "fields.series_id.validate", code: CodeUnknownValidator, sev: SeverityError},
		{name: "bad validator args", replace: [2]string{"{ const: x }", "{ from: y, validate: 'range(5,1)' }"}, path: "fields.series_id.validate", code: CodeValidatorArgs, sev: SeverityError},
		{name: "format missing for date", replace: [2]string{"  value:", "  unit: { from: u, type: date }\n  value:"}, path: "fields.unit.format", code: CodeFormatRequired, sev: SeverityError},
		{name: "bad regex in validate", replace: [2]string{"{ const: x }", "{ from: y, validate: 'regex([a)' }"}, path: "fields.series_id.validate", code: CodeBadRegex, sev: SeverityError},
		{name: "bad regex in transform", replace: [2]string{"{ const: x }", `{ from: y, transform: 'regex("(", x)' }`}, path: "fields.series_id.transform[0]", code: CodeBadRegex, sev: SeverityError},
		{name: "output on non-period", replace: [2]string{"{ const: x }", "{ from: y, output: period_start }"}, path: "fields.series_id.output", code: CodeOutputNotPeriod, sev: SeverityError},
		{name: "bad output", replace: [2]string{"output: period_type", "output: period_middle"}, path: "fields.period_type.output", code: CodeBadOutput, sev: SeverityError},
		{name: "output mismatch", replace: [2]string{"output: period_type", "output: period_end"}, path: "fields.period_type.output", code: CodeOutputMismatch, sev: SeverityError},
		{name: "bad template", replace: [2]string{"{ const: x }", `{ template: "a:{@foo.x}" }`}, path: "fields.series_id.template", code: CodeBadTemplate, sev: SeverityError},
		{name: "unbalanced template", replace: [2]string{"{ const: x }", `{ template: "a:{b" }`}, path: "fields.series_id.template", code: CodeBadTemplate, sev: SeverityError},
		{name: "flag to unknown field", replace: [2]string{"from: v, type: decimal", `from: v, type: decimal, null_markers: [x], flag: nope`}, path: "fields.value.flag", code: CodeUnknownFlagField, sev: SeverityError},
		{name: "null_markers without flag", replace: [2]string{"from: v, type: decimal", `from: v, type: decimal, null_markers: [x]`}, path: "fields.value.null_markers", code: CodeNullMarkersNoFlag, sev: SeverityWarning},
		{name: "bad precision", replace: [2]string{"output: period_type", "output: period_type, precision: week"}, path: "fields.period_type.precision", code: CodeBadPrecision, sev: SeverityError},
		{name: "dimension non-string type", extra: "dimensions:\n  d: { from: x, type: int }\n", path: "dimensions.d.type", code: CodeDimensionType, sev: SeverityError},
		{name: "dimension output", extra: "dimensions:\n  d: { from: x, output: period_start }\n", path: "dimensions.d.output", code: CodeOutputNotPeriod, sev: SeverityError},
		{name: "bad JSONPath", replace: [2]string{"{ const: x }", `{ from: "$.a..b" }`}, path: "fields.series_id.from[0]", code: CodeBadFrom, sev: SeverityError},
		{name: "bad @ctx", replace: [2]string{"{ const: x }", `{ from: "@ctx." }`}, path: "fields.series_id.from[0]", code: CodeBadFrom, sev: SeverityError},
		{name: "const not in enum", replace: [2]string{"  period_type:  { from: p, type: period, output: period_type }", "  period_type: { const: decade }"}, path: "fields.period_type.const", code: CodeConstNotInEnum, sev: SeverityError},
		{name: "dimensions in fields", replace: [2]string{"  value:", "  dimensions: { const: x }\n  value:"}, path: "fields.dimensions", code: CodeDimensionsField, sev: SeverityError},
		{name: "bad extra", extra: "extra: maybe\n", path: "extra", code: CodeBadExtra, sev: SeverityError},
		{name: "period on non-period field", replace: [2]string{"{ const: x }", "{ from: y, type: period }"}, path: "fields.series_id.type", code: CodeTypeMismatch, sev: SeverityError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := base
			if tt.replace[0] != "" {
				if !strings.Contains(doc, tt.replace[0]) {
					t.Fatalf("replace target %q not in base", tt.replace[0])
				}
				doc = strings.Replace(doc, tt.replace[0], tt.replace[1], 1)
			}
			doc += tt.extra
			issues := validate(t, doc)
			if !hasIssue(issues, tt.path, tt.code, tt.sev) {
				t.Errorf("want %s %s at %s, got %v", tt.sev, tt.code, tt.path, issues)
			}
			if tt.sev == SeverityWarning && HasErrors(issues) {
				t.Errorf("warning case must have no errors: %v", issues)
			}
		})
	}
}

func TestValidateValidVariants(t *testing.T) {
	tests := []struct{ name, replaceFrom, replaceTo string }{
		{"int feeds decimal", "from: v, type: decimal", "from: v, type: int"},
		{"typed date with format", "  value:", "  value: { from: v, type: decimal }\n  status: { from: d, type: string }\n  #"},
		{"template with ctx and meta", "{ const: x }", `{ template: "a:{@ctx.sheet}:{@meta.CDID}:{Area Code}", empty_ok: true }`},
		{"transform list with quoted args", "{ const: x }", `{ from: [a, b], transform: ['concat(" ")', trim, "regex('^a,', b)", 'split(",")', coalesce, upper, lower] }`},
		{"validators", "{ const: x }", "{ from: y, validate: 'range(0,100)' }"},
		{"validator email", "{ const: x }", "{ from: y, validate: email }"},
		{"json path index", "{ const: x }", `{ from: "$.a[0].b[*]" }`},
		{"null markers with flag", "from: v, type: decimal", "from: v, type: decimal, null_markers: [x, '..'], flag: quality_flag"},
		{"precision month", "output: period_type", "output: period_type, precision: month"},
		{"const enum", "  period_type:  { from: p, type: period, output: period_type }", "  period_type: { const: day }"},
		{"numeric const for decimal", "  value:        { from: v, type: decimal }", "  value: { const: 1 }"},
		{"extra drop", "source: s", "source: s\nextra: drop"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := strings.Replace(base, tt.replaceFrom, tt.replaceTo, 1)
			if issues := validate(t, doc); HasErrors(issues) {
				t.Errorf("unexpected errors: %v", issues)
			}
		})
	}
	// date field with format is fine
	doc := strings.Replace(base, "  value:", "  status: { from: d, type: date, format: '2006' }\n  value:", 1)
	for _, i := range validate(t, doc) {
		if i.Code == CodeFormatRequired {
			t.Errorf("format given but reported: %v", i)
		}
	}
}

func TestValidateMissingSource(t *testing.T) {
	m, err := Parse([]byte(strings.Replace(base, "source: s\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if !hasIssue(Validate(m, loadCanon(t)), "source", CodeMissingSource, SeverityError) {
		t.Error("missing source not reported")
	}
}

func TestIssueString(t *testing.T) {
	i := Issue{Path: "fields.a", Code: "X", Message: "m", Severity: SeverityError}
	if i.String() != "fields.a: X: m" {
		t.Error(i.String())
	}
}

func TestParseCall(t *testing.T) {
	tests := []struct {
		in   string
		name string
		args []string
	}{
		{"trim", "trim", nil},
		{`concat(" ")`, "concat", []string{" "}},
		{`regex("a,b", x)`, "regex", []string{"a,b", "x"}},
		{`regex('\d+', "")`, "regex", []string{`\d+`, ""}},
		{"range(1, 2)", "range", []string{"1", "2"}},
	}
	for _, tt := range tests {
		name, args, _, ok := parseCall(tt.in)
		if !ok || name != tt.name || strings.Join(args, "|") != strings.Join(tt.args, "|") || len(args) != len(tt.args) {
			t.Errorf("parseCall(%q) = %q %q %v", tt.in, name, args, ok)
		}
	}
}

func TestExamples(t *testing.T) {
	c := loadCanon(t)
	files, err := filepath.Glob(filepath.Join("..", "..", "docs", "examples", "*.mapping.yaml"))
	if err != nil || len(files) < 7 {
		t.Fatalf("examples: %v %v", files, err)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			m, err := Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			if issues := Validate(m, c); HasErrors(issues) {
				t.Errorf("errors: %v", issues)
			}
		})
	}
}
