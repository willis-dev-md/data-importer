package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

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

// TestServeServesDataService runs the real binary: import the ONS fixture, start
// `importer serve` and read it back over gRPC.
func TestServeServesDataService(t *testing.T) {
	absRepo, _ := filepath.Abs(filepath.Join("..", ".."))
	schema := filepath.Join(absRepo, "canonical-schema.yaml")
	fixture := filepath.Join(absRepo, "testdata", "ons", "l55o.csv")
	examples := filepath.Join(absRepo, "docs", "examples")

	c := &cli{t: t, bin: buildBinary(t), url: testutil.NewDatabaseURL(t)}
	dir := t.TempDir()
	src := readFile(t, filepath.Join(examples, "ons-l55o.source.yaml"))
	src = regexp.MustCompile(`(?m)^location:.*$`).ReplaceAllString(src, "location: "+fixture)
	writeFile(t, dir, "ons-l55o.source.yaml", src)
	writeFile(t, dir, "ons-l55o.mapping.yaml", readFile(t, filepath.Join(examples, "ons-l55o.mapping.yaml")))
	// A source without a mapping.
	writeFile(t, dir, "nomap.source.yaml", "id: nomap\nkind: csv\nlocation: "+fixture+"\nnatural_key: [id]\n")

	c.expect(nil, 0, "version 2", "migrate", "up")
	c.expect(nil, 0, "nomap: source created, mapping none", "sources", "apply", "--dir", dir, "--schema", schema)
	c.expect(nil, 0, "inserted=642", "run", "--source", "ons-l55o")

	cmd := exec.Command(c.bin, "serve")
	cmd.Env = append(os.Environ(), "DATABASE_URL="+c.url, "GRPC_ADDR=127.0.0.1:0", "TLS_CERT_FILE=", "TLS_KEY_FILE=")
	errb := &lockedBuf{}
	cmd.Stdout, cmd.Stderr = errb, errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-exited:
			if err != nil {
				t.Errorf("importer serve exited with error: %v\n%s", err, errb.String())
			}
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
			t.Errorf("importer serve did not exit after SIGTERM\n%s", errb.String())
		}
	}
	t.Cleanup(stop)

	// Poll the log for the bound address.
	addrRe := regexp.MustCompile(`serving (?:plaintext )?gRPC.* addr=(\S+)`)
	var addr string
	deadline := time.Now().Add(30 * time.Second)
	for addr == "" {
		if m := addrRe.FindStringSubmatch(errb.String()); m != nil {
			addr = m[1]
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("importer serve exited early: %v\n%s", err, errb.String())
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("no listen address logged:\n%s", errb.String())
		}
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	cl := pb.NewDataServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	seen := map[string]bool{}
	var first *pb.Record
	token := ""
	for {
		resp, err := cl.ListRecords(ctx, &pb.ListRecordsRequest{SourceId: "ons-l55o", PageSize: 100, PageToken: token}, grpc.WaitForReady(true))
		if err != nil {
			t.Fatalf("ListRecords: %v\n%s", err, errb.String())
		}
		for _, r := range resp.Records {
			if seen[r.Id] {
				t.Fatalf("duplicate record %s", r.Id)
			}
			seen[r.Id] = true
			if first == nil {
				first = r
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	if len(seen) != 642 {
		t.Fatalf("records = %d, want 642", len(seen))
	}

	got, err := cl.GetRecord(ctx, &pb.GetRecordRequest{SourceId: "ons-l55o", Id: first.Id})
	if err != nil {
		t.Fatalf("GetRecord: %v", err)
	}
	if got.Id != first.Id || got.SourceId != "ons-l55o" {
		t.Errorf("GetRecord = %v, want id %s", got, first.Id)
	}

	// Source without a mapping.
	_, err = cl.ListRecords(ctx, &pb.ListRecordsRequest{SourceId: "nomap"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("ListRecords without mapping: %v, want FailedPrecondition", err)
	}
	_, err = cl.GetRecord(ctx, &pb.GetRecordRequest{SourceId: "nomap", Id: "nomap:x"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("GetRecord without mapping: %v, want FailedPrecondition", err)
	}
	// group_id alone says so clearly.
	_, err = cl.ListRecords(ctx, &pb.ListRecordsRequest{GroupId: "g"})
	if status.Code(err) != codes.Unimplemented {
		t.Errorf("group_id alone: %v, want Unimplemented", err)
	}

	conn.Close()
	stop()
}

func TestServeRejectsBadScanLimit(t *testing.T) {
	var errb bytes.Buffer
	env := map[string]string{"DATABASE_URL": "postgres://x", "SCAN_LIMIT": "abc"}
	if code := serve(context.Background(), nil, func(k string) string { return env[k] }, &errb); code != 2 {
		t.Errorf("code = %d, stderr %q", code, errb.String())
	}
}
