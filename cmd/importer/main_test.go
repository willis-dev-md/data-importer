package main

import (
	"bytes"
	"os"
	"path/filepath"
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
		{"serve rejects extra args", []string{"serve", "x"}, 2, "", "usage: importer serve"},
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
		{"up", []string{"up"}, url, 0, "version 2", ""},
		{"up twice", []string{"up"}, url, 0, "version 2", ""},
		{"down one step", []string{"down"}, url, 0, "version 1", ""},
		{"down again", []string{"down"}, url, 0, "version 0", ""},
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

func TestRunMappingValidate(t *testing.T) {
	dir := t.TempDir()
	schema := "../../canonical-schema.yaml"
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	bad := write("bad.yaml", "source: s\nfields:\n  nope: {const: 1}\n")
	warn := write("warn.yaml", `source: s
fields:
  series_id: {const: a}
  measure: {const: m}
  period_type: {from: p, type: period, output: period_type}
  period_start: {from: p, type: period, output: period_start}
  period_end: {from: p, type: period, output: period_end}
  value: {from: v, type: decimal, null_markers: [x]}
`)
	notyaml := write("notyaml.yaml", "source: [")
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"valid example", []string{"mapping", "validate", "--schema", schema, "../../docs/examples/ons-l55o.mapping.yaml"}, 0, ""},
		{"errors have path code message", []string{"mapping", "validate", "--schema", schema, bad}, 1, "fields.nope: UNKNOWN_FIELD: "},
		{"warnings only", []string{"mapping", "validate", "--schema", schema, warn}, 0, "fields.value.null_markers: NULL_MARKERS_WITHOUT_FLAG: "},
		{"parse error", []string{"mapping", "validate", "--schema", schema, notyaml}, 1, "PARSE_ERROR"},
		{"unreadable file", []string{"mapping", "validate", "--schema", schema, filepath.Join(dir, "missing.yaml")}, 2, ""},
		{"unreadable schema", []string{"mapping", "validate", "--schema", filepath.Join(dir, "missing.yaml"), bad}, 2, ""},
		{"no file", []string{"mapping", "validate"}, 2, ""},
		{"no subcommand", []string{"mapping"}, 2, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(tt.args, &out, &errb)
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d (stdout %q stderr %q)", code, tt.wantCode, out.String(), errb.String())
			}
			if !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("stdout %q missing %q", out.String(), tt.wantOut)
			}
		})
	}
}
