// Package e2e holds the demo-level end-to-end proof: the real importer binary
// takes the ONS L55O fixture from CSV to Postgres to gRPC.
package e2e

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/willis-dev-md/data-importer/gen/dataimporter/v1"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// env is the child environment. The proxy variables point at a closed local
// port, so any outbound HTTP(S) dial by the binary would fail the run.
func env(dbURL string, extra ...string) []string {
	e := append(os.Environ(),
		"DATABASE_URL="+dbURL, "SOURCES_DIR=",
		"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1",
		"http_proxy=http://127.0.0.1:1", "https_proxy=http://127.0.0.1:1",
		"NO_PROXY=", "no_proxy=")
	return append(e, extra...)
}

var (
	filesMu   sync.Mutex
	filesDirs = map[*testing.T]string{}
)

// filesDir is the importer's file store of one test (kept across its invocations, outside the repo).
func filesDir(t *testing.T) string {
	filesMu.Lock()
	defer filesMu.Unlock()
	if d, ok := filesDirs[t]; ok {
		return d
	}
	d := t.TempDir()
	filesDirs[t] = d
	return d
}

func runImporter(t *testing.T, bin, dbURL string, wantOut string, args ...string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = env(dbURL, "FILE_STORE_DIR="+filesDir(t))
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("importer %v: %v\n%s", args, err, out.String())
	}
	if !strings.Contains(out.String(), wantOut) {
		t.Fatalf("importer %v: output lacks %q\n%s", args, wantOut, out.String())
	}
}

func count(t *testing.T, ctx context.Context, cl pb.DataServiceClient, filters ...*pb.Filter) int {
	t.Helper()
	return len(listAll(t, ctx, cl, filters...))
}

func listAll(t *testing.T, ctx context.Context, cl pb.DataServiceClient, filters ...*pb.Filter) []*pb.Record {
	t.Helper()
	return listSource(t, ctx, cl, "ons-l55o", filters...)
}

func listSource(t *testing.T, ctx context.Context, cl pb.DataServiceClient, source string, filters ...*pb.Filter) []*pb.Record {
	t.Helper()
	var all []*pb.Record
	token := ""
	for {
		resp, err := cl.ListRecords(ctx, &pb.ListRecordsRequest{
			SourceId: source, Filters: filters, PageSize: 1000, PageToken: token,
		}, grpc.WaitForReady(true))
		if err != nil {
			t.Fatalf("ListRecords: %v", err)
		}
		all = append(all, resp.Records...)
		if resp.NextPageToken == "" {
			return all
		}
		token = resp.NextPageToken
	}
}

func eq(field string, v any) *pb.Filter {
	sv, err := structpb.NewValue(v)
	if err != nil {
		panic(err)
	}
	return &pb.Filter{Field: field, Op: pb.FilterOp_FILTER_OP_EQ, Value: sv}
}

// startServe runs `importer serve` on an ephemeral plaintext port and returns its address and an idempotent stop
// (SIGTERM, exit code 0 expected). stop also runs at cleanup.
func startServe(t *testing.T, bin, dbURL string) (addr string, stop func()) {
	t.Helper()
	cmd := exec.Command(bin, "serve")
	cmd.Env = env(dbURL, "GRPC_ADDR=127.0.0.1:0", "TLS_CERT_FILE=", "TLS_KEY_FILE=")
	logs := &lockedBuf{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	done := false
	stop = func() {
		if done {
			return
		}
		done = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-exited:
			if err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					t.Errorf("serve exit code %d after SIGTERM, want 0\n%s", ee.ExitCode(), logs.String())
				} else {
					t.Errorf("serve: %v", err)
				}
			}
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
			t.Errorf("serve did not exit after SIGTERM\n%s", logs.String())
		}
	}
	t.Cleanup(stop)

	addrRe := regexp.MustCompile(`serving (?:plaintext )?gRPC.* addr=(\S+)`)
	deadline := time.Now().Add(30 * time.Second)
	for addr == "" {
		if m := addrRe.FindStringSubmatch(logs.String()); m != nil {
			return m[1], stop
		}
		select {
		case err := <-exited:
			t.Fatalf("serve exited early: %v\n%s", err, logs.String())
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("no listen address logged:\n%s", logs.String())
		}
	}
	return addr, stop
}

func TestONSSliceEndToEnd(t *testing.T) {
	root := repoRoot(t)
	fixture := filepath.Join(root, "testdata", "ons", "l55o.csv")
	examples := filepath.Join(root, "docs", "examples")
	schema := filepath.Join(root, "canonical-schema.yaml")
	dbURL := testutil.NewDatabaseURL(t)

	bin := filepath.Join(t.TempDir(), "importer")
	build := exec.Command("go", "build", "-o", bin, "./cmd/importer")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	// Temp copy of the example source whose location is the local fixture.
	dir := t.TempDir()
	src, err := os.ReadFile(filepath.Join(examples, "ons-l55o.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	loc := "location: file://" + fixture
	re := regexp.MustCompile(`(?m)^location:.*$`)
	if !re.Match(src) {
		t.Fatal("example source has no location line")
	}
	srcOut := re.ReplaceAll(src, []byte(loc))
	mp, err := os.ReadFile(filepath.Join(examples, "ons-l55o.mapping.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"ons-l55o.source.yaml": srcOut, "ons-l55o.mapping.yaml": mp} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	runImporter(t, bin, dbURL, "version 2", "migrate", "up")
	runImporter(t, bin, dbURL, "ons-l55o: source created", "sources", "apply", "--dir", dir, "--schema", schema)
	runImporter(t, bin, dbURL, "inserted=642", "run", "--source", "ons-l55o")

	addr, stop := startServe(t, bin, dbURL)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	cl := pb.NewDataServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	gte := &pb.Filter{Field: "value", Op: pb.FilterOp_FILTER_OP_GTE, Value: structpb.NewNumberValue(3.5)}
	counts := []struct {
		name    string
		filters []*pb.Filter
		want    int
	}{
		{"all", nil, 642},
		{"month", []*pb.Filter{eq("period_type", "month")}, 454},
		{"year", []*pb.Filter{eq("period_type", "year")}, 37},
		{"quarter", []*pb.Filter{eq("period_type", "quarter")}, 151},
		{"month and value>=3.5", []*pb.Filter{eq("period_type", "month"), gte}, 108},
	}
	check := func(stage string) {
		t.Helper()
		for _, c := range counts {
			if got := count(t, ctx, cl, c.filters...); got != c.want {
				t.Errorf("%s: %s = %d, want %d", stage, c.name, got, c.want)
			}
		}
	}
	check("after first run")

	// Spot-check records (labels 1989, 1989 Q1, 2026 OCT).
	type key struct{ ptype, start string }
	byKey := map[key]*pb.Record{}
	for _, r := range listAll(t, ctx, cl) {
		f := r.Fields.AsMap()
		byKey[key{f["period_type"].(string), f["period_start"].(string)}] = r
	}
	spots := []struct {
		label, ptype, start, end string
		value                    float64
	}{
		{"1989", "year", "1989-01-01", "1989-12-31", 5.7},
		{"1989 Q1", "quarter", "1989-01-01", "1989-03-31", 5.8},
		{"2026 OCT", "month", "2026-10-01", "2026-10-31", 3.7},
	}
	for _, s := range spots {
		r := byKey[key{s.ptype, s.start}]
		if r == nil {
			t.Errorf("%s: no %s record starting %s", s.label, s.ptype, s.start)
			continue
		}
		f := r.Fields.AsMap()
		want := map[string]any{
			"series_id": "L55O", "measure": "cpih_annual_rate", "period_type": s.ptype,
			"period_start": s.start, "period_end": s.end, "value": s.value, "unit": "%",
		}
		for k, w := range want {
			if f[k] != w {
				t.Errorf("%s: %s = %v (%T), want %v", s.label, k, f[k], f[k], w)
			}
		}
	}

	// A second run changes nothing.
	runImporter(t, bin, dbURL, "unchanged=642", "run", "--source", "ons-l55o")
	check("after second run")

	conn.Close()
	stop()
}

func in(field string, vs ...any) *pb.Filter {
	lv, err := structpb.NewList(vs)
	if err != nil {
		panic(err)
	}
	return &pb.Filter{Field: field, Op: pb.FilterOp_FILTER_OP_IN, Value: structpb.NewListValue(lv)}
}

// TestUKHPIEndToEnd runs the two HPI example sources (csv + unpivot row pipeline) through the real binary:
// validate, apply, run (twice), serve, ListRecords.
func TestUKHPIEndToEnd(t *testing.T) {
	root := repoRoot(t)
	examples := filepath.Join(root, "docs", "examples")
	schema := filepath.Join(root, "canonical-schema.yaml")
	dbURL := testutil.NewDatabaseURL(t)

	bin := filepath.Join(t.TempDir(), "importer")
	build := exec.Command("go", "build", "-o", bin, "./cmd/importer")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	type src struct{ id, fixture string }
	srcs := []src{
		{"uk-hpi-average-prices", "average-prices-trimmed.csv"},
		{"uk-hpi-property-type", "average-prices-property-type-trimmed.csv"},
	}
	locRe := regexp.MustCompile(`(?m)^location:.*$`)
	resolveRe := regexp.MustCompile(`(?m)^resolve:\n(?:[ \t]+.*\n)+`)
	dir := t.TempDir()
	for _, s := range srcs {
		// The examples validate as written (no network, no overrides).
		runImporter(t, bin, dbURL, "", "mapping", "validate", "--schema", schema, filepath.Join(examples, s.id+".mapping.yaml"))
		body, err := os.ReadFile(filepath.Join(examples, s.id+".source.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if !locRe.Match(body) || !resolveRe.Match(body) {
			t.Fatalf("%s: example has no location/resolve to override", s.id)
		}
		// file:// fixture, no `resolve`.
		body = resolveRe.ReplaceAll(body, nil)
		body = locRe.ReplaceAll(body, []byte("location: file://"+filepath.Join(root, "testdata", "hpi", s.fixture)))
		mp, err := os.ReadFile(filepath.Join(examples, s.id+".mapping.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for name, b := range map[string][]byte{s.id + ".source.yaml": body, s.id + ".mapping.yaml": mp} {
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	runImporter(t, bin, dbURL, "version 2", "migrate", "up")
	runImporter(t, bin, dbURL, "uk-hpi-average-prices: source created", "sources", "apply", "--dir", dir, "--schema", schema)
	runImporter(t, bin, dbURL, "inserted=3402", "run", "--source", "uk-hpi-average-prices")
	runImporter(t, bin, dbURL, "inserted=4152", "run", "--source", "uk-hpi-property-type")

	addr, stop := startServe(t, bin, dbURL)
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	cl := pb.NewDataServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	counts := []struct {
		name    string
		source  string
		filters []*pb.Filter
		want    int
	}{
		{"all", "uk-hpi-average-prices", nil, 3402},
		{"price", "uk-hpi-average-prices", []*pb.Filter{eq("measure", "average_price")}, 2487},
		{"price_sa", "uk-hpi-average-prices", []*pb.Filter{eq("measure", "average_price_sa")}, 915},
		{"UK price", "uk-hpi-average-prices", []*pb.Filter{eq("dimensions.area_code", "K02000001"), eq("measure", "average_price")}, 702},
		{"UK and England (IN)", "uk-hpi-average-prices", []*pb.Filter{in("dimensions.area_code", "K02000001", "E92000001"), eq("measure", "average_price")}, 1404},
		{"statistic", "uk-hpi-average-prices", []*pb.Filter{eq("statistic", "mean")}, 3402},
		{"pt all", "uk-hpi-property-type", nil, 4152},
		{"detached", "uk-hpi-property-type", []*pb.Filter{eq("dimensions.property_type", "detached")}, 915},
		{"semi", "uk-hpi-property-type", []*pb.Filter{eq("dimensions.property_type", "semi_detached")}, 915},
		{"terraced", "uk-hpi-property-type", []*pb.Filter{eq("dimensions.property_type", "terraced")}, 1026},
		{"flat", "uk-hpi-property-type", []*pb.Filter{eq("dimensions.property_type", "flat")}, 1296},
		{"pt flat+terraced (IN)", "uk-hpi-property-type", []*pb.Filter{in("dimensions.property_type", "flat", "terraced")}, 2322},
	}
	check := func(stage string) {
		t.Helper()
		for _, c := range counts {
			if got := len(listSource(t, ctx, cl, c.source, c.filters...)); got != c.want {
				t.Errorf("%s: %s = %d, want %d", stage, c.name, got, c.want)
			}
		}
	}
	check("after first run")

	// The 2026-09 United Kingdom average price.
	recs := listSource(t, ctx, cl, "uk-hpi-average-prices",
		eq("dimensions.area_code", "K02000001"), eq("measure", "average_price"), eq("period_start", "2026-09-01"))
	if len(recs) != 1 {
		t.Fatalf("2026-09 UK records = %d, want 1", len(recs))
	}
	f := recs[0].Fields.AsMap()
	want := map[string]any{
		"series_id": "ukhpi:average_price:K02000001", "measure": "average_price", "statistic": "mean",
		"period_type": "month", "period_start": "2026-09-01", "period_end": "2026-09-30", "value": 272642.0, "unit": "GBP",
	}
	for k, w := range want {
		if f[k] != w {
			t.Errorf("2026-09 UK: %s = %v (%T), want %v", k, f[k], f[k], w)
		}
	}
	if d, _ := f["dimensions"].(map[string]any); d["area_code"] != "K02000001" || d["property_type"] != "all" {
		t.Errorf("2026-09 UK dimensions = %v", f["dimensions"])
	}

	// Second runs change nothing. The examples set change_detection with content_hash, so an identical file ends the
	// run as unchanged_file before any row is parsed (the row-level unchanged=3402 / 4152 of a re-parse is asserted
	// in internal/importer TestUKHPIEndToEnd, which has no file store).
	runImporter(t, bin, dbURL, "status=unchanged_file inserted=0 updated=0 unchanged=0 failed=0", "run", "--source", "uk-hpi-average-prices")
	runImporter(t, bin, dbURL, "status=unchanged_file inserted=0 updated=0 unchanged=0 failed=0", "run", "--source", "uk-hpi-property-type")
	check("after second run")

	conn.Close()
	stop()
}
