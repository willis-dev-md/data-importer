package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/willis-dev-md/data-importer/internal/schedule"
)

// AbandonedError is the error text of a run closed by SweepAbandonedRuns.
const AbandonedError = "abandoned (process died)"

// Mapping is a stored mapping version. Spec is the mapping document as JSON;
// its "version" key always equals Version.
type Mapping struct {
	SourceID  string
	Version   int
	Spec      json.RawMessage
	CreatedAt time.Time
	CreatedBy string
}

// ApplyItem is one source (and optionally its mapping) for Apply.
type ApplyItem struct {
	Source    Source          // ActiveMappingVersion is ignored: Apply manages it
	Spec      json.RawMessage // mapping document as a JSON object; empty = leave mappings alone
	CreatedBy string

	// Schedule is the validated schedule of the source; nil = manual only. Disabled pauses it (the zero value is
	// enabled). Now is the clock for next_run_at; the zero value means time.Now().
	Schedule *schedule.Spec
	Disabled bool
	Now      time.Time
}

// ApplyResult says what Apply did for one item.
type ApplyResult struct {
	SourceID       string
	SourceCreated  bool
	SourceUpdated  bool // kind or config differed
	MappingVersion int  // active version after Apply; 0 = none
	MappingCreated bool // a new version was stored and activated

	ScheduleChanged bool       // the stored schedule or enabled flag differed
	NextRunAt       *time.Time // next_run_at after Apply; nil = not scheduled (manual only, or disabled)
}

// Apply upserts every item in one transaction (all or nothing). A source row is
// only written when it is new or its kind or config differs. A mapping is stored
// as a new version, and activated, only when its content (ignoring the
// "version" key) differs from the active version; otherwise nothing is written.
// The schedule and enabled flag are stored, and next_run_at is recomputed (from Now) when the schedule changes, the
// source is re-enabled, or next_run_at is unset; an identical schedule keeps next_run_at. A source without a
// schedule, or a disabled one, has next_run_at null.
// Items are processed in order, so give them in a stable order.
func (s *Store) Apply(ctx context.Context, items []ApplyItem) ([]ApplyResult, error) {
	var out []ApplyResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		out = out[:0]
		for _, it := range items {
			r, err := applyOne(ctx, tx, it)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func applyOne(ctx context.Context, tx pgx.Tx, it ApplyItem) (ApplyResult, error) {
	id := it.Source.ID
	res := ApplyResult{SourceID: id}
	cfg := it.Source.Config
	if len(cfg) == 0 {
		cfg = json.RawMessage(`{}`)
	}
	// Writes only when new or different; no row is returned for "unchanged".
	var inserted bool
	err := tx.QueryRow(ctx, `
INSERT INTO sources (id, kind, config) VALUES ($1, $2, $3::jsonb)
ON CONFLICT (id) DO UPDATE SET kind = EXCLUDED.kind, config = EXCLUDED.config
  WHERE sources.kind IS DISTINCT FROM EXCLUDED.kind OR sources.config IS DISTINCT FROM EXCLUDED.config
RETURNING (xmax = 0)`, id, it.Source.Kind, string(cfg)).Scan(&inserted)
	switch {
	case err == nil:
		res.SourceCreated, res.SourceUpdated = inserted, !inserted
	case errors.Is(err, pgx.ErrNoRows):
	default:
		return res, fmt.Errorf("apply source %q: %w", id, err)
	}

	var active *int
	if err := tx.QueryRow(ctx, `SELECT active_mapping_version FROM sources WHERE id = $1 FOR UPDATE`, id).Scan(&active); err != nil {
		return res, fmt.Errorf("apply source %q: %w", id, err)
	}
	if err := applySchedule(ctx, tx, it, &res); err != nil {
		return res, err
	}
	if active != nil {
		res.MappingVersion = *active
	}
	if len(it.Spec) == 0 {
		return res, nil
	}
	newSpec, err := decodeSpec(it.Spec)
	if err != nil {
		return res, fmt.Errorf("apply mapping of %q: %w", id, err)
	}
	if active != nil {
		var cur []byte
		if err := tx.QueryRow(ctx, `SELECT spec FROM mappings WHERE source_id = $1 AND version = $2`, id, *active).Scan(&cur); err != nil {
			return res, fmt.Errorf("apply mapping of %q: read active version %d: %w", id, *active, err)
		}
		curSpec, err := decodeSpec(cur)
		if err != nil {
			return res, fmt.Errorf("apply mapping of %q: stored version %d: %w", id, *active, err)
		}
		if sameSpec(curSpec, newSpec) {
			return res, nil
		}
	}
	var next int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) + 1 FROM mappings WHERE source_id = $1`, id).Scan(&next); err != nil {
		return res, fmt.Errorf("apply mapping of %q: %w", id, err)
	}
	newSpec["version"] = next
	body, err := json.Marshal(newSpec)
	if err != nil {
		return res, fmt.Errorf("apply mapping of %q: %w", id, err)
	}
	var by *string
	if it.CreatedBy != "" {
		by = &it.CreatedBy
	}
	if _, err := tx.Exec(ctx, `INSERT INTO mappings (source_id, version, spec, created_by) VALUES ($1, $2, $3::jsonb, $4)`,
		id, next, string(body), by); err != nil {
		return res, fmt.Errorf("apply mapping of %q: %w", id, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE sources SET active_mapping_version = $2 WHERE id = $1`, id, next); err != nil {
		return res, fmt.Errorf("apply mapping of %q: activate version %d: %w", id, next, err)
	}
	res.MappingVersion, res.MappingCreated = next, true
	return res, nil
}

func decodeSpec(b []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("spec is not a JSON object: %w", err)
	}
	if m == nil {
		return nil, errors.New("spec is not a JSON object")
	}
	return m, nil
}

// sameSpec compares two specs ignoring the "version" key.
func sameSpec(a, b map[string]any) bool {
	strip := func(m map[string]any) map[string]any {
		c := make(map[string]any, len(m))
		for k, v := range m {
			if k != "version" {
				c[k] = v
			}
		}
		return c
	}
	return reflect.DeepEqual(strip(a), strip(b))
}

// GetMapping returns one stored mapping version, or ErrNotFound.
func (s *Store) GetMapping(ctx context.Context, sourceID string, version int) (Mapping, error) {
	return s.scanMapping(ctx, `SELECT source_id, version, spec, created_at, coalesce(created_by,'') FROM mappings WHERE source_id = $1 AND version = $2`,
		sourceID, version)
}

// ActiveMapping returns the source's active mapping version, or ErrNotFound
// (no such source, or no active version).
func (s *Store) ActiveMapping(ctx context.Context, sourceID string) (Mapping, error) {
	return s.scanMapping(ctx, `
SELECT m.source_id, m.version, m.spec, m.created_at, coalesce(m.created_by,'')
FROM sources s JOIN mappings m ON m.source_id = s.id AND m.version = s.active_mapping_version
WHERE s.id = $1`, sourceID)
}

func (s *Store) scanMapping(ctx context.Context, q string, args ...any) (Mapping, error) {
	var m Mapping
	var spec []byte
	err := s.pool.QueryRow(ctx, q, args...).Scan(&m.SourceID, &m.Version, &spec, &m.CreatedAt, &m.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return Mapping{}, fmt.Errorf("mapping of source %v: %w", args[0], ErrNotFound)
	}
	if err != nil {
		return Mapping{}, fmt.Errorf("get mapping of source %v: %w", args[0], err)
	}
	m.Spec = spec
	return m, nil
}

// SweepAbandonedRuns marks runs left in status 'running' as failed (error
// AbandonedError, finished_at = now()) when no live session holds the advisory
// lock of their source: a hard kill or OOM cannot run deferred cleanup, and the
// lock dies with the session. A run whose source lock is held is never touched.
// Only locks of the current database are considered. It returns the ids of the
// runs it closed. Conservative by design: a stale row of a source that is being
// run right now is left until that run ends.
func (s *Store) SweepAbandonedRuns(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
UPDATE import_runs r SET status = 'failed', finished_at = now(), error = $1
WHERE r.status = 'running' AND NOT EXISTS (
  SELECT 1 FROM pg_locks l
  WHERE l.locktype = 'advisory' AND l.granted
    AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND ((l.classid::bigint << 32) | l.objid::bigint) = hashtextextended('data-importer:source:' || r.source_id, 0))
RETURNING r.id`, AbandonedError)
	if err != nil {
		return nil, fmt.Errorf("sweep abandoned runs: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sweep abandoned runs: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sweep abandoned runs: %w", err)
	}
	return ids, nil
}

// applySchedule stores the schedule and enabled flag of one source (its row is already locked) and maintains
// next_run_at. last_scheduled_at is never touched here.
func applySchedule(ctx context.Context, tx pgx.Tx, it ApplyItem, res *ApplyResult) error {
	id := it.Source.ID
	var (
		oldRaw     []byte
		oldEnabled bool
		oldNext    *time.Time
	)
	if err := tx.QueryRow(ctx, `SELECT schedule, enabled, next_run_at FROM sources WHERE id = $1`, id).
		Scan(&oldRaw, &oldEnabled, &oldNext); err != nil {
		return fmt.Errorf("apply schedule of %q: %w", id, err)
	}
	enabled := !it.Disabled
	var newJSON []byte
	if it.Schedule != nil {
		b, err := json.Marshal(it.Schedule)
		if err != nil {
			return fmt.Errorf("apply schedule of %q: %w", id, err)
		}
		newJSON = b
	}
	var old *schedule.Spec
	if oldRaw != nil {
		var o schedule.Spec
		if err := json.Unmarshal(oldRaw, &o); err == nil {
			old = &o
		}
	}
	same := (old == nil) == (it.Schedule == nil) && (old == nil || *old == *it.Schedule)
	res.ScheduleChanged = !same || oldEnabled != enabled

	var next *time.Time
	switch {
	case it.Schedule == nil || !enabled:
		// manual only or paused: nothing is due
	case same && oldEnabled && oldNext != nil:
		next = oldNext
	default:
		now := it.Now
		if now.IsZero() {
			now = time.Now()
		}
		n, err := schedule.Next(*it.Schedule, now)
		if err != nil {
			return fmt.Errorf("apply schedule of %q: %w", id, err)
		}
		next = &n
	}
	res.NextRunAt = next
	if !res.ScheduleChanged && sameTime(oldNext, next) {
		return nil
	}
	var sched any
	if newJSON != nil {
		sched = string(newJSON)
	}
	if _, err := tx.Exec(ctx, `UPDATE sources SET schedule = $2::jsonb, enabled = $3, next_run_at = $4 WHERE id = $1`,
		id, sched, enabled, next); err != nil {
		return fmt.Errorf("apply schedule of %q: %w", id, err)
	}
	return nil
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
