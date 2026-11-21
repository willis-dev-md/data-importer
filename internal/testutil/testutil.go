// Package testutil provides helpers for integration tests against a real Postgres.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DatabaseURL returns DATABASE_URL. If it is unset the test is skipped,
// unless CI_REQUIRE_DB=1, in which case the test fails.
func DatabaseURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv("DATABASE_URL")
	if u == "" {
		if os.Getenv("CI_REQUIRE_DB") == "1" {
			t.Fatal("DATABASE_URL is not set and CI_REQUIRE_DB=1")
		}
		t.Skip("DATABASE_URL is not set; run scripts/testdb.sh start")
	}
	return u
}

// NewDatabaseURL creates a fresh, uniquely named database on the server named
// by DATABASE_URL, drops it in t.Cleanup, and returns its connection URL.
func NewDatabaseURL(t testing.TB) string {
	t.Helper()
	adminURL := DatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect to admin database: %v", err)
	}
	defer admin.Close(ctx)

	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random name: %v", err)
	}
	name := "t_" + hex.EncodeToString(b[:])
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, err := pgx.Connect(ctx, adminURL)
		if err != nil {
			t.Errorf("cleanup connect: %v", err)
			return
		}
		defer c.Close(ctx)
		if _, err := c.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}

// DB returns a pool connected to a fresh per-test database. The pool is closed
// and the database dropped in t.Cleanup.
func DB(t testing.TB) *pgxpool.Pool {
	t.Helper()
	dbURL := NewDatabaseURL(t)
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	// Registered after the drop cleanup, so it runs first (LIFO).
	t.Cleanup(pool.Close)
	return pool
}
