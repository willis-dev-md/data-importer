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

// UK HPI, end to end: the csv adapter with the row pipeline (unpivot), real Postgres, and the example mappings
// evaluated over every stored payload with zero mapping errors.
func TestUKHPIEndToEnd(t *testing.T) {
	root := filepath.Join("..", "..")
	tests := []struct {
		id, fixture, dimKey string
		total               int
		perMeasure          map[string]int // by measure (average prices) or by property_type
	}{
		{"uk-hpi-average-prices", "average-prices-trimmed.csv", "measure", 3402,
			map[string]int{"average_price": 2487, "average_price_sa": 915}},
		{"uk-hpi-property-type", "average-prices-property-type-trimmed.csv", "property_type", 4152,
			map[string]int{"detached": 915, "semi_detached": 915, "terraced": 1026, "flat": 1296}},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			cfg, err := config.LoadSource(filepath.Join(root, "docs", "examples", tt.id+".source.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.Resolve = nil // fixed location, no resolution
			cfg.Location = "file://" + mustAbs(t, filepath.Join(root, "testdata", "hpi", tt.fixture))
			md, err := os.ReadFile(filepath.Join(root, "docs", "examples", tt.id+".mapping.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			m, err := mapping.Parse(md)
			if err != nil {
				t.Fatal(err)
			}
			e := newEnv(t)
			e.addSource(t, cfg)
			deps := importer.Deps{Store: e.st, Adapters: map[string]source.Adapter{config.KindCSV: source.NewCSV(source.FetchOptions{})}}
			ctx := context.Background()

			res, err := importer.Run(ctx, deps, cfg.ID, importer.Options{})
			if err != nil {
				t.Fatalf("run: %v (%+v)", err, res)
			}
			if res.Status != importer.StatusSucceeded || res.Inserted != tt.total || res.Failed != 0 || res.Total != tt.total || res.Updated != 0 || res.Unchanged != 0 {
				t.Fatalf("first run: %+v", res)
			}
			if errs, err := e.st.ListErrors(ctx, res.RunID); err != nil || len(errs) != 0 {
				t.Fatalf("row errors: %v %v", errs, err)
			}

			got := map[string]int{}
			ukAvg, n, after := 0, 0, ""
			seenSeries := map[string]bool{}
			for {
				page, err := e.st.ListRaw(ctx, cfg.ID, after, 500)
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
					dims, _ := f["dimensions"].(map[string]string)
					if tt.dimKey == "measure" {
						got[f["measure"].(string)]++
					} else {
						got[dims["property_type"]]++
					}
					if f["unit"] != "GBP" || f["period_type"] != "month" || f["statistic"] != "mean" {
						t.Fatalf("fields: %v", f)
					}
					ps, pe := f["period_start"].(time.Time), f["period_end"].(time.Time)
					if ps.Day() != 1 || pe.Month() != ps.Month() || pe.AddDate(0, 0, 1).Day() != 1 {
						t.Fatalf("month period %v..%v (payload %v)", ps, pe, r.Payload)
					}
					sid := f["series_id"].(string) + "|" + ps.Format("2006-01")
					if seenSeries[sid] {
						t.Fatalf("series/period seen twice: %s", sid)
					}
					seenSeries[sid] = true
					if tt.dimKey == "measure" && f["measure"] == "average_price" && dims["area_code"] == "K02000001" {
						ukAvg++
						if ps.Format("2006-01") == "2026-09" && f["value"] != 272642.0 {
							t.Errorf("2026-09 UK value = %v", f["value"])
						}
					}
				}
			}
			if n != tt.total {
				t.Errorf("payloads = %d, want %d", n, tt.total)
			}
			for k, w := range tt.perMeasure {
				if got[k] != w {
					t.Errorf("%s = %d, want %d", k, got[k], w)
				}
			}
			if len(got) != len(tt.perMeasure) {
				t.Errorf("groups = %v", got)
			}
			if tt.dimKey == "measure" && ukAvg != 702 {
				t.Errorf("UK average_price records = %d, want 702", ukAvg)
			}

			res2, err := importer.Run(ctx, deps, cfg.ID, importer.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if res2.Status != importer.StatusSucceeded || res2.Unchanged != tt.total || res2.Inserted != 0 || res2.Updated != 0 || res2.Failed != 0 {
				t.Fatalf("second run: %+v", res2)
			}
		})
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	a, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
