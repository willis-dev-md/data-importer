package e2e

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/willis-dev-md/data-importer/gen/dataimporter/v1"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

const (
	ashe2026Member = "PROV - Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2026.xlsx"
	// The real 2025 revised member has no "PROV - " prefix; the member regex must match both forms.
	ashe2025Member = "Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2025.xlsx"
)

// asheZip builds a zip holding the trimmed Table 20.7a workbook under member, plus decoy members that must not match.
func asheZip(t *testing.T, workbook []byte, member string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, m := range []struct {
		name string
		body []byte
	}{
		{"PROV - Age by Occupation SOC20 (2) Table 20.7b   Annual pay - Gross 2026 CV.xlsx", []byte("not a workbook")},
		{"PROV - Age by Occupation SOC20 (2) Table 20.1a   Weekly pay - Gross 2026.xlsx", []byte("not a workbook")},
		{member, workbook},
	} {
		w, err := zw.Create(m.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(m.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fixtureCounts reads the fixture independently of the importer: per sheet, the number of kept rows (the "All employees"
// row and the age x occupation rows, not the age-only duplicates), the numeric cells and the marker cells in the
// jobs (C), median (D) and mean (F) columns.
func fixtureCounts(t *testing.T, path string) map[string][3]int {
	t.Helper()
	f, err := excelize.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ageRow := regexp.MustCompile(`^\s*(18-21|22-29|30-39|40-49|50-59|60\+)\s+\S`)
	out := map[string][3]int{}
	for _, sh := range f.GetSheetList() {
		if sh == "Notes" {
			continue
		}
		rows, err := f.GetRows(sh, excelize.Options{RawCellValue: true})
		if err != nil {
			t.Fatal(err)
		}
		var c [3]int
		for i, row := range rows {
			if i < 5 || len(row) == 0 {
				continue
			}
			if d := strings.TrimSpace(row[0]); d != "All employees" && !ageRow.MatchString(row[0]) {
				continue
			}
			c[0]++
			for _, col := range []int{2, 3, 5} {
				if col >= len(row) || strings.TrimSpace(row[col]) == "" {
					continue
				}
				if _, err := strconv.ParseFloat(strings.TrimSpace(row[col]), 64); err == nil {
					c[1]++
				} else {
					c[2]++
				}
			}
		}
		out[sh] = c
	}
	return out
}

// TestASHETable20EndToEnd runs the two standalone ASHE Table 20.7a sources (xlsx in a zip, row pipeline with
// suppression markers) through the real binary: validate, apply, run from an httptest TLS server, serve, gRPC.
func TestASHETable20EndToEnd(t *testing.T) {
	root := repoRoot(t)
	examples := filepath.Join(root, "docs", "examples")
	schema := filepath.Join(root, "canonical-schema.yaml")
	fixture := filepath.Join(root, "testdata", "ashe", "t20-7a-trimmed.xlsx")
	dbURL := testutil.NewDatabaseURL(t)

	workbook, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	zip2026 := asheZip(t, workbook, ashe2026Member)
	zip2025 := asheZip(t, workbook, ashe2025Member)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2026.zip":
			_, _ = w.Write(zip2026)
		case "/2025.zip":
			_, _ = w.Write(zip2025)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	certFile := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(certFile, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	tlsEnv := []string{"SSL_CERT_FILE=" + certFile}

	bin := filepath.Join(t.TempDir(), "importer")
	build := exec.Command("go", "build", "-o", bin, "./cmd/importer")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	locRe := regexp.MustCompile(`(?m)^location:.*$`)
	dir := t.TempDir()
	for id, path := range map[string]string{"ashe-t20-2026p": "/2026.zip", "ashe-t20-2025r": "/2025.zip"} {
		// The examples validate as written.
		runImporter(t, bin, dbURL, "", "mapping", "validate", "--schema", schema, filepath.Join(examples, id+".mapping.yaml"))
		body, err := os.ReadFile(filepath.Join(examples, id+".source.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "{") && regexp.MustCompile(`\{(year|status)\}`).Match(body) {
			t.Fatalf("%s: placeholders remain", id)
		}
		if !locRe.Match(body) {
			t.Fatalf("%s: no location line", id)
		}
		body = locRe.ReplaceAll(body, []byte("location: "+srv.URL+path))
		mp, err := os.ReadFile(filepath.Join(examples, id+".mapping.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for name, b := range map[string][]byte{id + ".source.yaml": body, id + ".mapping.yaml": mp} {
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	fc := fixtureCounts(t, fixture)
	all, mft := fc["All"], fc["Male Full-Time"]
	if all[0] != 217 || mft[0] != 217 {
		t.Fatalf("fixture rows per sheet: All %d, Male Full-Time %d, want 217", all[0], mft[0])
	}
	if all[1] != 620 || all[2] != 30 || mft[1] != 597 {
		t.Fatalf("fixture counts All numeric %d markers %d, MFT numeric %d; docs say 620, 30, 597", all[1], all[2], mft[1])
	}
	wantAll, wantMFT := all[1]+all[2], mft[1]+mft[2]
	if wantAll != 650 {
		t.Fatalf("All records = %d, want 650", wantAll)
	}
	total := wantAll + wantMFT
	t.Logf("expected records per edition: All %d, Male Full-Time %d (markers %d)", wantAll, wantMFT, mft[2])

	runImporter(t, bin, dbURL, "version 3", "migrate", "up")
	runImporterEnv(t, bin, dbURL, tlsEnv, "ashe-t20-2025r: source created", "sources", "apply", "--dir", dir, "--schema", schema)
	runImporterEnv(t, bin, dbURL, tlsEnv, "inserted="+strconv.Itoa(total)+" updated=0 unchanged=0 failed=0", "run", "--source", "ashe-t20-2026p")

	// The archive (zip bytes and SHA-256) is what source_files stores.
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var gotSum []byte
	var gotSize int64
	if err := pool.QueryRow(context.Background(),
		`SELECT sha256, size_bytes FROM source_files WHERE source_id = 'ashe-t20-2026p'`).Scan(&gotSum, &gotSize); err != nil {
		t.Fatal(err)
	}
	if want := sha256.Sum256(zip2026); !bytes.Equal(gotSum, want[:]) || gotSize != int64(len(zip2026)) {
		t.Errorf("source_files sha256/size do not match the zip (size %d, want %d)", gotSize, len(zip2026))
	}
	var rowErrs int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM import_errors`).Scan(&rowErrs); err != nil || rowErrs != 0 {
		t.Errorf("import_errors = %d (err %v), want 0", rowErrs, err)
	}

	// A second run: same archive, nothing parsed.
	runImporterEnv(t, bin, dbURL, tlsEnv, "status=unchanged_file inserted=0 updated=0 unchanged=0 failed=0", "run", "--source", "ashe-t20-2026p")

	runImporterEnv(t, bin, dbURL, tlsEnv, "inserted="+strconv.Itoa(total), "run", "--source", "ashe-t20-2025r")

	addr, stop := startServe(t, bin, dbURL)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	cl := pb.NewDataServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	const prov, rev = "ashe-t20-2026p", "ashe-t20-2025r"
	pop := func(p string) *pb.Filter { return eq("dimensions.population", p) }
	band := func(b string) *pb.Filter { return eq("dimensions.age_band", b) }

	for _, src := range []string{prov, rev} {
		recs := listSource(t, ctx, cl, src)
		if len(recs) != total {
			t.Errorf("%s: %d records, want %d", src, len(recs), total)
		}
		wantStatus, wantStart, wantEnd := "provisional", "2025-04-06", "2026-04-05"
		if src == rev {
			wantStatus, wantStart, wantEnd = "revised", "2024-04-06", "2025-04-05"
		}
		seen := map[string]bool{}
		sheets := map[string]int{}
		flagged := 0
		for _, r := range recs {
			f := r.Fields.AsMap()
			if len(r.MappingErrors) != 0 {
				t.Errorf("%s: mapping errors on %v: %v", src, f["series_id"], r.MappingErrors)
			}
			d, _ := f["dimensions"].(map[string]any)
			key := strings.Join([]string{str(d["population"]), str(d["age_band"]), str(d["soc_code"]), str(f["statistic"])}, "|")
			if seen[key] {
				t.Errorf("%s: duplicate natural key %s", src, key)
			}
			seen[key] = true
			sheets[str(d["population"])]++
			if f["status"] != wantStatus || f["period_type"] != "tax_year" || f["period_start"] != wantStart || f["period_end"] != wantEnd {
				t.Errorf("%s: %s: status/period = %v %v %v %v", src, key, f["status"], f["period_type"], f["period_start"], f["period_end"])
			}
			if f["quality_flag"] != nil {
				flagged++
				if f["value"] != nil {
					t.Errorf("%s: %s: flagged record has value %v", src, key, f["value"])
				}
			} else if f["value"] == nil {
				t.Errorf("%s: %s: no value and no flag", src, key)
			}
		}
		if sheets["All"] != wantAll || sheets["Male Full-Time"] != wantMFT || len(sheets) != 2 {
			t.Errorf("%s: records per sheet = %v, want All %d, Male Full-Time %d", src, sheets, wantAll, wantMFT)
		}
		if want := all[2] + mft[2]; flagged != want {
			t.Errorf("%s: flagged = %d, want %d", src, flagged, want)
		}
	}
	// Per-sheet counts through filters (numeric = value present is checked above; here the filter path).
	if got := len(listSource(t, ctx, cl, prov, pop("All"))); got != 650 {
		t.Errorf("All = %d, want 650", got)
	}
	if got := len(listSource(t, ctx, cl, prov, pop("Male Full-Time"))); got != wantMFT {
		t.Errorf("Male Full-Time = %d, want %d", got, wantMFT)
	}

	// Spot values.
	one := func(src string, filters ...*pb.Filter) map[string]any {
		t.Helper()
		recs := listSource(t, ctx, cl, src, filters...)
		if len(recs) != 1 {
			t.Fatalf("%s %v: %d records, want 1", src, filters, len(recs))
		}
		if len(recs[0].MappingErrors) != 0 {
			t.Errorf("mapping errors: %v", recs[0].MappingErrors)
		}
		return recs[0].Fields.AsMap()
	}
	spots := []struct {
		name    string
		filters []*pb.Filter
		value   float64
		unit    string
	}{
		{"22-29 SOC 11 median", []*pb.Filter{pop("All"), band("22-29"), eq("dimensions.soc_code", "11"), eq("statistic", "median")}, 35763.7, "GBP"},
		{"22-29 SOC 11 mean", []*pb.Filter{pop("All"), band("22-29"), eq("dimensions.soc_code", "11"), eq("statistic", "mean")}, 44541.8, "GBP"},
		{"22-29 SOC 11 jobs", []*pb.Filter{pop("All"), band("22-29"), eq("dimensions.soc_code", "11"), eq("statistic", "jobs")}, 97, "thousand_jobs"},
		{"30-39 SOC 2 median", []*pb.Filter{pop("All"), band("30-39"), eq("dimensions.soc_code", "2"), eq("statistic", "median")}, 47066, "GBP"},
		{"30-39 SOC 2 mean", []*pb.Filter{pop("All"), band("30-39"), eq("dimensions.soc_code", "2"), eq("statistic", "mean")}, 52704.8, "GBP"},
		{"22-29 all occupations median", []*pb.Filter{pop("All"), band("22-29"), eq("dimensions.soc_code", "total"), eq("statistic", "median")}, 30737.9, "GBP"},
		{"22-29 all occupations jobs", []*pb.Filter{pop("All"), band("22-29"), eq("dimensions.soc_code", "total"), eq("statistic", "jobs")}, 3438, "thousand_jobs"},
		{"all employees median", []*pb.Filter{pop("All"), band("all"), eq("statistic", "median")}, 34229.1, "GBP"},
		{"all employees jobs", []*pb.Filter{pop("All"), band("all"), eq("statistic", "jobs")}, 25156, "thousand_jobs"},
	}
	for _, s := range spots {
		f := one(prov, s.filters...)
		d, _ := f["dimensions"].(map[string]any)
		if f["value"] != s.value || f["unit"] != s.unit || f["measure"] != "annual_pay_gross" || f["status"] != "provisional" ||
			f["period_type"] != "tax_year" || f["period_start"] != "2025-04-06" || f["period_end"] != "2026-04-05" || d["population"] != "All" {
			t.Errorf("%s: %v", s.name, f)
		}
		if _, hasFlag := f["quality_flag"]; hasFlag && f["quality_flag"] != nil {
			t.Errorf("%s: unexpected quality_flag %v", s.name, f["quality_flag"])
		}
	}
	if f := one(prov, pop("All"), band("22-29"), eq("dimensions.soc_code", "total"), eq("statistic", "median")); f["series_id"] != "ashe20.7a:All:22-29::median" { // empty soc_code stays empty in the series id (dimension default is separate)
		t.Errorf("series_id: %v", f["series_id"])
	}
	if f := one(prov, pop("All"), band("all"), eq("statistic", "jobs")); f["series_id"] != "ashe20.7a:All:::jobs" {
		t.Errorf("All employees series_id: %v", f["series_id"])
	}

	// A suppressed cell: null value, flag x, no mapping errors.
	sup := one(prov, pop("All"), band("18-21"), eq("dimensions.soc_code", "1"), eq("statistic", "median"))
	if sup["value"] != nil || sup["quality_flag"] != "x" {
		t.Errorf("suppressed cell: value %v flag %v, want null and x", sup["value"], sup["quality_flag"])
	}

	// median x 22-29: one record per SOC group plus the all-occupations row, per sheet.
	med := []*pb.Filter{eq("statistic", "median"), band("22-29")}
	for _, p := range []string{"All", "Male Full-Time"} {
		if got := len(listSource(t, ctx, cl, prov, append([]*pb.Filter{pop(p)}, med...)...)); got != 36 {
			t.Errorf("median 22-29 on %s = %d, want 36 (35 SOC groups + all occupations)", p, got)
		}
	}
	if got := len(listSource(t, ctx, cl, prov, med...)); got != 72 {
		t.Errorf("median 22-29 over both sheets = %d, want 72", got)
	}

	// Both editions coexist and are separate: nothing from one source under the other.
	for src, other := range map[string]string{prov: "revised", rev: "provisional"} {
		if got := len(listSource(t, ctx, cl, src, eq("status", other))); got != 0 {
			t.Errorf("%s has %d records with status %s", src, got, other)
		}
	}
	if got := len(listSource(t, ctx, cl, rev, eq("status", "revised"))); got != total {
		t.Errorf("%s status=revised = %d, want %d", rev, got, total)
	}
	r25 := one(rev, pop("All"), band("22-29"), eq("dimensions.soc_code", "11"), eq("statistic", "median"))
	if r25["status"] != "revised" || r25["period_start"] != "2024-04-06" || r25["period_end"] != "2025-04-05" ||
		r25["series_id"] != "ashe20.7a:All:22-29:11:median" {
		t.Errorf("2025 revised record: %v", r25)
	}

	// Re-running after both editions exist still changes nothing.
	runImporterEnv(t, bin, dbURL, tlsEnv, "status=unchanged_file", "run", "--source", "ashe-t20-2025r")

	conn.Close()
	stop()
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
