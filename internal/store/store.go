package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/record"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Store is the Postgres-backed store for sources, runs, raw records and import errors.
type Store struct {
	pool *pgxpool.Pool
}

// New returns a Store using pool. The schema must already be migrated (see Migrate).
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Source is a row of the sources table.
type Source struct {
	ID                   string
	Kind                 string // csv, xlsx or http_json
	Config               json.RawMessage
	ActiveMappingVersion *int
	CreatedAt            time.Time
}

// Counts are the per-batch outcomes of UpsertRaw.
type Counts struct {
	Inserted  int
	Updated   int
	Unchanged int
}

// Add returns the sum of c and o.
func (c Counts) Add(o Counts) Counts {
	return Counts{c.Inserted + o.Inserted, c.Updated + o.Updated, c.Unchanged + o.Unchanged}
}

// RunCounts are the totals stored on an import run when it is finished.
type RunCounts struct {
	Counts
	Failed     int
	Tombstoned int
}

// Run is a row of the import_runs table.
type Run struct {
	ID         int64
	SourceID   string
	Status     string
	Trigger    string
	SourceMeta json.RawMessage
	Shape      json.RawMessage
	StartedAt  time.Time
	FinishedAt *time.Time
	RunCounts
	Error *string
}

// RowError is one bad input row, recorded in import_errors.
type RowError struct {
	Position string // csv row number, api page+index, ...
	Raw      string // offending raw input
	Reason   string
}

// RawRecord is a row of raw_records.
type RawRecord struct {
	SourceID    string
	Key         string
	Payload     map[string]any // numbers are json.Number
	ContentHash []byte
	FirstRunID  int64
	LastRunID   int64
	ImportedAt  time.Time
	DeletedAt   *time.Time
}

// UpsertSource inserts the source, or replaces its kind and config if it exists.
// active_mapping_version and created_at are left untouched on update.
func (s *Store) UpsertSource(ctx context.Context, src Source) error {
	cfg := src.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage(`{}`)
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO sources (id, kind, config, active_mapping_version)
VALUES ($1, $2, $3::jsonb, $4)
ON CONFLICT (id) DO UPDATE SET kind = EXCLUDED.kind, config = EXCLUDED.config`,
		src.ID, src.Kind, string(cfg), src.ActiveMappingVersion)
	if err != nil {
		return fmt.Errorf("upsert source %q: %w", src.ID, err)
	}
	return nil
}

// GetSource returns the source, or ErrNotFound.
func (s *Store) GetSource(ctx context.Context, id string) (Source, error) {
	var src Source
	var cfg []byte
	err := s.pool.QueryRow(ctx,
		`SELECT id, kind, config, active_mapping_version, created_at FROM sources WHERE id = $1`, id).
		Scan(&src.ID, &src.Kind, &cfg, &src.ActiveMappingVersion, &src.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, fmt.Errorf("source %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Source{}, fmt.Errorf("get source %q: %w", id, err)
	}
	src.Config = cfg
	return src, nil
}

// CreateRun starts a run in status 'running' and returns its id. trigger is
// 'manual' or 'schedule'; an empty trigger means 'manual'.
func (s *Store) CreateRun(ctx context.Context, sourceID, trigger string) (int64, error) {
	if trigger == "" {
		trigger = "manual"
	}
	var id int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO import_runs (source_id, status, trigger) VALUES ($1, 'running', $2) RETURNING id`,
		sourceID, trigger).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create run for source %q: %w", sourceID, err)
	}
	return id, nil
}

// FinishRun closes a run: it sets status, finished_at = now(), the counts, the
// shape summary and source metadata (nil = NULL) and the error text ("" = NULL).
// status must be one of succeeded, unchanged_file, failed, aborted (or running).
func (s *Store) FinishRun(ctx context.Context, runID int64, status string, counts RunCounts, shape, sourceMeta json.RawMessage, errText string) error {
	var shapeArg, metaArg, errArg *string
	if len(shape) > 0 {
		v := string(shape)
		shapeArg = &v
	}
	if len(sourceMeta) > 0 {
		v := string(sourceMeta)
		metaArg = &v
	}
	if errText != "" {
		errArg = &errText
	}
	tag, err := s.pool.Exec(ctx, `
UPDATE import_runs SET status = $2, finished_at = now(),
  inserted = $3, updated = $4, unchanged = $5, failed = $6, tombstoned = $7,
  shape = $8::jsonb, source_meta = $9::jsonb, error = $10
WHERE id = $1`,
		runID, status, counts.Inserted, counts.Updated, counts.Unchanged, counts.Failed, counts.Tombstoned,
		shapeArg, metaArg, errArg)
	if err != nil {
		return fmt.Errorf("finish run %d: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %d: %w", runID, ErrNotFound)
	}
	return nil
}

// GetRun returns the run, or ErrNotFound.
func (s *Store) GetRun(ctx context.Context, runID int64) (Run, error) {
	var r Run
	var shape, meta []byte
	err := s.pool.QueryRow(ctx, `
SELECT id, source_id, status, trigger, source_meta, shape, started_at, finished_at,
       inserted, updated, unchanged, failed, tombstoned, error
FROM import_runs WHERE id = $1`, runID).Scan(
		&r.ID, &r.SourceID, &r.Status, &r.Trigger, &meta, &shape, &r.StartedAt, &r.FinishedAt,
		&r.Inserted, &r.Updated, &r.Unchanged, &r.Failed, &r.Tombstoned, &r.Error)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, fmt.Errorf("run %d: %w", runID, ErrNotFound)
	}
	if err != nil {
		return Run{}, fmt.Errorf("get run %d: %w", runID, err)
	}
	r.Shape, r.SourceMeta = shape, meta
	return r, nil
}

// upsertSQL is one statement for a whole batch. "old" and the data-modifying CTE
// share one snapshot, so old.content_hash is the hash before this statement.
// Unchanged rows only get last_run_id updated: payload, content_hash and
// imported_at are kept (imported_at means "last time the content changed").
const upsertSQL = `
WITH input AS (
  SELECT * FROM unnest($3::text[], $4::text[], $5::bytea[]) AS t(natural_key, payload, content_hash)
), old AS (
  SELECT natural_key, content_hash FROM raw_records
  WHERE source_id = $1 AND natural_key = ANY($3::text[])
), up AS (
  INSERT INTO raw_records (source_id, natural_key, payload, content_hash, first_run_id, last_run_id)
  SELECT $1, natural_key, payload::jsonb, content_hash, $2, $2 FROM input
  ON CONFLICT (source_id, natural_key) DO UPDATE SET
    payload      = CASE WHEN raw_records.content_hash = EXCLUDED.content_hash THEN raw_records.payload ELSE EXCLUDED.payload END,
    imported_at  = CASE WHEN raw_records.content_hash = EXCLUDED.content_hash THEN raw_records.imported_at ELSE now() END,
    content_hash = EXCLUDED.content_hash,
    last_run_id  = EXCLUDED.last_run_id,
    deleted_at   = NULL
  RETURNING natural_key, content_hash
)
SELECT
  count(*) FILTER (WHERE old.natural_key IS NULL),
  count(*) FILTER (WHERE old.natural_key IS NOT NULL AND old.content_hash <> up.content_hash),
  count(*) FILTER (WHERE old.natural_key IS NOT NULL AND old.content_hash = up.content_hash)
FROM up LEFT JOIN old USING (natural_key)`

// UpsertRaw writes recs for sourceID in run runID, in one transaction and one
// statement. A new key is Inserted (first_run_id = last_run_id = runID); an
// existing key with the same content hash is Unchanged (payload and imported_at
// are not rewritten, last_run_id is set to runID); an existing key with a
// different hash is Updated. A tombstoned row that reappears has deleted_at
// cleared. If a key occurs more than once in recs the last occurrence wins and
// the key is counted once. A record with an empty key or a payload that cannot
// be canonicalised makes the whole call fail and nothing is written.
func (s *Store) UpsertRaw(ctx context.Context, runID int64, sourceID string, recs []record.Record) (Counts, error) {
	if len(recs) == 0 {
		return Counts{}, nil
	}
	idx := make(map[string]int, len(recs))
	keys := make([]string, 0, len(recs))
	payloads := make([]string, 0, len(recs))
	hashes := make([][]byte, 0, len(recs))
	for _, r := range recs {
		if r.Key == "" {
			return Counts{}, errors.New("upsert raw: record with empty key")
		}
		canon, err := record.Canonical(r.Payload)
		if err != nil {
			return Counts{}, fmt.Errorf("upsert raw: record %q: %w", r.Key, err)
		}
		h, err := record.Sum(r.Payload)
		if err != nil {
			return Counts{}, fmt.Errorf("upsert raw: record %q: %w", r.Key, err)
		}
		if i, dup := idx[r.Key]; dup {
			payloads[i], hashes[i] = string(canon), h
			continue
		}
		idx[r.Key] = len(keys)
		keys = append(keys, r.Key)
		payloads = append(payloads, string(canon))
		hashes = append(hashes, h)
	}

	var c Counts
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, upsertSQL, sourceID, runID, keys, payloads, hashes).
			Scan(&c.Inserted, &c.Updated, &c.Unchanged)
	})
	if err != nil {
		return Counts{}, fmt.Errorf("upsert raw (source %q, run %d, %d records): %w", sourceID, runID, len(recs), err)
	}
	return c, nil
}

// RecordErrors stores bad rows for the run in one statement. NUL bytes and
// invalid UTF-8 in Position and Raw are replaced/removed so any input can be stored.
func (s *Store) RecordErrors(ctx context.Context, runID int64, errs []RowError) error {
	if len(errs) == 0 {
		return nil
	}
	pos := make([]string, len(errs))
	raw := make([]string, len(errs))
	reason := make([]string, len(errs))
	for i, e := range errs {
		pos[i], raw[i], reason[i] = cleanText(e.Position), cleanText(e.Raw), cleanText(e.Reason)
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO import_errors (run_id, position, raw, reason)
SELECT $1, position, raw, reason FROM unnest($2::text[], $3::text[], $4::text[]) AS t(position, raw, reason)`,
		runID, pos, raw, reason)
	if err != nil {
		return fmt.Errorf("record %d errors for run %d: %w", len(errs), runID, err)
	}
	return nil
}

func cleanText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.ReplaceAll(s, "\x00", "")
}

// ListErrors returns the recorded errors of a run in insertion order.
func (s *Store) ListErrors(ctx context.Context, runID int64) ([]RowError, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT coalesce(position,''), coalesce(raw,''), reason FROM import_errors WHERE run_id = $1 ORDER BY id`, runID)
	if err != nil {
		return nil, fmt.Errorf("list errors for run %d: %w", runID, err)
	}
	defer rows.Close()
	var out []RowError
	for rows.Next() {
		var e RowError
		if err := rows.Scan(&e.Position, &e.Raw, &e.Reason); err != nil {
			return nil, fmt.Errorf("scan import error: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const rawColumns = `source_id, natural_key, payload, content_hash, first_run_id, last_run_id, imported_at, deleted_at`

func scanRaw(row pgx.Row) (RawRecord, error) {
	var r RawRecord
	var payload []byte
	if err := row.Scan(&r.SourceID, &r.Key, &payload, &r.ContentHash, &r.FirstRunID, &r.LastRunID, &r.ImportedAt, &r.DeletedAt); err != nil {
		return RawRecord{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&r.Payload); err != nil {
		return RawRecord{}, fmt.Errorf("decode payload of %q: %w", r.Key, err)
	}
	return r, nil
}

// GetRaw returns one raw record (including tombstoned ones), or ErrNotFound.
func (s *Store) GetRaw(ctx context.Context, sourceID, key string) (RawRecord, error) {
	r, err := scanRaw(s.pool.QueryRow(ctx,
		`SELECT `+rawColumns+` FROM raw_records WHERE source_id = $1 AND natural_key = $2`, sourceID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return RawRecord{}, fmt.Errorf("raw record %q/%q: %w", sourceID, key, ErrNotFound)
	}
	if err != nil {
		return RawRecord{}, fmt.Errorf("get raw record %q/%q: %w", sourceID, key, err)
	}
	return r, nil
}

// ListRaw returns up to limit records of the source with natural_key > afterKey
// ("" = from the start), ordered by natural_key. To page, pass the last key of
// the previous page. Tombstoned rows are included (see DeletedAt).
func (s *Store) ListRaw(ctx context.Context, sourceID, afterKey string, limit int) ([]RawRecord, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list raw: limit must be positive, got %d", limit)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+rawColumns+` FROM raw_records WHERE source_id = $1 AND natural_key > $2 ORDER BY natural_key LIMIT $3`,
		sourceID, afterKey, limit)
	if err != nil {
		return nil, fmt.Errorf("list raw for %q: %w", sourceID, err)
	}
	defer rows.Close()
	var out []RawRecord
	for rows.Next() {
		r, err := scanRaw(rows)
		if err != nil {
			return nil, fmt.Errorf("list raw for %q: %w", sourceID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list raw for %q: %w", sourceID, err)
	}
	return out, nil
}

// TryAdvisoryLock tries to take a session-level Postgres advisory lock for the
// source on a dedicated connection. If ok, the caller must call unlock (safe to
// call more than once) which releases the lock and the connection. The lock is
// also released by Postgres if the connection is lost. ok is false with a nil
// error when another session holds the lock; a non-nil error means the attempt
// itself failed (database down, closed pool, cancelled context) and says nothing
// about who holds the lock. unlock is never nil and is a no-op unless ok.
func (s *Store) TryAdvisoryLock(ctx context.Context, sourceID string) (unlock func(), ok bool, err error) {
	noop := func() {}
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return noop, false, fmt.Errorf("advisory lock for %q: acquire connection: %w", sourceID, err)
	}
	const key = `hashtextextended('data-importer:source:' || $1::text, 0)`
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+key+`)`, sourceID).Scan(&got); err != nil {
		// Unknown connection state: do not return it to the pool.
		_ = conn.Hijack().Close(context.Background())
		return noop, false, fmt.Errorf("advisory lock for %q: %w", sourceID, err)
	}
	if !got {
		conn.Release()
		return noop, false, nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var released bool
			err := conn.QueryRow(bg, `SELECT pg_advisory_unlock(`+key+`)`, sourceID).Scan(&released)
			if err != nil || !released {
				// Lock state unknown or connection broken: closing the session drops the lock.
				_ = conn.Hijack().Close(bg)
				return
			}
			conn.Release()
		})
	}, true, nil
}
