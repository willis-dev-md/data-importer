package mapping

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Severity of an Issue.
type Severity string

// Severities.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue codes returned by Validate.
const (
	CodeMissingSource      = "MISSING_SOURCE"
	CodeBadExtra           = "BAD_EXTRA"
	CodeUnknownField       = "UNKNOWN_FIELD"
	CodeDimensionsField    = "DIMENSIONS_AS_FIELD"
	CodeRequiredNotCovered = "REQUIRED_NOT_COVERED"
	CodeUnknownType        = "UNKNOWN_TYPE"
	CodeTypeMismatch       = "TYPE_MISMATCH"
	CodeDimensionType      = "DIMENSION_TYPE"
	CodeSourceConflict     = "SOURCE_CONFLICT"
	CodeSourceMissing      = "SOURCE_MISSING"
	CodeBadFrom            = "BAD_FROM"
	CodeConstNotScalar     = "CONST_NOT_SCALAR"
	CodeConstNotInEnum     = "CONST_NOT_IN_ENUM"
	CodeUnknownTransform   = "UNKNOWN_TRANSFORM"
	CodeTransformArgs      = "TRANSFORM_ARGS"
	CodeUnknownValidator   = "UNKNOWN_VALIDATOR"
	CodeValidatorArgs      = "VALIDATOR_ARGS"
	CodeFormatRequired     = "FORMAT_REQUIRED"
	CodeBadRegex           = "BAD_REGEX"
	CodeOutputNotPeriod    = "OUTPUT_NOT_PERIOD"
	CodeBadOutput          = "BAD_OUTPUT"
	CodeOutputMismatch     = "OUTPUT_MISMATCH"
	CodeBadTemplate        = "BAD_TEMPLATE"
	CodeUnknownFlagField   = "UNKNOWN_FLAG_FIELD"
	CodeNullMarkersNoFlag  = "NULL_MARKERS_WITHOUT_FLAG"
	CodeBadPrecision       = "BAD_PRECISION"
)

// Issue is one validation finding. Path is the field path, e.g. "fields.value.type".
type Issue struct {
	Path     string
	Code     string
	Message  string
	Severity Severity
}

// String renders the issue as "path: code: message".
func (i Issue) String() string { return fmt.Sprintf("%s: %s: %s", i.Path, i.Code, i.Message) }

// HasErrors reports whether any issue has error severity.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

var (
	mappingTypes   = map[string]bool{"string": true, "int": true, "decimal": true, "bool": true, "date": true, "timestamp": true, "period": true}
	precisions     = map[string]bool{"year": true, "quarter": true, "month": true, "day": true}
	periodOutputs  = map[string]bool{"period_type": true, "period_start": true, "period_end": true}
	jsonPathRe     = regexp.MustCompile(`^\$(\.[A-Za-z0-9_\-]+|\[\d+\]|\[\*\])+$`)
	metaCtxRe      = regexp.MustCompile(`^@(ctx|meta)\.[^\s{}]+$`)
	transformNames = map[string]struct{ min, max int }{
		"trim": {0, 0}, "lower": {0, 0}, "upper": {0, 0}, "coalesce": {0, 0},
		"concat": {1, 1}, "split": {1, 1}, "regex": {2, 2},
	}
	callRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)\s*(\((.*)\))?$`)
)

// compatible lists, per canonical type, the mapping types that may feed it.
var compatible = map[string][]string{
	"string":    {"string"},
	"enum":      {"string"},
	"int":       {"int"},
	"decimal":   {"decimal", "int"},
	"bool":      {"bool"},
	"date":      {"date"},
	"timestamp": {"timestamp", "date"},
}

type validator struct {
	c      *Canonical
	issues []Issue
}

func (v *validator) add(sev Severity, path, code, format string, args ...any) {
	v.issues = append(v.issues, Issue{Path: path, Code: code, Severity: sev, Message: fmt.Sprintf(format, args...)})
}

func (v *validator) err(path, code, format string, args ...any) {
	v.add(SeverityError, path, code, format, args...)
}

func sortedKeys(m map[string]*Field) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Validate checks a mapping against the canonical schema and returns every finding.
func Validate(m *Mapping, canonical *Canonical) []Issue {
	v := &validator{c: canonical}
	if strings.TrimSpace(m.Source) == "" {
		v.err("source", CodeMissingSource, "source is required")
	}
	if m.Extra != "" && m.Extra != "keep" && m.Extra != "drop" {
		v.err("extra", CodeBadExtra, "extra must be keep or drop, got %q", m.Extra)
	}
	for _, name := range sortedKeys(m.Fields) {
		path := "fields." + name
		cf, ok := canonical.Fields[name]
		f := m.Fields[name]
		if f == nil {
			f = &Field{}
		}
		switch {
		case name == "dimensions" && ok:
			v.err(path, CodeDimensionsField, "dimensions are declared in the top-level dimensions block")
			cf = nil
		case !ok:
			v.err(path, CodeUnknownField, "%q is not a canonical field", name)
		}
		v.field(path, name, cf, f, false)
	}
	for _, name := range sortedKeys(m.Dimensions) {
		f := m.Dimensions[name]
		if f == nil {
			f = &Field{}
		}
		v.field("dimensions."+name, name, nil, f, true)
	}
	var names []string
	for name := range canonical.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cf := canonical.Fields[name]
		if cf.Required && m.Fields[name] == nil {
			v.err("fields."+name, CodeRequiredNotCovered, "required canonical field %q is not mapped", name)
		}
	}
	return v.issues
}

func (v *validator) field(path, name string, cf *CanonicalField, f *Field, dim bool) {
	typ := f.Type
	if typ == "" {
		typ = "string"
		if f.Const != nil && f.Template == "" && len(f.From) == 0 {
			typ = constType(f.Const)
		}
	}
	// type
	switch {
	case !mappingTypes[typ]:
		v.err(path+".type", CodeUnknownType, "unknown type %q", f.Type)
	case dim && typ != "string":
		v.err(path+".type", CodeDimensionType, "dimension values are strings, got type %q", f.Type)
	case cf != nil:
		v.compat(path, name, cf, typ, f)
	}
	// source: exactly one of from, const, template
	n := 0
	if len(f.From) > 0 {
		n++
	}
	if f.Const != nil {
		n++
	}
	if f.Template != "" {
		n++
	}
	switch {
	case n == 0:
		v.err(path, CodeSourceMissing, "exactly one of from, const or template is required")
	case n > 1:
		v.err(path, CodeSourceConflict, "from, const and template are mutually exclusive (exactly one is allowed)")
	}
	for i, s := range f.From {
		if !validFrom(s) {
			v.err(fmt.Sprintf("%s.from[%d]", path, i), CodeBadFrom, "%q is not a field name, JSONPath ($.a.b), @meta.X or @ctx.X", s)
		}
	}
	switch f.Const.(type) {
	case map[string]any, []any, map[any]any:
		v.err(path+".const", CodeConstNotScalar, "const must be a scalar")
	}
	if f.Const != nil && cf != nil && cf.Type == "enum" {
		if s := fmt.Sprint(f.Const); !contains(cf.Values, s) {
			v.err(path+".const", CodeConstNotInEnum, "%q is not one of %s", s, strings.Join(cf.Values, "|"))
		}
	}
	if f.Template != "" {
		v.template(path+".template", f.Template)
	}
	// format
	if (typ == "date" || typ == "timestamp") && len(f.Format) == 0 {
		v.err(path+".format", CodeFormatRequired, "type %s needs a format", typ)
	}
	for i, s := range f.Format {
		if s == "" {
			v.err(fmt.Sprintf("%s.format[%d]", path, i), CodeFormatRequired, "format must not be empty")
		}
	}
	// precision
	if f.Precision != "" && !precisions[f.Precision] {
		v.err(path+".precision", CodeBadPrecision, "precision must be year, quarter, month or day, got %q", f.Precision)
	}
	// transforms and validators
	for i, t := range f.Transform {
		v.transform(fmt.Sprintf("%s.transform[%d]", path, i), t)
	}
	if f.Validate != "" {
		v.validator(path+".validate", f.Validate)
	}
	// output
	if f.Output != "" {
		switch {
		case typ != "period":
			v.err(path+".output", CodeOutputNotPeriod, "output is only allowed on type period fields")
		case !periodOutputs[f.Output]:
			v.err(path+".output", CodeBadOutput, "output must be period_type, period_start or period_end, got %q", f.Output)
		case cf != nil && periodOutputs[name] && f.Output != name:
			v.err(path+".output", CodeOutputMismatch, "output %q does not match canonical field %q", f.Output, name)
		}
	}
	// null markers and flag
	if f.Flag != "" {
		if _, ok := v.c.Fields[f.Flag]; !ok {
			v.err(path+".flag", CodeUnknownFlagField, "flag names %q, which is not a canonical field", f.Flag)
		}
	}
	if len(f.NullMarkers) > 0 && f.Flag == "" {
		v.add(SeverityWarning, path+".null_markers", CodeNullMarkersNoFlag, "null_markers without flag: the matched marker is discarded")
	}
}

func (v *validator) compat(path, name string, cf *CanonicalField, typ string, f *Field) {
	if typ == "period" {
		if !periodOutputs[name] {
			v.err(path+".type", CodeTypeMismatch, "type period can only feed period_type, period_start or period_end, not %q", name)
		}
		return
	}
	if cf.Type == "map" {
		v.err(path+".type", CodeTypeMismatch, "canonical field %q is a map and cannot be mapped directly", name)
		return
	}
	if !contains(compatible[cf.Type], typ) {
		v.err(path+".type", CodeTypeMismatch, "type %s is not compatible with canonical %s field %q", typ, cf.Type, name)
	}
}

// constType infers the type of an untyped const from its YAML scalar.
func constType(c any) string {
	switch c.(type) {
	case int, int64, uint64:
		return "int"
	case float64:
		return "decimal"
	case bool:
		return "bool"
	}
	return "string"
}

func validFrom(s string) bool {
	switch {
	case s == "":
		return false
	case strings.HasPrefix(s, "@"):
		return metaCtxRe.MatchString(s)
	case strings.HasPrefix(s, "$"):
		return jsonPathRe.MatchString(s)
	}
	return true
}

// template checks placeholder syntax: {name}, {@ctx.x}, {@meta.x}; no nesting, no empty or unbalanced braces.
func (v *validator) template(path, t string) {
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '}':
			v.err(path, CodeBadTemplate, "unbalanced '}' at position %d", i)
			return
		case '{':
			j := strings.IndexAny(t[i+1:], "{}")
			if j < 0 || t[i+1+j] == '{' {
				v.err(path, CodeBadTemplate, "unclosed or nested '{' at position %d", i)
				return
			}
			ph := strings.TrimSpace(t[i+1 : i+1+j])
			if ph == "" {
				v.err(path, CodeBadTemplate, "empty placeholder at position %d", i)
				return
			}
			if strings.HasPrefix(ph, "@") && !metaCtxRe.MatchString(ph) {
				v.err(path, CodeBadTemplate, "unknown placeholder {%s}: use {@ctx.name} or {@meta.name}", ph)
				return
			}
			i += j + 1
		}
	}
}

// parseCall splits "name(arg, arg)" into name and arguments. Arguments may be quoted with " or '.
func parseCall(s string) (name string, args []string, hasParens bool, ok bool) {
	m := callRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return "", nil, false, false
	}
	name = m[1]
	if m[2] == "" {
		return name, nil, false, true
	}
	body := m[3]
	var cur strings.Builder
	var quote byte
	quoted := false
	flush := func() {
		a := cur.String()
		if !quoted {
			a = strings.TrimSpace(a)
		}
		args = append(args, a)
		cur.Reset()
		quoted = false
	}
	if strings.TrimSpace(body) == "" {
		return name, nil, true, true
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case quote != 0:
			if c == '\\' && i+1 < len(body) && body[i+1] == quote {
				cur.WriteByte(quote)
				i++
			} else if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
		case (c == '"' || c == '\'') && strings.TrimSpace(cur.String()) == "" && !quoted:
			cur.Reset()
			quote, quoted = c, true
		case c == ',':
			flush()
		default:
			if quoted && c == ' ' {
				continue
			}
			cur.WriteByte(c)
		}
	}
	flush()
	return name, args, true, true
}

func (v *validator) transform(path, t string) {
	name, args, _, ok := parseCall(t)
	if !ok {
		v.err(path, CodeUnknownTransform, "%q is not a valid transform", t)
		return
	}
	spec, known := transformNames[name]
	if !known {
		v.err(path, CodeUnknownTransform, "unknown transform %q (allowed: coalesce, concat(sep), lower, regex(pat,repl), split(sep), trim, upper)", name)
		return
	}
	if len(args) < spec.min || len(args) > spec.max {
		v.err(path, CodeTransformArgs, "transform %s takes %d argument(s), got %d", name, spec.min, len(args))
		return
	}
	if name == "regex" {
		if _, err := regexp.Compile(args[0]); err != nil {
			v.err(path, CodeBadRegex, "bad regex %q: %v", args[0], err)
		}
	}
}

func (v *validator) validator(path, s string) {
	name, args, _, ok := parseCall(s)
	if !ok {
		v.err(path, CodeUnknownValidator, "%q is not a valid validator", s)
		return
	}
	switch name {
	case "email", "url":
		if len(args) != 0 {
			v.err(path, CodeValidatorArgs, "validator %s takes no arguments", name)
		}
	case "regex":
		if len(args) != 1 {
			v.err(path, CodeValidatorArgs, "validator regex takes 1 argument, got %d", len(args))
			return
		}
		if _, err := regexp.Compile(args[0]); err != nil {
			v.err(path, CodeBadRegex, "bad regex %q: %v", args[0], err)
		}
	case "range":
		if len(args) != 2 {
			v.err(path, CodeValidatorArgs, "validator range takes 2 arguments (min,max), got %d", len(args))
			return
		}
		lo, e1 := strconv.ParseFloat(args[0], 64)
		hi, e2 := strconv.ParseFloat(args[1], 64)
		if e1 != nil || e2 != nil || lo > hi {
			v.err(path, CodeValidatorArgs, "range(min,max) needs numbers with min <= max")
		}
	default:
		v.err(path, CodeUnknownValidator, "unknown validator %q (allowed: email, url, regex(pat), range(min,max))", name)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
