package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	rpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/willis-dev-md/data-importer/gen/dataimporter/v1"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func okPing(context.Context) error { return nil }

// startBuf runs srv on a bufconn listener and returns a client connection.
func startBuf(t *testing.T, srv *Server) (*grpc.ClientConn, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	stop := func() {
		conn.Close()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}
	return conn, stop
}

func TestHealth(t *testing.T) {
	pool := testutil.DB(t)
	tests := []struct {
		name string
		ping func(context.Context) error
		want healthpb.HealthCheckResponse_ServingStatus
	}{
		{"real database ok", pool.Ping, healthpb.HealthCheckResponse_SERVING},
		{"ping fails", func(context.Context) error { return errors.New("down") }, healthpb.HealthCheckResponse_NOT_SERVING},
		{"no ping configured", nil, healthpb.HealthCheckResponse_NOT_SERVING},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn, stop := startBuf(t, New(Options{Ping: tt.ping, Logger: quiet()}))
			defer stop()
			c := healthpb.NewHealthClient(conn)
			for _, svc := range []string{"", "dataimporter.v1.DataService"} {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				resp, err := c.Check(ctx, &healthpb.HealthCheckRequest{Service: svc})
				cancel()
				if err != nil {
					t.Fatalf("Check(%q): %v", svc, err)
				}
				if resp.Status != tt.want {
					t.Errorf("Check(%q) = %v, want %v", svc, resp.Status, tt.want)
				}
			}
		})
	}
}

func TestHealthFollowsDatabase(t *testing.T) {
	var down bool
	ping := func(context.Context) error {
		if down {
			return errors.New("down")
		}
		return nil
	}
	srv := New(Options{Ping: ping, Logger: quiet()})
	if err := srv.CheckHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	down = true
	if err := srv.CheckHealth(context.Background()); err == nil {
		t.Fatal("want error when ping fails")
	}
}

func TestHealthRecoversAfterStartupOutage(t *testing.T) {
	var up atomic.Bool
	ping := func(context.Context) error {
		if !up.Load() {
			return errors.New("down")
		}
		return nil
	}
	conn, stop := startBuf(t, New(Options{Ping: ping, HealthInterval: 10 * time.Millisecond, Logger: quiet()}))
	defer stop()
	c := healthpb.NewHealthClient(conn)
	status := func() healthpb.HealthCheckResponse_ServingStatus {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := c.Check(ctx, &healthpb.HealthCheckRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Status
	}
	if got := status(); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status with DB down = %v", got)
	}
	up.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for status() != healthpb.HealthCheckResponse_SERVING {
		if time.Now().After(deadline) {
			t.Fatal("health did not recover to SERVING")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHealthBeforePingIsNotServing(t *testing.T) {
	srv := New(Options{Ping: okPing, Logger: quiet()})
	resp, err := srv.health.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status before first ping = %v", resp.Status)
	}
}

func TestReflection(t *testing.T) {
	conn, stop := startBuf(t, New(Options{Ping: okPing, Logger: quiet()}))
	defer stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := rpb.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&rpb.ServerReflectionRequest{MessageRequest: &rpb.ServerReflectionRequest_ListServices{}}); err != nil {
		t.Fatal(err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range resp.GetListServicesResponse().GetService() {
		got = append(got, s.Name)
	}
	for _, want := range []string{"dataimporter.v1.DataService", "grpc.health.v1.Health"} {
		found := false
		for _, g := range got {
			found = found || g == want
		}
		if !found {
			t.Errorf("reflection services %v missing %q", got, want)
		}
	}
}

func TestUnimplemented(t *testing.T) {
	conn, stop := startBuf(t, New(Options{Ping: okPing, Logger: quiet()}))
	defer stop()
	c := pb.NewDataServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tests := []struct {
		name string
		call func() error
	}{
		{"GetRecord", func() error { _, err := c.GetRecord(ctx, &pb.GetRecordRequest{}); return err }},
		{"ListRecords", func() error { _, err := c.ListRecords(ctx, &pb.ListRecordsRequest{}); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status.Code(tt.call()); got != codes.Unimplemented {
				t.Errorf("code = %v, want Unimplemented", got)
			}
		})
	}
}

// blockingData blocks ListRecords until released, to model an in-flight call.
type blockingData struct {
	pb.UnimplementedDataServiceServer
	started chan struct{}
	release chan struct{}
}

func (b *blockingData) ListRecords(context.Context, *pb.ListRecordsRequest) (*pb.ListRecordsResponse, error) {
	close(b.started)
	<-b.release
	return &pb.ListRecordsResponse{NextPageToken: "done"}, nil
}

func TestGracefulShutdownCompletesInFlight(t *testing.T) {
	data := &blockingData{started: make(chan struct{}), release: make(chan struct{})}
	srv := New(Options{Ping: okPing, Data: data, Logger: quiet()})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx, lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	type result struct {
		resp *pb.ListRecordsResponse
		err  error
	}
	res := make(chan result, 1)
	go func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer ccancel()
		r, err := pb.NewDataServiceClient(conn).ListRecords(cctx, &pb.ListRecordsRequest{})
		res <- result{r, err}
	}()
	select {
	case <-data.started:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not start")
	}

	cancel() // shutdown requested while the call is in flight
	select {
	case err := <-runDone:
		t.Fatalf("Run returned before in-flight call finished: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(data.release)

	select {
	case r := <-res:
		if r.err != nil || r.resp.NextPageToken != "done" {
			t.Fatalf("in-flight call = %v, %v", r.resp, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight call did not complete")
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after in-flight call finished")
	}
}

func TestShutdownWithOpenWatch(t *testing.T) {
	srv := New(Options{Ping: okPing, Logger: quiet()})
	conn, stop := startBuf(t, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, err := healthpb.NewHealthClient(conn).Watch(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Recv(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	stop() // fails the test if Run errors
	if d := time.Since(start); d > ShutdownTimeout/2 {
		t.Fatalf("shutdown with open Watch took %v", d)
	}
}

func TestTCPListener(t *testing.T) {
	srv := New(Options{Ping: okPing, Logger: quiet()})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, lis) }()
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	resp, err := healthpb.NewHealthClient(conn).Check(cctx, &healthpb.HealthCheckRequest{})
	if err != nil || resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health = %v, %v", resp, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func writeSelfSigned(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	cert, _ := x509.ParseCertificate(der)
	pool.AddCert(cert)
	return certFile, keyFile, pool
}

func TestConfigTLS(t *testing.T) {
	certFile, keyFile, _ := writeSelfSigned(t)
	tests := []struct {
		name    string
		cfg     Config
		wantTLS bool
		wantErr string
	}{
		{"neither is plaintext", Config{}, false, ""},
		{"both", Config{TLSCertFile: certFile, TLSKeyFile: keyFile}, true, ""},
		{"cert only", Config{TLSCertFile: certFile}, false, "TLS_KEY_FILE"},
		{"key only", Config{TLSKeyFile: keyFile}, false, "TLS_CERT_FILE"},
		{"missing files", Config{TLSCertFile: "/nonexistent/c", TLSKeyFile: "/nonexistent/k"}, false, "load TLS key pair"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cfg.TLS()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tt.wantTLS {
				t.Errorf("tls config present = %v, want %v", got != nil, tt.wantTLS)
			}
		})
	}
}

func TestServeWithTLS(t *testing.T) {
	certFile, keyFile, pool := writeSelfSigned(t)
	tlsCfg, err := Config{TLSCertFile: certFile, TLSKeyFile: keyFile}.TLS()
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Ping: okPing, TLS: tlsCfg, Logger: quiet()})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, lis) }()
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	resp, err := healthpb.NewHealthClient(conn).Check(cctx, &healthpb.HealthCheckRequest{})
	if err != nil || resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health over TLS = %v, %v", resp, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if got := ConfigFromEnv(env(nil)).Addr; got != ":8443" {
		t.Errorf("default addr = %q", got)
	}
	c := ConfigFromEnv(env(map[string]string{"GRPC_ADDR": "127.0.0.1:1", "TLS_CERT_FILE": "c", "TLS_KEY_FILE": "k"}))
	if c.Addr != "127.0.0.1:1" || c.TLSCertFile != "c" || c.TLSKeyFile != "k" {
		t.Errorf("config = %+v", c)
	}
}
