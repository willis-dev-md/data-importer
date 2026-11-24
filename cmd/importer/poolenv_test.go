package main

import (
	"strings"
	"testing"
)

// Invalid pool/lock settings are usage errors (exit 2) reported before any
// database work: DATABASE_URL points at a closed port, and a connection attempt
// would give exit 1 with "connect to database".
func TestInvalidPoolEnvIsUsageError(t *testing.T) {
	c := &cli{t: t, bin: buildBinary(t), url: "postgres://postgres@127.0.0.1:1/x?sslmode=disable&connect_timeout=2"}
	cases := []struct{ name, val, want string }{
		{"DB_MAX_CONNS", "0", "positive integer"},
		{"DB_MAX_CONNS", "abc", "positive integer"},
		{"DB_LOCK_MAX_CONNS", "-1", "positive integer"},
		{"DB_LOCK_MAX_CONNS", "x", "positive integer"},
		{"DB_LOCK_WAIT", "0", "positive Go duration such as 30s"},
		{"DB_LOCK_WAIT", "xx", "positive Go duration such as 30s"},
		{"DB_LOCK_CHECK_INTERVAL", "0", "Go duration between"},
		{"DB_LOCK_CHECK_INTERVAL", "1h", "Go duration between"},
		{"DB_LOCK_CHECK_INTERVAL", "often", "Go duration between"},
	}
	dir := t.TempDir()
	for _, f := range []string{"ons-l55o.source.yaml", "ons-l55o.mapping.yaml"} {
		writeFile(t, dir, f, readFile(t, "../../docs/examples/"+f))
	}
	cmds := [][]string{
		{"run", "--source", "s"},
		{"serve"},
		{"sources", "apply", "--dir", dir, "--schema", "../../canonical-schema.yaml"},
	}
	for _, tc := range cases {
		for _, args := range cmds {
			stdout, stderr, code := c.do([]string{tc.name + "=" + tc.val}, args...)
			if code != 2 {
				t.Errorf("%s=%s %v: exit %d, want 2 (stderr %q)", tc.name, tc.val, args, code, stderr)
			}
			if !strings.Contains(stderr, tc.name+"=") || !strings.Contains(stderr, tc.want) {
				t.Errorf("%s=%s %v: stderr %q does not name the variable and form", tc.name, tc.val, args, stderr)
			}
			for _, bad := range []string{"connect to database", "panic", "goroutine", "invalid configuration"} {
				if strings.Contains(stderr+stdout, bad) {
					t.Errorf("%s=%s %v: output contains %q: %q", tc.name, tc.val, args, bad, stderr)
				}
			}
		}
	}
}
