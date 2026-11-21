package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{"no args prints usage", nil, 0, "migrate serve run mapping", ""},
		{"help", []string{"help"}, 0, "usage: importer", ""},
		{"version", []string{"version"}, 0, version, ""},
		{"planned command not implemented", []string{"serve"}, 1, "", "not implemented"},
		{"unknown exits 2", []string{"bogus"}, 2, "", "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(tt.args, &out, &errb)
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
			if tt.name == "no args prints usage" {
				for _, c := range []string{"migrate", "serve", "run", "mapping"} {
					if !strings.Contains(out.String(), c) {
						t.Errorf("usage missing %q", c)
					}
				}
			} else if !strings.Contains(out.String(), tt.wantStdout) {
				t.Errorf("stdout %q missing %q", out.String(), tt.wantStdout)
			}
			if !strings.Contains(errb.String(), tt.wantStderr) {
				t.Errorf("stderr %q missing %q", errb.String(), tt.wantStderr)
			}
		})
	}
}
