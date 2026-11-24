package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/willis-dev-md/data-importer/internal/schedule"
	"gopkg.in/yaml.v3"
)

const schedBase = `id: s
kind: csv
location: file:///x.csv
natural_key: [id]
`

func TestScheduleParse(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string // empty = valid
	}{
		{"no schedule", ``, nil},
		{"monthly last", "schedule: { every: monthly, day_of_month: last }\n", nil},
		{"monthly int", "schedule: { every: monthly, day_of_month: 18, at: \"09:30\" }\n", nil},
		{"defaults only", "schedule: { every: daily }\n", nil},
		{"cron rejected", "schedule: { cron: \"0 6 * * 1\" }\n", []string{"schedule.cron: not supported yet"}},
		{"empty block", "schedule: {}\n", []string{"schedule.every: required"}},
		{"day 31", "schedule: { every: monthly, day_of_month: 31 }\n", []string{"schedule.day_of_month: must be 1-28 or last"}},
		{"day word", "schedule: { every: monthly, day_of_month: first }\n", []string{"day_of_month must be 1-28 or last"}},
		{"several errors", "schedule: { every: weekly, at: \"6\", timezone: Nope/Zone, jitter: 2h }\n",
			[]string{"schedule.day_of_week", "schedule.at", "schedule.timezone", "schedule.jitter"}},
		{"errors beside other fields", "max_error_ratio: 2\nschedule: { every: nope }\n", []string{"max_error_ratio", "schedule.every"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSource([]byte(schedBase + tc.yaml))
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("want error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestScheduleDayOfMonthRoundTrip(t *testing.T) {
	for _, in := range []string{"last", "18"} {
		s, err := ParseSource([]byte(schedBase + "schedule: { every: monthly, day_of_month: " + in + " }\n"))
		if err != nil {
			t.Fatal(err)
		}
		out, err := yaml.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out, []byte("day_of_month: "+in)) {
			t.Errorf("marshalled form lost day_of_month %s:\n%s", in, out)
		}
		again, err := ParseSource(out)
		if err != nil || again.Schedule.DayOfMonth != s.Schedule.DayOfMonth {
			t.Errorf("round trip: %v %+v", err, again)
		}
	}
	s, _ := ParseSource([]byte(schedBase + "schedule: { every: monthly, day_of_month: last }\n"))
	if s.Schedule.DayOfMonth != schedule.LastDay {
		t.Errorf("last = %d", s.Schedule.DayOfMonth)
	}
}

func TestScheduleEnabled(t *testing.T) {
	s, _ := ParseSource([]byte(schedBase))
	if !s.IsEnabled() {
		t.Error("default must be enabled")
	}
	s, _ = ParseSource([]byte(schedBase + "enabled: false\n"))
	if s.IsEnabled() {
		t.Error("enabled: false not honoured")
	}
}

// Every example source, and every schedule block inside the example groups, still validates.
func TestAllExampleSchedulesValidate(t *testing.T) {
	dir := filepath.Join("..", "..", "docs", "examples")
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no examples: %v", err)
	}
	withSchedule := 0
	for _, f := range files {
		name := filepath.Base(f)
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// Whole-file validation for the kinds the loader supports (http_json is not implemented yet).
		if strings.HasSuffix(name, ".source.yaml") && (doc["kind"] == KindCSV || doc["kind"] == KindXLSX) {
			if _, err := LoadSource(f); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
		for _, blk := range findSchedules(doc) {
			b, _ := yaml.Marshal(blk)
			var sc Schedule
			if err := yaml.Unmarshal(b, &sc); err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			if _, err := sc.Spec(); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			withSchedule++
		}
	}
	if withSchedule < 8 {
		t.Errorf("only %d schedules checked", withSchedule)
	}
}

func findSchedules(v any) []any {
	var out []any
	switch x := v.(type) {
	case map[string]any:
		for k, c := range x {
			if k == "schedule" {
				out = append(out, c)
			} else {
				out = append(out, findSchedules(c)...)
			}
		}
	case []any:
		for _, c := range x {
			out = append(out, findSchedules(c)...)
		}
	}
	return out
}
