package mapping

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FieldError codes reported by Eval.
const (
	ErrMissingRequired  = "MISSING_REQUIRED"
	ErrTypeMismatch     = "TYPE_MISMATCH"
	ErrParseFailed      = "PARSE_FAILED"
	ErrValidationFailed = "VALIDATION_FAILED"
)

// FieldError is one per-field evaluation failure. Field is the canonical field name, or
// "dimensions.<name>" for a dimension entry.
type FieldError struct {
	Field   string
	Code    string
	Message string
}

// Result is the outcome of evaluating a mapping against one raw payload.
//
// Fields holds the canonical fields by name (value types: string, int64, float64, bool, time.Time;
// period_type is a string) plus "dimensions" (map[string]string, always present). A field whose value
// matched a null marker is present with a nil value. Extra holds unmapped payload keys (never nil).
type Result struct {
	Fields map[string]any
	Extra  map[string]any
	Errors []FieldError
}

// fieldErr is an evaluation failure before the field name is attached.
type fieldErr struct{ code, msg string }

func (e *fieldErr) Error() string { return e.code + ": " + e.msg }

func ferr(code, format string, args ...any) *fieldErr {
	return &fieldErr{code: code, msg: fmt.Sprintf(format, args...)}
}

// out is the successful outcome of one field.
type out struct {
	value  any
	set    bool   // false: the field is omitted (optional and empty)
	marker string // matched null marker, written to the flag field
	isMark bool
}

type evaluator struct {
	payload  map[string]any
	consumed map[string]bool
}

// Eval maps one raw payload through a mapping. A failing field never aborts the others; Eval never panics.
// Errors are ordered: canonical fields by name, then dimensions by name.
func Eval(m *Mapping, payload map[string]any) Result {
	res := Result{Fields: map[string]any{}, Extra: map[string]any{}}
	dims := map[string]string{}
	res.Fields["dimensions"] = dims
	if m == nil {
		return res
	}
	ev := &evaluator{payload: payload, consumed: map[string]bool{}}
	flags := map[string]string{}
	var flagOrder []string

	for _, name := range sortedKeys(m.Fields) {
		if name == "dimensions" {
			continue
		}
		f := m.Fields[name]
		if f == nil {
			continue
		}
		o, fe := ev.safeField(name, f)
		if fe != nil {
			res.Errors = append(res.Errors, FieldError{Field: name, Code: fe.code, Message: fe.msg})
			continue
		}
		if !o.set {
			continue
		}
		res.Fields[name] = o.value
		if o.isMark && f.Flag != "" {
			if _, dup := flags[f.Flag]; !dup {
				flagOrder = append(flagOrder, f.Flag)
				flags[f.Flag] = o.marker
			}
		}
	}
	for _, name := range sortedKeys(m.Dimensions) {
		f := m.Dimensions[name]
		if f == nil {
			continue
		}
		o, fe := ev.safeField("dimensions."+name, f)
		if fe != nil {
			res.Errors = append(res.Errors, FieldError{Field: "dimensions." + name, Code: fe.code, Message: fe.msg})
			continue
		}
		if s, ok := o.value.(string); ok && o.set && s != "" {
			dims[name] = s
		} else if o.set && o.value != nil {
			if s, ok := scalarString(o.value); ok && s != "" {
				dims[name] = s
			}
		}
	}
	for _, flag := range flagOrder {
		if cur, ok := res.Fields[flag]; !ok || cur == nil {
			res.Fields[flag] = flags[flag]
		}
	}
	if m.Extra == "keep" {
		for k, v := range payload {
			if !ev.consumed[k] && !strings.HasPrefix(k, "_") {
				res.Extra[k] = v
			}
		}
	}
	return res
}

// safeField evaluates one field and converts a panic (a bug) into a PARSE_FAILED error.
func (ev *evaluator) safeField(name string, f *Field) (o out, fe *fieldErr) {
	defer func() {
		if r := recover(); r != nil {
			o, fe = out{}, ferr(ErrParseFailed, "internal error evaluating field: %v", r)
		}
	}()
	return ev.field(name, f)
}

func (ev *evaluator) field(name string, f *Field) (out, *fieldErr) {
	typ := f.Type
	if typ == "" {
		typ = "string"
		if f.Const != nil && f.Template == "" && len(f.From) == 0 {
			typ = constType(f.Const)
		}
	}
	var in any
	switch {
	case len(f.From) > 0:
		vals := make([]any, len(f.From))
		for i, ref := range f.From {
			vals[i], _ = ev.resolve(ref)
		}
		if len(vals) == 1 {
			in = vals[0]
		} else {
			in = vals
		}
	case f.Template != "":
		s, fe := ev.template(f.Template, f.EmptyOK)
		if fe != nil {
			return out{}, fe
		}
		in = s
	case f.Const != nil:
		in = f.Const
	}

	// null markers: the field becomes null and the marker goes to the flag field
	if len(f.NullMarkers) > 0 {
		if s, ok := scalarString(in); ok {
			s = strings.TrimSpace(s)
			for _, mk := range f.NullMarkers {
				if s != "" && s == strings.TrimSpace(mk) {
					return out{value: nil, set: true, marker: s, isMark: true}, nil
				}
			}
		}
	}

	in, fe := applyTransforms(in, f.Transform)
	if fe != nil {
		return out{}, fe
	}
	in = collapse(in)

	if in == nil {
		switch {
		case f.Default != nil:
			in = f.Default
		case f.Required:
			return out{}, ferr(ErrMissingRequired, "required field has no value")
		default:
			return out{}, nil
		}
	} else if f.Map != nil {
		if s, ok := scalarString(in); ok {
			if v, found := f.Map[s]; found {
				in = v
			} else if v, found := f.Map[strings.TrimSpace(s)]; found {
				in = v
			}
		}
	}

	v, fe := convert(typ, f, name, in)
	if fe != nil {
		return out{}, fe
	}
	if f.Validate != "" {
		if fe := runValidator(f.Validate, v); fe != nil {
			return out{}, fe
		}
	}
	return out{value: v, set: true}, nil
}

// collapse turns a list into a single string (non-empty trimmed elements joined with a space) and
// an empty or blank string into nil.
func collapse(v any) any {
	if list, ok := v.([]any); ok {
		parts := flatten(list)
		if len(parts) == 0 {
			return nil
		}
		return strings.Join(parts, " ")
	}
	if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
		return nil
	}
	return v
}

// flatten returns the trimmed, non-empty scalar texts of a (possibly nested) list.
func flatten(list []any) []string {
	var parts []string
	for _, e := range list {
		switch x := e.(type) {
		case []any:
			parts = append(parts, flatten(x)...)
		default:
			if s, ok := scalarString(e); ok {
				if s = strings.TrimSpace(s); s != "" {
					parts = append(parts, s)
				}
			}
		}
	}
	return parts
}

// scalarString renders a JSON-like scalar as text. ok is false for nil, maps and lists.
func scalarString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case int:
		return strconv.FormatInt(int64(x), 10), true
	case int8:
		return strconv.FormatInt(int64(x), 10), true
	case int16:
		return strconv.FormatInt(int64(x), 10), true
	case int32:
		return strconv.FormatInt(int64(x), 10), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case uint:
		return strconv.FormatUint(uint64(x), 10), true
	case uint8:
		return strconv.FormatUint(uint64(x), 10), true
	case uint16:
		return strconv.FormatUint(uint64(x), 10), true
	case uint32:
		return strconv.FormatUint(uint64(x), 10), true
	case uint64:
		return strconv.FormatUint(x, 10), true
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case json.Number:
		return x.String(), true
	case time.Time:
		return x.Format(time.RFC3339), true
	}
	return "", false
}

// template renders "{ref}" placeholders. An empty placeholder is MISSING_REQUIRED unless emptyOK.
func (ev *evaluator) template(t string, emptyOK bool) (string, *fieldErr) {
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		c := t[i]
		if c != '{' {
			b.WriteByte(c)
			continue
		}
		j := strings.IndexByte(t[i+1:], '}')
		if j < 0 {
			return "", ferr(ErrParseFailed, "template has an unclosed '{'")
		}
		ref := strings.TrimSpace(t[i+1 : i+1+j])
		i += j + 1
		v, _ := ev.resolve(ref)
		v = collapse(v)
		s, ok := scalarString(v)
		if !ok || strings.TrimSpace(s) == "" {
			if !emptyOK {
				return "", ferr(ErrMissingRequired, "template placeholder {%s} is empty", ref)
			}
			continue
		}
		b.WriteString(s)
	}
	return b.String(), nil
}

// resolve reads one input reference: a column name, a JSONPath, @meta.X or @ctx.X.
func (ev *evaluator) resolve(ref string) (any, bool) {
	switch {
	case strings.HasPrefix(ref, "@meta."):
		return lookupNested(ev.payload["_meta"], ref[len("@meta."):])
	case strings.HasPrefix(ref, "@ctx."):
		return lookupNested(ev.payload["_ctx"], ref[len("@ctx."):])
	case strings.HasPrefix(ref, "$"):
		segs, ok := parseJSONPath(ref)
		if !ok {
			return nil, false
		}
		if len(segs) > 0 && segs[0].kind == segKey {
			ev.consumed[segs[0].key] = true
		}
		return walk(ev.payload, segs)
	}
	ev.consumed[ref] = true
	v, ok := ev.payload[ref]
	return v, ok && v != nil
}

func lookupNested(container any, key string) (any, bool) {
	switch m := container.(type) {
	case map[string]any:
		v, ok := m[key]
		return v, ok && v != nil
	case map[string]string:
		v, ok := m[key]
		return v, ok
	}
	return nil, false
}

const (
	segKey = iota
	segIdx
	segAll
)

type seg struct {
	kind int
	key  string
	idx  int
}

// parseJSONPath parses the supported subset: $.a.b, $.a[0], $.a[*].
func parseJSONPath(p string) ([]seg, bool) {
	if !strings.HasPrefix(p, "$") {
		return nil, false
	}
	var segs []seg
	i := 1
	for i < len(p) {
		switch p[i] {
		case '.':
			j := i + 1
			for j < len(p) && p[j] != '.' && p[j] != '[' {
				j++
			}
			if j == i+1 {
				return nil, false
			}
			segs = append(segs, seg{kind: segKey, key: p[i+1 : j]})
			i = j
		case '[':
			j := strings.IndexByte(p[i:], ']')
			if j < 0 {
				return nil, false
			}
			inner := p[i+1 : i+j]
			if inner == "*" {
				segs = append(segs, seg{kind: segAll})
			} else {
				n, err := strconv.Atoi(inner)
				if err != nil || n < 0 {
					return nil, false
				}
				segs = append(segs, seg{kind: segIdx, idx: n})
			}
			i += j + 1
		default:
			return nil, false
		}
	}
	return segs, true
}

func walk(v any, segs []seg) (any, bool) {
	if len(segs) == 0 {
		return v, v != nil
	}
	s := segs[0]
	switch s.kind {
	case segKey:
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		nv, ok := m[s.key]
		if !ok {
			return nil, false
		}
		return walk(nv, segs[1:])
	case segIdx:
		l, ok := v.([]any)
		if !ok || s.idx >= len(l) {
			return nil, false
		}
		return walk(l[s.idx], segs[1:])
	default:
		l, ok := v.([]any)
		if !ok {
			return nil, false
		}
		var res []any
		for _, e := range l {
			if r, ok := walk(e, segs[1:]); ok {
				res = append(res, r)
			}
		}
		if len(res) == 0 {
			return nil, false
		}
		return res, true
	}
}
