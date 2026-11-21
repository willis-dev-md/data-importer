package server

import (
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/willis-dev-md/data-importer/gen/dataimporter/v1"
	"github.com/willis-dev-md/data-importer/internal/mapping"
)

// kind is the comparison type of a filterable field.
type kind int

const (
	kString kind = iota + 1
	kNumber      // int and decimal
	kBool
	kDate
)

func (k kind) String() string {
	switch k {
	case kString:
		return "string"
	case kNumber:
		return "number"
	case kBool:
		return "bool"
	case kDate:
		return "date (string YYYY-MM-DD)"
	}
	return "unknown"
}

const dateLayout = "2006-01-02"

// canonicalKinds lists the filterable canonical fields (canonical-schema.yaml,
// minus the `dimensions` map, which is filtered as dimensions.<key>). A test
// keeps it in step with the schema file.
var canonicalKinds = map[string]kind{
	"series_id":    kString,
	"series_title": kString,
	"measure":      kString,
	"statistic":    kString,
	"period_type":  kString,
	"period_start": kDate,
	"period_end":   kDate,
	"value":        kNumber,
	"unit":         kString,
	"status":       kString,
	"quality_flag": kString,
}

const dimPrefix = "dimensions."

// compiled is a validated filter.
type compiled struct {
	field string // as given, e.g. "value" or "dimensions.area"
	dim   string // dimension key when the field is dimensions.<key>
	isDim bool
	op    pb.FilterOp
	kind  kind
	null  bool  // value is null (EQ / NEQ only)
	vals  []any // normalised: string (dates as YYYY-MM-DD), float64 or bool; one entry unless IN
}

func invalid(format string, args ...any) error {
	return status.Errorf(codes.InvalidArgument, format, args...)
}

func compileFilters(fs []*pb.Filter) ([]*compiled, error) {
	out := make([]*compiled, 0, len(fs))
	for _, f := range fs {
		c, err := compileFilter(f)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func compileFilter(f *pb.Filter) (*compiled, error) {
	if f == nil || f.Field == "" {
		return nil, invalid("filter field is empty")
	}
	c := &compiled{field: f.Field, op: f.Op}
	switch f.Op {
	case pb.FilterOp_FILTER_OP_EQ, pb.FilterOp_FILTER_OP_NEQ, pb.FilterOp_FILTER_OP_GT, pb.FilterOp_FILTER_OP_GTE,
		pb.FilterOp_FILTER_OP_LT, pb.FilterOp_FILTER_OP_LTE, pb.FilterOp_FILTER_OP_CONTAINS, pb.FilterOp_FILTER_OP_IN:
	default:
		return nil, invalid("filter on field %q: operator %v is not supported", f.Field, f.Op)
	}
	switch {
	case strings.HasPrefix(f.Field, dimPrefix):
		c.dim = f.Field[len(dimPrefix):]
		if c.dim == "" {
			return nil, invalid("filter field %q: a dimension key is required (dimensions.<key>)", f.Field)
		}
		c.isDim, c.kind = true, kString
		switch f.Op {
		case pb.FilterOp_FILTER_OP_EQ, pb.FilterOp_FILTER_OP_NEQ, pb.FilterOp_FILTER_OP_IN:
		default:
			return nil, invalid("filter on field %q: dimensions support only EQ, NEQ and IN, got %v", f.Field, f.Op)
		}
	case f.Field == "dimensions":
		return nil, invalid("filter field %q is not filterable: use dimensions.<key>", f.Field)
	default:
		k, ok := canonicalKinds[f.Field]
		if !ok {
			return nil, invalid("filter field %q is unknown or not filterable", f.Field)
		}
		c.kind = k
	}

	switch f.Op {
	case pb.FilterOp_FILTER_OP_GT, pb.FilterOp_FILTER_OP_GTE, pb.FilterOp_FILTER_OP_LT, pb.FilterOp_FILTER_OP_LTE:
		if c.kind == kBool {
			return nil, invalid("filter on field %q: %v is not supported on a bool field", f.Field, f.Op)
		}
	case pb.FilterOp_FILTER_OP_CONTAINS:
		if c.kind != kString {
			return nil, invalid("filter on field %q: CONTAINS is only supported on string fields", f.Field)
		}
	}

	if f.Value == nil || f.Value.Kind == nil {
		return nil, invalid("filter on field %q: value is required", f.Field)
	}
	if _, isNull := f.Value.Kind.(*structpb.Value_NullValue); isNull {
		if c.isDim || (f.Op != pb.FilterOp_FILTER_OP_EQ && f.Op != pb.FilterOp_FILTER_OP_NEQ) {
			return nil, invalid("filter on field %q: a null value is only allowed with EQ or NEQ on a canonical field", f.Field)
		}
		c.null = true
		return c, nil
	}
	if f.Op == pb.FilterOp_FILTER_OP_IN {
		l, ok := f.Value.Kind.(*structpb.Value_ListValue)
		if !ok {
			return nil, invalid("filter on field %q: IN needs a list value", f.Field)
		}
		for _, e := range l.ListValue.GetValues() {
			v, err := scalar(c, e)
			if err != nil {
				return nil, err
			}
			c.vals = append(c.vals, v)
		}
		return c, nil
	}
	v, err := scalar(c, f.Value)
	if err != nil {
		return nil, err
	}
	c.vals = []any{v}
	return c, nil
}

// scalar checks that v has the type of the field and normalises it.
func scalar(c *compiled, v *structpb.Value) (any, error) {
	bad := func() error {
		return invalid("filter on field %q: value has the wrong type, expected %v", c.field, c.kind)
	}
	switch c.kind {
	case kString:
		if s, ok := v.GetKind().(*structpb.Value_StringValue); ok {
			return s.StringValue, nil
		}
	case kNumber:
		if n, ok := v.GetKind().(*structpb.Value_NumberValue); ok {
			return n.NumberValue, nil
		}
	case kBool:
		if b, ok := v.GetKind().(*structpb.Value_BoolValue); ok {
			return b.BoolValue, nil
		}
	case kDate:
		if s, ok := v.GetKind().(*structpb.Value_StringValue); ok {
			t, err := time.Parse(dateLayout, s.StringValue)
			if err != nil {
				return nil, invalid("filter on field %q: %q is not a date (YYYY-MM-DD)", c.field, s.StringValue)
			}
			return t.Format(dateLayout), nil
		}
	}
	return nil, bad()
}

// actualValue normalises a mapped value to string, float64, bool or nil.
func actualValue(v any) any {
	switch x := v.(type) {
	case string, bool, float64:
		return x
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case time.Time:
		return x.UTC().Format(dateLayout)
	}
	return nil
}

func compareVals(a, b any) (int, bool) {
	switch x := a.(type) {
	case string:
		y, ok := b.(string)
		return strings.Compare(x, y), ok
	case float64:
		y, ok := b.(float64)
		switch {
		case !ok:
			return 0, false
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	case bool:
		y, ok := b.(bool)
		if x == y {
			return 0, ok
		}
		return 1, ok
	}
	return 0, false
}

// matches reports whether the mapped record passes the filter. A field that has
// a mapping error, or a dimension that is absent, never matches. A null or
// omitted canonical field matches only "EQ null".
func (c *compiled) matches(res mapping.Result, errFields map[string]bool) bool {
	// Mapping errors are keyed per dimension ("dimensions.<key>"); an error on
	// "dimensions" as a whole also disqualifies every dimension filter.
	if errFields[c.field] || (c.isDim && errFields["dimensions"]) {
		return false
	}
	var actual any
	if c.isDim {
		dims, _ := res.Fields["dimensions"].(map[string]string)
		s, ok := dims[c.dim]
		if !ok {
			return false
		}
		actual = s
	} else {
		actual = actualValue(res.Fields[c.field])
	}
	if c.null {
		return (actual == nil) == (c.op == pb.FilterOp_FILTER_OP_EQ)
	}
	if actual == nil {
		return false
	}
	switch c.op {
	case pb.FilterOp_FILTER_OP_IN:
		for _, v := range c.vals {
			if n, ok := compareVals(actual, v); ok && n == 0 {
				return true
			}
		}
		return false
	case pb.FilterOp_FILTER_OP_CONTAINS:
		s, ok := actual.(string)
		return ok && strings.Contains(s, c.vals[0].(string))
	}
	n, ok := compareVals(actual, c.vals[0])
	if !ok {
		return false
	}
	switch c.op {
	case pb.FilterOp_FILTER_OP_EQ:
		return n == 0
	case pb.FilterOp_FILTER_OP_NEQ:
		return n != 0
	case pb.FilterOp_FILTER_OP_GT:
		return n > 0
	case pb.FilterOp_FILTER_OP_GTE:
		return n >= 0
	case pb.FilterOp_FILTER_OP_LT:
		return n < 0
	case pb.FilterOp_FILTER_OP_LTE:
		return n <= 0
	}
	return false
}
