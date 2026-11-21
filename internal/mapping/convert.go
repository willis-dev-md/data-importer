package mapping

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// convert turns a resolved value into the field's type.
func convert(typ string, f *Field, name string, v any) (any, *fieldErr) {
	switch typ {
	case "string":
		s, ok := scalarString(v)
		if !ok {
			return nil, ferr(ErrTypeMismatch, "expected a scalar for a string field, got %T", v)
		}
		return s, nil
	case "int":
		return toInt(v)
	case "decimal":
		return toDecimal(v)
	case "bool":
		return toBool(v)
	case "date", "timestamp":
		return toTime(typ, f.Format, v)
	case "period":
		return toPeriod(f, name, v)
	}
	return nil, ferr(ErrTypeMismatch, "unknown type %q", typ)
}

func toInt(v any) (any, *fieldErr) {
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int8:
		return int64(x), nil
	case int16:
		return int64(x), nil
	case int32:
		return int64(x), nil
	case int64:
		return x, nil
	case uint, uint8, uint16, uint32, uint64:
		s, _ := scalarString(x)
		return parseIntText(s)
	case float32:
		return floatToInt(float64(x))
	case float64:
		return floatToInt(x)
	case string:
		return parseIntText(x)
	}
	if s, ok := scalarString(v); ok {
		if _, isBool := v.(bool); isBool {
			return nil, ferr(ErrTypeMismatch, "expected an int, got a bool")
		}
		return parseIntText(s)
	}
	return nil, ferr(ErrTypeMismatch, "expected an int, got %T", v)
}

func parseIntText(s string) (any, *fieldErr) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return nil, ferr(ErrParseFailed, "%q is not an integer", s)
	}
	return n, nil
}

func floatToInt(f float64) (any, *fieldErr) {
	if f != math.Trunc(f) || f < -9.2e18 || f > 9.2e18 {
		return nil, ferr(ErrParseFailed, "%v is not an integer", f)
	}
	return int64(f), nil
}

// toDecimal emits float64. Known limitation: no exact decimal round trip.
func toDecimal(v any) (any, *fieldErr) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case float32:
		f = float64(x)
	case bool:
		return nil, ferr(ErrTypeMismatch, "expected a number, got a bool")
	default:
		s, ok := scalarString(v)
		if !ok {
			return nil, ferr(ErrTypeMismatch, "expected a number, got %T", v)
		}
		p, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, ferr(ErrParseFailed, "%q is not a number", s)
		}
		f = p
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, ferr(ErrParseFailed, "%v is not a finite number", f)
	}
	return f, nil
}

func toBool(v any) (any, *fieldErr) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	s, ok := scalarString(v)
	if !ok {
		return nil, ferr(ErrTypeMismatch, "expected a bool, got %T", v)
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "y", "1", "t":
		return true, nil
	case "false", "no", "n", "0", "f":
		return false, nil
	}
	return nil, ferr(ErrParseFailed, "%q is not a bool", s)
}

func toTime(typ string, formats []string, v any) (any, *fieldErr) {
	if t, ok := v.(time.Time); ok {
		return finishTime(typ, t), nil
	}
	s, ok := scalarString(v)
	if !ok {
		return nil, ferr(ErrTypeMismatch, "expected a %s, got %T", typ, v)
	}
	s = strings.TrimSpace(s)
	layouts := formats
	if len(layouts) == 0 {
		layouts = []string{"2006-01-02"}
		if typ == "timestamp" {
			layouts = []string{time.RFC3339}
		}
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return finishTime(typ, t), nil
		}
	}
	return nil, ferr(ErrParseFailed, "%q does not match format %s", s, strings.Join(layouts, " | "))
}

func finishTime(typ string, t time.Time) time.Time {
	if typ == "date" {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	return t.UTC()
}
