package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/willis-dev-md/data-importer/internal/record"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// ErrLockCapacity means no lock connection became free within the lock wait.
// No run row is created in that case.
var ErrLockCapacity = errors.New("no lock connection available")

// ErrLockLost means the session holding a monitored source lock died or no
// longer holds the lock (for example pg_terminate_backend, a database restart or
// a broken network). It is the text of the error of a run stopped for that reason.
var ErrLockLost = errors.New("lost source lock (session terminated)")

// lockCheckTimeout bounds one monitor check so a hung connection is detected.
const lockCheckTimeout = 10 * time.Second

// Store is the Postgres-backed store for sources, runs, raw records and import errors.
type Store struct {
	pool      *pgxpool.Pool
	lockCfg   *pgx.ConnConfig // connection settings of the dedicated lock connections
	lockSlots chan struct{}   // bounds concurrent lock connections
	lockWait  time.Duration
	lockCheck time.Duration // interval of the lock monitor
}

// New returns a Store using pool with default lock options. The schema must
// already be migrated (see Migrate).
func New(pool *pgxpool.Pool) *Store { return NewWithOptions(pool, Options{}) }

// NewWithOptions is New with explicit lock options (zero values mean defaults).
func NewWithOptions(pool *pgxpool.Pool, opts Options) *Store {
	if opts.LockConns <= 0 {
		opts.LockConns = DefaultLockConns
	}
	if opts.LockWait <= 0 {
		opts.LockWait = DefaultLockWait
	}
	switch {
	case opts.LockCheckInterval <= 0:
		opts.LockCheckInterval = DefaultLockCheckInterval
	case opts.LockCheckInterval < MinLockCheckInterval:
		opts.LockCheckInterval = MinLockCheckInterval
	case opts.LockCheckInterval > MaxLockCheckInterval:
		opts.LockCheckInterval = MaxLockCheckInterval
	}
	return &Store{
		lockCheck: opts.LockCheckInterval,
		pool:      pool,
		lockCfg:   pool.Config().ConnConfig.Copy(),
		lockSlots: make(chan struct{}, opts.LockConns),
		lockWait:  opts.LockWait,
	}
}

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
	Error  *string
	FileID *int64 // source_files.id parsed (or found unchanged) by this run; nil if none
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
       inserted, updated, unchanged, failed, tombstoned, error, file_id
FROM import_runs WHERE id = $1`, runID).Scan(
		&r.ID, &r.SourceID, &r.Status, &r.Trigger, &meta, &shape, &r.StartedAt, &r.FinishedAt,
		&r.Inserted, &r.Updated, &r.Unchanged, &r.Failed, &r.Tombstoned, &r.Error, &r.FileID)
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
//
// UpsertRaw does not know about duplicates across calls: a key already written
// by the same run is overwritten (the importer uses UpsertRawUnique instead).
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

// upsertUniqueSQL is upsertSQL minus the rows already written by this run: "fresh"
// drops every input key whose stored row has last_run_id = $2 (same snapshot as
// "old", i.e. before this statement). Those keys are returned as the 4th column.
const upsertUniqueSQL = `
WITH input AS (
  SELECT * FROM unnest($3::text[], $4::text[], $5::bytea[]) AS t(natural_key, payload, content_hash)
), old AS (
  SELECT natural_key, content_hash, last_run_id FROM raw_records
  WHERE source_id = $1 AND natural_key = ANY($3::text[])
), fresh AS (
  SELECT input.* FROM input
  WHERE NOT EXISTS (SELECT 1 FROM old WHERE old.natural_key = input.natural_key AND old.last_run_id = $2)
), up AS (
  INSERT INTO raw_records (source_id, natural_key, payload, content_hash, first_run_id, last_run_id)
  SELECT $1, natural_key, payload::jsonb, content_hash, $2, $2 FROM fresh
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
  count(*) FILTER (WHERE old.natural_key IS NOT NULL AND old.content_hash = up.content_hash),
  (SELECT coalesce(array_agg(natural_key), '{}'::text[]) FROM old WHERE last_run_id = $2)
FROM up LEFT JOIN old USING (natural_key)`

// Dup is a record UpsertRawUnique did not write because its key was already
// written by the same run. Index is the position in the recs slice.
type Dup struct {
	Index int
	// First is the index in recs of the record that was written for this key in
	// this call, or -1 when the key was written by an EARLIER call of the same
	// run (the store does not keep positions, so that record is not known).
	First int
}

// UpsertRawUnique is UpsertRaw with the importer's duplicate rule: within one run
// the FIRST record for a natural key wins and a later one is never merged into it.
//   - If a key occurs more than once in recs, the first occurrence is written and
//     each later one is returned as Dup{Index, First: index of the first}.
//   - If a stored row for the key has last_run_id = runID (written by an earlier
//     call of the same run, with or without a content change), the stored row is
//     left untouched, is not counted, and every record with that key in recs is
//     returned as Dup{Index, First: -1}.
//
// Rows last seen by a previous run are inserted/updated/unchanged exactly as in
// UpsertRaw (a tombstoned row from an earlier run is revived). Written records
// are counted once; Counts.Inserted+Updated+Unchanged+len(dups) == len(recs).
// Detection uses the database (last_run_id), so memory is O(len(recs)) per call
// and nothing is kept between calls. Calls for the same run must not run
// concurrently (the importer calls it sequentially under the source lock).
// Validation errors (empty key, uncanonicalisable payload) fail the whole call.
func (s *Store) UpsertRawUnique(ctx context.Context, runID int64, sourceID string, recs []record.Record) (Counts, []Dup, error) {
	if len(recs) == 0 {
		return Counts{}, nil, nil
	}
	first := make(map[string]int, len(recs)) // key -> index of first occurrence in recs
	var dups []Dup
	keys := make([]string, 0, len(recs))
	payloads := make([]string, 0, len(recs))
	hashes := make([][]byte, 0, len(recs))
	for i, r := range recs {
		if r.Key == "" {
			return Counts{}, nil, errors.New("upsert raw: record with empty key")
		}
		canon, err := record.Canonical(r.Payload)
		if err != nil {
			return Counts{}, nil, fmt.Errorf("upsert raw: record %q: %w", r.Key, err)
		}
		h, err := record.Sum(r.Payload)
		if err != nil {
			return Counts{}, nil, fmt.Errorf("upsert raw: record %q: %w", r.Key, err)
		}
		if f, dup := first[r.Key]; dup {
			dups = append(dups, Dup{Index: i, First: f})
			continue
		}
		first[r.Key] = i
		keys = append(keys, r.Key)
		payloads = append(payloads, string(canon))
		hashes = append(hashes, h)
	}

	var c Counts
	var touched []string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, upsertUniqueSQL, sourceID, runID, keys, payloads, hashes).
			Scan(&c.Inserted, &c.Updated, &c.Unchanged, &touched)
	})
	if err != nil {
		return Counts{}, nil, fmt.Errorf("upsert raw (source %q, run %d, %d records): %w", sourceID, runID, len(recs), err)
	}
	if len(touched) > 0 {
		earlier := make(map[string]bool, len(touched))
		for _, k := range touched {
			earlier[k] = true
		}
		// Records whose key was written by an earlier call are all duplicates of
		// a record we do not know: rewrite the in-call ones too.
		for i := range dups {
			if earlier[recs[dups[i].Index].Key] {
				dups[i].First = -1
			}
		}
		for k := range earlier {
			if f, ok := first[k]; ok {
				dups = append(dups, Dup{Index: f, First: -1})
			}
		}
	}
	sort.Slice(dups, func(i, j int) bool { return dups[i].Index < dups[j].Index })
	return c, dups, nil
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
// source on a dedicated connection that is NOT part of the work pool (so a run
// holding its lock can never starve its own queries) and is bounded by the lock
// slots of the Store (Options.LockConns). If all slots are taken it waits up to
// Options.LockWait and then fails with ErrLockCapacity (never waits forever).
// If ok, the caller must call unlock (safe to call more than once) which
// releases the lock, closes the connection and frees the slot. The lock is also
// released by Postgres if the connection is lost. ok is false with a nil error
// when another session holds the lock; a non-nil error means the attempt itself
// failed (database down, no lock slot, cancelled context) and says nothing about
// who holds the lock. unlock is never nil and is a no-op unless ok.
func (s *Store) TryAdvisoryLock(ctx context.Context, sourceID string) (unlock func(), ok bool, err error) {
	return s.tryLock(ctx, sourceID, nil)
}

// TryAdvisoryLockMonitored is TryAdvisoryLock plus a monitor: a goroutine checks
// every Options.LockCheckInterval (one cheap pg_locks lookup on the lock
// connection itself) that the session is alive and still holds the lock. When it
// does not, onLost is called once, from the monitor goroutine, with an error
// wrapping ErrLockLost, and the monitor ends. onLost must not block; a run uses
// it to cancel itself. unlock stops the monitor and waits for it (also when
// called while a check is in flight) before releasing the lock, so no goroutine
// outlives unlock and onLost is never called after unlock returned.
func (s *Store) TryAdvisoryLockMonitored(ctx context.Context, sourceID string, onLost func(error)) (unlock func(), ok bool, err error) {
	return s.tryLock(ctx, sourceID, onLost)
}

func (s *Store) tryLock(ctx context.Context, sourceID string, onLost func(error)) (unlock func(), ok bool, err error) {
	noop := func() {}
	if err := s.acquireLockSlot(ctx); err != nil {
		return noop, false, fmt.Errorf("advisory lock for %q: %w", sourceID, err)
	}
	conn, err := pgx.ConnectConfig(ctx, s.lockCfg.Copy())
	if err != nil {
		<-s.lockSlots
		return noop, false, fmt.Errorf("advisory lock for %q: connect: %w", sourceID, err)
	}
	const key = `hashtextextended('data-importer:source:' || $1::text, 0)`
	var got bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(`+key+`)`, sourceID).Scan(&got); err != nil {
		// Unknown connection state: closing the session drops any lock.
		closeConn(conn)
		<-s.lockSlots
		return noop, false, fmt.Errorf("advisory lock for %q: %w", sourceID, err)
	}
	if !got {
		closeConn(conn)
		<-s.lockSlots
		return noop, false, nil
	}
	stopMon := func() {}
	if onLost != nil {
		mctx, mcancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.monitorLock(mctx, conn, sourceID, onLost)
		}()
		stopMon = func() { mcancel(); <-done }
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			defer func() { <-s.lockSlots }()
			stopMon() // the monitor is the only other user of conn: join it first
			bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			// Best effort explicit unlock; closing the session drops the lock anyway.
			var released bool
			_ = conn.QueryRow(bg, `SELECT pg_advisory_unlock(`+key+`)`, sourceID).Scan(&released)
			_ = conn.Close(bg)
		})
	}, true, nil
}

// monitorLock runs until ctx is cancelled (unlock) or the lock is lost. A check
// that fails because ctx was cancelled is not a loss.
func (s *Store) monitorLock(ctx context.Context, conn *pgx.Conn, sourceID string, onLost func(error)) {
	t := time.NewTicker(s.lockCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cctx, cancel := context.WithTimeout(ctx, lockCheckTimeout)
		var held bool
		err := conn.QueryRow(cctx, `
SELECT EXISTS (SELECT 1 FROM pg_locks
  WHERE locktype = 'advisory' AND granted AND pid = pg_backend_pid()
    AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND ((classid::bigint << 32) | objid::bigint) = hashtextextended('data-importer:source:' || $1::text, 0))`,
			sourceID).Scan(&held)
		cancel()
		if ctx.Err() != nil {
			return
		}
		switch {
		case err != nil:
			onLost(fmt.Errorf("%w: %v", ErrLockLost, err))
			return
		case !held:
			onLost(fmt.Errorf("%w: session no longer holds the lock", ErrLockLost))
			return
		}
	}
}

func closeConn(c *pgx.Conn) {
	bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.Close(bg)
}

func (s *Store) acquireLockSlot(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("acquire lock connection: %w", err)
	}
	select {
	case s.lockSlots <- struct{}{}:
		return nil
	default:
	}
	timer := time.NewTimer(s.lockWait)
	defer timer.Stop()
	select {
	case s.lockSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("acquire lock connection: %w", ctx.Err())
	case <-timer.C:
		return fmt.Errorf("%w: all %d are in use after waiting %s", ErrLockCapacity, cap(s.lockSlots), s.lockWait)
	}
}

// ListRawPage is the keyset read used by the DataService: up to limit records of
// the source with natural_key > afterKey ("" = from the start), ordered by
// natural_key. Unless includeDeleted is set, tombstoned rows are not returned
// (so they are not counted against a scan limit either).
func (s *Store) ListRawPage(ctx context.Context, sourceID, afterKey string, limit int, includeDeleted bool) ([]RawRecord, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list raw page: limit must be positive, got %d", limit)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+rawColumns+` FROM raw_records
WHERE source_id = $1 AND natural_key > $2 AND ($4 OR deleted_at IS NULL)
ORDER BY natural_key LIMIT $3`,
		sourceID, afterKey, limit, includeDeleted)
	if err != nil {
		return nil, fmt.Errorf("list raw page for %q: %w", sourceID, err)
	}
	defer rows.Close()
	var out []RawRecord
	for rows.Next() {
		r, err := scanRaw(rows)
		if err != nil {
			return nil, fmt.Errorf("list raw page for %q: %w", sourceID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list raw page for %q: %w", sourceID, err)
	}
	return out, nil
}
