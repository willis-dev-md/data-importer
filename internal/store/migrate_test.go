package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/willis-dev-md/data-importer/internal/store"
	"github.com/willis-dev-md/data-importer/internal/testutil"
)

var coreTables = []string{"sources", "mappings", "import_runs", "raw_records", "import_errors"}

func connect(t *testing.T, url string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func tableSet(t *testing.T, c *pgx.Conn) map[string]bool {
	t.Helper()
	rows, err := c.Query(context.Background(),
		`SELECT table_name FROM information_schema.tables WHERE table_schema='public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got[n] = true
	}
	return got
}

func TestMigrateUpDown(t *testing.T) {
	ctx := context.Background()
	url := testutil.NewDatabaseURL(t)

	v, err := store.Migrate(ctx, url, store.Version)
	if err != nil || v != 0 {
		t.Fatalf("initial version = %d, %v; want 0", v, err)
	}
	for i := 0; i < 2; i++ { // twice: idempotent
		if v, err = store.Migrate(ctx, url, store.Up); err != nil || v != 1 {
			t.Fatalf("up #%d = %d, %v; want 1", i, v, err)
		}
	}
	c := connect(t, url)
	got := tableSet(t, c)
	for _, n := range coreTables {
		if !got[n] {
			t.Errorf("table %s missing after up", n)
		}
	}
	for _, n := range []string{"source_files", "source_groups", "mapping_templates"} {
		if got[n] {
			t.Errorf("table %s must not exist yet", n)
		}
	}

	// Constraints and indexes.
	var n int
	q := func(sql string, args ...any) int {
		t.Helper()
		if err := c.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if q(`SELECT count(*) FROM information_schema.table_constraints
	      WHERE table_name='raw_records' AND constraint_type='PRIMARY KEY'`) != 1 {
		t.Error("raw_records primary key missing")
	}
	if q(`SELECT count(*) FROM information_schema.key_column_usage
	      WHERE constraint_name='raw_records_pkey' AND column_name IN ('source_id','natural_key')`) != 2 {
		t.Error("raw_records PK is not (source_id, natural_key)")
	}
	if q(`SELECT count(*) FROM information_schema.table_constraints
	      WHERE table_name='raw_records' AND constraint_type='FOREIGN KEY'`) != 3 {
		t.Error("raw_records should have 3 foreign keys")
	}
	if q(`SELECT count(*) FROM pg_indexes WHERE tablename='raw_records'
	      AND indexname='raw_records_payload_gin' AND indexdef LIKE '%gin%jsonb_path_ops%'`) != 1 {
		t.Error("GIN jsonb_path_ops index missing")
	}
	if q(`SELECT count(*) FROM pg_constraint WHERE conrelid='import_runs'::regclass AND contype='c'
	      AND pg_get_constraintdef(oid) LIKE '%unchanged_file%'`) != 1 {
		t.Error("import_runs status CHECK missing unchanged_file")
	}

	// Behaviour.
	if _, err := c.Exec(ctx, `INSERT INTO sources (id, kind, config) VALUES ('s','csv','{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, `INSERT INTO import_runs (source_id, status) VALUES ('s','bogus')`); err == nil {
		t.Error("status 'bogus' accepted")
	}
	if _, err := c.Exec(ctx, `INSERT INTO sources (id, kind, config) VALUES ('k','nope','{}')`); err == nil {
		t.Error("kind 'nope' accepted")
	}
	for _, st := range []string{"running", "succeeded", "unchanged_file", "failed", "aborted"} {
		if _, err := c.Exec(ctx, `INSERT INTO import_runs (source_id, status) VALUES ('s',$1)`, st); err != nil {
			t.Errorf("status %q rejected: %v", st, err)
		}
	}
	ins := `INSERT INTO raw_records (source_id, natural_key, payload, content_hash, first_run_id, last_run_id)
	        VALUES ($1,'k','{}','\x00',$2,$2)`
	var runID int64
	if err := c.QueryRow(ctx, `SELECT min(id) FROM import_runs`).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, ins, "unknown", runID); err == nil {
		t.Error("raw_records row for unknown source accepted")
	}
	if _, err := c.Exec(ctx, ins, "s", runID); err != nil {
		t.Fatalf("valid raw_records insert: %v", err)
	}
	if _, err := c.Exec(ctx, ins, "s", runID); err == nil {
		t.Error("duplicate (source_id, natural_key) accepted")
	}

	// Down removes everything (only the migrator's own table may remain).
	if v, err = store.Migrate(ctx, url, store.Down); err != nil || v != 0 {
		t.Fatalf("down = %d, %v; want 0", v, err)
	}
	c2 := connect(t, url)
	for n := range tableSet(t, c2) {
		if n != "schema_migrations" {
			t.Errorf("table %s left after down", n)
		}
	}
	if v, err = store.Migrate(ctx, url, store.Down); err != nil || v != 0 {
		t.Errorf("second down = %d, %v; want no-op at 0", v, err)
	}

	// up/down/up leaves a working schema.
	if v, err = store.Migrate(ctx, url, store.Up); err != nil || v != 1 {
		t.Fatalf("re-up = %d, %v", v, err)
	}
	c3 := connect(t, url)
	if _, err := c3.Exec(ctx, `INSERT INTO sources (id, kind, config) VALUES ('s','http_json','{}')`); err != nil {
		t.Errorf("insert after re-up: %v", err)
	}
}

func TestMigrateErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := store.Migrate(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2", store.Up); err == nil {
		t.Error("unreachable database: want error")
	}
	if _, err := store.Migrate(ctx, "postgres://x", store.Direction("sideways")); err == nil {
		t.Error("bad direction: want error")
	}
}
