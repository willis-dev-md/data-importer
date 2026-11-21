package record

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Record is one imported row: its natural key and its structurally parsed payload.
type Record struct {
	Key     string
	Payload map[string]any
}

// Key encodes a natural key as a JSON array of strings, e.g. ["L55O","1989"].
// The encoding is injective: different part lists never give the same key,
// whatever characters the parts contain.
func Key(parts ...string) string {
	if parts == nil {
		parts = []string{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	// Encoding a []string cannot fail.
	_ = enc.Encode(parts)
	return string(bytes.TrimRight(buf.Bytes(), "\n"))
}

// Canonical returns the canonical JSON encoding of payload: object keys sorted
// at every level, no insignificant whitespace, strings escaped without HTML
// escaping, and numbers formatted deterministically (every number as its
// exact value in plain decimal with no exponent and no trailing zeros; floats
// use their shortest round-trip digits, so 1, 1.0 and json.Number("1.0") all
// encode as 1 and 9.3e18 as 9300000000000000000). NaN and infinities are rejected.
// A nil payload encodes as {}.
func Canonical(payload map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	if payload == nil {
		payload = map[string]any{}
	}
	if err := writeValue(&buf, payload); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Sum returns the SHA-256 of the canonical JSON encoding of payload.
func Sum(payload map[string]any) ([]byte, error) {
	c, err := Canonical(payload)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(c)
	return h[:], nil
}

// Hash is Sum for payloads known to be encodable. It returns nil if the
// payload cannot be canonicalised (for example it contains NaN).
func Hash(payload map[string]any) []byte {
	h, err := Sum(payload)
	if err != nil {
		return nil
	}
	return h
}

func writeValue(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(x))
	case string:
		return writeString(buf, x)
	case json.Number:
		return writeNumberString(buf, string(x))
	case float64:
		return writeFloat(buf, x)
	case float32:
		return writeFloat(buf, float64(x))
	case int:
		buf.WriteString(strconv.FormatInt(int64(x), 10))
	case int8:
		buf.WriteString(strconv.FormatInt(int64(x), 10))
	case int16:
		buf.WriteString(strconv.FormatInt(int64(x), 10))
	case int32:
		buf.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		buf.WriteString(strconv.FormatInt(x, 10))
	case uint:
		buf.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint8:
		buf.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint16:
		buf.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint32:
		buf.WriteString(strconv.FormatUint(uint64(x), 10))
	case uint64:
		buf.WriteString(strconv.FormatUint(x, 10))
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeValue(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeString(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeValue(buf, x[k]); err != nil {
				return fmt.Errorf("key %q: %w", k, err)
			}
		}
		buf.WriteByte('}')
	default:
		// Other types (typed slices and maps, structs, time.Time, ...): go through
		// encoding/json once, then canonicalise the generic result.
		raw, err := json.Marshal(x)
		if err != nil {
			return fmt.Errorf("unsupported value of type %T: %w", v, err)
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var generic any
		if err := dec.Decode(&generic); err != nil {
			return fmt.Errorf("unsupported value of type %T: %w", v, err)
		}
		return writeValue(buf, generic)
	}
	return nil
}

func writeString(buf *bytes.Buffer, s string) error {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.Write(bytes.TrimRight(tmp.Bytes(), "\n"))
	return nil
}

// maxDecimalDigits bounds the plain-decimal expansion of a number (PostgreSQL
// numeric itself allows far fewer digits before the point than this).
const maxDecimalDigits = 20000

// writeNumberString writes a JSON number token in canonical form: see plainDecimal.
func writeNumberString(buf *bytes.Buffer, s string) error {
	out, err := plainDecimal(s)
	if err != nil {
		return fmt.Errorf("invalid number %q: %w", s, err)
	}
	buf.WriteString(out)
	return nil
}

func writeFloat(buf *bytes.Buffer, f float64) error {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("cannot encode %v in JSON", f)
	}
	// Shortest round-trip digits, then the same plain-decimal expansion that a
	// json.Number goes through.
	return writeNumberString(buf, strconv.FormatFloat(f, 'e', -1, 64))
}

// plainDecimal normalises a JSON number token to its exact value written as a
// plain decimal: no exponent, no leading zeros, no trailing fractional zeros,
// no "-0". This is how PostgreSQL numeric (and so jsonb) renders a value apart
// from preserving trailing zeros, so the text is identical before and after a
// jsonb round trip.
func plainDecimal(tok string) (string, error) {
	s := tok
	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	mant, expStr := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mant, expStr = s[:i], s[i+1:]
		if expStr == "" {
			return "", errors.New("missing exponent")
		}
	}
	intPart, fracPart := mant, ""
	if i := strings.IndexByte(mant, '.'); i >= 0 {
		intPart, fracPart = mant[:i], mant[i+1:]
		if fracPart == "" {
			return "", errors.New("missing fraction digits")
		}
	}
	if intPart == "" || !allDigits(intPart) || !allDigits(fracPart) {
		return "", errors.New("not a JSON number")
	}
	exp := 0
	if expStr != "" {
		sign := 1
		switch expStr[0] {
		case '+':
			expStr = expStr[1:]
		case '-':
			sign, expStr = -1, expStr[1:]
		}
		if expStr == "" || !allDigits(expStr) || len(expStr) > 6 {
			return "", errors.New("bad exponent")
		}
		e, _ := strconv.Atoi(expStr)
		exp = sign * e
	}
	digits := strings.TrimLeft(intPart+fracPart, "0")
	if digits == "" {
		return "0", nil
	}
	// Value = 0.digits... : decimal point sits pointPos digits into `digits`.
	pointPos := len(intPart) - (len(intPart+fracPart) - len(digits)) + exp
	digits = strings.TrimRight(digits, "0")
	if pointPos > maxDecimalDigits || -pointPos > maxDecimalDigits {
		return "", errors.New("exponent out of range")
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	switch {
	case pointPos <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -pointPos))
		b.WriteString(digits)
	case pointPos >= len(digits):
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", pointPos-len(digits)))
	default:
		b.WriteString(digits[:pointPos])
		b.WriteByte('.')
		b.WriteString(digits[pointPos:])
	}
	return b.String(), nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
