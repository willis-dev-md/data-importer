package mapping

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Period precision ranks, coarse to fine.
const (
	pYear = iota
	pQuarter
	pMonth
	pDay
)

var precisionNames = []string{"year", "quarter", "month", "day"}

var precisionRank = map[string]int{"year": pYear, "quarter": pQuarter, "month": pMonth, "day": pDay}

var (
	yearRe    = regexp.MustCompile(`^\d{4}$`)
	quarterRe = regexp.MustCompile(`^(\d{4})\s+[Qq]([1-4])$`)
	monthRe   = regexp.MustCompile(`^(\d{4})\s+([A-Za-z]{3})$`)
)

var monthAbbr = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
	"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

var defaultPeriodLayouts = []string{"YYYY", "YYYY Qn", "YYYY MON"}

// period is a parsed period: type name plus start and end dates (UTC midnight).
type period struct {
	typ        string
	start, end time.Time
}

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// bounds returns the first and last day of the unit of the given precision containing (y, m, d).
func bounds(prec, y int, m time.Month, d int) (time.Time, time.Time) {
	switch prec {
	case pYear:
		return date(y, time.January, 1), date(y, time.December, 31)
	case pQuarter:
		qs := time.Month((int(m)-1)/3*3 + 1)
		return date(y, qs, 1), date(y, qs+3, 0)
	case pMonth:
		return date(y, m, 1), date(y, m+1, 0)
	}
	return date(y, m, d), date(y, m, d)
}

func newPeriod(prec int, forced string, y int, m time.Month, d int) period {
	if r, ok := precisionRank[forced]; ok && r < prec {
		prec = r
	}
	s, e := bounds(prec, y, m, d)
	return period{typ: precisionNames[prec], start: s, end: e}
}

// layoutInfo reports which date components a Go layout carries.
func layoutInfo(layout string) (hasYear, hasMonth, hasDay bool) {
	for i := 0; i < len(layout); {
		rest := layout[i:]
		switch {
		case strings.HasPrefix(rest, "January"):
			hasMonth, i = true, i+7
		case strings.HasPrefix(rest, "Jan"):
			hasMonth, i = true, i+3
		case strings.HasPrefix(rest, "Monday"):
			i += 6
		case strings.HasPrefix(rest, "Mon"):
			i += 3
		case strings.HasPrefix(rest, "MST"):
			i += 3
		case strings.HasPrefix(rest, "2006"):
			hasYear, i = true, i+4
		case strings.HasPrefix(rest, "_2006"):
			i++ // literal underscore, then the year
		case strings.HasPrefix(rest, "_2"):
			hasDay, i = true, i+2
		case strings.HasPrefix(rest, "002"):
			hasMonth, hasDay, i = true, true, i+3
		case strings.HasPrefix(rest, "01"):
			hasMonth, i = true, i+2
		case strings.HasPrefix(rest, "02"):
			hasDay, i = true, i+2
		case strings.HasPrefix(rest, "06"):
			hasYear, i = true, i+2
		case strings.HasPrefix(rest, "15"):
			i += 2
		case rest[0] == '1':
			hasMonth, i = true, i+1
		case rest[0] == '2':
			hasDay, i = true, i+1
		default:
			i++
		}
	}
	return
}

// parsePeriodText tries each layout in order; the first match wins.
func parsePeriodText(s string, formats []string, forced string) (period, *fieldErr) {
	s = strings.TrimSpace(s)
	layouts := formats
	if len(layouts) == 0 {
		layouts = defaultPeriodLayouts
	}
	for _, l := range layouts {
		if p, ok := tryLayout(s, l, forced); ok {
			return p, nil
		}
	}
	return period{}, ferr(ErrParseFailed, "period %q does not match layout %s", s, strings.Join(layouts, " | "))
}

func tryLayout(s, layout, forced string) (period, bool) {
	switch layout {
	case "YYYY":
		if !yearRe.MatchString(s) {
			return period{}, false
		}
		y, _ := strconv.Atoi(s)
		return newPeriod(pYear, forced, y, time.January, 1), true
	case "tax_year_ending":
		if !yearRe.MatchString(s) {
			return period{}, false
		}
		y, _ := strconv.Atoi(s)
		return period{typ: "tax_year", start: date(y-1, time.April, 6), end: date(y, time.April, 5)}, true
	case "YYYY Qn":
		m := quarterRe.FindStringSubmatch(s)
		if m == nil {
			return period{}, false
		}
		y, _ := strconv.Atoi(m[1])
		q, _ := strconv.Atoi(m[2])
		return newPeriod(pQuarter, forced, y, time.Month((q-1)*3+1), 1), true
	case "YYYY MON":
		m := monthRe.FindStringSubmatch(s)
		if m == nil {
			return period{}, false
		}
		mon, ok := monthAbbr[strings.ToLower(m[2])]
		if !ok {
			return period{}, false
		}
		y, _ := strconv.Atoi(m[1])
		return newPeriod(pMonth, forced, y, mon, 1), true
	}
	hasYear, hasMonth, hasDay := layoutInfo(layout)
	if !hasYear {
		return period{}, false
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return period{}, false
	}
	prec := pYear
	switch {
	case hasDay:
		prec = pDay
	case hasMonth:
		prec = pMonth
	}
	return newPeriod(prec, forced, t.Year(), t.Month(), t.Day()), true
}

// toPeriod parses v and returns the output the field selects (period_type, period_start or period_end).
func toPeriod(f *Field, name string, v any) (any, *fieldErr) {
	output := f.Output
	if output == "" {
		output = name
	}
	if output != "period_type" && output != "period_start" && output != "period_end" {
		return nil, ferr(ErrTypeMismatch, "period field needs output period_type, period_start or period_end")
	}
	var p period
	if t, ok := v.(time.Time); ok {
		p = newPeriod(pDay, f.Precision, t.Year(), t.Month(), t.Day())
	} else {
		s, ok := scalarString(v)
		if !ok {
			return nil, ferr(ErrTypeMismatch, "expected a period label, got %T", v)
		}
		var fe *fieldErr
		if p, fe = parsePeriodText(s, f.Format, f.Precision); fe != nil {
			return nil, fe
		}
	}
	switch output {
	case "period_type":
		return p.typ, nil
	case "period_start":
		return p.start, nil
	}
	return p.end, nil
}
