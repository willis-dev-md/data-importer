package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5 database driver
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/willis-dev-md/data-importer/migrations"
)

// Direction is what Migrate should do.
type Direction string

const (
	// Up applies all pending migrations.
	Up Direction = "up"
	// Down rolls back exactly one migration.
	Down Direction = "down"
	// Version only reports the current version.
	Version Direction = "version"
)

// Migrate applies the embedded migrations to the database at url and returns the
// resulting version (0 when none is applied). It is safe to call repeatedly:
// Up with nothing pending, or Down at version 0, are no-ops. A dirty database
// returns an error naming the dirty version.
func Migrate(ctx context.Context, url string, dir Direction) (uint, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	switch dir {
	case Up, Down, Version:
	default:
		return 0, fmt.Errorf("unknown migrate direction %q (want up, down or version)", dir)
	}
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return 0, fmt.Errorf("open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, driverURL(url))
	if err != nil {
		return 0, fmt.Errorf("connect to database: %w", err)
	}
	defer m.Close()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			m.GracefulStop <- true
		case <-stop:
		}
	}()

	switch dir {
	case Up:
		err = m.Up()
	case Down:
		err = m.Steps(-1)
		if errors.Is(err, os.ErrNotExist) { // already at version 0
			err = nil
		}
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) && !errors.Is(err, migrate.ErrNilVersion) {
		return 0, fmt.Errorf("migrate %s: %w", dir, err)
	}
	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read migration version: %w", err)
	}
	if dirty {
		return v, fmt.Errorf("database is at dirty migration version %d: fix manually, then force the version (see docs/runbooks/database-migrations.md)", v)
	}
	return v, nil
}

// driverURL rewrites postgres:// and postgresql:// to the pgx5:// scheme used by golang-migrate.
func driverURL(u string) string {
	for _, p := range []string{"postgresql://", "postgres://"} {
		if strings.HasPrefix(u, p) {
			return "pgx5://" + strings.TrimPrefix(u, p)
		}
	}
	return u
}
