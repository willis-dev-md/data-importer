package importer_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points TMPDIR at a fresh directory and fails the package if an archive spool file is left behind.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "importer-tests-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("TMPDIR", dir)
	code := m.Run()
	if left, _ := filepath.Glob(filepath.Join(dir, "importer-archive-*")); len(left) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: archive spool files left behind: %v\n", left)
		code = 1
	}
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
