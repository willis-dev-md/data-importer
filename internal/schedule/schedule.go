package schedule

import (
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the zone database travels with the binary, so a minimal image still resolves IANA names
)

// Cadences and policies.
const (
	Daily   = "daily"
	Weekly  = "weekly"
	Monthly = "monthly"

	RunOnce = "run_once"
	Skip    = "skip"

	DefaultTimezone = "Europe/London"
	DefaultAt       = "00:00"

	// LastDay is the DayOfMonth value meaning the last day of the month.
	LastDay = -1

	// MaxJitter is the largest allowed jitter.
	MaxJitter = time.Hour

	// SkipGrace: with on_missed = skip, the latest slot still runs when it is at most this old (it is on time,
	// not missed); older slots are skipped.
	SkipGrace = 5 * time.Minute
)

// Input is a schedule as written in a source file. DayOfMonth is 0 when unset, LastDay for `last`.
type Input struct {
	Every      string
	At         string
	DayOfWeek  string
	DayOfMonth int
	Timezone   string
	Jitter     string
	OnMissed   string
	Cron       string
}

// Spec is a validated, normalised schedule with every default filled in. It is plain data (it is stored as
// sources.schedule), and comparable with ==. Build it with New.
type Spec struct {
	Every      string `json:"every"`
	At         string `json:"at"`
	DayOfWeek  string `json:"day_of_week,omitempty"`
	DayOfMonth int    `json:"day_of_month,omitempty"` // 1-28 or LastDay
	Timezone   string `json:"timezone"`
	OnMissed   string `json:"on_missed"`
	Jitter     string `json:"jitter"` // Go duration, 0s..1h
}

var (
	atRE     = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)
	weekdays = map[string]time.Weekday{
		"sunday": time.Sunday, "monday": time.Monday, "tuesday": time.Tuesday, "wednesday": time.Wednesday,
		"thursday": time.Thursday, "friday": time.Friday, "saturday": time.Saturday,
	}
	locs sync.Map // name -> *time.Location
)

func loadLocation(name string) (*time.Location, error) {
	if l, ok := locs.Load(name); ok {
		return l.(*time.Location), nil
	}
	if name == "Local" || name == "" {
		return nil, fmt.Errorf("unknown time zone %q", name)
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q (use an IANA name such as Europe/London)", name)
	}
	locs.Store(name, l)
	return l, nil
}

// New validates in and returns the normalised Spec. Every error names its field ("schedule.<field>: ...") and
// all errors are returned together.
func New(in Input) (Spec, error) {
	var errs []error
	bad := func(field, format string, args ...any) {
		errs = append(errs, fmt.Errorf("schedule.%s: %s", field, fmt.Sprintf(format, args...)))
	}
	sp := Spec{
		Every: strings.ToLower(strings.TrimSpace(in.Every)), At: strings.TrimSpace(in.At),
		Timezone: strings.TrimSpace(in.Timezone), OnMissed: strings.ToLower(strings.TrimSpace(in.OnMissed)),
	}

	switch {
	case strings.TrimSpace(in.Cron) != "":
		bad("cron", "not supported yet (use every: daily, weekly or monthly)")
	case sp.Every == "":
		bad("every", "required (daily, weekly or monthly)")
	case sp.Every != Daily && sp.Every != Weekly && sp.Every != Monthly:
		bad("every", "must be daily, weekly or monthly, got %q", in.Every)
	}

	if sp.At == "" {
		sp.At = DefaultAt
	} else if !atRE.MatchString(sp.At) {
		bad("at", "must be HH:MM in 24-hour time (for example 06:30), got %q", in.At)
	}

	dow := strings.ToLower(strings.TrimSpace(in.DayOfWeek))
	switch {
	case sp.Every == Weekly && dow == "":
		bad("day_of_week", "required when every is weekly (monday..sunday)")
	case dow != "" && sp.Every != Weekly && sp.Every != "":
		bad("day_of_week", "only valid when every is weekly")
	case dow != "":
		if _, ok := weekdays[dow]; !ok {
			bad("day_of_week", "must be monday..sunday, got %q", in.DayOfWeek)
		} else if sp.Every == Weekly {
			sp.DayOfWeek = dow
		}
	}

	dom := in.DayOfMonth
	switch {
	case sp.Every == Monthly && dom == 0:
		bad("day_of_month", "required when every is monthly (1-28 or last)")
	case dom != 0 && sp.Every != Monthly && sp.Every != "":
		bad("day_of_month", "only valid when every is monthly")
	case dom != 0 && dom != LastDay && (dom < 1 || dom > 28):
		bad("day_of_month", "must be 1-28 or last, got %d (use last for the end of the month)", dom)
	case sp.Every == Monthly:
		sp.DayOfMonth = dom
	}

	if sp.Timezone == "" {
		sp.Timezone = DefaultTimezone
	}
	if _, err := loadLocation(sp.Timezone); err != nil {
		bad("timezone", "%v", err)
	}

	switch sp.OnMissed {
	case "":
		sp.OnMissed = RunOnce
	case RunOnce, Skip:
	default:
		bad("on_missed", "must be run_once or skip, got %q", in.OnMissed)
	}

	sp.Jitter = "0s"
	if j := strings.TrimSpace(in.Jitter); j != "" {
		d, err := time.ParseDuration(j)
		switch {
		case err != nil:
			bad("jitter", "not a duration (for example 5m): %q", in.Jitter)
		case d < 0 || d > MaxJitter:
			bad("jitter", "must be between 0 and %s, got %s", MaxJitter, d)
		default:
			sp.Jitter = d.String()
		}
	}

	if len(errs) > 0 {
		return Spec{}, errors.Join(errs...)
	}
	return sp, nil
}

// JitterDuration returns the jitter (0 if unset or invalid).
func (s Spec) JitterDuration() time.Duration {
	d, _ := time.ParseDuration(s.Jitter)
	return d
}

type compiled struct {
	spec    Spec
	loc     *time.Location
	hour    int
	minute  int
	weekday time.Weekday
}

func compile(s Spec) (compiled, error) {
	n, err := New(Input{Every: s.Every, At: s.At, DayOfWeek: s.DayOfWeek, DayOfMonth: s.DayOfMonth,
		Timezone: s.Timezone, Jitter: s.Jitter, OnMissed: s.OnMissed})
	if err != nil {
		return compiled{}, fmt.Errorf("invalid schedule: %w", err)
	}
	c := compiled{spec: n, weekday: weekdays[n.DayOfWeek]}
	c.loc, _ = loadLocation(n.Timezone)
	m := atRE.FindStringSubmatch(n.At)
	c.hour, _ = strconv.Atoi(m[1])
	c.minute, _ = strconv.Atoi(m[2])
	return c, nil
}

// matches reports whether the civil date has a slot.
func (c compiled) matches(y int, m time.Month, d int) bool {
	switch c.spec.Every {
	case Weekly:
		return time.Date(y, m, d, 12, 0, 0, 0, time.UTC).Weekday() == c.weekday
	case Monthly:
		if c.spec.DayOfMonth == LastDay {
			return time.Date(y, m, d+1, 12, 0, 0, 0, time.UTC).Day() == 1
		}
		return d == c.spec.DayOfMonth
	}
	return true
}

func offsetAt(l *time.Location, t time.Time) int { _, o := t.In(l).Zone(); return o }

// slot resolves a civil date and the configured wall time to one instant. A wall time that occurs twice (clocks
// going back) resolves to its first occurrence; one that does not exist (clocks going forward) resolves to the
// transition instant, the first valid instant of that day after the gap.
func (c compiled) slot(y int, m time.Month, d int) time.Time {
	wall := time.Date(y, m, d, c.hour, c.minute, 0, 0, time.UTC) // the wall clock written as if it were UTC
	before := offsetAt(c.loc, wall.Add(-24*time.Hour))
	after := offsetAt(c.loc, wall.Add(24*time.Hour))
	var best time.Time
	for _, o := range []int{before, offsetAt(c.loc, wall), after} {
		cand := wall.Add(-time.Duration(o) * time.Second)
		if offsetAt(c.loc, cand) == o && (best.IsZero() || cand.Before(best)) {
			best = cand
		}
	}
	if !best.IsZero() {
		return best
	}
	// In a gap: find the first second whose offset is no longer the one in force before the transition.
	lo := wall.Add(-time.Duration(after) * time.Second).Unix()  // still before the transition
	hi := wall.Add(-time.Duration(before) * time.Second).Unix() // at or after it
	for hi-lo > 1 {
		mid := lo + (hi-lo)/2
		if offsetAt(c.loc, time.Unix(mid, 0)) == before {
			lo = mid
		} else {
			hi = mid
		}
	}
	return time.Unix(hi, 0).UTC()
}

// Next returns the first slot strictly after the given instant, in UTC.
func Next(spec Spec, after time.Time) (time.Time, error) {
	c, err := compile(spec)
	if err != nil {
		return time.Time{}, err
	}
	y, m, d := after.In(c.loc).Date()
	for i := -1; i <= 62; i++ {
		cd := time.Date(y, m, d+i, 12, 0, 0, 0, time.UTC)
		cy, cm, cdd := cd.Date()
		if !c.matches(cy, cm, cdd) {
			continue
		}
		if t := c.slot(cy, cm, cdd); t.After(after) {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("schedule: no slot found within 62 days")
}

// Result is the outcome of Plan.
type Result struct {
	Due    []time.Time // slots to run now (at most one), oldest first
	Missed int         // slots that passed without a run and are not in Due
	Next   time.Time   // the first slot after now
}

const maxMissed = 100000

// Plan decides what to do at now, given the last slot that was scheduled (the slot, not when its run finished;
// the zero time means there is no history, so nothing is due). Slots in (lastScheduled, now] are due. After
// downtime on_missed = run_once yields ONE catch-up slot (the latest); skip yields none, unless the latest slot is
// at most SkipGrace old (on time, not missed). Next is always computed from the slots, never from a finish time,
// so cadence does not drift.
func Plan(spec Spec, lastScheduled, now time.Time) (Result, error) {
	var res Result
	var latest time.Time
	count := 0
	if !lastScheduled.IsZero() {
		cur := lastScheduled
		for {
			n, err := Next(spec, cur)
			if err != nil {
				return Result{}, err
			}
			if n.After(now) {
				res.Next = n
				break
			}
			latest, cur = n, n
			if count++; count > maxMissed {
				return Result{}, fmt.Errorf("schedule: more than %d slots missed since %s", maxMissed, lastScheduled.UTC().Format(time.RFC3339))
			}
		}
	} else {
		n, err := Next(spec, now)
		if err != nil {
			return Result{}, err
		}
		res.Next = n
	}
	if count == 0 {
		return res, nil
	}
	if spec.OnMissed == Skip && now.Sub(latest) > SkipGrace {
		res.Missed = count
		return res, nil
	}
	res.Due = []time.Time{latest}
	res.Missed = count - 1
	return res, nil
}

// Offset is the deterministic jitter for a source: FNV-1a of the id, modulo the jitter, in [0, jitter). It is
// stable across processes and releases. Zero (or negative) jitter gives 0.
func Offset(sourceID string, jitter time.Duration) time.Duration {
	if jitter <= 0 {
		return 0
	}
	h := fnv.New64a()
	h.Write([]byte(sourceID))
	return time.Duration(h.Sum64() % uint64(jitter))
}
