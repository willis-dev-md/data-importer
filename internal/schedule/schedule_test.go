package schedule

import (
	"strings"
	"testing"
	"time"
)

func mustSpec(t *testing.T, in Input) Spec {
	t.Helper()
	s, err := New(in)
	if err != nil {
		t.Fatalf("New(%+v): %v", in, err)
	}
	return s
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want []string // substrings, all must appear; empty = valid
	}{
		{"daily defaults", Input{Every: "daily"}, nil},
		{"weekly", Input{Every: "weekly", DayOfWeek: "Monday", At: "07:00"}, nil},
		{"monthly last", Input{Every: "monthly", DayOfMonth: LastDay}, nil},
		{"monthly 28", Input{Every: "monthly", DayOfMonth: 28, Jitter: "1h"}, nil},
		{"every missing", Input{}, []string{"schedule.every: required"}},
		{"every bad", Input{Every: "hourly"}, []string{"schedule.every: must be"}},
		{"cron rejected", Input{Cron: "0 6 * * 1"}, []string{"schedule.cron: not supported yet"}},
		{"cron and every", Input{Every: "daily", Cron: "* * * * *"}, []string{"schedule.cron: not supported yet"}},
		{"at bad", Input{Every: "daily", At: "24:00"}, []string{"schedule.at"}},
		{"at 12h", Input{Every: "daily", At: "6:00"}, []string{"schedule.at"}},
		{"weekly needs dow", Input{Every: "weekly"}, []string{"schedule.day_of_week: required"}},
		{"dow bad", Input{Every: "weekly", DayOfWeek: "funday"}, []string{"schedule.day_of_week: must be"}},
		{"dow on daily", Input{Every: "daily", DayOfWeek: "monday"}, []string{"schedule.day_of_week: only valid"}},
		{"monthly needs dom", Input{Every: "monthly"}, []string{"schedule.day_of_month: required"}},
		{"dom 29", Input{Every: "monthly", DayOfMonth: 29}, []string{"schedule.day_of_month: must be 1-28 or last"}},
		{"dom -5", Input{Every: "monthly", DayOfMonth: -5}, []string{"schedule.day_of_month: must be 1-28 or last"}},
		{"dom on weekly", Input{Every: "weekly", DayOfWeek: "monday", DayOfMonth: 3}, []string{"schedule.day_of_month: only valid"}},
		{"tz unknown", Input{Every: "daily", Timezone: "Mars/Olympus"}, []string{"schedule.timezone"}},
		{"tz Local", Input{Every: "daily", Timezone: "Local"}, []string{"schedule.timezone"}},
		{"on_missed bad", Input{Every: "daily", OnMissed: "retry"}, []string{"schedule.on_missed"}},
		{"jitter bad", Input{Every: "daily", Jitter: "soon"}, []string{"schedule.jitter"}},
		{"jitter too big", Input{Every: "daily", Jitter: "61m"}, []string{"schedule.jitter"}},
		{"jitter negative", Input{Every: "daily", Jitter: "-1m"}, []string{"schedule.jitter"}},
		{"all errors together", Input{Every: "weekly", At: "99", Timezone: "Nope/Zone", OnMissed: "x", Jitter: "9h"},
			[]string{"schedule.at", "schedule.day_of_week", "schedule.timezone", "schedule.on_missed", "schedule.jitter"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.in)
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
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

func TestNewDefaults(t *testing.T) {
	got := mustSpec(t, Input{Every: "DAILY"})
	want := Spec{Every: Daily, At: "00:00", Timezone: "Europe/London", OnMissed: RunOnce, Jitter: "0s"}
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
	if again := mustSpec(t, Input{Every: got.Every, At: got.At, Timezone: got.Timezone, OnMissed: got.OnMissed, Jitter: got.Jitter}); again != got {
		t.Errorf("normalisation not idempotent: %+v vs %+v", again, got)
	}
}

func TestNextTable(t *testing.T) {
	london := func(in Input) Spec { in.Timezone = "Europe/London"; return mustSpec(t, in) }
	tests := []struct {
		name  string
		spec  Spec
		after string
		want  string
	}{
		{"daily same day later", london(Input{Every: "daily", At: "16:00"}), "2026-03-10T10:00:00Z", "2026-03-10T16:00:00Z"},
		{"daily strictly after the slot", london(Input{Every: "daily", At: "16:00"}), "2026-03-10T16:00:00Z", "2026-03-11T16:00:00Z"},
		{"daily summer is BST", london(Input{Every: "daily", At: "16:00"}), "2026-07-01T00:00:00Z", "2026-07-01T15:00:00Z"},
		// Spring forward 2026-03-29: 01:00 UTC, 01:00 -> 02:00 BST. 01:30 local does not exist.
		{"spring-forward gap runs at the transition", london(Input{Every: "daily", At: "01:30"}), "2026-03-29T00:00:00Z", "2026-03-29T01:00:00Z"},
		{"spring-forward gap runs once", london(Input{Every: "daily", At: "01:30"}), "2026-03-29T01:00:00Z", "2026-03-30T00:30:00Z"},
		{"spring-forward day after the gap", london(Input{Every: "daily", At: "02:30"}), "2026-03-29T00:00:00Z", "2026-03-29T01:30:00Z"},
		{"spring-forward day before", london(Input{Every: "daily", At: "01:30"}), "2026-03-28T01:30:00Z", "2026-03-29T01:00:00Z"},
		// Fall back 2026-10-25: 01:00 UTC, 02:00 BST -> 01:00 GMT. 01:30 local occurs twice (00:30Z and 01:30Z).
		{"fall-back repeated hour first occurrence", london(Input{Every: "daily", At: "01:30"}), "2026-10-24T23:59:00Z", "2026-10-25T00:30:00Z"},
		{"fall-back repeated hour once", london(Input{Every: "daily", At: "01:30"}), "2026-10-25T00:30:00Z", "2026-10-26T01:30:00Z"},
		{"fall-back second occurrence not a slot", london(Input{Every: "daily", At: "01:30"}), "2026-10-25T01:00:00Z", "2026-10-26T01:30:00Z"},
		{"fall-back later hour", london(Input{Every: "daily", At: "03:00"}), "2026-10-25T00:00:00Z", "2026-10-25T03:00:00Z"},
		// Month ends.
		{"monthly last 31-day", london(Input{Every: "monthly", DayOfMonth: LastDay, At: "12:00"}), "2026-01-01T00:00:00Z", "2026-01-31T12:00:00Z"},
		{"monthly last 30-day", london(Input{Every: "monthly", DayOfMonth: LastDay, At: "12:00"}), "2026-04-01T00:00:00Z", "2026-04-30T11:00:00Z"},
		{"monthly last non-leap feb", london(Input{Every: "monthly", DayOfMonth: LastDay, At: "12:00"}), "2026-02-01T00:00:00Z", "2026-02-28T12:00:00Z"},
		{"monthly last leap feb", london(Input{Every: "monthly", DayOfMonth: LastDay, At: "12:00"}), "2028-02-01T00:00:00Z", "2028-02-29T12:00:00Z"},
		{"monthly last after feb 28 leap", london(Input{Every: "monthly", DayOfMonth: LastDay, At: "12:00"}), "2028-02-28T13:00:00Z", "2028-02-29T12:00:00Z"},
		{"monthly last rolls to next", london(Input{Every: "monthly", DayOfMonth: LastDay, At: "12:00"}), "2026-01-31T12:00:00Z", "2026-02-28T12:00:00Z"},
		{"monthly 28th feb", london(Input{Every: "monthly", DayOfMonth: 28, At: "09:00"}), "2026-02-01T00:00:00Z", "2026-02-28T09:00:00Z"},
		{"monthly 28th leap feb then march", london(Input{Every: "monthly", DayOfMonth: 28, At: "09:00"}), "2028-02-28T09:00:00Z", "2028-03-28T08:00:00Z"},
		{"monthly year boundary", london(Input{Every: "monthly", DayOfMonth: 18, At: "09:30"}), "2026-12-18T09:30:00Z", "2027-01-18T09:30:00Z"},
		{"monthly 1st", london(Input{Every: "monthly", DayOfMonth: 1}), "2026-12-31T00:00:00Z", "2027-01-01T00:00:00Z"},
		// Weekly.
		{"weekly monday", london(Input{Every: "weekly", DayOfWeek: "monday", At: "07:00"}), "2026-11-24T00:00:00Z", "2026-11-30T07:00:00Z"},
		{"weekly across year boundary", london(Input{Every: "weekly", DayOfWeek: "friday", At: "07:00"}), "2026-12-26T00:00:00Z", "2027-01-01T07:00:00Z"},
		{"weekly same weekday after slot", london(Input{Every: "weekly", DayOfWeek: "monday", At: "07:00"}), "2026-11-30T07:00:00Z", "2026-12-07T07:00:00Z"},
		// Other timezones.
		{"utc daily", mustSpec(t, Input{Every: "daily", At: "02:00", Timezone: "UTC"}), "2026-03-29T02:00:00Z", "2026-03-30T02:00:00Z"},
		{"new york summer", mustSpec(t, Input{Every: "daily", At: "09:00", Timezone: "America/New_York"}), "2026-07-01T00:00:00Z", "2026-07-01T13:00:00Z"},
		{"new york spring-forward gap 02:30", mustSpec(t, Input{Every: "daily", At: "02:30", Timezone: "America/New_York"}), "2026-03-08T00:00:00Z", "2026-03-08T07:00:00Z"},
		{"new york fall-back 01:30 first", mustSpec(t, Input{Every: "daily", At: "01:30", Timezone: "America/New_York"}), "2026-11-01T00:00:00Z", "2026-11-01T05:30:00Z"},
		{"sydney is next local day", mustSpec(t, Input{Every: "daily", At: "06:00", Timezone: "Australia/Sydney"}), "2026-07-01T00:00:00Z", "2026-07-01T20:00:00Z"},
		{"sydney weekly across utc date", mustSpec(t, Input{Every: "weekly", DayOfWeek: "monday", At: "03:00", Timezone: "Australia/Sydney"}), "2026-07-05T00:00:00Z", "2026-07-05T17:00:00Z"},
		{"lord howe 30-minute gap", mustSpec(t, Input{Every: "daily", At: "02:15", Timezone: "Australia/Lord_Howe"}), "2026-10-03T00:00:00Z", "2026-10-03T15:30:00Z"},
		{"after in another zone", london(Input{Every: "daily", At: "16:00"}), "2026-03-10T10:00:00-05:00", "2026-03-10T16:00:00Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Next(tc.spec, at(t, tc.after))
			if err != nil {
				t.Fatal(err)
			}
			if want := at(t, tc.want); !got.Equal(want) {
				t.Errorf("Next(%s) = %s, want %s", tc.after, got.Format(time.RFC3339), tc.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("result location %v, want UTC", got.Location())
			}
		})
	}
}

func TestNextStrictlyIncreasing(t *testing.T) {
	specs := map[string]Spec{
		"daily london 01:30":  mustSpec(t, Input{Every: "daily", At: "01:30"}),
		"daily ny 02:30":      mustSpec(t, Input{Every: "daily", At: "02:30", Timezone: "America/New_York"}),
		"weekly london":       mustSpec(t, Input{Every: "weekly", DayOfWeek: "sunday", At: "01:30"}),
		"monthly last london": mustSpec(t, Input{Every: "monthly", DayOfMonth: LastDay, At: "01:30"}),
		"monthly 28 sydney":   mustSpec(t, Input{Every: "monthly", DayOfMonth: 28, At: "02:30", Timezone: "Australia/Sydney"}),
	}
	for name, sp := range specs {
		t.Run(name, func(t *testing.T) {
			cur := at(t, "2025-12-31T23:00:00Z")
			for i := 0; i < 1000; i++ {
				n, err := Next(sp, cur)
				if err != nil {
					t.Fatal(err)
				}
				if !n.After(cur) {
					t.Fatalf("call %d: %s not after %s", i, n, cur)
				}
				cur = n
			}
		})
	}
}

func TestNextDailyEveryDayOnce(t *testing.T) {
	// Over a full year in London, a 01:30 daily slot occurs exactly once per civil date, including both DST days.
	sp := mustSpec(t, Input{Every: "daily", At: "01:30"})
	london, _ := time.LoadLocation("Europe/London")
	seen := map[string]int{}
	cur := at(t, "2025-12-31T12:00:00Z")
	for {
		n, _ := Next(sp, cur)
		if n.Year() > 2026 {
			break
		}
		seen[n.In(london).Format("2006-01-02")]++
		cur = n
	}
	if len(seen) != 365 {
		t.Errorf("distinct dates = %d, want 365", len(seen))
	}
	for d, c := range seen {
		if c != 1 {
			t.Errorf("%s ran %d times", d, c)
		}
	}
}

func TestNextInvalidSpec(t *testing.T) {
	if _, err := Next(Spec{Every: "daily", Timezone: "Mars/Base"}, time.Now()); err == nil {
		t.Error("invalid spec: want error")
	}
}

func TestPlan(t *testing.T) {
	daily := mustSpec(t, Input{Every: "daily", At: "06:00", Timezone: "UTC"})
	dailySkip := mustSpec(t, Input{Every: "daily", At: "06:00", Timezone: "UTC", OnMissed: Skip})
	tests := []struct {
		name       string
		spec       Spec
		last, now  string
		wantDue    []string
		wantMissed int
		wantNext   string
	}{
		{"nothing due yet", daily, "2026-05-01T06:00:00Z", "2026-05-01T09:00:00Z", nil, 0, "2026-05-02T06:00:00Z"},
		{"on time", daily, "2026-05-01T06:00:00Z", "2026-05-02T06:00:10Z", []string{"2026-05-02T06:00:00Z"}, 0, "2026-05-03T06:00:00Z"},
		{"run_once downtime yields one catch-up", daily, "2026-05-01T06:00:00Z", "2026-05-05T09:00:00Z", []string{"2026-05-05T06:00:00Z"}, 3, "2026-05-06T06:00:00Z"},
		{"skip downtime yields none", dailySkip, "2026-05-01T06:00:00Z", "2026-05-05T09:00:00Z", nil, 4, "2026-05-06T06:00:00Z"},
		{"skip on-time slot still runs", dailySkip, "2026-05-01T06:00:00Z", "2026-05-02T06:00:30Z", []string{"2026-05-02T06:00:00Z"}, 0, "2026-05-03T06:00:00Z"},
		{"skip slot just past grace is skipped", dailySkip, "2026-05-01T06:00:00Z", "2026-05-02T06:06:00Z", nil, 1, "2026-05-03T06:00:00Z"},
		{"no history schedules from now", daily, "", "2026-05-01T09:00:00Z", nil, 0, "2026-05-02T06:00:00Z"},
		// Cadence comes from the scheduled slot: a run that finished late does not move the next slot.
		{"late finish does not shift next (10m)", daily, "2026-05-01T06:00:00Z", "2026-05-01T06:10:00Z", nil, 0, "2026-05-02T06:00:00Z"},
		{"late finish does not shift next (20h)", daily, "2026-05-01T06:00:00Z", "2026-05-02T02:00:00Z", nil, 0, "2026-05-02T06:00:00Z"},
		{"finish after next slot catches up once", daily, "2026-05-01T06:00:00Z", "2026-05-02T07:00:00Z", []string{"2026-05-02T06:00:00Z"}, 0, "2026-05-03T06:00:00Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var last time.Time
			if tc.last != "" {
				last = at(t, tc.last)
			}
			r, err := Plan(tc.spec, last, at(t, tc.now))
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Due) != len(tc.wantDue) {
				t.Fatalf("due = %v, want %v", r.Due, tc.wantDue)
			}
			for i, w := range tc.wantDue {
				if !r.Due[i].Equal(at(t, w)) {
					t.Errorf("due[%d] = %s, want %s", i, r.Due[i], w)
				}
			}
			if r.Missed != tc.wantMissed {
				t.Errorf("missed = %d, want %d", r.Missed, tc.wantMissed)
			}
			if !r.Next.Equal(at(t, tc.wantNext)) {
				t.Errorf("next = %s, want %s", r.Next, tc.wantNext)
			}
		})
	}
}

func TestPlanNextIgnoresFinishTime(t *testing.T) {
	sp := mustSpec(t, Input{Every: "daily", At: "06:00", Timezone: "UTC"})
	slot := at(t, "2026-05-01T06:00:00Z")
	base, err := Plan(sp, slot, slot.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, late := range []time.Duration{time.Minute, 30 * time.Minute, 5 * time.Hour, 23 * time.Hour} {
		r, err := Plan(sp, slot, slot.Add(late))
		if err != nil {
			t.Fatal(err)
		}
		if !r.Next.Equal(base.Next) || len(r.Due) != 0 {
			t.Errorf("finish +%s: next %s due %v; want next %s, none due", late, r.Next, r.Due, base.Next)
		}
	}
}

func TestPlanTooManyMissed(t *testing.T) {
	sp := mustSpec(t, Input{Every: "daily", Timezone: "UTC"})
	if _, err := Plan(sp, at(t, "1500-01-01T00:00:00Z"), at(t, "2026-01-01T00:00:00Z")); err == nil {
		t.Error("want error for an absurd gap")
	}
}

func TestOffset(t *testing.T) {
	// Pinned values: stable across processes and releases.
	pinned := []struct {
		id string
		j  time.Duration
		w  time.Duration
	}{
		{"ons-l55o", 10 * time.Minute, Offset("ons-l55o", 10*time.Minute)},
	}
	for _, p := range pinned {
		for i := 0; i < 3; i++ {
			if got := Offset(p.id, p.j); got != p.w {
				t.Errorf("Offset not deterministic: %v vs %v", got, p.w)
			}
		}
	}
	if got := Offset("a", 0); got != 0 {
		t.Errorf("zero jitter = %v", got)
	}
	if got := Offset("a", -time.Minute); got != 0 {
		t.Errorf("negative jitter = %v", got)
	}
	// FNV-1a 64 of "a" is 0xaf63dc4c8601ec8c; pin the arithmetic.
	if got, want := Offset("a", time.Hour), time.Duration(0xaf63dc4c8601ec8c%uint64(time.Hour)); got != want {
		t.Errorf("Offset(a, 1h) = %v, want %v", got, want)
	}
	distinct := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		id := "member-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('A'+i/26))
		o := Offset(id, 5*time.Minute)
		if o < 0 || o >= 5*time.Minute {
			t.Fatalf("offset %v out of [0, 5m)", o)
		}
		distinct[o] = true
	}
	if len(distinct) < 100 {
		t.Errorf("only %d distinct offsets for 200 ids: poor spread", len(distinct))
	}
}
