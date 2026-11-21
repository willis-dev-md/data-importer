package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExample(t *testing.T) {
	s, err := LoadSource(filepath.Join("..", "..", "docs", "examples", "acme-csv.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "acme-csv" || s.Kind != KindCSV || s.CSV == nil || s.CSV.DelimiterRune() != ',' || !s.CSV.HasHeader() ||
		s.MissingPolicy != MissingTombstone || s.MaxErrorRatio != 0.05 || len(s.NaturalKey) != 1 || s.Schedule == nil || s.Schedule.Every != "weekly" {
		t.Errorf("unexpected: %+v", s)
	}
}

func TestParseSource(t *testing.T) {
	const ok = "id: a\nkind: csv\nlocation: /x.csv\nnatural_key: [id]\n"
	tests := []struct {
		name, yaml, wantErr string // wantErr "" = valid
	}{
		{"minimal", ok, ""},
		{"all csv options", ok + "csv: {delimiter: ';', header: false, encoding: windows-1252, columns: [id, v]}\nmax_error_ratio: 0.5\nmissing_policy: keep\n", ""},
		{"unknown key", ok + "bogus: 1\n", "field bogus not found"},
		{"unknown csv key", ok + "csv: {delim: ';'}\n", "field delim not found"},
		{"empty", "", "empty document"},
		{"two documents", ok + "---\n" + ok, "more than one"},
		{"missing id", "kind: csv\nlocation: /x\nnatural_key: [id]\n", "id: required"},
		{"missing kind", "id: a\nlocation: /x\nnatural_key: [id]\n", "kind: required"},
		{"unsupported kind", "id: a\nkind: json\nlocation: /x\nnatural_key: [id]\n", "kind: unsupported"},
		{"missing location", "id: a\nkind: csv\nnatural_key: [id]\n", "location: required"},
		{"missing natural key", "id: a\nkind: csv\nlocation: /x\n", "natural_key: at least one"},
		{"empty natural key item", "id: a\nkind: csv\nlocation: /x\nnatural_key: [id, ' ']\n", "natural_key[1]: must not be empty"},
		{"ratio high", ok + "max_error_ratio: 1.5\n", "max_error_ratio: must be between"},
		{"ratio negative", ok + "max_error_ratio: -0.1\n", "max_error_ratio: must be between"},
		{"bad missing policy", ok + "missing_policy: delete\n", "missing_policy: must be"},
		{"long delimiter", ok + "csv: {delimiter: ';;'}\n", "csv.delimiter: must be a single character"},
		{"quote delimiter", ok + "csv: {delimiter: '\"'}\n", "csv.delimiter: invalid"},
		{"bad encoding", ok + "csv: {encoding: koi8}\n", "csv.encoding: unsupported"},
		{"empty column", ok + "csv: {columns: [id, '']}\n", "csv.columns[1]: must not be empty"},
		{"duplicate column", ok + "csv: {columns: [id, id]}\n", "csv.columns[1]: duplicate"},
		{"key not in columns", ok + "csv: {columns: [a, b]}\n", "natural_key[0]: \"id\" is not in csv.columns"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSource([]byte(tt.yaml))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadSourceMissingFile(t *testing.T) {
	if _, err := LoadSource("/nonexistent.yaml"); err == nil {
		t.Fatal("expected error")
	}
}
