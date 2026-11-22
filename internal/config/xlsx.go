package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// KindXLSX is the source kind of Excel workbooks.
const KindXLSX = "xlsx"

// CtxPrefix introduces a natural-key reference to a context variable, e.g. "@ctx.sheet".
const CtxPrefix = "@ctx."

// SheetContextName is the context variable that holds the sheet name of a record.
const SheetContextName = "sheet"

// MaxSheetRows and MaxSheetColumns are the worksheet limits of the xlsx format.
const (
	MaxSheetRows    = 1048576
	MaxSheetColumns = 16384
)

// XLSX holds the options of an xlsx source (see docs/mapping-spec.md, "Source: XLSX workbook").
// Exactly one of Sheet, SheetIndex and Sheets selects the sheets to read.
type XLSX struct {
	Sheet           string            `yaml:"sheet,omitempty"`
	SheetIndex      *int              `yaml:"sheet_index,omitempty"` // 0-based position in the workbook
	Sheets          *SheetSet         `yaml:"sheets,omitempty"`
	StartRow        int               `yaml:"start_row,omitempty"` // 1-based, inclusive
	EndRow          int               `yaml:"end_row,omitempty"`   // 1-based, inclusive; 0 = last row
	Header          *bool             `yaml:"header,omitempty"`    // default true
	HeaderRow       int               `yaml:"header_row,omitempty"`
	Columns         map[string]string `yaml:"columns,omitempty"` // column letter -> field name
	UseCachedValues *bool             `yaml:"use_cached_values,omitempty"`
}

// SheetSet selects several sheets: either a list of names or `{except: [names]}` (every other sheet).
type SheetSet struct {
	Names  []string
	Except []string
}

// UnmarshalYAML accepts a list of names or a mapping with the single key except.
func (s *SheetSet) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.SequenceNode:
		var names []string
		if err := n.Decode(&names); err != nil {
			return err
		}
		*s = SheetSet{Names: names}
		return nil
	case yaml.MappingNode:
		var m struct {
			Except []string `yaml:"except"`
		}
		if len(n.Content) != 2 || n.Content[0].Value != "except" {
			return fmt.Errorf("line %d: sheets must be a list of names or {except: [names]}", n.Line)
		}
		if err := n.Content[1].Decode(&m.Except); err != nil {
			return err
		}
		*s = SheetSet{Except: m.Except}
		if s.Except == nil {
			s.Except = []string{}
		}
		return nil
	}
	return fmt.Errorf("line %d: sheets must be a list of names or {except: [names]}", n.Line)
}

// MarshalYAML is the inverse of UnmarshalYAML.
func (s SheetSet) MarshalYAML() (any, error) {
	if s.Except != nil {
		return map[string][]string{"except": s.Except}, nil
	}
	return s.Names, nil
}

// HasHeader reports whether the sheets have a header row (default true).
func (x XLSX) HasHeader() bool { return x.Header == nil || *x.Header }

// HeaderRowNumber returns the 1-based header row (default 1).
func (x XLSX) HeaderRowNumber() int {
	if x.HeaderRow > 0 {
		return x.HeaderRow
	}
	return 1
}

// FirstRow returns the first data row (1-based).
func (x XLSX) FirstRow() int {
	if x.StartRow > 0 {
		return x.StartRow
	}
	if x.HasHeader() {
		return x.HeaderRowNumber() + 1
	}
	return 1
}

// Col is one configured column: its 1-based index, upper-case letters and field name.
type Col struct {
	Index  int
	Letter string
	Name   string
}

// ColumnList returns the configured columns ordered by position. Invalid letters are skipped (Validate reports them).
func (x XLSX) ColumnList() []Col {
	var cols []Col
	for l, n := range x.Columns {
		if i, ok := ColumnIndex(l); ok {
			cols = append(cols, Col{Index: i, Letter: strings.ToUpper(strings.TrimSpace(l)), Name: n})
		}
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].Index < cols[j].Index })
	return cols
}

// ColumnIndex converts column letters (A, Z, AA, XFD) to a 1-based index; ok is false for anything else
// or for a column beyond XFD.
func ColumnIndex(letters string) (int, bool) {
	letters = strings.ToUpper(strings.TrimSpace(letters))
	if letters == "" || len(letters) > 3 {
		return 0, false
	}
	n := 0
	for _, r := range letters {
		if r < 'A' || r > 'Z' {
			return 0, false
		}
		n = n*26 + int(r-'A') + 1
	}
	if n > MaxSheetColumns {
		return 0, false
	}
	return n, true
}

// Step is one row-pipeline step; exactly one field is set.
type Step struct {
	SkipRows    *SkipRows             `yaml:"skip_rows,omitempty"`
	DropIfEmpty []string              `yaml:"drop_if_empty,omitempty"`
	Section     *Section              `yaml:"section,omitempty"`
	FillDown    *FillDown             `yaml:"fill_down,omitempty"`
	Normalize   map[string]Transforms `yaml:"normalize,omitempty"`
	Unpivot     *Unpivot              `yaml:"unpivot,omitempty"`
	Split       *Split                `yaml:"split,omitempty"`
	Strip       map[string][]string   `yaml:"strip,omitempty"`
}

// Table is the row pipeline (`table:`): steps run in order.
type Table []Step

// SkipRows drops rows whose column matches the regular expression.
type SkipRows struct {
	Where string `yaml:"where"`
	Match string `yaml:"match"`
}

// Section marks heading rows: Column values matching Match set the context variable Set and the row is dropped.
type Section struct {
	Column  string  `yaml:"column"`
	Match   string  `yaml:"match"`
	Set     string  `yaml:"set"`
	Default *string `yaml:"default,omitempty"`
}

// FillDown carries the last non-empty value of each column down; ResetOn "section" forgets it at a heading.
type FillDown struct {
	Columns []string `yaml:"columns"`
	ResetOn string   `yaml:"reset_on,omitempty"`
}

// Transforms is a list of normalize transforms; a single scalar is accepted in YAML.
type Transforms []string

// UnmarshalYAML accepts a scalar or a list.
func (t *Transforms) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*t = Transforms{n.Value}
		return nil
	case yaml.SequenceNode:
		var l []string
		if err := n.Decode(&l); err != nil {
			return err
		}
		*t = l
		return nil
	}
	return fmt.Errorf("line %d: transforms must be a name or a list of names", n.Line)
}

// Normalize transforms.
var NormalizeTransforms = []string{"trim", "lower", "upper", "to_int", "to_string", "collapse_ws", "to_number"}

// Unpivot turns wide rows into long ones.
type Unpivot struct {
	ID      []string       `yaml:"id"`
	Columns UnpivotColumns `yaml:"columns"`
	Name    string         `yaml:"name"`
	Value   string         `yaml:"value"`
}

// UnpivotColumn is one column to unpivot and the label stored in the name column (the column name when empty).
type UnpivotColumn struct {
	Column string
	Label  string
}

// UnpivotColumns is a list of columns, or a mapping column: label (order is kept).
type UnpivotColumns []UnpivotColumn

// UnmarshalYAML accepts a list of column names or a mapping column: label.
func (u *UnpivotColumns) UnmarshalYAML(n *yaml.Node) error {
	var out UnpivotColumns
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: unpivot columns must be names", c.Line)
			}
			out = append(out, UnpivotColumn{Column: c.Value})
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode || v.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: unpivot columns must map column names to labels", k.Line)
			}
			out = append(out, UnpivotColumn{Column: k.Value, Label: v.Value})
		}
	default:
		return fmt.Errorf("line %d: unpivot columns must be a list or a column: label mapping", n.Line)
	}
	*u = out
	return nil
}

// MarshalYAML is the inverse of UnmarshalYAML (order kept).
func (u UnpivotColumns) MarshalYAML() (any, error) {
	labelled := false
	for _, c := range u {
		if c.Label != "" {
			labelled = true
		}
	}
	if !labelled {
		l := make([]string, len(u))
		for i, c := range u {
			l[i] = c.Column
		}
		return l, nil
	}
	n := &yaml.Node{Kind: yaml.MappingNode}
	for _, c := range u {
		label := c.Label
		if label == "" {
			label = c.Column
		}
		n.Content = append(n.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: c.Column},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: label})
	}
	return n, nil
}

// Split writes the named capture groups of Regex (applied to Column) to new columns.
type Split struct {
	Column   string `yaml:"column"`
	Regex    string `yaml:"regex"`
	Optional bool   `yaml:"optional,omitempty"`
}

// GroupNames returns the named capture groups of the regex (empty if it does not compile).
func (s Split) GroupNames() []string {
	re, err := regexp.Compile(s.Regex)
	if err != nil {
		return nil
	}
	var names []string
	for _, n := range re.SubexpNames() {
		if n != "" {
			names = append(names, n)
		}
	}
	return names
}

// InputFields returns the field names a sheet yields before the pipeline runs; ok is false when they are only
// known from the header row of the workbook (header: true without columns).
func (x XLSX) InputFields() (fields []string, ok bool) {
	if x.HasHeader() && len(x.Columns) == 0 {
		return nil, false
	}
	for _, c := range x.ColumnList() {
		fields = append(fields, c.Name)
	}
	return fields, true
}

// CheckTable validates the pipeline against the input fields (when known) and returns the output field
// names (when known) and the context variable names the pipeline sets. Errors name the field path.
func (t Table) CheckTable(in []string, known bool, bad func(path, format string, args ...any)) (out []string, sets []string, outKnown bool) {
	fields := map[string]bool{}
	var order []string
	add := func(n string) {
		if !fields[n] {
			fields[n] = true
			order = append(order, n)
		}
	}
	if known {
		for _, f := range in {
			add(f)
		}
	}
	need := func(path, col string) {
		switch {
		case strings.TrimSpace(col) == "":
			bad(path, "column name must not be empty")
		case known && !fields[col]:
			bad(path, "unknown column %q (columns here: %s)", col, strings.Join(order, ", "))
		}
	}
	setNames := map[string]bool{}
	for i, st := range t {
		p := fmt.Sprintf("table[%d]", i)
		n := 0
		if st.SkipRows != nil {
			n++
		}
		if st.DropIfEmpty != nil {
			n++
		}
		if st.Section != nil {
			n++
		}
		if st.FillDown != nil {
			n++
		}
		if st.Normalize != nil {
			n++
		}
		if st.Unpivot != nil {
			n++
		}
		if st.Split != nil {
			n++
		}
		if st.Strip != nil {
			n++
		}
		if n != 1 {
			bad(p, "a step must have exactly one of skip_rows, drop_if_empty, section, fill_down, normalize, unpivot, split, strip (got %d)", n)
			continue
		}
		switch {
		case st.SkipRows != nil:
			s := st.SkipRows
			need(p+".skip_rows.where", s.Where)
			if _, err := regexp.Compile(s.Match); err != nil {
				bad(p+".skip_rows.match", "invalid regular expression: %v", err)
			} else if s.Match == "" {
				bad(p+".skip_rows.match", "required")
			}
		case st.DropIfEmpty != nil:
			if len(st.DropIfEmpty) == 0 {
				bad(p+".drop_if_empty", "at least one column required")
			}
			for j, c := range st.DropIfEmpty {
				need(fmt.Sprintf("%s.drop_if_empty[%d]", p, j), c)
			}
		case st.Section != nil:
			s := st.Section
			need(p+".section.column", s.Column)
			if s.Match == "" {
				bad(p+".section.match", "required")
			} else if _, err := regexp.Compile(s.Match); err != nil {
				bad(p+".section.match", "invalid regular expression: %v", err)
			}
			switch {
			case strings.TrimSpace(s.Set) == "":
				bad(p+".section.set", "required")
			case s.Set == SheetContextName:
				bad(p+".section.set", "%q is reserved for the sheet name", s.Set)
			case strings.HasPrefix(s.Set, "_"):
				bad(p+".section.set", "must not start with an underscore")
			default:
				setNames[s.Set] = true
			}
		case st.FillDown != nil:
			f := st.FillDown
			if len(f.Columns) == 0 {
				bad(p+".fill_down.columns", "at least one column required")
			}
			for j, c := range f.Columns {
				need(fmt.Sprintf("%s.fill_down.columns[%d]", p, j), c)
			}
			if f.ResetOn != "" && f.ResetOn != "section" {
				bad(p+".fill_down.reset_on", "must be section, got %q", f.ResetOn)
			}
		case st.Normalize != nil:
			if len(st.Normalize) == 0 {
				bad(p+".normalize", "at least one column required")
			}
			cols := make([]string, 0, len(st.Normalize))
			for c := range st.Normalize {
				cols = append(cols, c)
			}
			sort.Strings(cols)
			for _, c := range cols {
				need(p+".normalize."+c, c)
				ts := st.Normalize[c]
				if len(ts) == 0 {
					bad(p+".normalize."+c, "at least one transform required")
				}
				for j, tr := range ts {
					ok := false
					for _, k := range NormalizeTransforms {
						ok = ok || tr == k
					}
					if !ok {
						bad(fmt.Sprintf("%s.normalize.%s[%d]", p, c, j), "unknown transform %q (supported: %s)", tr, strings.Join(NormalizeTransforms, ", "))
					}
				}
			}
		case st.Unpivot != nil:
			u := st.Unpivot
			if len(u.Columns) == 0 {
				bad(p+".unpivot.columns", "at least one column required")
			}
			idSet := map[string]bool{}
			for j, c := range u.ID {
				need(fmt.Sprintf("%s.unpivot.id[%d]", p, j), c)
				if idSet[c] {
					bad(fmt.Sprintf("%s.unpivot.id[%d]", p, j), "duplicate column %q", c)
				}
				idSet[c] = true
			}
			seen := map[string]bool{}
			for j, c := range u.Columns {
				path := fmt.Sprintf("%s.unpivot.columns[%d]", p, j)
				need(path, c.Column)
				if seen[c.Column] {
					bad(path, "duplicate column %q", c.Column)
				}
				seen[c.Column] = true
				if idSet[c.Column] {
					bad(path, "column %q is also in id", c.Column)
				}
			}
			switch {
			case strings.TrimSpace(u.Name) == "":
				bad(p+".unpivot.name", "required")
			case strings.TrimSpace(u.Value) == "":
				bad(p+".unpivot.value", "required")
			case u.Name == u.Value:
				bad(p+".unpivot.value", "must differ from name")
			case idSet[u.Name] || idSet[u.Value]:
				bad(p+".unpivot.name", "name and value must not be id columns")
			}
			if strings.HasPrefix(u.Name, "_") || strings.HasPrefix(u.Value, "_") {
				bad(p+".unpivot", "name and value must not start with an underscore")
			}
			// Only the id columns, name and value survive.
			fields = map[string]bool{}
			order = nil
			for _, c := range u.ID {
				add(c)
			}
			add(u.Name)
			add(u.Value)
			known = true
		case st.Split != nil:
			s := st.Split
			need(p+".split.column", s.Column)
			_, err := regexp.Compile(s.Regex)
			switch {
			case s.Regex == "":
				bad(p+".split.regex", "required")
			case err != nil:
				bad(p+".split.regex", "invalid regular expression: %v", err)
			default:
				groups := s.GroupNames()
				if len(groups) == 0 {
					bad(p+".split.regex", "needs at least one named group (?P<name>...)")
				}
				for _, g := range groups {
					if strings.HasPrefix(g, "_") {
						bad(p+".split.regex", "group name %q must not start with an underscore", g)
					}
					if known {
						add(g)
					}
				}
			}
		case st.Strip != nil:
			if len(st.Strip) == 0 {
				bad(p+".strip", "at least one column required")
			}
			cols := make([]string, 0, len(st.Strip))
			for c := range st.Strip {
				cols = append(cols, c)
			}
			sort.Strings(cols)
			for _, c := range cols {
				need(p+".strip."+c, c)
				if len(st.Strip[c]) == 0 {
					bad(p+".strip."+c, "at least one string to remove required")
				}
				for j, s := range st.Strip[c] {
					if s == "" {
						bad(fmt.Sprintf("%s.strip.%s[%d]", p, c, j), "must not be empty")
					}
				}
			}
		}
	}
	for s := range setNames {
		sets = append(sets, s)
	}
	sort.Strings(sets)
	return order, sets, known
}

func (s *Source) validateXLSX(bad func(path, format string, args ...any)) {
	x := s.XLSX
	if x == nil {
		bad("xlsx", "required for kind xlsx")
		return
	}
	// Which sheets.
	n := 0
	if x.Sheet != "" {
		n++
	}
	if x.SheetIndex != nil {
		n++
		if *x.SheetIndex < 0 {
			bad("xlsx.sheet_index", "must not be negative, got %d", *x.SheetIndex)
		}
	}
	if x.Sheets != nil {
		n++
		if x.Sheets.Except == nil && len(x.Sheets.Names) == 0 {
			bad("xlsx.sheets", "at least one sheet name required")
		}
		for i, nm := range append(append([]string{}, x.Sheets.Names...), x.Sheets.Except...) {
			if strings.TrimSpace(nm) == "" {
				bad(fmt.Sprintf("xlsx.sheets[%d]", i), "sheet name must not be empty")
			}
		}
	}
	if n != 1 {
		bad("xlsx", "set exactly one of sheet, sheet_index and sheets (got %d)", n)
	}
	// Rows.
	switch {
	case x.StartRow < 0 || x.StartRow > MaxSheetRows:
		bad("xlsx.start_row", "must be between 1 and %d, got %d", MaxSheetRows, x.StartRow)
	case x.EndRow < 0 || x.EndRow > MaxSheetRows:
		bad("xlsx.end_row", "must be between 1 and %d, got %d", MaxSheetRows, x.EndRow)
	case x.EndRow > 0 && x.EndRow < x.FirstRow():
		bad("xlsx.end_row", "%d is before the first data row %d", x.EndRow, x.FirstRow())
	}
	if x.HeaderRow < 0 || x.HeaderRow > MaxSheetRows {
		bad("xlsx.header_row", "must be between 1 and %d, got %d", MaxSheetRows, x.HeaderRow)
	}
	if x.HeaderRow != 0 && !x.HasHeader() {
		bad("xlsx.header_row", "only valid with header: true")
	}
	if x.HasHeader() && x.StartRow > 0 && x.StartRow <= x.HeaderRowNumber() {
		bad("xlsx.start_row", "%d must be after the header row %d", x.StartRow, x.HeaderRowNumber())
	}
	if x.UseCachedValues != nil && !*x.UseCachedValues {
		bad("xlsx.use_cached_values", "formulas are never evaluated, only cached values can be read: must be true (or omitted)")
	}
	// Columns.
	if !x.HasHeader() && len(x.Columns) == 0 {
		bad("xlsx.columns", "required when header is false")
	}
	letters := make([]string, 0, len(x.Columns))
	for l := range x.Columns {
		letters = append(letters, l)
	}
	sort.Strings(letters)
	seenCol, seenName := map[int]string{}, map[string]string{}
	for _, l := range letters {
		name := x.Columns[l]
		path := "xlsx.columns." + l
		idx, ok := ColumnIndex(l)
		switch {
		case !ok:
			bad(path, "%q is not a column letter (A to XFD)", l)
		case seenCol[idx] != "":
			bad(path, "column %s is listed twice (also as %q)", strings.ToUpper(l), seenCol[idx])
		default:
			seenCol[idx] = l
		}
		switch {
		case strings.TrimSpace(name) == "":
			bad(path, "field name must not be empty")
		case strings.HasPrefix(name, "_"):
			bad(path, "field name %q must not start with an underscore", name)
		case seenName[name] != "":
			bad(path, "duplicate field name %q (also column %s)", name, seenName[name])
		default:
			seenName[name] = l
		}
	}
	ctxNames := s.validateContext(bad)
	// Pipeline and natural key.
	in, known := x.InputFields()
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
		case strings.HasPrefix(k, CtxPrefix):
			name := k[len(CtxPrefix):]
			if strings.TrimSpace(name) == "" {
				bad(path, "%q names no context variable", k)
			} else if !ctxNames[name] {
				bad(path, "%q is not a context variable (known: %s)", k, strings.Join(sortedSet(ctxNames), ", "))
			}
		case strings.HasPrefix(k, "@"):
			bad(path, "%q is not supported for kind xlsx (use @ctx.<name>)", k)
		case outKnown && !have[k] && !ctxNames[k]:
			bad(path, "%q is neither a field after the table pipeline (%s) nor a context variable", k, strings.Join(out, ", "))
		}
	}
}

// validateContext checks the static `context:` variables and returns the names of all context variables
// (the reserved sheet name included).
func (s *Source) validateContext(bad func(path, format string, args ...any)) map[string]bool {
	ctxNames := map[string]bool{SheetContextName: true}
	for k := range s.Context {
		switch {
		case strings.TrimSpace(k) == "":
			bad("context", "variable name must not be empty")
		case k == SheetContextName:
			bad("context."+k, "%q is reserved for the sheet name", k)
		case strings.HasPrefix(k, "_"):
			bad("context."+k, "variable name must not start with an underscore")
		}
		ctxNames[k] = true
	}
	return ctxNames
}

func sortedSet(m map[string]bool) []string {
	l := make([]string, 0, len(m))
	for k := range m {
		l = append(l, k)
	}
	sort.Strings(l)
	return l
}
