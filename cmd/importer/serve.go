package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/scheduler"
	"github.com/willis-dev-md/data-importer/internal/server"
	"github.com/willis-dev-md/data-importer/internal/source"
	"github.com/willis-dev-md/data-importer/internal/store"
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
	scanLimit, err := server.ScanLimitFromEnv(env)
	if err != nil {
		fmt.Fprintf(stderr, "importer serve: %v\n", err)
		return 2
	}
	sched, err := scheduler.SettingsFromEnv(env)
	if err != nil {
		fmt.Fprintf(stderr, "importer serve: %s\n", strings.TrimPrefix(err.Error(), scheduler.ErrConfig.Error()+": "))
		return 2
	}
	var files store.FileStore
	if sched.Enabled {
		var code int
		if files, code = openFileStoreEnv("serve", env, stderr); code != exitOK {
			return code
		}
	}
	pool, popts, err := store.OpenPool(ctx, url, env)
	if errors.Is(err, store.ErrConfig) {
		fmt.Fprintf(stderr, "importer serve: %s\n", strings.TrimPrefix(err.Error(), store.ErrConfig.Error()+": "))
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "importer serve: %v\n", err)
		return 1
	}
	defer pool.Close()
	lockConns := popts.LockConns
	if lockConns <= 0 {
		lockConns = store.DefaultLockConns
	}
	if sched.Enabled && sched.Concurrency > lockConns {
		// Every running import holds one lock connection; more concurrent runs than lock connections would lose
		// slots waiting for one.
		fmt.Fprintf(stderr, "importer serve: IMPORT_CONCURRENCY=%d is more than DB_LOCK_MAX_CONNS=%d: raise DB_LOCK_MAX_CONNS or lower IMPORT_CONCURRENCY\n",
			sched.Concurrency, lockConns)
		return 2
	}
	store.WarnIfOverBudget(ctx, pool, popts, log)

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
	data := server.NewDataService(store.New(pool), server.DataOptions{ScanLimit: scanLimit, Logger: log})
	srv := server.New(server.Options{Ping: pool.Ping, TLS: tlsCfg, Logger: log, Data: data})

	// The scheduler stops with the server (or when the server fails) and is always joined before serve returns.
	sctx, stopSched := context.WithCancel(ctx)
	schedDone := make(chan struct{})
	if sched.Enabled {
		st := store.NewWithOptions(pool, popts)
		s := scheduler.New(scheduler.Config{
			Store: st, Run: scheduledRun(st, files, env), Fail: recordFailedRun(st), Sweep: st.SweepAbandonedRuns, PollInterval: sched.PollInterval, Concurrency: sched.Concurrency,
			ShutdownGrace: server.ShutdownTimeout, Logger: log,
		})
		log.Info("scheduler started", "poll_interval", sched.PollInterval.String(), "concurrency", sched.Concurrency)
		go func() { defer close(schedDone); s.Run(sctx) }()
	} else {
		log.Info("scheduler disabled (SCHEDULER_ENABLED=false)")
		close(schedDone)
	}
	err = srv.Run(ctx, lis)
	stopSched()
	<-schedDone
	if err != nil {
		fmt.Fprintf(stderr, "importer serve: %v\n", err)
		return 1
	}
	log.Info("server stopped")
	return 0
}

// scheduledRun returns the scheduler's RunFunc: it loads the source, resolves its secrets from the environment
// (as `importer run` does) and runs it with trigger 'schedule' and scheduled_for = the slot.
func scheduledRun(st *store.Store, files store.FileStore, env func(string) string) scheduler.RunFunc {
	return func(ctx context.Context, id string, slot time.Time) (importer.RunResult, error) {
		src, err := st.GetSource(ctx, id)
		if err != nil {
			return importer.RunResult{}, err
		}
		cfg, err := importer.ParseConfigJSON(src.Config)
		if err != nil {
			return importer.RunResult{}, fmt.Errorf("source %q: stored config: %w", id, err)
		}
		var fetch source.FetchOptions
		if cfg.Auth != nil {
			header, value, err := cfg.Auth.Resolve(env)
			if err != nil {
				return importer.RunResult{}, fmt.Errorf("source %q: %w", id, err)
			}
			fetch.Client = authClient(nil, cfg.Location, header, value)
		}
		return importer.Run(ctx, importer.Deps{
			Store:    st,
			Adapters: map[string]source.Adapter{config.KindCSV: source.NewCSV(fetch), config.KindXLSX: source.NewXLSX(fetch)},
			Files:    files,
		}, id, importer.Options{Trigger: "schedule", ScheduledFor: slot})
	}
}

// recordFailedRun is the scheduler's FailFunc: a failed run row (trigger schedule, scheduled_for = the slot).
func recordFailedRun(st *store.Store) scheduler.FailFunc {
	return func(ctx context.Context, id string, slot time.Time, cause error) error {
		_, err := st.RecordFailedRun(ctx, id, "schedule", slot, cause.Error())
		return err
	}
}
