package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	pb "github.com/willis-dev-md/data-importer/gen/dataimporter/v1"
)

const (
	// DefaultAddr is the listen address used when GRPC_ADDR is unset.
	DefaultAddr = ":8443"
	// ShutdownTimeout bounds how long graceful shutdown waits for in-flight calls.
	ShutdownTimeout = 10 * time.Second
	// DefaultHealthInterval is how often the database is pinged to refresh health.
	DefaultHealthInterval = 5 * time.Second
)

// Config is the server configuration, normally read from the environment.
type Config struct {
	Addr        string // GRPC_ADDR; empty means DefaultAddr
	TLSCertFile string // TLS_CERT_FILE
	TLSKeyFile  string // TLS_KEY_FILE
}

// ConfigFromEnv builds a Config from GRPC_ADDR, TLS_CERT_FILE and TLS_KEY_FILE.
func ConfigFromEnv(getenv func(string) string) Config {
	c := Config{
		Addr:        getenv("GRPC_ADDR"),
		TLSCertFile: getenv("TLS_CERT_FILE"),
		TLSKeyFile:  getenv("TLS_KEY_FILE"),
	}
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	return c
}

// TLS returns the TLS config, or nil for plaintext when neither file is set.
// Setting only one of the two files is an error.
func (c Config) TLS() (*tls.Config, error) {
	switch {
	case c.TLSCertFile == "" && c.TLSKeyFile == "":
		return nil, nil
	case c.TLSCertFile == "":
		return nil, errors.New("TLS_KEY_FILE is set but TLS_CERT_FILE is not: set both or neither")
	case c.TLSKeyFile == "":
		return nil, errors.New("TLS_CERT_FILE is set but TLS_KEY_FILE is not: set both or neither")
	}
	cert, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

// Options configure New.
type Options struct {
	// Ping checks the database. Health is SERVING only while it returns nil.
	Ping func(context.Context) error
	// Data is the DataService implementation; nil serves codes.Unimplemented.
	Data pb.DataServiceServer
	// TLS enables TLS when non-nil.
	TLS *tls.Config
	// HealthInterval overrides DefaultHealthInterval.
	HealthInterval time.Duration
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// Server is the gRPC server: DataService, grpc.health.v1 and reflection.
type Server struct {
	grpc     *grpc.Server
	health   *health.Server
	stopping chan struct{} // closed when shutdown begins; ends Watch streams
	ping     func(context.Context) error
	interval time.Duration
	log      *slog.Logger
}

// New builds a Server. Health starts as NOT_SERVING until a ping succeeds.
func New(o Options) *Server {
	data := o.Data
	if data == nil {
		data = pb.UnimplementedDataServiceServer{}
	}
	var opts []grpc.ServerOption
	if o.TLS != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(o.TLS)))
	}
	s := &Server{
		grpc:     grpc.NewServer(opts...),
		health:   health.NewServer(),
		stopping: make(chan struct{}),
		ping:     o.Ping,
		interval: o.HealthInterval,
		log:      o.Logger,
	}
	if s.interval <= 0 {
		s.interval = DefaultHealthInterval
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	pb.RegisterDataServiceServer(s.grpc, data)
	healthpb.RegisterHealthServer(s.grpc, watchEnder{s.health, s.stopping})
	reflection.Register(s.grpc)
	s.setHealth(healthpb.HealthCheckResponse_NOT_SERVING)
	return s
}

func (s *Server) setHealth(st healthpb.HealthCheckResponse_ServingStatus) {
	s.health.SetServingStatus("", st)
	s.health.SetServingStatus(pb.DataService_ServiceDesc.ServiceName, st)
}

// CheckHealth pings the database once and updates the health status.
func (s *Server) CheckHealth(ctx context.Context) error {
	var err error
	if s.ping == nil {
		err = errors.New("no database ping configured")
	} else {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = s.ping(pctx)
		cancel()
	}
	if err != nil {
		s.setHealth(healthpb.HealthCheckResponse_NOT_SERVING)
		return fmt.Errorf("database ping: %w", err)
	}
	s.setHealth(healthpb.HealthCheckResponse_SERVING)
	return nil
}

// Run serves on lis until ctx is cancelled, then shuts down gracefully:
// in-flight calls may finish for up to ShutdownTimeout, after which they are cut off.
func (s *Server) Run(ctx context.Context, lis net.Listener) error {
	hctx, stopHealth := context.WithCancel(ctx)
	loopDone := make(chan struct{})
	defer func() { stopHealth(); <-loopDone }()

	// First check runs before Serve so the status is never a stale NOT_SERVING
	// once the server answers. Connections made meanwhile queue in the listener.
	if err := s.CheckHealth(hctx); err != nil {
		s.log.Warn("health check failed", "err", err)
	}
	go func() { defer close(loopDone); s.healthLoop(hctx) }()

	errc := make(chan error, 1)
	go func() { errc <- s.grpc.Serve(lis) }()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	stopHealth()
	<-loopDone
	s.health.Shutdown()
	close(s.stopping)
	done := make(chan struct{})
	go func() { s.grpc.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(ShutdownTimeout):
		s.log.Warn("graceful shutdown timed out; forcing stop")
		s.grpc.Stop()
		<-done
	}
	return nil
}

func (s *Server) healthLoop(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	var last error
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			err := s.CheckHealth(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil && last == nil {
				s.log.Warn("health check failed", "err", err)
			}
			last = err
		}
	}
}

// watchEnder ends health Watch streams when shutdown begins; otherwise an open
// Watch would hold GracefulStop until ShutdownTimeout.
type watchEnder struct {
	*health.Server
	stopping <-chan struct{}
}

func (w watchEnder) Watch(req *healthpb.HealthCheckRequest, stream healthpb.Health_WatchServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	go func() {
		select {
		case <-w.stopping:
			cancel()
		case <-ctx.Done():
		}
	}()
	return w.Server.Watch(req, watchStream{stream, ctx})
}

type watchStream struct {
	healthpb.Health_WatchServer
	ctx context.Context
}

func (s watchStream) Context() context.Context { return s.ctx }
