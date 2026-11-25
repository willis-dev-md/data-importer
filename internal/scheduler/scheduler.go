package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/willis-dev-md/data-importer/internal/importer"
	"github.com/willis-dev-md/data-importer/internal/store"
)

// Settings and their environment variables.
const (
	EnvEnabled      = "SCHEDULER_ENABLED"
	EnvPollInterval = "SCHEDULER_POLL_INTERVAL"
	EnvConcurrency  = "IMPORT_CONCURRENCY"

	DefaultPollInterval = 30 * time.Second
	MinPollInterval     = 100 * time.Millisecond
	MaxPollInterval     = time.Hour
	DefaultConcurrency  = 2
	MaxConcurrency      = 1 << 16

	// DefaultShutdownGrace is how long in-flight runs may finish after the scheduler is asked to stop; after
	// that they are cancelled and finish as failed (cancelled), like a manual run that gets SIGTERM.
	DefaultShutdownGrace = 10 * time.Second
)

// ErrConfig marks an invalid scheduler environment setting (a usage error).
var ErrConfig = errors.New("invalid configuration")

// Settings are the scheduler's environment settings.
type Settings struct {
	Enabled      bool
	PollInterval time.Duration
	Concurrency  int
}

// SettingsFromEnv reads SCHEDULER_ENABLED (default true), SCHEDULER_POLL_INTERVAL (Go duration, default 30s,
// 100ms..1h) and IMPORT_CONCURRENCY (positive integer, default 2). Errors wrap ErrConfig and name the variable.
func SettingsFromEnv(getenv func(string) string) (Settings, error) {
	s := Settings{Enabled: true, PollInterval: DefaultPollInterval, Concurrency: DefaultConcurrency}
	if v := getenv(EnvEnabled); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return s, fmt.Errorf("%w: %s=%q: want true or false", ErrConfig, EnvEnabled, v)
		}
		s.Enabled = b
	}
	if v := getenv(EnvPollInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < MinPollInterval || d > MaxPollInterval {
			return s, fmt.Errorf("%w: %s=%q: want a Go duration between %s and %s such as 30s", ErrConfig, EnvPollInterval, v, MinPollInterval, MaxPollInterval)
		}
		s.PollInterval = d
	}
	if v := getenv(EnvConcurrency); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxConcurrency {
			return s, fmt.Errorf("%w: %s=%q: want a positive integer", ErrConfig, EnvConcurrency, v)
		}
		s.Concurrency = n
	}
	return s, nil
}

// Clock is the time source of the scheduler (injected so tests need no real waiting).
type Clock interface {
	Now() time.Time
	// NewTicker returns a channel that receives about every d, and a stop func.
	NewTicker(d time.Duration) (<-chan time.Time, func())
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// Claimer is the part of *store.Store the scheduler needs.
type Claimer interface {
	ClaimDue(ctx context.Context, now time.Time, max int) ([]store.Claim, error)
}

// RunFunc runs one scheduled import of sourceID for slot (trigger 'schedule', scheduled_for = slot).
// importer.ErrAlreadyRunning means the previous run is still going: the slot is skipped.
type RunFunc func(ctx context.Context, sourceID string, slot time.Time) (importer.RunResult, error)

// FailFunc records a scheduled run that failed before it had a run row (lock capacity, missing secret, unknown
// source, ...) as a failed run, so the lost slot is visible. Its error is only logged.
type FailFunc func(ctx context.Context, sourceID string, slot time.Time, cause error) error

// SweepFunc closes runs left 'running' by a process that died (store.SweepAbandonedRuns); it only touches rows
// whose source lock is not held.
type SweepFunc func(ctx context.Context) ([]int64, error)

// Config configures a Scheduler.
type Config struct {
	Store         Claimer
	Run           RunFunc
	Fail          FailFunc      // optional; see FailFunc
	Sweep         SweepFunc     // optional; called at the start of every poll (and so at start)
	Clock         Clock         // default: the real clock
	PollInterval  time.Duration // default DefaultPollInterval
	Concurrency   int           // default DefaultConcurrency
	ShutdownGrace time.Duration // default DefaultShutdownGrace
	Logger        *slog.Logger  // default slog.Default()
}

// Scheduler polls for due sources and runs them.
type Scheduler struct {
	cfg Config
	log *slog.Logger

	runCtx    context.Context // parent of every run; cancelled when the grace period ends
	cancelRun context.CancelFunc
	wg        sync.WaitGroup
	sem       chan struct{} // one token per running import

	mu    sync.Mutex
	stats Stats
}

// Stats counts what the scheduler did (for tests and logs).
type Stats struct {
	Claimed        int // slots handed to a run
	Started        int // runs that created a run row or ended without one
	SkippedRunning int // slots skipped because the source's previous run still held its lock
	PassedOver     int // slots passed over without a run (on_missed = skip, or older slots of run_once)
	Failed         int // runs that ended failed or aborted (not retried)
	Panics         int
}

// New returns a Scheduler; Run starts it.
func New(cfg Config) *Scheduler {
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = DefaultShutdownGrace
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Scheduler{cfg: cfg, log: cfg.Logger, sem: make(chan struct{}, cfg.Concurrency)}
}

// Stats returns a snapshot of the counters.
func (s *Scheduler) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

func (s *Scheduler) count(f func(*Stats)) {
	s.mu.Lock()
	f(&s.stats)
	s.mu.Unlock()
}

// Run polls until ctx is cancelled, then stops claiming, gives in-flight runs ShutdownGrace to finish, cancels
// the rest (they finish as failed, cancelled) and returns only after every goroutine it started has ended.
// It polls once immediately, then every PollInterval.
func (s *Scheduler) Run(ctx context.Context) {
	s.runCtx, s.cancelRun = context.WithCancel(context.WithoutCancel(ctx))
	defer s.cancelRun()
	tick, stop := s.cfg.Clock.NewTicker(s.cfg.PollInterval)
	defer stop()

	s.Poll(ctx)
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-tick:
			s.Poll(ctx)
		}
	}

	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	grace := time.NewTimer(s.cfg.ShutdownGrace)
	defer grace.Stop()
	select {
	case <-done:
	case <-grace.C:
		s.log.Warn("scheduler shutdown: cancelling runs still in flight")
		s.cancelRun()
		<-done
	}
}

// Poll claims what is due now and starts a run for each claimed slot. It never blocks on a run. Run calls it;
// tests may call it directly after setting a context for runs with Start.
func (s *Scheduler) Poll(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if s.cfg.Sweep != nil {
		ids, err := s.cfg.Sweep(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			s.log.Error("scheduler: sweep of abandoned runs failed", "err", err)
		case len(ids) > 0:
			s.log.Warn("scheduler: marked abandoned runs failed (their process died)", "runs", ids)
		}
	}
	free := cap(s.sem) - len(s.sem)
	if free <= 0 {
		return
	}
	claims, err := s.cfg.Store.ClaimDue(ctx, s.cfg.Clock.Now(), free)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("scheduler: claim failed", "err", err)
		}
		return
	}
	for _, c := range claims {
		switch {
		case c.Err != nil:
			s.log.Error("scheduler: source skipped", "source", c.SourceID, "err", c.Err)
		case c.Slot.IsZero():
			s.count(func(st *Stats) { st.PassedOver += c.Missed })
			s.log.Warn("scheduler: missed slots passed over (on_missed: skip)", "source", c.SourceID, "missed", c.Missed, "next_run_at", c.Next)
		default:
			if c.Missed > 0 {
				s.count(func(st *Stats) { st.PassedOver += c.Missed })
				s.log.Warn("scheduler: running the latest missed slot once; older ones passed over", "source", c.SourceID, "slot", c.Slot, "passed_over", c.Missed)
			}
			s.start(c)
		}
	}
}

func (s *Scheduler) start(c store.Claim) {
	if s.runCtx == nil { // Poll called without Run: runs are not cancelled by grace
		s.runCtx, s.cancelRun = context.WithCancel(context.Background())
	}
	s.sem <- struct{}{} // never blocks: Poll claimed at most the free slots, and only Poll adds tokens
	s.count(func(st *Stats) { st.Claimed++ })
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.sem }()
		s.runOne(s.runCtx, c)
	}()
}

// Wait blocks until every started run has ended (for tests that call Poll directly).
func (s *Scheduler) Wait() { s.wg.Wait() }

func (s *Scheduler) runOne(ctx context.Context, c store.Claim) {
	defer func() {
		if p := recover(); p != nil {
			s.count(func(st *Stats) { st.Panics++ })
			s.log.Error("scheduler: run panicked (the run is closed as failed; the loop continues)", "source", c.SourceID, "slot", c.Slot, "panic", fmt.Sprint(p))
		}
	}()
	res, err := s.cfg.Run(ctx, c.SourceID, c.Slot)
	switch {
	case errors.Is(err, importer.ErrAlreadyRunning):
		s.count(func(st *Stats) { st.SkippedRunning++ })
		s.log.Warn("scheduler: slot skipped, the previous run of this source is still running", "source", c.SourceID, "slot", c.Slot)
	case err != nil:
		if res.RunID == 0 && s.cfg.Fail != nil { // it never got a run row: make the lost slot visible
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if ferr := s.cfg.Fail(fctx, c.SourceID, c.Slot, err); ferr != nil {
				s.log.Error("scheduler: could not record the failed run", "source", c.SourceID, "slot", c.Slot, "err", ferr)
			}
			cancel()
		}
		s.count(func(st *Stats) { st.Started++; st.Failed++ })
		s.log.Error("scheduler: scheduled run failed (not retried; the next slot is the retry)", "source", c.SourceID, "slot", c.Slot, "run", res.RunID, "status", res.Status, "err", err)
	default:
		s.count(func(st *Stats) { st.Started++ })
		s.log.Info("scheduler: scheduled run finished", "source", c.SourceID, "slot", c.Slot, "run", res.RunID, "status", res.Status,
			"inserted", res.Inserted, "updated", res.Updated, "unchanged", res.Unchanged, "failed", res.Failed)
	}
}
