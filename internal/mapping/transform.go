package mapping

import (
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var regexCache sync.Map // pattern -> *regexp.Regexp

func compileRegex(pat string) (*regexp.Regexp, error) {
	if re, ok := regexCache.Load(pat); ok {
		return re.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, err
	}
	regexCache.Store(pat, re)
	return re, nil
}

// applyTransforms runs the closed transform set in order. A list value (several inputs, a split result or a
// JSONPath wildcard) is handled by concat and coalesce; the string transforms apply to each element.
func applyTransforms(v any, transforms []string) (any, *fieldErr) {
	for _, t := range transforms {
		name, args, _, ok := parseCall(t)
		if !ok {
			return nil, ferr(ErrParseFailed, "invalid transform %q", t)
		}
		switch name {
		case "trim":
			v = mapStrings(v, strings.TrimSpace)
		case "lower":
			v = mapStrings(v, strings.ToLower)
		case "upper":
			v = mapStrings(v, strings.ToUpper)
		case "concat":
			if len(args) != 1 {
				return nil, ferr(ErrParseFailed, "transform concat takes 1 argument")
			}
			if l, ok := v.([]any); ok {
				parts := flatten(l)
				if len(parts) == 0 {
					v = nil
				} else {
					v = strings.Join(parts, args[0])
				}
			}
		case "split":
			if len(args) != 1 {
				return nil, ferr(ErrParseFailed, "transform split takes 1 argument")
			}
			if s, ok := v.(string); ok && args[0] != "" {
				var l []any
				for _, p := range strings.Split(s, args[0]) {
					l = append(l, p)
				}
				v = l
			}
		case "regex":
			if len(args) != 2 {
				return nil, ferr(ErrParseFailed, "transform regex takes 2 arguments")
			}
			re, err := compileRegex(args[0])
			if err != nil {
				return nil, ferr(ErrParseFailed, "bad regex %q: %v", args[0], err)
			}
			v = mapStrings(v, func(s string) string { return re.ReplaceAllString(s, args[1]) })
		case "coalesce":
			if l, ok := v.([]any); ok {
				v = nil
				if parts := flatten(l); len(parts) > 0 {
					v = parts[0]
				}
			}
		default:
			return nil, ferr(ErrParseFailed, "unknown transform %q", name)
		}
	}
	return v, nil
}

func mapStrings(v any, fn func(string) string) any {
	switch x := v.(type) {
	case string:
		return fn(x)
	case []any:
		res := make([]any, len(x))
		for i, e := range x {
			res[i] = mapStrings(e, fn)
		}
		return res
	}
	return v
}

// runValidator checks a converted value with email, url, regex(pat) or range(min,max).
func runValidator(spec string, v any) *fieldErr {
	name, args, _, ok := parseCall(spec)
	if !ok {
		return ferr(ErrValidationFailed, "invalid validator %q", spec)
	}
	s, _ := scalarString(v)
	switch name {
	case "email":
		a, err := mail.ParseAddress(s)
		if err != nil || a.Address != s {
			return ferr(ErrValidationFailed, "%q is not an email address", s)
		}
	case "url":
		u, err := url.Parse(s)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return ferr(ErrValidationFailed, "%q is not a URL", s)
		}
	case "regex":
		if len(args) != 1 {
			return ferr(ErrValidationFailed, "validator regex takes 1 argument")
		}
		re, err := compileRegex(args[0])
		if err != nil {
			return ferr(ErrValidationFailed, "bad regex %q: %v", args[0], err)
		}
		if !re.MatchString(s) {
			return ferr(ErrValidationFailed, "%q does not match %s", s, args[0])
		}
	case "range":
		if len(args) != 2 {
			return ferr(ErrValidationFailed, "validator range takes 2 arguments")
		}
		lo, e1 := strconv.ParseFloat(args[0], 64)
		hi, e2 := strconv.ParseFloat(args[1], 64)
		if e1 != nil || e2 != nil {
			return ferr(ErrValidationFailed, "range needs numeric bounds")
		}
		var n float64
		switch x := v.(type) {
		case float64:
			n = x
		case int64:
			n = float64(x)
		default:
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return ferr(ErrTypeMismatch, "range needs a number, got %q", s)
			}
			n = f
		}
		if n < lo || n > hi {
			return ferr(ErrValidationFailed, "%v is outside %s..%s", n, args[0], args[1])
		}
	default:
		return ferr(ErrValidationFailed, "unknown validator %q", name)
	}
	return nil
}
