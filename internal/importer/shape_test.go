package importer

import (
	"encoding/json"
	"testing"
)

func TestShape(t *testing.T) {
	s := newShape()
	s.Add(map[string]any{"value": "1", "period": "1989", "_meta": map[string]string{"a": "b"}, "_source": map[string]any{}})
	s.Add(map[string]any{"value": json.Number("2"), "flag": true, "x": nil, "o": map[string]any{}, "l": []any{}})
	s.Add(map[string]any{"value": 3.5})
	want := `{"flag":{"boolean":1},"l":{"array":1},"o":{"object":1},"period":{"string":1},"value":{"number":2,"string":1},"x":{"null":1}}`
	if got := string(s.JSON()); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := string(newShape().JSON()); got != `{}` {
		t.Fatalf("empty shape %s", got)
	}
}

func TestExceeds(t *testing.T) {
	cases := []struct {
		failed, total int
		ratio         float64
		want          bool
	}{
		{0, 0, 0, false}, {0, 10, 0, false}, {1, 10, 0, true}, {1, 10, 0.1, false},
		{2, 10, 0.1, true}, {1, 100, 0.01, false}, {3, 10, 0.3, false}, {1, 3, 1.0 / 3, false}, {1, 2, 0.4, true},
	}
	for _, c := range cases {
		if got := exceeds(c.failed, c.total, c.ratio); got != c.want {
			t.Errorf("exceeds(%d,%d,%v)=%v want %v", c.failed, c.total, c.ratio, got, c.want)
		}
	}
}
