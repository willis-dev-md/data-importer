package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/willis-dev-md/data-importer/internal/testutil"
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

func TestMigrateCommand(t *testing.T) {
	url := testutil.NewDatabaseURL(t)
	tests := []struct {
		name     string
		args     []string
		url      string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"missing url", []string{"up"}, "", 1, "", "DATABASE_URL is not set"},
		{"no direction", nil, url, 2, "", "usage"},
		{"bad direction", []string{"sideways"}, url, 2, "", "unknown direction"},
		{"unreachable", []string{"up"}, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2", 1, "", "importer migrate up"},
		{"version before", []string{"version"}, url, 0, "version 0", ""},
		{"up", []string{"up"}, url, 0, "version 1", ""},
		{"up twice", []string{"up"}, url, 0, "version 1", ""},
		{"down", []string{"down"}, url, 0, "version 0", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := runMigrate(tt.args, tt.url, &out, &errb)
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d (stderr %q)", code, tt.wantCode, errb.String())
			}
			if !strings.Contains(out.String(), tt.wantOut) || !strings.Contains(errb.String(), tt.wantErr) {
				t.Errorf("stdout %q stderr %q; want %q / %q", out.String(), errb.String(), tt.wantOut, tt.wantErr)
			}
		})
	}
}
