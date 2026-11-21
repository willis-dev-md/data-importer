package source

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/willis-dev-md/data-importer/internal/config"
)

// pipe compiles a pipeline from YAML steps.
func pipe(t *testing.T, steps string) *Pipeline {
	t.Helper()
	var tbl config.Table
	dec := yaml.NewDecoder(strings.NewReader(steps))
	dec.KnownFields(true)
	if err := dec.Decode(&tbl); err != nil {
		t.Fatalf("steps: %v", err)
	}
	p, err := NewPipeline(tbl)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func prow(kv ...any) pipeRow {
	f := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		f[kv[i].(string)] = kv[i+1]
	}
	return pipeRow{fields: f, ctx: map[string]string{"sheet": "S"}}
}

// runAll feeds the rows in order and returns the fields of every output row (nil entry = step error).
func runAll(t *testing.T, p *Pipeline, rows ...pipeRow) (out []map[string]any, errs []error) {
	t.Helper()
	for _, r := range rows {
		o, err := p.Run(r)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, x := range o {
			out = append(out, x.fields)
		}
	}
	return out, errs
}

func TestPipelineSkipRows(t *testing.T) {
	p := pipe(t, `[{skip_rows: {where: a, match: '^(KEY|Source)'}}]`)
	out, errs := runAll(t, p, prow("a", "KEY x"), prow("a", "data"), prow("a", "Source: y"), prow("a", int64(5)), prow("b", "no a column"))
	if len(errs) != 0 || len(out) != 3 {
		t.Fatalf("out=%v errs=%v", out, errs)
	}
}

func TestPipelineDropIfEmpty(t *testing.T) {
	p := pipe(t, `[{drop_if_empty: [a, b]}]`)
	out, _ := runAll(t, p,
		prow("a", "", "b", "  "),       // all empty: dropped
		prow("a", "x", "b", ""),        // one filled: kept
		prow("a", "", "b", int64(0)),   // zero is data
		prow("c", "only other column"), // missing columns are empty
	)
	if len(out) != 2 || out[0]["a"] != "x" || out[1]["b"] != int64(0) {
		t.Errorf("out = %v", out)
	}
}

func TestPipelineSection(t *testing.T) {
	steps := `[{section: {column: a, match: '^\s*[A-Za-z]', set: grp, default: Main}}]`
	p := pipe(t, steps)
	var ctxs []string
	var vals []any
	for _, r := range []pipeRow{prow("a", "1", "v", "x"), prow("a", "Heading One"), prow("a", "2", "v", "y"), prow("a", int64(3), "v", "z"), prow("a", " Heading Two "), prow("a", "4")} {
		o, err := p.Run(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range o {
			ctxs = append(ctxs, x.ctx["grp"])
			vals = append(vals, x.fields["a"])
		}
	}
	if !reflect.DeepEqual(ctxs, []string{"Main", "Heading One", "Heading One", "Heading Two"}) {
		t.Errorf("ctx = %v (headings are dropped, default applies before the first heading, value trimmed)", ctxs)
	}
	if len(vals) != 4 {
		t.Errorf("vals = %v", vals)
	}
	// Without default the variable is unset before the first heading; the static context is kept.
	p = pipe(t, `[{section: {column: a, match: '^H', set: grp}}]`)
	o, _ := p.Run(prow("a", "1"))
	if _, ok := o[0].ctx["grp"]; ok || o[0].ctx["sheet"] != "S" {
		t.Errorf("ctx = %v", o[0].ctx)
	}
	// A section variable overrides a static one of the same name.
	p = pipe(t, steps)
	r := prow("a", "1")
	r.ctx["grp"] = "static"
	o, _ = p.Run(r)
	if o[0].ctx["grp"] != "Main" {
		t.Errorf("ctx = %v", o[0].ctx)
	}
}

func TestPipelineFillDown(t *testing.T) {
	steps := `[{section: {column: y, match: '^[A-Za-z]', set: s, default: d}}, {fill_down: {columns: [y, z], reset_on: section}}]`
	p := pipe(t, steps)
	out, _ := runAll(t, p,
		prow("y", "1694", "z", "keep"), prow("y", "", "z", ""), prow("y", " ", "z", "new"),
		prow("y", "Heading"), // resets the memory
		prow("y", "", "z", ""), prow("y", int64(2025), "z", ""), prow("y", "", "z", ""))
	var ys, zs []any
	for _, o := range out {
		ys, zs = append(ys, o["y"]), append(zs, o["z"])
	}
	if !reflect.DeepEqual(ys, []any{"1694", "1694", "1694", "", int64(2025), int64(2025)}) {
		t.Errorf("y = %#v (cells before the first value after a heading stay empty)", ys)
	}
	if !reflect.DeepEqual(zs, []any{"keep", "keep", "new", "", "", ""}) {
		t.Errorf("z = %#v", zs)
	}
	// Without reset_on the value survives headings.
	p = pipe(t, `[{section: {column: y, match: '^[A-Za-z]', set: s}}, {fill_down: {columns: [y]}}]`)
	out, _ = runAll(t, p, prow("y", "1694"), prow("y", "Heading"), prow("y", ""))
	if len(out) != 2 || out[1]["y"] != "1694" {
		t.Errorf("out = %v", out)
	}
	// The input row is not modified (a step must not alias what an earlier step emitted).
	in := prow("y", "")
	p = pipe(t, `[{fill_down: {columns: [y]}}]`)
	_, _ = p.Run(prow("y", "a"))
	_, _ = p.Run(in)
	if in.fields["y"] != "" {
		t.Error("fill_down modified its input")
	}
}

func TestPipelineNormalize(t *testing.T) {
	tests := []struct {
		name  string
		steps string
		in    any
		want  any
		bad   string // substring of the step error
	}{
		{"trim", "[trim]", "  Feb ", "Feb", ""},
		{"lower", "[lower]", "FeB", "feb", ""},
		{"upper", "[upper]", "feb", "FEB", ""},
		{"trim lower in order", "[trim, lower]", "Feb ", "feb", ""},
		{"collapse_ws", "[collapse_ws]", "a \t b\n\nc", "a b c", ""},
		{"collapse then trim", "[collapse_ws, trim]", "  a   b  ", "a b", ""},
		{"scalar form", "trim", " x ", "x", ""},
		{"text transform leaves a number", "[trim, lower]", int64(5), int64(5), ""},
		{"to_int text", "[to_int]", "1694", int64(1694), ""},
		{"to_int padded", "[to_int]", " 1694 ", int64(1694), ""},
		{"to_int whole float text", "[to_int]", "1694.0", int64(1694), ""},
		{"to_int exponent", "[to_int]", "1e3", int64(1000), ""},
		{"to_int number", "[to_int]", int64(2025), int64(2025), ""},
		{"to_int whole float", "[to_int]", float64(2025), int64(2025), ""},
		{"to_int negative", "[to_int]", "-4", int64(-4), ""},
		{"to_int empty stays", "[to_int]", "", "", ""},
		{"to_int blank stays", "[to_int]", "  ", "  ", ""},
		{"to_int fractional", "[to_int]", "1.5", nil, "not a whole number"},
		{"to_int fractional float", "[to_int]", 1.5, nil, "not a whole number"},
		{"to_int text", "[to_int]", "abc", nil, "not a number"},
		{"to_int marker", "[to_int]", "x", nil, "not a number"},
		{"to_int hex", "[to_int]", "0x10", nil, "not a number"},
		{"to_int inf", "[to_int]", "inf", nil, "not a number"},
		{"to_int nan", "[to_int]", "NaN", nil, "not a number"},
		{"to_int underscore", "[to_int]", "1_000", nil, "not a number"},
		{"to_int huge", "[to_int]", "1e30", nil, "whole number in range"},
		{"to_int boolean", "[to_int]", true, nil, "boolean"},
		{"to_number int", "[to_number]", "42", int64(42), ""},
		{"to_number float", "[to_number]", "13.8438", 13.8438, ""},
		{"to_number negative", "[to_number]", "-0.5", -0.5, ""},
		{"to_number padded", "[to_number]", " 7 ", int64(7), ""},
		{"to_number marker untouched", "[to_number]", "x", "x", ""},
		{"to_number dots untouched", "[to_number]", "..", "..", ""},
		{"to_number empty untouched", "[to_number]", "", "", ""},
		{"to_number thousands untouched", "[to_number]", "1,234", "1,234", ""},
		{"to_number hex untouched", "[to_number]", "0x10", "0x10", ""},
		{"to_number inf untouched", "[to_number]", "Inf", "Inf", ""},
		{"to_number number stays", "[to_number]", 3.5, 3.5, ""},
		{"to_number too many digits stays text", "[to_number]", "0.12345678901234567", "0.12345678901234567", ""},
		{"to_number overflow stays text", "[to_number]", "123456789012345678901", "123456789012345678901", ""},
		{"to_number big int fits", "[to_number]", "9007199254740993", int64(9007199254740993), ""},
		{"to_string number", "[to_string]", int64(5), "5", ""},
		{"to_string float", "[to_string]", 1.5, "1.5", ""},
		{"to_string text", "[to_string]", "a", "a", ""},
		{"to_string bool", "[to_string]", true, "true", ""},
		{"to_string nil", "[to_string]", nil, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := pipe(t, "[{normalize: {c: "+tc.steps+"}}]")
			o, err := p.Run(prow("c", tc.in))
			if tc.bad != "" {
				var se *stepError
				if !errors.As(err, &se) || !strings.Contains(err.Error(), tc.bad) || !strings.Contains(err.Error(), `column "c"`) {
					t.Fatalf("err = %v, want step error containing %q", err, tc.bad)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(o[0].fields["c"], tc.want) {
				t.Errorf("got %#v, want %#v", o[0].fields["c"], tc.want)
			}
		})
	}
	// A column that is not in the row is skipped, not an error; other columns are still normalised.
	p := pipe(t, "[{normalize: {missing: to_int, c: trim}}]")
	o, err := p.Run(prow("c", " x "))
	if err != nil || o[0].fields["c"] != "x" {
		t.Errorf("%v %v", o, err)
	}
}

func TestPipelineUnpivot(t *testing.T) {
	list := pipe(t, `[{unpivot: {id: [k], columns: [a, b, c], name: stat, value: v}}]`)
	o, err := list.Run(prow("k", "r1", "a", "1", "b", "", "c", int64(0), "dropped", "x"))
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	for _, r := range o {
		got = append(got, r.fields)
	}
	want := []map[string]any{{"k": "r1", "stat": "a", "v": "1"}, {"k": "r1", "stat": "c", "v": int64(0)}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("list: %v\nwant %v (empty cells dropped, zero kept, unlisted columns not carried)", got, want)
	}
	mp := pipe(t, `[{unpivot: {id: [k], columns: {a: Alpha, c: Gamma}, name: stat, value: v}}]`)
	o, _ = mp.Run(prow("k", "r", "a", "1", "c", "3"))
	if len(o) != 2 || o[0].fields["stat"] != "Alpha" || o[1].fields["stat"] != "Gamma" {
		t.Errorf("map: %v", o)
	}
	// All cells empty: the row yields nothing, which is not an error.
	o, err = mp.Run(prow("k", "r", "a", "", "c", " "))
	if err != nil || len(o) != 0 {
		t.Errorf("%v %v", o, err)
	}
	// Order of map columns is the order in the file.
	var tbl config.Table
	if err := yaml.Unmarshal([]byte(`[{unpivot: {id: [k], columns: {z: Z, a: A, m: M}, name: n, value: v}}]`), &tbl); err != nil {
		t.Fatal(err)
	}
	if c := tbl[0].Unpivot.Columns; c[0].Column != "z" || c[1].Column != "a" || c[2].Column != "m" {
		t.Errorf("order lost: %v", c)
	}
	// Later steps see each long row; text markers survive as data.
	p := pipe(t, `[{unpivot: {id: [k], columns: [a, b], name: s, value: v}}, {normalize: {v: to_number}}]`)
	o, _ = p.Run(prow("k", "r", "a", "x", "b", "2.5"))
	if len(o) != 2 || o[0].fields["v"] != "x" || o[1].fields["v"] != 2.5 {
		t.Errorf("%v", o)
	}
}

func TestPipelineSplit(t *testing.T) {
	steps := `[{split: {column: d, regex: '^\s*(?P<age>18-21|22-29)\s+(?P<name>.*?)\s*$', optional: true}}]`
	p := pipe(t, steps)
	o, err := p.Run(prow("d", "  18-21   Corporate managers  "))
	if err != nil || o[0].fields["age"] != "18-21" || o[0].fields["name"] != "Corporate managers" || o[0].fields["d"] != "  18-21   Corporate managers  " {
		t.Errorf("%v %v", o, err)
	}
	o, err = p.Run(prow("d", "All employees"))
	if err != nil || o[0].fields["age"] != "" || o[0].fields["name"] != "" {
		t.Errorf("optional non-match: %v %v", o, err)
	}
	// Not optional: a non-matching row is a row error naming the column.
	p = pipe(t, `[{split: {column: d, regex: '^(?P<a>\d+)-(?P<b>\d+)$'}}]`)
	if _, err := p.Run(prow("d", "nope")); err == nil || !strings.Contains(err.Error(), `column "d"`) {
		t.Errorf("err = %v", err)
	}
	if _, err := p.Run(prow("x", "no column")); err == nil {
		t.Error("a missing column does not match a non-optional split")
	}
	o, err = p.Run(prow("d", "10-20"))
	if err != nil || o[0].fields["a"] != "10" || o[0].fields["b"] != "20" {
		t.Errorf("%v %v", o, err)
	}
	// An optional group that did not take part is empty.
	p = pipe(t, `[{split: {column: d, regex: '^(?P<a>\w+)(?:-(?P<b>\w+))?$'}}]`)
	o, _ = p.Run(prow("d", "solo"))
	if o[0].fields["a"] != "solo" || o[0].fields["b"] != "" {
		t.Errorf("%v", o)
	}
}

func TestPipelineStrip(t *testing.T) {
	p := pipe(t, `[{strip: {c: ["'", "-"]}}]`)
	o, _ := p.Run(prow("c", "'1-2'", "other", "'keep'"))
	if o[0].fields["c"] != "12" || o[0].fields["other"] != "'keep'" {
		t.Errorf("%v", o[0].fields)
	}
	o, _ = p.Run(prow("c", int64(5)))
	if o[0].fields["c"] != int64(5) {
		t.Errorf("non-text values are left alone: %v", o[0].fields)
	}
}

func TestPipelineStepsApplyInOrder(t *testing.T) {
	// strip then to_int works, to_int then strip does not (the apostrophe makes to_int fail first).
	ok := pipe(t, `[{strip: {c: ["'"]}}, {normalize: {c: to_int}}]`)
	if o, err := ok.Run(prow("c", "'42")); err != nil || o[0].fields["c"] != int64(42) {
		t.Errorf("%v %v", o, err)
	}
	bad := pipe(t, `[{normalize: {c: to_int}}, {strip: {c: ["'"]}}]`)
	if _, err := bad.Run(prow("c", "'42")); err == nil {
		t.Error("want an error when to_int runs before strip")
	}
	// A dropped row never reaches later steps (the failing normalize is not run).
	p := pipe(t, `[{skip_rows: {where: c, match: '^x'}}, {normalize: {c: to_int}}]`)
	if o, err := p.Run(prow("c", "x1")); err != nil || len(o) != 0 {
		t.Errorf("%v %v", o, err)
	}
}

func TestPipelineNeverPanics(t *testing.T) {
	p := pipe(t, `[{normalize: {a: [to_int, lower]}}, {split: {column: a, regex: '(?P<x>.)', optional: true}}, {strip: {a: ["z"]}}, {unpivot: {id: [x], columns: [a], name: n, value: v}}]`)
	for _, in := range []any{nil, "", "\x00", int64(-1 << 63), float64(1e300), true, []any{1}, map[string]any{"a": 1}, struct{}{}} {
		if _, err := p.Run(prow("a", in)); err != nil {
			var se *stepError
			if !errors.As(err, &se) {
				t.Errorf("%#v: error is not a *stepError: %v", in, err)
			}
		}
	}
	// A nil row map must not panic either.
	if _, err := p.Run(pipeRow{}); err != nil {
		var se *stepError
		if !errors.As(err, &se) {
			t.Error(err)
		}
	}
}

func TestNewPipelineRejectsBadRegex(t *testing.T) {
	for _, st := range []config.Step{
		{SkipRows: &config.SkipRows{Where: "a", Match: "("}},
		{Section: &config.Section{Column: "a", Match: "(", Set: "s"}},
		{Split: &config.Split{Column: "a", Regex: "(?P<x"}},
		{},
	} {
		if _, err := NewPipeline(config.Table{st}); err == nil {
			t.Errorf("%+v: want an error", st)
		}
	}
}
