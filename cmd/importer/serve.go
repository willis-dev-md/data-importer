package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/server"
)

// runServe implements `importer serve`: it runs the gRPC server until SIGINT or
// SIGTERM, then shuts down gracefully (at most server.ShutdownTimeout).
func runServe(args []string, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal restore default handling so a second one kills the process.
	go func() { <-ctx.Done(); stop() }()
	return serve(ctx, args, getenv, stderr)
}

func serve(ctx context.Context, args []string, env func(string) string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: importer serve")
		return 2
	}
	log := slog.New(slog.NewTextHandler(stderr, nil))
	cfg := server.ConfigFromEnv(env)
	tlsCfg, err := cfg.TLS()
	if err != nil {
		fmt.Fprintf(stderr, "importer serve: %v\n", err)
		return 1
	}
	url := env("DATABASE_URL")
	if url == "" {
		fmt.Fprintln(stderr, "importer serve: DATABASE_URL is not set")
		return 1
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		fmt.Fprintf(stderr, "importer serve: %v\n", err)
		return 1
	}
	defer pool.Close()

	lis, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		fmt.Fprintf(stderr, "importer serve: %v\n", err)
		return 1
	}
	if tlsCfg == nil {
		log.Warn("TLS_CERT_FILE and TLS_KEY_FILE are not set: serving plaintext gRPC", "addr", lis.Addr().String())
	} else {
		log.Info("serving gRPC with TLS", "addr", lis.Addr().String())
	}
	srv := server.New(server.Options{Ping: pool.Ping, TLS: tlsCfg, Logger: log})
	if err := srv.Run(ctx, lis); err != nil {
		fmt.Fprintf(stderr, "importer serve: %v\n", err)
		return 1
	}
	log.Info("server stopped")
	return 0
}
