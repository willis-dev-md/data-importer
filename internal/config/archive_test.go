package config

import (
	"strings"
	"testing"
)

func TestArchiveAndResolveConfig(t *testing.T) {
	const head = "id: a\nkind: xlsx\nxlsx: {sheet: S}\nnatural_key: [id]\n"
	const csvHead = "id: a\nkind: csv\nnatural_key: [id]\n"
	res := func(layout, start string, back int, kind string) string {
		return "resolve:\n  ym: {kind: " + kind + ", layout: " + layout + ", start: " + start + ", back: " + itoa(back) + "}\n"
	}
	tests := []struct {
		name, yaml, wantErr string // wantErr "" = valid
	}{
		{"archive ok", head + "location: https://h/x.zip\narchive: {type: zip, member: '(?i)Table 20\\.7a .*\\.xlsx$'}\n", ""},
		{"archive on csv", csvHead + "location: https://h/x.zip\narchive: {type: zip, member: 'a\\.csv$'}\n", ""},
		{"archive type missing", head + "location: x\narchive: {member: a}\n", "archive.type: required"},
		{"archive type tar", head + "location: x\narchive: {type: tar, member: a}\n", `archive.type: unsupported archive type "tar"`},
		{"archive member missing", head + "location: x\narchive: {type: zip}\n", "archive.member: required"},
		{"archive member bad regex", head + "location: x\narchive: {type: zip, member: '('}\n", "archive.member: invalid regular expression"},
		{"archive unknown key", head + "location: x\narchive: {type: zip, member: a, extract: true}\n", "field extract not found"},
		{"resolve ok", csvHead + "location: https://h/f-{ym}.csv\n" + res(`"2006-01"`, `"-1m"`, 6, "month"), ""},
		{"resolve start 0m", csvHead + "location: https://h/f-{ym}.csv\n" + res(`"2006-01"`, `"0m"`, 1, "month"), ""},
		{"resolve with archive", head + "location: https://h/f-{ym}.zip\narchive: {type: zip, member: a}\n" + res(`"200601"`, `"-1m"`, 3, "month"), ""},
		{"resolve unknown key", csvHead + "location: f-{ym}\nresolve: {ym: {kind: month, layout: '2006-01', start: -1m, back: 2, step: 1}}\n", "field step not found"},
		{"kind missing", csvHead + "location: f-{ym}\n" + res(`"2006-01"`, `"-1m"`, 2, `""`), "resolve.ym.kind: required"},
		{"kind week", csvHead + "location: f-{ym}\n" + res(`"2006-01"`, `"-1m"`, 2, "week"), `resolve.ym.kind: unsupported kind "week"`},
		{"layout empty", csvHead + "location: f-{ym}\n" + res(`""`, `"-1m"`, 2, "month"), "resolve.ym.layout: required"},
		{"layout without month", csvHead + "location: f-{ym}\n" + res(`"2006"`, `"-1m"`, 2, "month"), "resolve.ym.layout"},
		{"layout without year", csvHead + "location: f-{ym}\n" + res(`"01"`, `"-1m"`, 2, "month"), "does not contain the year"},
		{"layout with slash", csvHead + "location: f-{ym}\n" + res(`"2006/01"`, `"-1m"`, 2, "month"), "resolve.ym.layout"},
		{"layout with space", csvHead + "location: f-{ym}\n" + res(`"Jan 2006"`, `"-1m"`, 2, "month"), "resolve.ym.layout"},
		{"layout with query chars", csvHead + "location: f-{ym}\n" + res(`"2006-01?x=1"`, `"-1m"`, 2, "month"), "resolve.ym.layout"},
		{"layout too long", csvHead + "location: f-{ym}\n" + res(`"2006-01-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`, `"-1m"`, 2, "month"), "too long"},
		{"start empty", csvHead + "location: f-{ym}\n" + res(`"2006-01"`, `""`, 2, "month"), "resolve.ym.start"},
		{"start in the future", csvHead + "location: f-{ym}\n" + res(`"2006-01"`, `"+1m"`, 2, "month"), "resolve.ym.start"},
		{"start in days", csvHead + "location: f-{ym}\n" + res(`"2006-01"`, `"-1d"`, 2, "month"), "resolve.ym.start"},
		{"start word", csvHead + "location: f-{ym}\n" + res(`"2006-01"`, `"last"`, 2, "month"), "resolve.ym.start"},
		{"start huge", csvHead + "location: f-{ym}\n" + res(`"2006-01"`, `"-99999m"`, 2, "month"), "resolve.ym.start"},
		{"back zero", csvHead + "location: f-{ym}\n" + res(`"2006-01"`, `"-1m"`, 0, "month"), "resolve.ym.back: must be between 1 and 120, got 0"},
		{"back negative", csvHead + "location: f-{ym}\n" + res(`"2006-01"`, `"-1m"`, -3, "month"), "resolve.ym.back"},
		{"back too many", csvHead + "location: f-{ym}\n" + res(`"2006-01"`, `"-1m"`, 121, "month"), "resolve.ym.back"},
		{"resolve name not in location", csvHead + "location: https://h/fixed.csv\n" + res(`"2006-01"`, `"-1m"`, 2, "month"), "the location has no {ym} placeholder"},
		{"bad placeholder name", csvHead + "location: f-{Y M}\nresolve: {'Y M': {kind: month, layout: '2006-01', start: -1m, back: 2}}\n", "resolve.Y M: invalid placeholder name"},
		{"two resolve entries", csvHead + "location: f-{a}-{b}\nresolve:\n  a: {kind: month, layout: '2006-01', start: -1m, back: 2}\n  b: {kind: month, layout: '2006-01', start: -1m, back: 2}\n", "at most one placeholder"},
		{"unresolved placeholder", csvHead + "location: https://h/{year}{status}/f.csv\n", "location: unresolved placeholder {year}"},
		{"unresolved next to a resolved one", csvHead + "location: https://h/{year}/f-{ym}.csv\n" + res(`"2006-01"`, `"-1m"`, 2, "month"), "location: unresolved placeholder {year}"},
		{"empty braces", csvHead + "location: https://h/{}/f.csv\n", "unresolved placeholder {}"},
		{"no placeholders and no resolve", csvHead + "location: https://h/f.csv\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSource([]byte(tc.yaml))
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestResolveStartMonths(t *testing.T) {
	for in, want := range map[string]int{"0m": 0, "-1m": -1, "-12m": -12, "-1200m": -1200} {
		if n, ok := (Resolve{Start: in}).StartMonths(); !ok || n != want {
			t.Errorf("%q = %d, %v", in, n, ok)
		}
	}
	for _, in := range []string{"", "1m", "+1m", "-0m", "-1", "-1M", "-1m ", " -1m", "-1.5m", "-12345m", "-1201m", "m"} {
		if _, ok := (Resolve{Start: in}).StartMonths(); ok {
			t.Errorf("%q should be invalid", in)
		}
	}
}

func itoa(n int) string {
	if n < 0 {
		return "-" + itoa(-n)
	}
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
