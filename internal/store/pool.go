package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Defaults for the lock connections (see Options).
const (
	DefaultLockConns = 8
	DefaultLockWait  = 30 * time.Second

	// The lock monitor checks its session every interval; the interval is bounded
	// so a typo can neither hammer the database nor make detection useless.
	DefaultLockCheckInterval = 5 * time.Second
	MinLockCheckInterval     = 10 * time.Millisecond
	MaxLockCheckInterval     = 10 * time.Minute
)

// Options configures the advisory-lock connections of a Store. They are
// dedicated connections outside the work pool, so the total number of database
// connections of a process is at most work pool MaxConns + LockConns.
type Options struct {
	LockConns int           // max concurrent source locks (default DefaultLockConns)
	LockWait  time.Duration // max wait for a free lock slot (default DefaultLockWait)
	// LockCheckInterval is how often a monitored lock (TryAdvisoryLockMonitored)
	// verifies that its session still holds the lock (default
	// DefaultLockCheckInterval; clamped to [MinLockCheckInterval, MaxLockCheckInterval]).
	LockCheckInterval time.Duration
}

// Environment variables read by OpenPool.
const (
	EnvMaxConns     = "DB_MAX_CONNS"      // work pool size (default max(4, NumCPU), pgxpool's default)
	EnvLockMaxConns = "DB_LOCK_MAX_CONNS" // concurrent source locks (default 8)
	EnvLockWait     = "DB_LOCK_WAIT"      // Go duration, max wait for a lock slot (default 30s)
	// EnvLockCheckInterval is the Go duration between lock-session checks of a
	// running import (default 5s, allowed 10ms to 10m).
	EnvLockCheckInterval = "DB_LOCK_CHECK_INTERVAL"
)

// ErrConfig marks an invalid pool/lock environment setting (a usage error, not
// a connection failure; nothing has touched the database when it is returned).
var ErrConfig = errors.New("invalid configuration")

func envInt(getenv func(string) string, name string) (int, bool, error) {
	v := getenv(name)
	if v == "" {
		return 0, false, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 1<<20 {
		return 0, false, fmt.Errorf("%w: %s=%q: want a positive integer", ErrConfig, name, v)
	}
	return n, true, nil
}

// OpenPool builds the work pool and the lock options from url and the
// environment (getenv). Invalid or non-positive values return an error wrapping
// ErrConfig before any connection is made.
func OpenPool(ctx context.Context, url string, getenv func(string) string) (*pgxpool.Pool, Options, error) {
	var opts Options
	maxConns, haveMax, err := envInt(getenv, EnvMaxConns)
	if err != nil {
		return nil, opts, err
	}
	if n, ok, err := envInt(getenv, EnvLockMaxConns); err != nil {
		return nil, opts, err
	} else if ok {
		opts.LockConns = n
	}
	if v := getenv(EnvLockWait); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, opts, fmt.Errorf("%w: %s=%q: want a positive Go duration such as 30s", ErrConfig, EnvLockWait, v)
		}
		opts.LockWait = d
	}
	if v := getenv(EnvLockCheckInterval); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < MinLockCheckInterval || d > MaxLockCheckInterval {
			return nil, opts, fmt.Errorf("%w: %s=%q: want a Go duration between %s and %s such as 5s", ErrConfig, EnvLockCheckInterval, v, MinLockCheckInterval, MaxLockCheckInterval)
		}
		opts.LockCheckInterval = d
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, opts, fmt.Errorf("parse database url: %w", err)
	}
	if haveMax {
		cfg.MaxConns = int32(maxConns)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, opts, err
	}
	return pool, opts, nil
}

// ConnectionBudgetWarning returns a warning message and true when the work pool
// plus the lock connections can exceed the server's max_connections.
func ConnectionBudgetWarning(workConns, lockConns, serverMax int) (string, bool) {
	if workConns+lockConns <= serverMax {
		return "", false
	}
	return fmt.Sprintf("%s (%d) + %s (%d) = %d exceeds the server's max_connections (%d); connections may be refused under load",
		EnvMaxConns, workConns, EnvLockMaxConns, lockConns, workConns+lockConns, serverMax), true
}

var budgetOnce sync.Once

// WarnIfOverBudget reads SHOW max_connections once per process and logs a WARN
// when ConnectionBudgetWarning says so. It never fails: errors are ignored.
// lockConns is Options.LockConns (0 = default).
func WarnIfOverBudget(ctx context.Context, pool *pgxpool.Pool, opts Options, log *slog.Logger) {
	budgetOnce.Do(func() {
		var raw string
		if err := pool.QueryRow(ctx, `SHOW max_connections`).Scan(&raw); err != nil {
			return
		}
		serverMax, err := strconv.Atoi(raw)
		if err != nil {
			return
		}
		lock := opts.LockConns
		if lock <= 0 {
			lock = DefaultLockConns
		}
		if msg, warn := ConnectionBudgetWarning(int(pool.Config().MaxConns), lock, serverMax); warn {
			log.Warn(msg)
		}
	})
}
