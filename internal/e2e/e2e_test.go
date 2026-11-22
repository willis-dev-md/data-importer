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
	var all []*pb.Record
	token := ""
	for {
		resp, err := cl.ListRecords(ctx, &pb.ListRecordsRequest{
			SourceId: "ons-l55o", Filters: filters, PageSize: 1000, PageToken: token,
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
	stop := func() {
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
	var addr string
	deadline := time.Now().Add(30 * time.Second)
	for addr == "" {
		if m := addrRe.FindStringSubmatch(logs.String()); m != nil {
			addr = m[1]
			break
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
