package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodSource = "id: a\nkind: csv\nlocation: /x.csv\nnatural_key: [id]\n"

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoadDir(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		wantErr string // "" = ok
		wantIDs []string
		wantMap []bool
	}{
		{"source and mapping", map[string]string{"a.source.yaml": goodSource, "a.mapping.yaml": "source: a\n", "notes.txt": "x"},
			"", []string{"a"}, []bool{true}},
		{"source without mapping", map[string]string{"a.source.yaml": goodSource}, "", []string{"a"}, []bool{false}},
		{"sorted by file name", map[string]string{
			"z.source.yaml": strings.Replace(goodSource, "id: a", "id: z", 1),
			"b.source.yaml": strings.Replace(goodSource, "id: a", "id: b", 1)}, "", []string{"b", "z"}, []bool{false, false}},
		{"orphan mapping", map[string]string{"a.source.yaml": goodSource, "b.mapping.yaml": "source: b\n"}, "b.mapping.yaml: mapping file has no matching b.source.yaml", nil, nil},
		{"invalid source names file", map[string]string{"a.source.yaml": "id: a\nkind: csv\n"}, "a.source.yaml", nil, nil},
		{"duplicate id", map[string]string{"a.source.yaml": goodSource, "b.source.yaml": goodSource}, "duplicate source id \"a\"", nil, nil},
		{"empty dir", map[string]string{"readme.md": "x"}, "no *.source.yaml files", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadDir(writeFiles(t, tt.files))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.wantIDs) {
				t.Fatalf("got %d sources", len(got))
			}
			for i, g := range got {
				if g.Source.ID != tt.wantIDs[i] || (g.MappingPath != "") != tt.wantMap[i] || (len(g.MappingData) > 0) != tt.wantMap[i] {
					t.Errorf("source %d: %+v", i, g)
				}
			}
		})
	}
	if _, err := LoadDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing dir: want error")
	}
}

func TestLoadDirReportsAllProblems(t *testing.T) {
	_, err := LoadDir(writeFiles(t, map[string]string{
		"a.source.yaml": "id: a\n", "b.source.yaml": "id: b\n", "c.mapping.yaml": "x: 1\n"}))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"a.source.yaml", "b.source.yaml", "c.mapping.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %s", err, want)
		}
	}
}

func TestAuth(t *testing.T) {
	tests := []struct {
		name, auth, wantErr string
	}{
		{"bearer", "{type: bearer, token_env: MY_TOKEN}", ""},
		{"api key", "{type: api_key, token_env: K, header: X-Api-Key}", ""},
		{"no type", "{token_env: K}", "auth.type: required"},
		{"bad type", "{type: basic, token_env: K}", "auth.type: must be"},
		{"token env is not a name", "{type: bearer, token_env: 'abc def'}", "auth.token_env"},
		{"no token env", "{type: bearer}", "auth.token_env"},
		{"api key without header", "{type: api_key, token_env: K}", "auth.header: required"},
		{"bearer with header", "{type: bearer, token_env: K, header: X}", "auth.header: only valid"},
		{"literal secret key rejected", "{type: bearer, token: abc}", "field token not found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSource([]byte(goodSource + "auth: " + tt.auth + "\n"))
			if (err == nil) != (tt.wantErr == "") || (err != nil && !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestAuthResolve(t *testing.T) {
	env := map[string]string{"T": "  tok  ", "E": ""}
	get := func(k string) string { return env[k] }
	h, v, err := (&Auth{Type: AuthBearer, TokenEnv: "T"}).Resolve(get)
	if err != nil || h != "Authorization" || v != "Bearer tok" {
		t.Errorf("bearer: %q %q %v", h, v, err)
	}
	h, v, err = (&Auth{Type: AuthAPIKey, TokenEnv: "T", Header: "X-Key"}).Resolve(get)
	if err != nil || h != "X-Key" || v != "tok" {
		t.Errorf("api key: %q %q %v", h, v, err)
	}
	for _, name := range []string{"E", "UNSET"} {
		_, _, err = (&Auth{Type: AuthBearer, TokenEnv: name}).Resolve(get)
		var m *MissingSecretError
		if !errorsAs(err, &m) || m.Env != name || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func errorsAs(err error, target any) bool { return errors.As(err, target) }
