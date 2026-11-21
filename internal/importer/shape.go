package importer

import (
	"encoding/json"
	"strings"
)

// shape counts, per top-level field name, the observed value types.
type shape map[string]map[string]int

func newShape() shape { return shape{} }

// Add records the fields of one record. Keys starting with "_" are skipped.
func (s shape) Add(fields map[string]any) {
	for k, v := range fields {
		if strings.HasPrefix(k, "_") {
			continue
		}
		m := s[k]
		if m == nil {
			m = map[string]int{}
			s[k] = m
		}
		m[typeName(v)]++
	}
}

// JSON encodes the shape, e.g. {"value":{"string":642}}.
func (s shape) JSON() json.RawMessage {
	b, err := json.Marshal(map[string]map[string]int(s))
	if err != nil { // cannot happen for map[string]map[string]int
		return json.RawMessage(`{}`)
	}
	return b
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	case map[string]any, map[string]string:
		return "object"
	case []any, []string:
		return "array"
	default:
		return "other"
	}
}
