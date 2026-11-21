package record

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"math/rand"
	"testing"
)

func TestKey(t *testing.T) {
	tests := []struct {
		parts []string
		want  string
	}{
		{[]string{"L55O", "1989"}, `["L55O","1989"]`},
		{nil, `[]`},
		{[]string{""}, `[""]`},
		{[]string{"a|b", `q"uote`}, `["a|b","q\"uote"]`},
		{[]string{"<&>"}, `["<&>"]`},
	}
	for _, tt := range tests {
		if got := Key(tt.parts...); got != tt.want {
			t.Errorf("Key(%q) = %s, want %s", tt.parts, got, tt.want)
		}
	}
}

func TestKeyInjective(t *testing.T) {
	alphabet := []string{"", "a", "|", `"`, `\`, `","`, "a|b", "[", "]", ","}
	rng := rand.New(rand.NewSource(1))
	seen := map[string][]string{}
	for i := 0; i < 20000; i++ {
		n := rng.Intn(4)
		parts := make([]string, n)
		for j := range parts {
			parts[j] = alphabet[rng.Intn(len(alphabet))] + alphabet[rng.Intn(len(alphabet))]
		}
		k := Key(parts...)
		var back []string
		if err := json.Unmarshal([]byte(k), &back); err != nil {
			t.Fatalf("Key(%q) = %s is not a JSON array: %v", parts, k, err)
		}
		if len(back) != len(parts) {
			t.Fatalf("round trip of %q lost parts: %q", parts, back)
		}
		for j := range parts {
			if back[j] != parts[j] {
				t.Fatalf("round trip of %q gave %q", parts, back)
			}
		}
		if prev, ok := seen[k]; ok && !equal(prev, parts) {
			t.Fatalf("collision: %q and %q both give %s", prev, parts, k)
		}
		seen[k] = parts
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCanonical(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"nil", nil, `{}`},
		{"sorted nested", map[string]any{"b": 1, "a": map[string]any{"z": nil, "y": []any{true, "x"}}}, `{"a":{"y":[true,"x"],"z":null},"b":1}`},
		{"numbers", map[string]any{"a": 1.0, "b": int64(-3), "c": json.Number("2.0"), "d": 0.5, "e": math.Copysign(0, -1), "f": uint64(math.MaxUint64)}, `{"a":1,"b":-3,"c":2,"d":0.5,"e":0,"f":18446744073709551615}`},
		{"big floats", map[string]any{"a": 9.3e18, "b": 1.8446744073709552e19, "c": 1e21, "d": json.Number("9300000000000000000")}, `{"a":9300000000000000000,"b":18446744073709552000,"c":1000000000000000000000,"d":9300000000000000000}`},
		{"small and trailing zeros", map[string]any{"a": 1e-7, "b": 0.1, "c": json.Number("1.500"), "d": json.Number("0.10E1"), "e": json.Number("-0.0"), "f": 5e-324 > 0}, `{"a":0.0000001,"b":0.1,"c":1.5,"d":1,"e":0,"f":true}`},
		{"no html escape", map[string]any{"k": "<a&b>"}, `{"k":"<a&b>"}`},
		{"typed slice and map", map[string]any{"s": []string{"b", "a"}, "m": map[string]int{"y": 2, "x": 1}}, `{"m":{"x":1,"y":2},"s":["b","a"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonical(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCanonicalRejectsNaN(t *testing.T) {
	for _, v := range []any{math.NaN(), math.Inf(1), []any{math.Inf(-1)}, json.Number("abc")} {
		if _, err := Canonical(map[string]any{"v": v}); err == nil {
			t.Errorf("Canonical(%v) = nil error, want error", v)
		}
		if Hash(map[string]any{"v": v}) != nil {
			t.Errorf("Hash(%v) not nil", v)
		}
	}
}

func TestHashKnownAnswer(t *testing.T) {
	p := map[string]any{
		"c": map[string]any{"e": "é<", "d": 1.5},
		"b": []any{true, nil, "x"},
		"a": 1,
	}
	const want = "873e2a0eece715e110cd29145190d4571bfe54b962b5f5b6f4af45efb6c9647e" // sha256 of {"a":1,"b":[true,null,"x"],"c":{"d":1.5,"e":"é<"}}
	if got := hex.EncodeToString(Hash(p)); got != want {
		t.Errorf("Hash = %s, want %s", got, want)
	}
}

func TestHashSensitivity(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{"a": "x", "n": map[string]any{"l": []any{1, 2, map[string]any{"k": "v"}}}}
	}
	h0 := Hash(base())
	if !bytes.Equal(h0, Hash(base())) {
		t.Fatal("hash not deterministic")
	}
	mutations := map[string]func(m map[string]any){
		"top value":    func(m map[string]any) { m["a"] = "y" },
		"nested value": func(m map[string]any) { m["n"].(map[string]any)["l"].([]any)[2].(map[string]any)["k"] = "w" },
		"array order":  func(m map[string]any) { l := m["n"].(map[string]any)["l"].([]any); l[0], l[1] = l[1], l[0] },
		"array append": func(m map[string]any) { m["n"].(map[string]any)["l"] = append(m["n"].(map[string]any)["l"].([]any), 3) },
		"new key":      func(m map[string]any) { m["extra"] = nil },
		"type change":  func(m map[string]any) { m["a"] = 1 },
		"string vs num": func(m map[string]any) {
			m["n"].(map[string]any)["l"].([]any)[0] = "1"
		},
	}
	for name, mut := range mutations {
		m := base()
		mut(m)
		if bytes.Equal(h0, Hash(m)) {
			t.Errorf("%s: hash unchanged", name)
		}
	}
}

func TestHashKeyOrderIndependent(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	want := Hash(build(keys))
	for i := 0; i < 50; i++ {
		rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
		if !bytes.Equal(want, Hash(build(keys))) {
			t.Fatalf("hash depends on insertion order %v", keys)
		}
	}
}

func build(keys []string) map[string]any {
	m := map[string]any{}
	for _, k := range keys {
		m[k] = map[string]any{"v": int(k[0]), "w": []any{k}}
	}
	return m
}

// The hash must survive a Postgres jsonb round trip, which yields json.Number.
func TestHashStableAcrossJSONRoundTrip(t *testing.T) {
	p := map[string]any{"i": 3, "f": 2.5, "big": 123456789012345680.0, "small": 1e-7, "s": "x", "int64": int64(9007199254740993)}
	c, err := Canonical(p)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(c))
	dec.UseNumber()
	var back map[string]any
	if err := dec.Decode(&back); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(Hash(p), Hash(back)) {
		t.Errorf("hash changed across round trip: %s", c)
	}
}

func TestCanonicalFloatEqualsNumber(t *testing.T) {
	for _, f := range []float64{9.3e18, 1.8446744073709552e19, 1e21, 1e-7, 0.1, 5e-324, math.MaxFloat64, -math.MaxFloat64, 1.0 / 3, 123456.789, 0} {
		a, err := Canonical(map[string]any{"v": f})
		if err != nil {
			t.Fatal(err)
		}
		// The plain-decimal text, as jsonb renders it, hashes the same.
		b, err := Canonical(map[string]any{"v": json.Number(string(a[5 : len(a)-1]))})
		if err != nil || !bytes.Equal(a, b) {
			t.Errorf("%v: %s vs %s (%v)", f, a, b, err)
		}
	}
}
