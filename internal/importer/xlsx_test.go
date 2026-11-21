package importer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/mapping"
	"github.com/willis-dev-md/data-importer/internal/source"
)

// BoE base rate, end to end: the real xlsx adapter and row pipeline, real Postgres, and the example mapping
// evaluated over every stored payload.
func TestBoEBaseRateEndToEnd(t *testing.T) {
	root := filepath.Join("..", "..")
	cfg, err := config.LoadSource(filepath.Join(root, "docs", "examples", "boe-baserate.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Location = filepath.Join(root, "testdata", "boe", "baserate-trimmed.xlsx") // no network in tests
	md, err := os.ReadFile(filepath.Join(root, "docs", "examples", "boe-baserate.mapping.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mapping.Parse(md)
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)
	e.addSource(t, cfg)
	deps := importer.Deps{Store: e.st, Adapters: map[string]source.Adapter{config.KindXLSX: source.NewXLSX(source.FetchOptions{})}}
	ctx := context.Background()

	res, err := importer.Run(ctx, deps, cfg.ID, importer.Options{})
	if err != nil {
		t.Fatalf("run: %v (%+v)", err, res)
	}
	if res.Status != importer.StatusSucceeded || res.Inserted != 854 || res.Failed != 0 || res.Total != 854 || res.Updated != 0 || res.Unchanged != 0 {
		t.Fatalf("first run: %+v", res)
	}
	runs, err := e.st.ListErrors(ctx, res.RunID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("row errors: %v %v", runs, err)
	}

	// Evaluate ALL stored payloads (the database may hold other sources' rows: scope to this source).
	wantSeries := map[string]bool{"BOE_BANK_RATE": true, "BOE_MIN_LENDING_RATE": true, "BOE_MIN_BAND1_DEALING_RATE": true, "BOE_REPO_RATE": true, "BOE_OFFICIAL_BANK_RATE": true}
	gotSeries := map[string]int{}
	types := map[string]int{}
	n, after := 0, ""
	var oct1694 bool
	for {
		page, err := e.st.ListRaw(ctx, cfg.ID, after, 200)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, r := range page {
			after = r.Key
			n++
			out := mapping.Eval(m, r.Payload)
			if len(out.Errors) != 0 {
				t.Fatalf("payload %s: mapping errors %v", r.Key, out.Errors)
			}
			f := out.Fields
			types[f["period_type"].(string)]++
			sid, _ := f["series_id"].(string)
			if !wantSeries[sid] {
				t.Errorf("series_id %q", sid)
			}
			gotSeries[sid]++
			if f["period_type"] == "month" {
				if r.Payload["day"] != "" {
					t.Errorf("month precision for an event with a day: %v", r.Payload)
				}
				if !f["period_start"].(time.Time).Equal(time.Date(f["period_start"].(time.Time).Year(), f["period_start"].(time.Time).Month(), 1, 0, 0, 0, 0, time.UTC)) {
					t.Errorf("month period must start on the 1st: %v", f)
				}
			} else if f["period_start"] != f["period_end"] {
				t.Errorf("day period: %v", f)
			}
			if sid == "BOE_BANK_RATE" && f["period_type"] == "month" && f["period_start"] == time.Date(1694, 10, 1, 0, 0, 0, 0, time.UTC) {
				oct1694 = f["period_end"] == time.Date(1694, 10, 31, 0, 0, 0, 0, time.UTC) && f["value"] == 6.0
			}
		}
	}
	if n != 854 || types["month"] != 17 || types["day"] != 854-17 {
		t.Errorf("payloads=%d period types=%v", n, types)
	}
	if len(gotSeries) != 5 {
		t.Errorf("series: %v", gotSeries)
	}
	if !oct1694 {
		t.Error("1694 Oct (Bank Rate) did not map to month 1694-10-01..1694-10-31 with value 6")
	}

	// Same file again: nothing changes.
	res2, err := importer.Run(ctx, deps, cfg.ID, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Status != importer.StatusSucceeded || res2.Unchanged != 854 || res2.Inserted != 0 || res2.Updated != 0 || res2.Failed != 0 {
		t.Fatalf("second run: %+v", res2)
	}
}
