package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ArchiveZip is the only supported archive type.
const ArchiveZip = "zip"

// Archive says the fetched file is a zip holding the real file (see docs/mapping-spec.md, "Archives").
type Archive struct {
	Type   string `yaml:"type"`   // must be "zip"
	Member string `yaml:"member"` // regexp; exactly one member name must match
}

// ResolveMonth is the only supported resolve kind.
const ResolveMonth = "month"

// Resolve limits (config errors beyond them).
const (
	MaxResolveBack  = 120  // candidates per placeholder
	MaxResolveStart = 1200 // months back from now that start may point to
)

// Resolve describes how the value of one `{name}` placeholder of the location is found (see docs/mapping-spec.md,
// "Location resolution").
type Resolve struct {
	Kind   string `yaml:"kind"`   // "month"
	Layout string `yaml:"layout"` // Go reference-time layout of the month, e.g. "2006-01"
	Start  string `yaml:"start"`  // offset of the newest candidate from the current month, e.g. "-1m" (0m = this month)
	Back   int    `yaml:"back"`   // number of candidates, newest first (1..MaxResolveBack)
}

var (
	resolveNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	placeholderRe  = regexp.MustCompile(`\{[^{}]*\}`)
	resolveStartRe = regexp.MustCompile(`^(0|-[1-9][0-9]{0,3})m$`)
	layoutSafeRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// StartMonths returns Start as a number of months (<= 0). ok is false for an invalid value.
func (r Resolve) StartMonths() (n int, ok bool) {
	if !resolveStartRe.MatchString(r.Start) {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(r.Start, "m"))
	if err != nil || n < -MaxResolveStart {
		return 0, false
	}
	return n, true
}

// FormatMonth renders the month of t (UTC) with Layout.
func (r Resolve) FormatMonth(t time.Time) string { return t.UTC().Format(r.Layout) }

// checkLayout reports why layout cannot identify a month in a URL, or "".
func checkLayout(layout string) string {
	if layout == "" {
		return "required"
	}
	if len(layout) > 32 {
		return "too long"
	}
	var seen = map[string]time.Month{}
	for m := time.January; m <= time.December; m++ {
		t := time.Date(2026, m, 1, 0, 0, 0, 0, time.UTC)
		s := t.Format(layout)
		if !layoutSafeRe.MatchString(s) {
			return fmt.Sprintf("%q renders %q: only letters, digits and . _ - are allowed in a URL part", layout, s)
		}
		if _, dup := seen[s]; dup {
			return fmt.Sprintf("%q does not tell the months apart (needs a month such as 01 or Jan)", layout)
		}
		seen[s] = m
	}
	a := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	b := time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC)
	if a.Format(layout) == b.Format(layout) {
		return fmt.Sprintf("%q does not contain the year (use 2006)", layout)
	}
	return ""
}

func (s *Source) validateArchiveAndResolve(bad func(path, format string, args ...any)) {
	if a := s.Archive; a != nil {
		switch {
		case a.Type == "":
			bad("archive.type", "required (supported: zip)")
		case a.Type != ArchiveZip:
			bad("archive.type", "unsupported archive type %q (supported: zip)", a.Type)
		}
		if strings.TrimSpace(a.Member) == "" {
			bad("archive.member", "required: a regular expression matching exactly one member name")
		} else if _, err := regexp.Compile(a.Member); err != nil {
			bad("archive.member", "invalid regular expression: %v", err)
		}
	}

	used := map[string]bool{}
	if len(s.Resolve) > 1 {
		bad("resolve", "at most one placeholder can be resolved, got %d", len(s.Resolve))
	}
	for name, r := range s.Resolve {
		p := "resolve." + name
		if !resolveNameRe.MatchString(name) {
			bad(p, "invalid placeholder name (lower-case letters, digits and _, starting with a letter)")
		}
		switch r.Kind {
		case ResolveMonth:
		case "":
			bad(p+".kind", "required (supported: month)")
		default:
			bad(p+".kind", "unsupported kind %q (supported: month)", r.Kind)
		}
		if msg := checkLayout(r.Layout); msg != "" {
			bad(p+".layout", "%s", msg)
		}
		if _, ok := r.StartMonths(); !ok {
			bad(p+".start", "must be 0m or a negative number of months such as -1m, got %q", r.Start)
		}
		if r.Back < 1 || r.Back > MaxResolveBack {
			bad(p+".back", "must be between 1 and %d, got %d", MaxResolveBack, r.Back)
		}
		if !strings.Contains(s.Location, "{"+name+"}") {
			bad(p, "the location has no {%s} placeholder", name)
		}
		used[name] = true
	}
	// Placeholders left after group templating and resolve are mistakes, not text.
	seen := map[string]bool{}
	for _, ph := range placeholderRe.FindAllString(s.Location, -1) {
		if name := ph[1 : len(ph)-1]; !used[name] && !seen[ph] {
			seen[ph] = true
			bad("location", "unresolved placeholder %s (no group template value and no resolve entry)", ph)
		}
	}
	if s.Archive != nil && s.Kind != "" && s.Kind != KindCSV && s.Kind != KindXLSX {
		bad("archive", "only valid for kind csv or xlsx")
	}
}
