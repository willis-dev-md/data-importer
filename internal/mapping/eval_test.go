package mapping

import (
	"encoding/csv"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func exampleMapping(t *testing.T, name string) *Mapping {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func inlineMapping(t *testing.T, y string) *Mapping {
	t.Helper()
	m, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, y)
	}
	return m
}

func d(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }

func errCodes(r Result) []string {
	var s []string
	for _, e := range r.Errors {
		s = append(s, e.Field+":"+e.Code)
	}
	return s
}

func onsMeta() map[string]any {
	return map[string]any{"Title": "CPIH ANNUAL RATE 00: ALL ITEMS 2015=100", "CDID": "L55O", "Unit": "%"}
}

func TestExampleMappingsValidate(t *testing.T) {
	c := loadCanon(t)
	for _, n := range []string{"ons-l55o", "uk-hpi-average-prices", "ashe-t20-annual-pay", "boe-baserate"} {
		m := exampleMapping(t, n+".mapping.yaml")
		for _, i := range Validate(m, c) {
			if i.Severity == SeverityError {
				t.Errorf("%s: %s", n, i)
			}
		}
	}
}

func TestONSFixtureAllRows(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "ons", "l55o.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	m := exampleMapping(t, "ons-l55o.mapping.yaml")
	n := 0
	for _, row := range rows[8:] {
		if len(row) != 2 {
			t.Fatalf("bad fixture row %v", row)
		}
		n++
		res := Eval(m, map[string]any{"period": row[0], "value": row[1], "_meta": onsMeta(), "_source": map[string]any{"line": n}})
		if len(res.Errors) != 0 {
			t.Fatalf("row %v: %v", row, res.Errors)
		}
		want, _ := strconv.ParseFloat(row[1], 64)
		if res.Fields["value"] != want || res.Fields["series_id"] != "L55O" || res.Fields["measure"] != "cpih_annual_rate" {
			t.Fatalf("row %v: %v", row, res.Fields)
		}
		if len(res.Extra) != 0 {
			t.Fatalf("row %v: extra %v", row, res.Extra)
		}
	}
	if n != 642 {
		t.Fatalf("rows = %d, want 642", n)
	}
}

func TestONSSpotChecks(t *testing.T) {
	m := exampleMapping(t, "ons-l55o.mapping.yaml")
	tests := []struct {
		period, value string
		typ           string
		start, end    time.Time
		val           float64
	}{
		{"1989", "5.7", "year", d(1989, 1, 1), d(1989, 12, 31), 5.7},
		{"1989 Q1", "5.7", "quarter", d(1989, 1, 1), d(1989, 3, 31), 5.7},
		{"1989 Q4", "5.7", "quarter", d(1989, 10, 1), d(1989, 12, 31), 5.7},
		{"2026 OCT", "3.7", "month", d(2026, 10, 1), d(2026, 10, 31), 3.7},
		{"2024 feb", "1", "month", d(2024, 2, 1), d(2024, 2, 29), 1},
		{"2023 Feb", "1", "month", d(2023, 2, 1), d(2023, 2, 28), 1},
	}
	for _, tt := range tests {
		res := Eval(m, map[string]any{"period": tt.period, "value": tt.value, "_meta": onsMeta()})
		if len(res.Errors) != 0 {
			t.Fatalf("%s: %v", tt.period, res.Errors)
		}
		f := res.Fields
		if f["period_type"] != tt.typ || f["period_start"] != tt.start || f["period_end"] != tt.end || f["value"] != tt.val {
			t.Errorf("%s: %v", tt.period, f)
		}
		if f["unit"] != "%" || f["series_id"] != "L55O" || f["measure"] != "cpih_annual_rate" ||
			f["series_title"] != "CPIH ANNUAL RATE 00: ALL ITEMS 2015=100" {
			t.Errorf("%s: %v", tt.period, f)
		}
	}
	// bad inputs are per-field errors; the other fields still evaluate
	res := Eval(m, map[string]any{"period": "Q1 1989", "value": "n/a", "_meta": onsMeta()})
	got := strings.Join(errCodes(res), ",")
	if got != "period_end:PARSE_FAILED,period_start:PARSE_FAILED,period_type:PARSE_FAILED,value:PARSE_FAILED" {
		t.Errorf("errors = %s", got)
	}
	if res.Fields["series_id"] != "L55O" {
		t.Errorf("series_id lost: %v", res.Fields)
	}
	res = Eval(m, map[string]any{"value": "1"})
	if got := strings.Join(errCodes(res), ","); !strings.Contains(got, "series_id:MISSING_REQUIRED") {
		t.Errorf("errors = %s", got)
	}
}

func TestHPI(t *testing.T) {
	m := exampleMapping(t, "uk-hpi-average-prices.mapping.yaml")
	res := Eval(m, map[string]any{"Date": "2026-09-01", "Region_Name": "United Kingdom", "Area_Code": "K02000001",
		"measure_col": "average_price", "value": "272642"})
	if len(res.Errors) != 0 {
		t.Fatal(res.Errors)
	}
	want := map[string]any{
		"measure": "average_price", "period_type": "month", "period_start": d(2026, 9, 1), "period_end": d(2026, 9, 30),
		"value": 272642.0, "unit": "GBP", "series_id": "ukhpi:average_price:K02000001", "statistic": "mean",
		"series_title": "UK HPI average_price: United Kingdom",
		"dimensions":   map[string]string{"area_code": "K02000001", "area_name": "United Kingdom", "property_type": "all"},
	}
	if !reflect.DeepEqual(res.Fields, want) {
		t.Errorf("fields = %#v\nwant %#v", res.Fields, want)
	}
	if len(res.Extra) != 0 {
		t.Errorf("extra = %v", res.Extra)
	}
	// a missing template placeholder is MISSING_REQUIRED, other fields still work
	res = Eval(m, map[string]any{"Date": "2026-09-01", "measure_col": "average_price", "value": "1"})
	if got := strings.Join(errCodes(res), ","); got != "series_id:MISSING_REQUIRED,series_title:MISSING_REQUIRED" {
		t.Errorf("errors = %s", got)
	}
	if res.Fields["value"] != 1.0 {
		t.Errorf("value lost: %v", res.Fields)
	}
}

func TestASHE(t *testing.T) {
	m := exampleMapping(t, "ashe-t20-annual-pay.mapping.yaml")
	ctx := func() map[string]any {
		return map[string]any{"sheet": "All", "year": 2026, "status": "provisional"}
	}
	row := map[string]any{"age_band": "18-21", "soc_code": "1", "soc_name": "Managers, directors and senior officials",
		"statistic": "median", "value": "x", "_ctx": ctx()}
	res := Eval(m, row)
	if len(res.Errors) != 0 {
		t.Fatal(res.Errors)
	}
	f := res.Fields
	if v, ok := f["value"]; !ok || v != nil {
		t.Errorf("value = %v (present %v)", v, ok)
	}
	wantDims := map[string]string{"population": "All", "age_band": "18-21", "soc_code": "1", "soc_name": "Managers, directors and senior officials"}
	if f["quality_flag"] != "x" || f["period_type"] != "tax_year" || f["status"] != "provisional" || f["unit"] != "GBP" ||
		f["period_start"] != d(2025, 4, 6) || f["period_end"] != d(2026, 4, 5) || !reflect.DeepEqual(f["dimensions"], wantDims) {
		t.Errorf("fields = %#v", f)
	}
	if f["series_id"] != "ashe20.7a:All:18-21:1:median" {
		t.Errorf("series_id = %v", f["series_id"])
	}

	// "All employees": empty age_band and soc_code, defaults apply; jobs gets thousand_jobs
	res = Eval(m, map[string]any{"age_band": "", "soc_code": "", "soc_name": "", "statistic": "jobs", "value": 25000.5, "_ctx": ctx()})
	if len(res.Errors) != 0 {
		t.Fatal(res.Errors)
	}
	dims := res.Fields["dimensions"].(map[string]string)
	if dims["age_band"] != "all" || dims["soc_code"] != "total" || dims["soc_name"] != "All occupations" || res.Fields["unit"] != "thousand_jobs" {
		t.Errorf("fields = %#v", res.Fields)
	}
	if res.Fields["value"] != 25000.5 {
		t.Errorf("value = %v", res.Fields["value"])
	}
	if _, ok := res.Fields["quality_flag"]; ok {
		t.Error("quality_flag set without a marker")
	}
	// a non-marker, non-numeric string is still PARSE_FAILED
	res = Eval(m, map[string]any{"statistic": "mean", "value": "abc", "_ctx": ctx()})
	if got := strings.Join(errCodes(res), ","); got != "value:PARSE_FAILED" {
		t.Errorf("errors = %s", got)
	}
	// other markers
	for _, mk := range []string{"..", ":", "-"} {
		res = Eval(m, map[string]any{"statistic": "mean", "value": mk, "_ctx": ctx()})
		if len(res.Errors) != 0 || res.Fields["quality_flag"] != mk || res.Fields["value"] != nil {
			t.Errorf("marker %q: %v %v", mk, res.Errors, res.Fields)
		}
	}
}

func TestBoE(t *testing.T) {
	m := exampleMapping(t, "boe-baserate.mapping.yaml")
	ctx := map[string]any{"series_name": "Bank Rate"}
	tests := []struct {
		name       string
		payload    map[string]any
		typ        string
		start, end time.Time
		wantErr    bool
	}{
		{"day", map[string]any{"year": 2025, "day": 6, "month": "Feb ", "rate": 4.5, "_ctx": ctx}, "day", d(2025, 2, 6), d(2025, 2, 6), false},
		{"float json numbers", map[string]any{"year": 2025.0, "day": 6.0, "month": "Feb", "rate": 4.5, "_ctx": ctx}, "day", d(2025, 2, 6), d(2025, 2, 6), false},
		{"month only 1694", map[string]any{"year": 1694, "day": nil, "month": "Oct", "rate": 6, "_ctx": ctx}, "month", d(1694, 10, 1), d(1694, 10, 31), false},
		{"lower may", map[string]any{"year": 2009, "day": 7, "month": "may", "rate": 0.5, "_ctx": ctx}, "day", d(2009, 5, 7), d(2009, 5, 7), false},
		{"Aug trailing space", map[string]any{"year": 2016, "day": 4, "month": "Aug ", "rate": 0.25, "_ctx": ctx}, "day", d(2016, 8, 4), d(2016, 8, 4), false},
		{"empty day string", map[string]any{"year": 1694, "day": "", "month": "Feb", "rate": 6, "_ctx": ctx}, "month", d(1694, 2, 1), d(1694, 2, 28), false},
		{"bad month", map[string]any{"year": 2025, "day": 6, "month": "Foo", "rate": 4.5, "_ctx": ctx}, "", time.Time{}, time.Time{}, true},
		{"bad day", map[string]any{"year": 2025, "day": 31, "month": "Feb", "rate": 4.5, "_ctx": ctx}, "", time.Time{}, time.Time{}, true},
	}
	for _, tt := range tests {
		res := Eval(m, tt.payload)
		if tt.wantErr {
			if got := strings.Join(errCodes(res), ","); got != "period_end:PARSE_FAILED,period_start:PARSE_FAILED,period_type:PARSE_FAILED" {
				t.Errorf("%s: errors = %s", tt.name, got)
			}
			if res.Fields["value"] != 4.5 || res.Fields["series_id"] != "BOE_BANK_RATE" {
				t.Errorf("%s: other fields lost: %v", tt.name, res.Fields)
			}
			continue
		}
		if len(res.Errors) != 0 {
			t.Errorf("%s: %v", tt.name, res.Errors)
			continue
		}
		f := res.Fields
		if f["period_type"] != tt.typ || f["period_start"] != tt.start || f["period_end"] != tt.end {
			t.Errorf("%s: %v", tt.name, f)
		}
		if f["series_id"] != "BOE_BANK_RATE" || f["unit"] != "%" || f["measure"] != "bank_rate" || f["series_title"] != "Bank Rate" {
			t.Errorf("%s: %v", tt.name, f)
		}
		if !reflect.DeepEqual(f["dimensions"], map[string]string{"rate_type": "Bank Rate"}) {
			t.Errorf("%s: dims %v", tt.name, f["dimensions"])
		}
	}
}

func TestPeriodVariants(t *testing.T) {
	tests := []struct {
		name       string
		field      string
		in         any
		typ        string
		start, end time.Time
		code       string
	}{
		{"iso month precision", `{from: x, type: period, format: "2006-01-02", precision: month, output: period_type}`, "2026-02-17", "month", d(2026, 2, 1), d(2026, 2, 28), ""},
		{"iso year precision", `{from: x, type: period, format: "2006-01-02", precision: year, output: period_type}`, "2026-02-17", "year", d(2026, 1, 1), d(2026, 12, 31), ""},
		{"iso quarter precision", `{from: x, type: period, format: "2006-01-02", precision: quarter, output: period_type}`, "2026-05-17", "quarter", d(2026, 4, 1), d(2026, 6, 30), ""},
		{"iso day", `{from: x, type: period, format: "2006-01-02", output: period_type}`, "2026-02-17", "day", d(2026, 2, 17), d(2026, 2, 17), ""},
		{"finer precision ignored", `{from: x, type: period, format: "Jan 2006", precision: day, output: period_type}`, "Mar 2026", "month", d(2026, 3, 1), d(2026, 3, 31), ""},
		{"tax year", `{from: x, type: period, format: tax_year_ending, output: period_type}`, 2026, "tax_year", d(2025, 4, 6), d(2026, 4, 5), ""},
		{"tax year float", `{from: x, type: period, format: tax_year_ending, output: period_type}`, 2026.0, "tax_year", d(2025, 4, 6), d(2026, 4, 5), ""},
		{"builtin names", `{from: x, type: period, format: ["YYYY Qn", "YYYY"], output: period_type}`, "2020", "year", d(2020, 1, 1), d(2020, 12, 31), ""},
		{"time input", `{from: x, type: period, precision: month, output: period_type}`, d(2020, 6, 15), "month", d(2020, 6, 1), d(2020, 6, 30), ""},
		{"no match", `{from: x, type: period, format: "YYYY Qn", output: period_type}`, "2020", "", time.Time{}, time.Time{}, "PARSE_FAILED"},
		{"layout without year", `{from: x, type: period, format: "Jan", output: period_type}`, "Oct", "", time.Time{}, time.Time{}, "PARSE_FAILED"},
		{"q5", `{from: x, type: period, output: period_type}`, "2020 Q5", "", time.Time{}, time.Time{}, "PARSE_FAILED"},
		{"map type", `{from: x, type: period, output: period_type}`, map[string]any{"a": 1}, "", time.Time{}, time.Time{}, "TYPE_MISMATCH"},
	}
	for _, tt := range tests {
		for _, out := range []string{"period_type", "period_start", "period_end"} {
			y := "source: s\nfields:\n  " + out + ": " + strings.Replace(tt.field, "output: period_type", "output: "+out, 1) + "\n"
			res := Eval(inlineMapping(t, y), map[string]any{"x": tt.in})
			if tt.code != "" {
				if got := errCodes(res); len(got) != 1 || got[0] != out+":"+tt.code {
					t.Errorf("%s/%s: errors = %v", tt.name, out, got)
				}
				continue
			}
			want := map[string]any{"period_type": tt.typ, "period_start": tt.start, "period_end": tt.end}[out]
			if len(res.Errors) != 0 || res.Fields[out] != want {
				t.Errorf("%s/%s: got %v (%v), want %v", tt.name, out, res.Fields[out], res.Errors, want)
			}
		}
	}
}

func TestTypesAndTransforms(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		payload map[string]any
		want    any
		code    string
		omitted bool
	}{
		{"string trim lower", `{from: a, transform: [trim, lower]}`, map[string]any{"a": "  HeLLo "}, "hello", "", false},
		{"upper", `{from: a, transform: upper}`, map[string]any{"a": "abc"}, "ABC", "", false},
		{"concat", `{from: [a, b], transform: 'concat(" ")'}`, map[string]any{"a": "Ada", "b": "Lovelace"}, "Ada Lovelace", "", false},
		{"concat skips empty", `{from: [a, b, c], transform: 'concat("-")'}`, map[string]any{"a": "x", "b": "", "c": "z"}, "x-z", "", false},
		{"implicit join", `{from: [a, b]}`, map[string]any{"a": "x", "b": "y"}, "x y", "", false},
		{"coalesce", `{from: [a, b], transform: coalesce}`, map[string]any{"a": "", "b": "second"}, "second", "", false},
		{"coalesce none", `{from: [a, b], transform: coalesce}`, map[string]any{"a": "", "b": nil}, nil, "", true},
		{"split coalesce", `{from: a, transform: ['split(";")', coalesce]}`, map[string]any{"a": "p;q"}, "p", "", false},
		{"split concat", `{from: a, transform: ['split(";")', 'concat("|")']}`, map[string]any{"a": "p;q"}, "p|q", "", false},
		{"regex", `{from: a, transform: 'regex("[0-9]+", "#")'}`, map[string]any{"a": "ab12cd345"}, "ab#cd#", "", false},
		{"regex groups", `{from: a, transform: 'regex("^(\w+)@.*$", "$1")'}`, map[string]any{"a": "bob@x.org"}, "bob", "", false},
		{"int", `{from: a, type: int}`, map[string]any{"a": "42"}, int64(42), "", false},
		{"int float", `{from: a, type: int}`, map[string]any{"a": 42.0}, int64(42), "", false},
		{"int frac", `{from: a, type: int}`, map[string]any{"a": 4.5}, nil, "PARSE_FAILED", false},
		{"int bad", `{from: a, type: int}`, map[string]any{"a": "4x"}, nil, "PARSE_FAILED", false},
		{"int bool", `{from: a, type: int}`, map[string]any{"a": true}, nil, "TYPE_MISMATCH", false},
		{"decimal", `{from: a, type: decimal}`, map[string]any{"a": "3.25"}, 3.25, "", false},
		{"decimal nan", `{from: a, type: decimal}`, map[string]any{"a": "NaN"}, nil, "PARSE_FAILED", false},
		{"decimal list", `{from: "$.a[*]", type: decimal}`, map[string]any{"a": []any{map[string]any{"b": 1}}}, nil, "", true},
		{"decimal map", `{from: a, type: decimal}`, map[string]any{"a": map[string]any{}}, nil, "TYPE_MISMATCH", false},
		{"bool", `{from: a, type: bool}`, map[string]any{"a": "Yes"}, true, "", false},
		{"bool bad", `{from: a, type: bool}`, map[string]any{"a": "maybe"}, nil, "PARSE_FAILED", false},
		{"bool map", `{from: a, type: bool, map: {Active: true, Lapsed: false}, default: false}`, map[string]any{"a": "Lapsed"}, false, "", false},
		{"bool default", `{from: a, type: bool, map: {Active: true}, default: false}`, map[string]any{}, false, "", false},
		{"date", `{from: a, type: date, format: "02/01/2006"}`, map[string]any{"a": "31/12/2020"}, d(2020, 12, 31), "", false},
		{"date bad", `{from: a, type: date, format: "02/01/2006"}`, map[string]any{"a": "2020-12-31"}, nil, "PARSE_FAILED", false},
		{"timestamp", `{from: a, type: timestamp, format: "2006-01-02T15:04:05Z07:00"}`, map[string]any{"a": "2020-12-31T10:00:00+01:00"}, time.Date(2020, 12, 31, 9, 0, 0, 0, time.UTC), "", false},
		{"string from number", `{from: a}`, map[string]any{"a": 1234567.0}, "1234567", "", false},
		{"string from map", `{from: a}`, map[string]any{"a": map[string]any{}}, nil, "TYPE_MISMATCH", false},
		{"const", `{const: "%"}`, map[string]any{}, "%", "", false},
		{"const int type", `{const: 3, type: decimal}`, map[string]any{}, 3.0, "", false},
		{"missing optional", `{from: a}`, map[string]any{}, nil, "", true},
		{"missing required", `{from: a, required: true}`, map[string]any{}, nil, "MISSING_REQUIRED", false},
		{"empty required", `{from: a, required: true}`, map[string]any{"a": ""}, nil, "MISSING_REQUIRED", false},
		{"required default", `{from: a, required: true, default: z}`, map[string]any{"a": " "}, "z", "", false},
		{"map lookup", `{from: a, map: {jobs: thousand_jobs}}`, map[string]any{"a": "jobs"}, "thousand_jobs", "", false},
		{"map passthrough", `{from: a, map: {jobs: thousand_jobs}}`, map[string]any{"a": "other"}, "other", "", false},
		{"jsonpath key", `{from: "$.a.b"}`, map[string]any{"a": map[string]any{"b": "deep"}}, "deep", "", false},
		{"jsonpath idx", `{from: "$.a[1]"}`, map[string]any{"a": []any{"x", "y"}}, "y", "", false},
		{"jsonpath idx out of range", `{from: "$.a[5]"}`, map[string]any{"a": []any{"x"}}, nil, "", true},
		{"jsonpath wildcard", `{from: "$.a[*].n", transform: 'concat(",")'}`, map[string]any{"a": []any{map[string]any{"n": "x"}, map[string]any{"n": "y"}}}, "x,y", "", false},
		{"jsonpath missing", `{from: "$.a.b"}`, map[string]any{"a": "scalar"}, nil, "", true},
		{"meta", `{from: "@meta.K"}`, map[string]any{"_meta": map[string]any{"K": "mv"}}, "mv", "", false},
		{"meta string map", `{from: "@meta.K"}`, map[string]any{"_meta": map[string]string{"K": "mv"}}, "mv", "", false},
		{"ctx", `{from: "@ctx.k"}`, map[string]any{"_ctx": map[string]any{"k": 7}}, "7", "", false},
		{"ctx missing", `{from: "@ctx.k"}`, map[string]any{}, nil, "", true},
		{"template", `{template: "{a}-{@ctx.k}-{@meta.m}"}`, map[string]any{"a": "A", "_ctx": map[string]any{"k": "K"}, "_meta": map[string]any{"m": "M"}}, "A-K-M", "", false},
		{"template empty", `{template: "{a}-{b}"}`, map[string]any{"a": "A", "b": ""}, nil, "MISSING_REQUIRED", false},
		{"template empty ok", `{template: "{a}-{b}", empty_ok: true}`, map[string]any{"a": "A"}, "A-", "", false},
		{"validate email ok", `{from: a, validate: email}`, map[string]any{"a": "a@b.org"}, "a@b.org", "", false},
		{"validate email bad", `{from: a, validate: email}`, map[string]any{"a": "nope"}, nil, "VALIDATION_FAILED", false},
		{"validate email display name", `{from: a, validate: email}`, map[string]any{"a": "Bob <a@b.org>"}, nil, "VALIDATION_FAILED", false},
		{"validate url ok", `{from: a, validate: url}`, map[string]any{"a": "https://x.org/p"}, "https://x.org/p", "", false},
		{"validate url bad", `{from: a, validate: url}`, map[string]any{"a": "x.org"}, nil, "VALIDATION_FAILED", false},
		{"validate regex ok", `{from: a, validate: 'regex("^[A-Z]{2}[0-9]+$")'}`, map[string]any{"a": "AB12"}, "AB12", "", false},
		{"validate regex bad", `{from: a, validate: 'regex("^[A-Z]{2}[0-9]+$")'}`, map[string]any{"a": "ab12"}, nil, "VALIDATION_FAILED", false},
		{"validate range ok", `{from: a, type: decimal, validate: "range(0,10)"}`, map[string]any{"a": "10"}, 10.0, "", false},
		{"validate range high", `{from: a, type: decimal, validate: "range(0,10)"}`, map[string]any{"a": "10.5"}, nil, "VALIDATION_FAILED", false},
		{"validate range int", `{from: a, type: int, validate: "range(0,10)"}`, map[string]any{"a": "-1"}, nil, "VALIDATION_FAILED", false},
		{"validate range string", `{from: a, validate: "range(0,10)"}`, map[string]any{"a": "x"}, nil, "TYPE_MISMATCH", false},
		{"null marker no flag", `{from: a, type: decimal, null_markers: ["x"]}`, map[string]any{"a": "x"}, nil, "", false},
	}
	for _, tt := range tests {
		y := "source: s\nfields:\n  series_title: " + tt.field + "\n"
		res := Eval(inlineMapping(t, y), tt.payload)
		if tt.code != "" {
			if got := errCodes(res); len(got) != 1 || got[0] != "series_title:"+tt.code {
				t.Errorf("%s: errors = %v (fields %v)", tt.name, got, res.Fields)
			}
			continue
		}
		if len(res.Errors) != 0 {
			t.Errorf("%s: %v", tt.name, res.Errors)
			continue
		}
		got, present := res.Fields["series_title"]
		switch {
		case tt.omitted && present:
			t.Errorf("%s: present: %v", tt.name, got)
		case !tt.omitted && !present:
			t.Errorf("%s: missing", tt.name)
		case !tt.omitted && !reflect.DeepEqual(got, tt.want):
			t.Errorf("%s: got %#v want %#v", tt.name, got, tt.want)
		}
	}
}

func TestExtraAndDimensions(t *testing.T) {
	payload := map[string]any{"a": "1", "b": "2", "c": "3", "_meta": map[string]any{}, "_source": 1, "d": "", "tp": "x"}
	keep := inlineMapping(t, `
source: s
extra: keep
fields:
  series_id: { from: a }
  series_title: { template: "{tp}" }
dimensions:
  dim_b: { from: b }
  dim_d: { from: d }
  dim_const: { const: k }
  dim_bad: { from: "$.zz.z[*]" }
`)
	res := Eval(keep, payload)
	if len(res.Errors) != 0 {
		t.Fatal(res.Errors)
	}
	if !reflect.DeepEqual(res.Extra, map[string]any{"c": "3"}) {
		t.Errorf("extra = %v", res.Extra)
	}
	if !reflect.DeepEqual(res.Fields["dimensions"], map[string]string{"dim_b": "2", "dim_const": "k"}) {
		t.Errorf("dims = %v", res.Fields["dimensions"])
	}
	for _, e := range []string{"drop", ""} {
		m := inlineMapping(t, "source: s\nextra: "+e+"\nfields:\n  series_id: { from: a }\n")
		res = Eval(m, payload)
		if res.Extra == nil || len(res.Extra) != 0 {
			t.Errorf("extra %q = %v", e, res.Extra)
		}
		if dm, ok := res.Fields["dimensions"].(map[string]string); !ok || len(dm) != 0 {
			t.Errorf("dimensions = %#v", res.Fields["dimensions"])
		}
	}
	// a failing dimension is reported under dimensions.<name>
	m := inlineMapping(t, "source: s\ndimensions:\n  x: { from: a, required: true }\n  y: { from: nope, required: true }\n")
	res = Eval(m, payload)
	if got := errCodes(res); !reflect.DeepEqual(got, []string{"dimensions.y:MISSING_REQUIRED"}) {
		t.Errorf("errors = %v", got)
	}
}

func TestFlagNotOverwritten(t *testing.T) {
	m := inlineMapping(t, `
source: s
fields:
  value: { from: v, type: decimal, null_markers: [x], flag: quality_flag }
  quality_flag: { from: q }
`)
	res := Eval(m, map[string]any{"v": "x", "q": "provided"})
	if res.Fields["quality_flag"] != "provided" || res.Fields["value"] != nil {
		t.Errorf("fields = %v", res.Fields)
	}
}

func TestEvalNilAndDeterminism(t *testing.T) {
	if r := Eval(nil, nil); r.Fields == nil || r.Extra == nil || len(r.Errors) != 0 {
		t.Errorf("nil mapping: %+v", r)
	}
	m := exampleMapping(t, "ons-l55o.mapping.yaml")
	p := map[string]any{"period": "bad", "value": "bad"}
	first := Eval(m, p)
	for i := 0; i < 20; i++ {
		if got := Eval(m, p); !reflect.DeepEqual(got, first) {
			t.Fatalf("non-deterministic result")
		}
	}
}

func TestBadTransformAtEvalTime(t *testing.T) {
	// Eval does not require Validate to have run: bad specs are field errors, not panics
	m := &Mapping{Fields: map[string]*Field{
		"series_id": {From: StringList{"a"}, Transform: StringList{"bogus"}},
		"measure":   {From: StringList{"a"}, Transform: StringList{"regex(\"(\",x)"}},
		"unit":      {From: StringList{"a"}, Validate: "bogus"},
		"status":    {From: StringList{"a"}, Type: "nonsense"},
		"statistic": nil,
	}}
	res := Eval(m, map[string]any{"a": "v"})
	if len(res.Errors) != 4 {
		t.Errorf("errors = %v", res.Errors)
	}
}

// FuzzEval checks that Eval never panics on arbitrary JSON-like payloads.
func FuzzEval(f *testing.F) {
	f.Add("1989", "5.7", "Feb ", "x")
	f.Add("2026 OCT", "", "may", "..")
	f.Add("2026 Q9", "NaN", "", "1e400")
	f.Add("", "-", "\x00", "$.a[*]")
	f.Add("0000", "9223372036854775808", "Aug ", "{x}")
	maps := []*Mapping{
		exampleMappingForFuzz("ons-l55o.mapping.yaml"),
		exampleMappingForFuzz("uk-hpi-average-prices.mapping.yaml"),
		exampleMappingForFuzz("ashe-t20-annual-pay.mapping.yaml"),
		exampleMappingForFuzz("boe-baserate.mapping.yaml"),
		exampleMappingForFuzz("acme-csv.mapping.yaml"),
		exampleMappingForFuzz("partner-api.mapping.yaml"),
	}
	f.Fuzz(func(t *testing.T, a, b, c, e string) {
		payloads := []map[string]any{
			{"period": a, "value": b, "month": c, "day": e, "year": a, "rate": b, "Date": a, "measure_col": c, "Area_Code": e,
				"age_band": a, "soc_code": b, "statistic": c, "_meta": map[string]any{"CDID": e}, "_ctx": map[string]any{"year": a, "sheet": c}},
			{"period": []any{a, b}, "value": map[string]any{"x": c}, "month": nil, "day": 1.5, "year": math.Inf(1), "rate": true,
				"_ctx": "not a map", "_meta": []any{a}},
			{a: b, c: e, "$": a, "_ctx": map[string]any{c: e}},
		}
		for _, m := range maps {
			for _, p := range payloads {
				Eval(m, p)
			}
		}
		// arbitrary text as a mapping spec element
		fm := &Mapping{Fields: map[string]*Field{"series_id": {From: StringList{a, "$." + b, "@ctx." + c}, Template: e,
			Transform: StringList{a, b}, Format: StringList{c, e}, Type: "period", Precision: a, Validate: b, Map: map[string]any{a: b}}},
			Dimensions: map[string]*Field{c: {Template: a + "{" + b + "}" + e}}}
		Eval(fm, payloads[0])
	})
}

func exampleMappingForFuzz(name string) *Mapping {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", name))
	if err != nil {
		panic(err)
	}
	m, err := Parse(data)
	if err != nil {
		panic(err)
	}
	return m
}
