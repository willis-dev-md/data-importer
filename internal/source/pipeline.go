package source

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/willis-dev-md/data-importer/internal/config"
)

// pipeRow is one row travelling through the row pipeline. Cells are strings as read from the sheet until a
// normalize step (to_int, to_number) turns them into int64 or float64.
type pipeRow struct {
	fields map[string]any
	ctx    map[string]string // context variables in force for this row (static context, sheet, section variables)
}

// stepError is a row-level failure of a step: the row becomes a RowError.
type stepError struct{ msg string }

func (e *stepError) Error() string { return e.msg }

// pipeStep processes one row. It returns the output rows (none = dropped) or a *stepError.
type pipeStep interface {
	apply(r pipeRow) ([]pipeRow, error)
}

// Pipeline is a compiled `table:` pipeline with its per-sheet state (section variables, fill-down memory).
// Build a new one for every sheet.
type Pipeline struct {
	steps []pipeStep
	state *pipeState
}

type pipeState struct {
	section    map[string]string // current value of every section variable
	generation int               // incremented at every heading: fill_down reset_on: section
}

// NewPipeline compiles the steps. Regular expressions are compiled once; an invalid one is an error
// (config validation normally catches it first).
func NewPipeline(t config.Table) (*Pipeline, error) {
	p := &Pipeline{state: &pipeState{section: map[string]string{}}}
	for i, st := range t {
		s, err := compileStep(st, p.state)
		if err != nil {
			return nil, fmt.Errorf("table[%d]: %w", i, err)
		}
		p.steps = append(p.steps, s)
	}
	// Defaults apply before the first heading.
	for _, st := range t {
		if st.Section != nil && st.Section.Default != nil {
			p.state.section[st.Section.Set] = *st.Section.Default
		}
	}
	return p, nil
}

// Run pushes one row through all steps in order. A step error aborts the row (reported once).
func (p *Pipeline) Run(r pipeRow) (out []pipeRow, err error) {
	defer func() {
		if rec := recover(); rec != nil { // a pipeline bug must never take the import down
			out, err = nil, &stepError{fmt.Sprintf("internal error in row pipeline: %v", rec)}
		}
	}()
	rows := []pipeRow{r}
	for _, s := range p.steps {
		var next []pipeRow
		for _, row := range rows {
			o, err := s.apply(row)
			if err != nil {
				return nil, err
			}
			next = append(next, o...)
		}
		rows = next
		if len(rows) == 0 {
			return nil, nil
		}
	}
	return rows, nil
}

func compileStep(st config.Step, state *pipeState) (pipeStep, error) {
	switch {
	case st.SkipRows != nil:
		re, err := regexp.Compile(st.SkipRows.Match)
		if err != nil {
			return nil, fmt.Errorf("skip_rows.match: %w", err)
		}
		return &skipRows{col: st.SkipRows.Where, re: re}, nil
	case st.DropIfEmpty != nil:
		return &dropIfEmpty{cols: st.DropIfEmpty}, nil
	case st.Section != nil:
		re, err := regexp.Compile(st.Section.Match)
		if err != nil {
			return nil, fmt.Errorf("section.match: %w", err)
		}
		return &section{col: st.Section.Column, re: re, set: st.Section.Set, state: state}, nil
	case st.FillDown != nil:
		return &fillDown{cols: st.FillDown.Columns, reset: st.FillDown.ResetOn == "section", state: state, last: map[string]any{}}, nil
	case st.Normalize != nil:
		cols := make([]string, 0, len(st.Normalize))
		for c := range st.Normalize {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		n := &normalize{}
		for _, c := range cols {
			n.cols = append(n.cols, c)
			n.transforms = append(n.transforms, st.Normalize[c])
		}
		return n, nil
	case st.Unpivot != nil:
		return &unpivot{id: st.Unpivot.ID, cols: st.Unpivot.Columns, name: st.Unpivot.Name, value: st.Unpivot.Value}, nil
	case st.Split != nil:
		re, err := regexp.Compile(st.Split.Regex)
		if err != nil {
			return nil, fmt.Errorf("split.regex: %w", err)
		}
		return &split{col: st.Split.Column, re: re, optional: st.Split.Optional}, nil
	case st.Strip != nil:
		cols := make([]string, 0, len(st.Strip))
		for c := range st.Strip {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		s := &strip{}
		for _, c := range cols {
			s.cols = append(s.cols, c)
			s.remove = append(s.remove, st.Strip[c])
		}
		return s, nil
	}
	return nil, fmt.Errorf("empty step")
}

// keyValue returns the natural-key part k of a pipeline output row: `@ctx.<name>` is a context variable; a plain
// name is a field, falling back to a context or section variable. nil when it is absent. Shared by the xlsx and
// csv adapters.
func keyValue(k string, r pipeRow) any {
	if name, ok := strings.CutPrefix(k, config.CtxPrefix); ok {
		if cv, ok := r.ctx[name]; ok {
			return cv
		}
		return nil
	}
	if fv, ok := r.fields[k]; ok {
		return fv
	}
	if cv, ok := r.ctx[k]; ok {
		return cv
	}
	return nil
}

// isEmpty reports whether a cell holds no data: missing, nil, or text with only whitespace.
func isEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	}
	return false
}

// textOf returns the text of a cell for matching: strings as they are, numbers in plain decimal.
func textOf(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

func cloneFields(m map[string]any) map[string]any {
	c := make(map[string]any, len(m)+2)
	for k, v := range m {
		c[k] = v
	}
	return c
}

type skipRows struct {
	col string
	re  *regexp.Regexp
}

func (s *skipRows) apply(r pipeRow) ([]pipeRow, error) {
	if t, ok := textOf(r.fields[s.col]); ok && s.re.MatchString(t) {
		return nil, nil
	}
	return []pipeRow{r}, nil
}

type dropIfEmpty struct{ cols []string }

func (d *dropIfEmpty) apply(r pipeRow) ([]pipeRow, error) {
	for _, c := range d.cols {
		if !isEmpty(r.fields[c]) {
			return []pipeRow{r}, nil
		}
	}
	return nil, nil
}

type section struct {
	col   string
	re    *regexp.Regexp
	set   string
	state *pipeState
}

func (s *section) apply(r pipeRow) ([]pipeRow, error) {
	if t, ok := r.fields[s.col].(string); ok && s.re.MatchString(t) {
		s.state.section[s.set] = strings.TrimSpace(t)
		s.state.generation++
		return nil, nil // headings are not data rows
	}
	if v, ok := s.state.section[s.set]; ok {
		r.ctx = withCtx(r.ctx, s.set, v)
	}
	return []pipeRow{r}, nil
}

func withCtx(m map[string]string, k, v string) map[string]string {
	c := make(map[string]string, len(m)+1)
	for a, b := range m {
		c[a] = b
	}
	c[k] = v
	return c
}

type fillDown struct {
	cols  []string
	reset bool
	state *pipeState
	last  map[string]any
	gen   int
}

func (f *fillDown) apply(r pipeRow) ([]pipeRow, error) {
	if f.reset && f.gen != f.state.generation {
		f.last = map[string]any{}
		f.gen = f.state.generation
	}
	out := pipeRow{fields: cloneFields(r.fields), ctx: r.ctx}
	for _, c := range f.cols {
		if v := r.fields[c]; !isEmpty(v) {
			f.last[c] = v
		} else if l, ok := f.last[c]; ok {
			out.fields[c] = l
		}
	}
	return []pipeRow{out}, nil
}

type normalize struct {
	cols       []string
	transforms []config.Transforms
}

func (n *normalize) apply(r pipeRow) ([]pipeRow, error) {
	out := pipeRow{fields: cloneFields(r.fields), ctx: r.ctx}
	for i, c := range n.cols {
		v, present := out.fields[c]
		if !present {
			continue
		}
		for _, t := range n.transforms[i] {
			nv, err := applyTransform(t, v)
			if err != nil {
				return nil, &stepError{fmt.Sprintf("normalize: column %q: %v", c, err)}
			}
			v = nv
		}
		out.fields[c] = v
	}
	return []pipeRow{out}, nil
}

var (
	numRe     = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)
	wsRunRe   = regexp.MustCompile(`\s+`)
	maxSigDig = 15
)

// sigDigits counts the significant digits of a numeric literal (mantissa only).
func sigDigits(s string) int {
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimLeft(strings.TrimLeft(s, "+-"), "0")
	s = strings.Replace(s, ".", "", 1)
	return len(strings.TrimLeft(s, "0"))
}

// parseNumber parses numeric text exactly as to_number does: int64 when it is an integer literal that fits,
// float64 otherwise. ok is false for anything that is not a plain decimal number, for non-finite results and
// for literals with more than 15 significant digits (they cannot round-trip through float64).
func parseNumber(s string) (v any, ok bool) {
	s = strings.TrimSpace(s)
	if !numRe.MatchString(s) {
		return nil, false
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i, true
	}
	if sigDigits(s) > maxSigDig {
		return nil, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil, false
	}
	return f, true
}

// toInt converts a value to int64: whole numbers only ("1694", "1694.0", float 1694); empty stays empty.
func toInt(v any) (any, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return int64(x), nil
		}
		return nil, fmt.Errorf("%v is not a whole number", x)
	case string:
		if strings.TrimSpace(x) == "" {
			return x, nil
		}
		t := strings.TrimSpace(x)
		if !numRe.MatchString(t) {
			return nil, fmt.Errorf("%q is not a number", truncate(x))
		}
		if i, err := strconv.ParseInt(t, 10, 64); err == nil {
			return i, nil
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil || math.IsInf(f, 0) || f != math.Trunc(f) || math.Abs(f) >= 1e15 {
			return nil, fmt.Errorf("%q is not a whole number in range", truncate(x))
		}
		return int64(f), nil
	case bool:
		return nil, fmt.Errorf("boolean %v is not a number", x)
	}
	return v, nil
}

func applyTransform(name string, v any) (any, error) {
	switch name {
	case "to_int":
		return toInt(v)
	case "to_number":
		if s, ok := v.(string); ok {
			if n, ok := parseNumber(s); ok {
				return n, nil
			}
		}
		return v, nil // numbers stay, markers such as x stay
	case "to_string":
		if t, ok := textOf(v); ok {
			return t, nil
		}
		return v, nil // nil stays nil
	}
	s, ok := v.(string)
	if !ok {
		return v, nil // text transforms leave numbers and booleans alone
	}
	switch name {
	case "trim":
		return strings.TrimSpace(s), nil
	case "lower":
		return strings.ToLower(s), nil
	case "upper":
		return strings.ToUpper(s), nil
	case "collapse_ws":
		return wsRunRe.ReplaceAllString(s, " "), nil
	}
	return nil, fmt.Errorf("unknown transform %q", name)
}

type unpivot struct {
	id          []string
	cols        config.UnpivotColumns
	name, value string
}

func (u *unpivot) apply(r pipeRow) ([]pipeRow, error) {
	var out []pipeRow
	for _, c := range u.cols {
		v := r.fields[c.Column]
		if isEmpty(v) {
			continue
		}
		label := c.Label
		if label == "" {
			label = c.Column
		}
		f := make(map[string]any, len(u.id)+2)
		for _, k := range u.id {
			f[k] = r.fields[k]
		}
		f[u.name] = label
		f[u.value] = v
		out = append(out, pipeRow{fields: f, ctx: r.ctx})
	}
	return out, nil
}

type split struct {
	col      string
	re       *regexp.Regexp
	optional bool
}

func (s *split) apply(r pipeRow) ([]pipeRow, error) {
	out := pipeRow{fields: cloneFields(r.fields), ctx: r.ctx}
	text, _ := textOf(r.fields[s.col])
	m := s.re.FindStringSubmatch(text)
	if m == nil && !s.optional {
		return nil, &stepError{fmt.Sprintf("split: column %q value %q does not match %q", s.col, truncate(text), s.re.String())}
	}
	for i, n := range s.re.SubexpNames() {
		if n == "" {
			continue
		}
		if m == nil {
			out.fields[n] = ""
		} else {
			out.fields[n] = m[i]
		}
	}
	return []pipeRow{out}, nil
}

type strip struct {
	cols   []string
	remove [][]string
}

func (s *strip) apply(r pipeRow) ([]pipeRow, error) {
	out := pipeRow{fields: cloneFields(r.fields), ctx: r.ctx}
	for i, c := range s.cols {
		str, ok := out.fields[c].(string)
		if !ok {
			continue
		}
		for _, rm := range s.remove[i] {
			str = strings.ReplaceAll(str, rm, "")
		}
		out.fields[c] = str
	}
	return []pipeRow{out}, nil
}
