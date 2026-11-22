package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Source kinds.
const KindCSV = "csv"

// Missing policies.
const (
	MissingKeep      = "keep"
	MissingTombstone = "tombstone"
)

// Source is the setup of one data source (see docs/mapping-spec.md).
type Source struct {
	ID              string             `yaml:"id"`
	Kind            string             `yaml:"kind"`
	Location        string             `yaml:"location"`
	Archive         *Archive           `yaml:"archive,omitempty"`
	Resolve         map[string]Resolve `yaml:"resolve,omitempty"`
	CSV             *CSV               `yaml:"csv,omitempty"`
	XLSX            *XLSX              `yaml:"xlsx,omitempty"`
	Table           Table              `yaml:"table,omitempty"`
	Context         map[string]string  `yaml:"context,omitempty"`
	NaturalKey      []string           `yaml:"natural_key"`
	MissingPolicy   string             `yaml:"missing_policy,omitempty"`
	MaxErrorRatio   float64            `yaml:"max_error_ratio"`
	Schedule        *Schedule          `yaml:"schedule,omitempty"`
	ChangeDetection []string           `yaml:"change_detection,omitempty"`
	Enabled         *bool              `yaml:"enabled,omitempty"`
	Auth            *Auth              `yaml:"auth,omitempty"`
}

// CSV holds the options of a csv source.
type CSV struct {
	Delimiter string    `yaml:"delimiter,omitempty"` // one character; default ","
	Header    *bool     `yaml:"header,omitempty"`    // default true
	Encoding  string    `yaml:"encoding,omitempty"`  // default utf-8
	Columns   []string  `yaml:"columns,omitempty"`
	Preamble  *Preamble `yaml:"preamble,omitempty"`
}

// Preamble describes metadata lines above the data. Exactly one of Lines or Until is set.
type Preamble struct {
	Lines int    `yaml:"lines,omitempty"` // number of physical-row preamble lines
	Until string `yaml:"until,omitempty"` // regexp matching the first data row; the preamble ends before it
	As    string `yaml:"as"`              // must be "metadata"
}

// MetaPrefix introduces a natural-key reference to preamble metadata, e.g. "@meta.CDID".
const MetaPrefix = "@meta."

// Schedule is carried through for the scheduler; it is not interpreted here.
type Schedule struct {
	Every      string `yaml:"every,omitempty"`
	DayOfWeek  string `yaml:"day_of_week,omitempty"`
	DayOfMonth int    `yaml:"day_of_month,omitempty"`
	At         string `yaml:"at,omitempty"`
	Timezone   string `yaml:"timezone,omitempty"`
	Jitter     string `yaml:"jitter,omitempty"`
	OnMissed   string `yaml:"on_missed,omitempty"`
}

// HasHeader reports whether the csv has a header row (default true).
func (c CSV) HasHeader() bool { return c.Header == nil || *c.Header }

// DelimiterRune returns the delimiter, default ','. "\t" and "tab" mean a tab.
func (c CSV) DelimiterRune() rune {
	switch c.Delimiter {
	case "", ",":
		return ','
	case "tab", "\\t":
		return '\t'
	}
	r, _ := utf8.DecodeRuneInString(c.Delimiter)
	return r
}

// EncodingName returns the normalised encoding name, default "utf-8".
func (c CSV) EncodingName() string {
	if c.Encoding == "" {
		return "utf-8"
	}
	return strings.ToLower(strings.TrimSpace(c.Encoding))
}

// SupportedEncodings lists the csv encodings the reader understands.
var SupportedEncodings = []string{"utf-8", "windows-1252", "utf-16", "utf-16le", "utf-16be"}

// ParseSource decodes and validates a source config. Unknown keys are rejected.
func ParseSource(data []byte) (*Source, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var s Source
	if err := dec.Decode(&s); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("source config: empty document")
		}
		return nil, fmt.Errorf("source config: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("source config: more than one YAML document")
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// LoadSource reads and validates the source config at path.
func LoadSource(path string) (*Source, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load source: %w", err)
	}
	s, err := ParseSource(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Validate checks the config; every error names the offending field path.
func (s *Source) Validate() error {
	var errs []error
	bad := func(path, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...)))
	}
	if strings.TrimSpace(s.ID) == "" {
		bad("id", "required")
	}
	switch s.Kind {
	case KindCSV, KindXLSX:
	case "":
		bad("kind", "required")
	default:
		bad("kind", "unsupported kind %q (supported: csv, xlsx)", s.Kind)
	}
	if strings.TrimSpace(s.Location) == "" {
		bad("location", "required")
	}
	s.validateArchiveAndResolve(bad)
	if s.MaxErrorRatio < 0 || s.MaxErrorRatio > 1 {
		bad("max_error_ratio", "must be between 0 and 1, got %v", s.MaxErrorRatio)
	}
	switch s.MissingPolicy {
	case "", MissingKeep, MissingTombstone:
	default:
		bad("missing_policy", "must be keep or tombstone, got %q", s.MissingPolicy)
	}
	for i, k := range s.NaturalKey {
		if strings.TrimSpace(k) == "" {
			bad(fmt.Sprintf("natural_key[%d]", i), "must not be empty")
		}
	}
	if len(s.NaturalKey) == 0 {
		bad("natural_key", "at least one field required")
	}
	if s.Auth != nil {
		s.Auth.validate(bad)
	}
	if s.Kind == KindCSV {
		s.validateCSV(bad)
	}
	if s.Kind == KindXLSX {
		s.validateXLSX(bad)
		if s.CSV != nil {
			bad("csv", "only valid for kind csv")
		}
	} else if s.Kind != "" {
		if s.XLSX != nil {
			bad("xlsx", "only valid for kind xlsx")
		}
		if s.Kind != KindCSV {
			if len(s.Table) > 0 {
				bad("table", "the row pipeline is only supported for kinds csv and xlsx")
			}
			if len(s.Context) > 0 {
				bad("context", "static context is only supported for kinds csv and xlsx")
			}
		}
	}
	return errors.Join(errs...)
}

func (s *Source) validateCSV(bad func(path, format string, args ...any)) {
	c := s.CSV
	if c == nil {
		c = &CSV{}
	}
	if c.Delimiter != "" && c.Delimiter != "tab" && c.Delimiter != "\\t" {
		if utf8.RuneCountInString(c.Delimiter) != 1 {
			bad("csv.delimiter", "must be a single character, got %q", c.Delimiter)
		} else if r := c.DelimiterRune(); r == '"' || r == '\n' || r == '\r' || r == utf8.RuneError {
			bad("csv.delimiter", "invalid delimiter %q", c.Delimiter)
		}
	}
	ok := false
	for _, e := range SupportedEncodings {
		if c.EncodingName() == e {
			ok = true
		}
	}
	if !ok {
		bad("csv.encoding", "unsupported encoding %q (supported: %s)", c.Encoding, strings.Join(SupportedEncodings, ", "))
	}
	seen := map[string]bool{}
	for i, col := range c.Columns {
		switch {
		case strings.TrimSpace(col) == "":
			bad(fmt.Sprintf("csv.columns[%d]", i), "must not be empty")
		case seen[col]:
			bad(fmt.Sprintf("csv.columns[%d]", i), "duplicate column %q", col)
		}
		seen[col] = true
	}
	if p := c.Preamble; p != nil {
		switch {
		case p.Lines != 0 && p.Until != "":
			bad("csv.preamble", "set exactly one of lines and until, not both")
		case p.Lines < 0:
			bad("csv.preamble.lines", "must be positive, got %d", p.Lines)
		case p.Lines == 0 && p.Until == "":
			bad("csv.preamble", "set exactly one of lines and until")
		}
		if p.Until != "" {
			if _, err := regexp.Compile(p.Until); err != nil {
				bad("csv.preamble.until", "invalid regular expression: %v", err)
			}
		}
		if p.As != "metadata" {
			bad("csv.preamble.as", "must be metadata, got %q", p.As)
		}
	}
	for i, k := range s.NaturalKey {
		if strings.HasPrefix(k, MetaPrefix) {
			if strings.TrimSpace(k[len(MetaPrefix):]) == "" {
				bad(fmt.Sprintf("natural_key[%d]", i), "%q names no metadata key", k)
			} else if c.Preamble == nil {
				bad(fmt.Sprintf("natural_key[%d]", i), "%q needs csv.preamble", k)
			}
		}
	}
	if len(s.Table) > 0 || len(s.Context) > 0 {
		s.validateCSVTable(c, bad)
		return
	}
	if len(c.Columns) > 0 {
		for i, k := range s.NaturalKey {
			if !seen[k] && !strings.HasPrefix(k, "@") {
				bad(fmt.Sprintf("natural_key[%d]", i), "%q is not in csv.columns", k)
			}
		}
	}
}

// validateCSVTable checks `context:` and the row pipeline of a csv source, and checks the natural key against
// what the pipeline outputs (field names, context or section variables, @ctx.<name>, @meta.<key>).
func (s *Source) validateCSVTable(c *CSV, bad func(path, format string, args ...any)) {
	ctxNames := s.validateContext(bad)
	var in []string
	known := len(c.Columns) > 0
	if known {
		in = c.Columns
	}
	out, sets, outKnown := s.Table.CheckTable(in, known, bad)
	for _, st := range sets {
		ctxNames[st] = true
	}
	have := map[string]bool{}
	for _, f := range out {
		have[f] = true
	}
	for i, k := range s.NaturalKey {
		path := fmt.Sprintf("natural_key[%d]", i)
		switch {
		case strings.HasPrefix(k, MetaPrefix):
		case strings.HasPrefix(k, CtxPrefix):
			name := k[len(CtxPrefix):]
			if strings.TrimSpace(name) == "" {
				bad(path, "%q names no context variable", k)
			} else if !ctxNames[name] {
				bad(path, "%q is not a context variable (known: %s)", k, strings.Join(sortedSet(ctxNames), ", "))
			}
		case strings.HasPrefix(k, "@"):
			bad(path, "%q is not supported for kind csv with table (use @ctx.<name> or @meta.<key>)", k)
		case outKnown && !have[k] && !ctxNames[k]:
			bad(path, "%q is neither a field after the table pipeline (%s) nor a context variable", k, strings.Join(out, ", "))
		}
	}
}
