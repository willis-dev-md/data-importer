package importer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/record"
	"github.com/willis-dev-md/data-importer/internal/source"
	"github.com/willis-dev-md/data-importer/internal/store"
)

// MaxDuplicateSamples is how many duplicate-key errors RunResult keeps.
const MaxDuplicateSamples = 10

// MaxDryRunKeys bounds duplicate detection in a dry run, which has no database
// and keeps one 128-bit key hash plus the first position per distinct key: about
// 50 bytes per key, so roughly 0.5 GB at the limit. Beyond it the dry run fails
// with a clear error instead of growing without bound. (A variable so tests can lower it.)
var MaxDryRunKeys = 10_000_000

// DefaultBatchSize is the number of records per UpsertRaw call.
const DefaultBatchSize = 500

// Run statuses written by this package.
const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusAborted   = "aborted"
	// StatusUnchangedFile closes a run whose file is the same as the last successful
	// run's (HTTP 304 or identical SHA-256): nothing was parsed or written.
	StatusUnchangedFile = "unchanged_file"
)

var (
	// ErrNoFileStore means a reparse or file retention was asked for but the
	// source cannot provide it (no file store, dry run, or not a file source).
	ErrNoFileStore = errors.New("reparse needs a file store, a real run and a csv or xlsx source")
	// ErrAlreadyRunning means another run of the source holds the advisory lock.
	ErrAlreadyRunning = errors.New("import already running for this source")
	// ErrAborted means the run ended with status aborted (max_error_ratio exceeded).
	ErrAborted = errors.New("import aborted")
)

// Store is the part of *store.Store that Run needs.
type Store interface {
	GetSource(ctx context.Context, id string) (store.Source, error)
	CreateRunFor(ctx context.Context, sourceID, trigger string, scheduledFor time.Time) (int64, error)
	FinishRun(ctx context.Context, runID int64, status string, counts store.RunCounts, shape, sourceMeta json.RawMessage, errText string) error
	UpsertRawUnique(ctx context.Context, runID int64, sourceID string, recs []record.Record) (store.Counts, []store.Dup, error)
	RecordErrors(ctx context.Context, runID int64, errs []store.RowError) error
	// TryAdvisoryLockMonitored takes the source lock and calls onLost (once, from
	// another goroutine) if the lock session dies; unlock joins that goroutine.
	TryAdvisoryLockMonitored(ctx context.Context, sourceID string, onLost func(error)) (unlock func(), ok bool, err error)
	// File retention (used only when Deps.Files is set).
	RecordFile(ctx context.Context, f store.File) (id int64, created bool, err error)
	SetRunFile(ctx context.Context, runID, fileID int64) error
	LastFile(ctx context.Context, sourceID string) (store.File, error)
	FileForReparse(ctx context.Context, sourceID string, runID int64) (store.File, error)
}

// Deps are the collaborators of Run. Adapters is keyed by source kind (csv, ...).
//
// Files, when set, makes every real run of a source whose adapter is a
// source.FileAdapter (csv, xlsx) keep the fetched file by SHA-256, detect an
// unchanged file and allow Options.Reparse. Without it adapters fetch for
// themselves and nothing is retained.
type Deps struct {
	Store    Store
	Adapters map[string]source.Adapter
	Files    store.FileStore
}

// Options tune one run.
type Options struct {
	Trigger string // manual (default) or schedule
	// ScheduledFor is the schedule slot of a scheduled run (import_runs.scheduled_for); zero for a manual run.
	ScheduledFor time.Time
	DryRun       bool // parse and count only; nothing is written and no lock is taken
	BatchSize    int  // records per upsert; default DefaultBatchSize

	// Reparse runs the adapter again on a file already in the file store, with no
	// network access and no change detection: a normal run (same lock, duplicate
	// rules, counts) whose input is the stored file. Needs Deps.Files, a real run
	// and a file adapter.
	Reparse *ReparseOptions
}

// ReparseOptions selects the stored file of a reparse.
type ReparseOptions struct {
	RunID int64 // the run whose file to parse again; 0 = the source's latest run that has a file
}

// RunResult describes a finished run. RunID is 0 for a dry run. In a dry run
// only Total, Failed, Shape and SourceMeta are filled (nothing is upserted).
type RunResult struct {
	RunID  int64
	Status string
	store.RunCounts
	Total int // rows seen: records plus failed rows
	// Duplicates is how many records were rejected as duplicate natural keys (also
	// counted in Failed); DuplicateSamples holds the first MaxDuplicateSamples of
	// them (position, key, reason), in a real run and in a dry run.
	Duplicates       int
	DuplicateSamples []store.RowError
	Shape            json.RawMessage
	SourceMeta       json.RawMessage
	Error            string
	DryRun           bool
}

// ConfigJSON converts a source config into the JSON stored in sources.config.
func ConfigJSON(cfg *config.Source) (json.RawMessage, error) {
	y, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode source config: %w", err)
	}
	var generic any
	if err := yaml.Unmarshal(y, &generic); err != nil {
		return nil, fmt.Errorf("encode source config: %w", err)
	}
	b, err := json.Marshal(generic)
	if err != nil {
		return nil, fmt.Errorf("encode source config: %w", err)
	}
	return b, nil
}

// ParseConfigJSON is the inverse of ConfigJSON (JSON is valid YAML).
func ParseConfigJSON(b []byte) (*config.Source, error) { return config.ParseSource(b) }

// Run imports one source. It returns ErrAlreadyRunning (and creates no run row)
// when another run holds the source lock. For any run that ends failed or
// aborted the RunResult is filled and a non-nil error is returned (aborted
// wraps ErrAborted); check RunResult.Status.
//
// Duplicate natural keys: within one run the first record for a key wins; a later
// record with the same key (same batch or a later batch) is not merged but
// recorded as a row error "duplicate natural key (first seen at <position>)"
// (position "unknown" when the first record was in an earlier batch), counted in
// Failed and in max_error_ratio. Keys are compared byte for byte (case,
// whitespace and unicode forms are NOT normalised). Detection is done by the
// store from raw_records.last_run_id, so the importer keeps only one batch in
// memory. A dry run has no database and detects duplicates with a set of 128-bit
// key hashes (at most MaxDryRunKeys distinct keys, then the run fails).
//
// Chunks committed before a failure or an abort stay in raw_records tagged with
// the run id (they are not rolled back).
func Run(ctx context.Context, deps Deps, sourceID string, opts Options) (RunResult, error) {
	if deps.Store == nil {
		return RunResult{}, errors.New("importer: no store")
	}
	if !opts.DryRun {
		// If the lock session dies the run is cancelled with cause store.ErrLockLost
		// and finishes failed. unlock (deferred, so it also runs on panic) joins the
		// monitor goroutine; cancel is deferred after it and so runs first.
		lctx, cancel := context.WithCancelCause(ctx)
		unlock, ok, err := deps.Store.TryAdvisoryLockMonitored(lctx, sourceID, func(e error) { cancel(e) })
		if err != nil {
			cancel(nil)
			return RunResult{}, fmt.Errorf("import %q: %w", sourceID, err)
		}
		if !ok {
			cancel(nil)
			return RunResult{}, fmt.Errorf("import %q: %w", sourceID, ErrAlreadyRunning)
		}
		defer unlock()
		defer cancel(nil)
		ctx = lctx
	}

	src, err := deps.Store.GetSource(ctx, sourceID)
	if err != nil {
		return RunResult{}, fmt.Errorf("import %q: %w", sourceID, err)
	}
	cfg, err := ParseConfigJSON(src.Config)
	if err != nil {
		return RunResult{}, fmt.Errorf("import %q: stored config: %w", sourceID, err)
	}
	adapter, ok := deps.Adapters[src.Kind]
	if !ok || adapter == nil {
		return RunResult{}, fmt.Errorf("import %q: no adapter for kind %q", sourceID, src.Kind)
	}

	fa, isFile := adapter.(source.FileAdapter)
	useFiles := !opts.DryRun && isFile && deps.Files != nil
	var reparseFile *store.File
	if opts.Reparse != nil {
		if !useFiles {
			return RunResult{}, fmt.Errorf("import %q: %w", sourceID, ErrNoFileStore)
		}
		// Resolved under the lock, before a run row exists: a missing file creates no run.
		f, err := deps.Store.FileForReparse(ctx, sourceID, opts.Reparse.RunID)
		if err != nil {
			return RunResult{}, fmt.Errorf("reparse %q: %w", sourceID, err)
		}
		reparseFile = &f
	}

	res := RunResult{DryRun: opts.DryRun}
	if !opts.DryRun {
		id, err := deps.Store.CreateRunFor(ctx, sourceID, opts.Trigger, opts.ScheduledFor)
		if err != nil {
			return RunResult{}, fmt.Errorf("import %q: %w", sourceID, err)
		}
		res.RunID = id
		// A panic must not leave the run row "running": close it as failed,
		// then re-panic so the panic is not swallowed (the lock is released by
		// its own defer).
		defer func() {
			if p := recover(); p != nil {
				fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				defer cancel()
				_ = deps.Store.FinishRun(fctx, id, StatusFailed, res.RunCounts, res.Shape, res.SourceMeta, fmt.Sprintf("panic: %v", p))
				panic(p)
			}
		}()
	}

	var (
		open      openFunc
		unchanged bool
		runErr    error
	)
	if useFiles {
		var in fileInput
		if in, runErr = prepareFile(ctx, deps, fa, cfg, sourceID, res.RunID, reparseFile); runErr == nil {
			open, unchanged = in.open, in.unchanged
			if unchanged && in.location != "" { // nothing is parsed, but the run still records where it looked
				res.SourceMeta, _ = json.Marshal(map[string]string{source.ResolveKey: in.location})
			}
		}
	} else {
		open = func() (source.Stream, func() error, error) {
			s, err := adapter.Open(ctx, cfg)
			return s, nil, err
		}
	}
	if runErr == nil && !unchanged {
		runErr = execute(ctx, deps.Store, open, cfg, sourceID, opts, &res)
	}
	// A lost source lock outranks every other outcome: another run of the source
	// may already be writing, so this run cannot be called succeeded or aborted.
	if cause := context.Cause(ctx); errors.Is(cause, store.ErrLockLost) {
		if runErr == nil {
			runErr = cause
		} else if !errors.Is(runErr, store.ErrLockLost) {
			runErr = fmt.Errorf("%w (%v)", cause, runErr)
		}
	}
	switch {
	case runErr == nil && unchanged:
		res.Status = StatusUnchangedFile
	case runErr == nil:
		res.Status = StatusSucceeded
	case errors.Is(runErr, ErrAborted):
		res.Status = StatusAborted
	default:
		res.Status = StatusFailed
		if ctx.Err() != nil && !errors.Is(runErr, store.ErrLockLost) && !strings.HasPrefix(runErr.Error(), "cancelled") {
			runErr = fmt.Errorf("cancelled: %w", runErr)
		}
	}
	if runErr != nil {
		res.Error = runErr.Error()
	}
	if !opts.DryRun {
		// The caller's context may be cancelled; the run row must still be closed.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if ferr := deps.Store.FinishRun(fctx, res.RunID, res.Status, res.RunCounts, res.Shape, res.SourceMeta, res.Error); ferr != nil {
			runErr = errors.Join(runErr, ferr)
		}
	}
	if runErr != nil {
		return res, fmt.Errorf("import %q run %d: %w", sourceID, res.RunID, runErr)
	}
	return res, nil
}

func execute(ctx context.Context, st Store, open openFunc, cfg *config.Source, sourceID string, opts Options, res *RunResult) error {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	stream, after, err := open()
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { _ = stream.Close() }()

	shape := newShape()
	var meta json.RawMessage
	if m := stream.Meta(); m != nil {
		if meta, err = json.Marshal(m); err != nil {
			return fmt.Errorf("encode source metadata: %w", err)
		}
	}
	res.SourceMeta = meta

	var (
		batch []record.Record
		pos   []string // pos[i] is the source position of batch[i]
		bad   []store.RowError
	)
	flush := func() error {
		if opts.DryRun {
			batch, pos, bad = batch[:0], pos[:0], bad[:0]
			return nil
		}
		if len(bad) > 0 {
			if err := st.RecordErrors(ctx, res.RunID, bad); err != nil {
				return err
			}
			bad = bad[:0]
		}
		if len(batch) > 0 {
			c, dups, err := st.UpsertRawUnique(ctx, res.RunID, sourceID, batch)
			if err != nil {
				return err
			}
			res.Counts = res.Counts.Add(c)
			// The first record of a key wins; a later one in this run is an error,
			// never a merge. The batch is the only state kept (bounded by batchSize).
			derrs := make([]store.RowError, 0, len(dups))
			for _, d := range dups {
				first := "unknown" // written by an earlier batch: positions are not kept
				if d.First >= 0 {
					first = pos[d.First]
				}
				derrs = append(derrs, store.RowError{
					Position: pos[d.Index], Raw: batch[d.Index].Key,
					Reason: fmt.Sprintf("duplicate natural key (first seen at %s)", first),
				})
			}
			res.Failed += len(derrs)
			res.addDuplicates(derrs)
			batch, pos = batch[:0], pos[:0]
			if err := st.RecordErrors(ctx, res.RunID, derrs); err != nil {
				return err
			}
		}
		return nil
	}
	finish := func() { res.Shape = shape.JSON() }
	var seen map[[16]byte]int64 // dry run only: key hash -> position of the first record
	if opts.DryRun {
		seen = make(map[[16]byte]int64)
	}

	for {
		if err := ctx.Err(); err != nil {
			finish()
			return fmt.Errorf("cancelled: %w", err)
		}
		it, ok := stream.Next()
		if !ok {
			break
		}
		res.Total++
		switch {
		case it.Error != nil:
			res.Failed++
			bad = append(bad, store.RowError{Position: fmt.Sprint(it.Error.Position), Raw: it.Error.Raw, Reason: it.Error.Reason})
		case it.Record != nil:
			if reason := checkRecord(it.Record); reason != "" {
				res.Failed++
				bad = append(bad, store.RowError{Position: fmt.Sprint(it.Record.Position), Raw: it.Record.NaturalKey, Reason: reason})
				break
			}
			shape.Add(it.Record.Fields)
			if opts.DryRun {
				h := sha256.Sum256([]byte(it.Record.NaturalKey))
				k := [16]byte(h[:16])
				if first, dup := seen[k]; dup {
					res.Failed++
					res.addDuplicates([]store.RowError{{
						Position: fmt.Sprint(it.Record.Position), Raw: it.Record.NaturalKey,
						Reason: fmt.Sprintf("duplicate natural key (first seen at %d)", first),
					}})
					break
				}
				if len(seen) >= MaxDryRunKeys {
					finish()
					return fmt.Errorf("dry run: more than %d distinct natural keys; duplicate detection is bounded, use a real run (it detects duplicates in the database)", MaxDryRunKeys)
				}
				seen[k] = it.Record.Position
				break
			}
			batch = append(batch, record.Record{Key: it.Record.NaturalKey, Payload: it.Record.Fields})
			pos = append(pos, fmt.Sprint(it.Record.Position))
		default:
			res.Failed++
			bad = append(bad, store.RowError{Reason: "adapter returned an empty item"})
		}
		if len(batch)+len(bad) >= batchSize {
			if err := flush(); err != nil {
				finish()
				return fmt.Errorf("write batch: %w", err)
			}
		}
	}
	finish()
	if err := stream.Err(); err != nil {
		_ = flush() // keep what was parsed so far; the run is failed anyway
		return fmt.Errorf("read source: %w", err)
	}
	if after != nil {
		if err := after(); err != nil {
			_ = flush()
			return fmt.Errorf("read source: %w", err)
		}
	}
	if err := flush(); err != nil {
		return fmt.Errorf("write batch: %w", err)
	}
	if exceeds(res.Failed, res.Total, cfg.MaxErrorRatio) {
		return fmt.Errorf("%w: %d of %d rows failed, above max_error_ratio %v",
			ErrAborted, res.Failed, res.Total, cfg.MaxErrorRatio)
	}
	return nil
}

func (r *RunResult) addDuplicates(errs []store.RowError) {
	r.Duplicates += len(errs)
	for _, e := range errs {
		if len(r.DuplicateSamples) >= MaxDuplicateSamples {
			return
		}
		r.DuplicateSamples = append(r.DuplicateSamples, e)
	}
}

// exceeds reports failed/total > ratio. Equal is fine; ratio 0 means any
// failure exceeds it. The division is correctly rounded, so 1/100 equals 0.01.
func exceeds(failed, total int, ratio float64) bool {
	if failed == 0 || total == 0 {
		return false
	}
	return float64(failed)/float64(total) > ratio
}

// checkRecord returns a reason if the record cannot be stored. Strings holding
// a NUL byte are rejected by jsonb and would fail a whole batch, so such a
// record is recorded as a row error instead (it is not silently altered).
func checkRecord(r *source.Record) string {
	if r.NaturalKey == "" {
		return "empty natural key"
	}
	if !utf8.ValidString(r.NaturalKey) || strings.ContainsRune(r.NaturalKey, 0) {
		return "natural key contains a NUL byte or invalid UTF-8; not storable"
	}
	if path := findNUL("", r.Fields); path != "" {
		return fmt.Sprintf("string contains a NUL byte (\\u0000) at %s; not storable as jsonb", path)
	}
	if _, err := record.Sum(r.Fields); err != nil {
		return fmt.Sprintf("payload cannot be encoded: %v", err)
	}
	return ""
}

// findNUL returns the path of the first string (value or object key) containing
// a NUL byte, or "".
func findNUL(path string, v any) string {
	switch x := v.(type) {
	case string:
		if strings.ContainsRune(x, 0) {
			return pathOrRoot(path)
		}
	case map[string]any:
		for _, k := range sortedKeys(x) {
			p := path + "." + strings.ReplaceAll(k, "\x00", "")
			if strings.ContainsRune(k, 0) {
				return p + " (key)"
			}
			if r := findNUL(p, x[k]); r != "" {
				return r
			}
		}
	case map[string]string:
		for k, s := range x {
			if strings.ContainsRune(k, 0) || strings.ContainsRune(s, 0) {
				return pathOrRoot(path + "." + strings.ReplaceAll(k, "\x00", ""))
			}
		}
	case []any:
		for i, e := range x {
			if r := findNUL(fmt.Sprintf("%s[%d]", path, i), e); r != "" {
				return r
			}
		}
	}
	return ""
}

func pathOrRoot(p string) string {
	if p == "" {
		return "$"
	}
	return "$" + p
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
