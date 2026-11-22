package main

import (
	"strings"
	"testing"
	"time"

	"github.com/willis-dev-md/data-importer/internal/importer"
)

func TestSummaryShowsResolvedLocation(t *testing.T) {
	r := importer.RunResult{RunID: 7, Status: "succeeded", SourceMeta: []byte(`{"_location":"https://h/f-2026-09.csv","CDID":"X"}`)}
	if got := summary("s", r, time.Second); !strings.HasSuffix(got, ` location="https://h/f-2026-09.csv"`) {
		t.Errorf("summary = %q", got)
	}
	for _, meta := range []string{``, `{"CDID":"X"}`, `not json`} {
		r.SourceMeta = []byte(meta)
		if got := summary("s", r, time.Second); strings.Contains(got, "location=") {
			t.Errorf("summary %q with meta %q shows a location", got, meta)
		}
	}
}
