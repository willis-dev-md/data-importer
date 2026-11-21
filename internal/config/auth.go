package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Auth types.
const (
	AuthBearer = "bearer"
	AuthAPIKey = "api_key"
)

// Auth describes how a source authenticates. It holds only the NAME of the
// environment variable that carries the secret; the value is resolved at run
// time (Resolve) and is never part of the config that is stored or printed.
type Auth struct {
	Type     string `yaml:"type"`
	TokenEnv string `yaml:"token_env"`
	Header   string `yaml:"header,omitempty"` // api_key only: the header that carries the key
}

var (
	envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerRE  = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
)

func (a *Auth) validate(bad func(path, format string, args ...any)) {
	switch a.Type {
	case AuthBearer, AuthAPIKey:
	case "":
		bad("auth.type", "required (bearer or api_key)")
	default:
		bad("auth.type", "must be bearer or api_key, got %q", a.Type)
	}
	if !envNameRE.MatchString(a.TokenEnv) {
		bad("auth.token_env", "must be the NAME of an environment variable (letters, digits, underscore)")
	}
	switch {
	case a.Type == AuthAPIKey && !headerRE.MatchString(a.Header):
		bad("auth.header", "required for api_key and must be a valid header name")
	case a.Type == AuthBearer && a.Header != "":
		bad("auth.header", "only valid with type api_key")
	}
}

// MissingSecretError reports an unset or empty environment variable. It names
// the variable, never a value.
type MissingSecretError struct{ Env string }

func (e *MissingSecretError) Error() string {
	return fmt.Sprintf("environment variable %s (auth.token_env) is not set or empty", e.Env)
}

// Resolve reads the secret with getenv and returns the HTTP header name and
// value to send. The value must not be stored or logged.
func (a *Auth) Resolve(getenv func(string) string) (header, value string, err error) {
	v := strings.TrimSpace(getenv(a.TokenEnv))
	if v == "" {
		return "", "", &MissingSecretError{Env: a.TokenEnv}
	}
	if a.Type == AuthAPIKey {
		return a.Header, v, nil
	}
	return "Authorization", "Bearer " + v, nil
}
