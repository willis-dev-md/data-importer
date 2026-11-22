package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/source"
	"github.com/willis-dev-md/data-importer/internal/store"
)

// openFunc opens the input of a run. after (may be nil) is called once the stream
// has ended cleanly and must fail if the input turned out to be bad (a stored
// file that no longer matches its SHA-256).
type openFunc func() (stream source.Stream, after func() error, err error)

// fileInput is what prepareFile decided: parse the stored file (open), or close
// the run as unchanged_file.
type fileInput struct {
	open      openFunc
	unchanged bool
	location  string // the resolved location, only when the source has `resolve`
}

// change_detection entries (docs/mapping-spec.md).
const (
	cdETag         = "etag"
	cdLastModified = "last_modified"
	cdContentHash  = "content_hash"
)

// prepareFile implements the file part of the import flow (docs/design.md, step 2a):
//
//   - reparse (reparse != nil): no fetch, no change detection; the stored file is verified and parsed.
//   - otherwise: fetch (a conditional request when change_detection lists etag /
//     last_modified and the last successful run stored them), stream the body into
//     the file store, register it in source_files and set import_runs.file_id.
//     A 304 ends the run as unchanged_file without storing or parsing anything;
//     a file whose SHA-256 equals the last successful run's does the same when
//     change_detection lists content_hash. Otherwise the stored copy is verified
//     and parsed.
func prepareFile(ctx context.Context, deps Deps, fa source.FileAdapter, cfg *config.Source, sourceID string, runID int64, reparse *store.File) (fileInput, error) {
	if reparse != nil {
		if err := verifyStored(ctx, deps.Files, reparse.SHA256); err != nil {
			return fileInput{}, err
		}
		if err := deps.Store.SetRunFile(ctx, runID, reparse.ID); err != nil {
			return fileInput{}, err
		}
		return fileInput{open: storedOpener(ctx, deps.Files, fa, cfg, reparse.SHA256, "")}, nil
	}

	cd := func(name string) bool { return slices.Contains(cfg.ChangeDetection, name) }
	fo := fa.FetchOptions(cfg)
	var prev *store.File
	if cd(cdETag) || cd(cdLastModified) || cd(cdContentHash) {
		p, err := deps.Store.LastFile(ctx, sourceID)
		switch {
		case err == nil:
			prev = &p
		case errors.Is(err, store.ErrNoFile):
		default:
			return fileInput{}, err
		}
	}
	if prev != nil {
		if cd(cdETag) {
			fo.IfNoneMatch = prev.ETag
		}
		if cd(cdLastModified) {
			fo.IfModifiedSince = prev.LastModifiedHeader()
		}
	}

	// A resolved location (cfg.Resolve) is chosen first; its validators are the baseline of the
	// conditional request: those of the file last fetched from the SAME url. When the chosen url
	// differs from the one recorded for that file, the old validators belong to another
	// resource and are not sent (content_hash still detects an unchanged file).
	loc, err := source.ResolveLocation(ctx, cfg, fo)
	if err != nil {
		return fileInput{}, fmt.Errorf("resolve location: %w", err)
	}
	resolved := ""
	if len(cfg.Resolve) > 0 {
		resolved = loc
	}
	if len(cfg.Resolve) > 0 && prev != nil && (fo.IfNoneMatch != "" || fo.IfModifiedSince != "") {
		if last := lastLocation(ctx, deps, prev); last != loc {
			fo.IfNoneMatch, fo.IfModifiedSince = "", ""
		}
	}

	body, err := source.Fetch(ctx, loc, fo)
	if errors.Is(err, source.ErrNotModified) && prev != nil {
		if err := deps.Store.SetRunFile(ctx, runID, prev.ID); err != nil {
			return fileInput{}, err
		}
		return fileInput{unchanged: true, location: resolved}, nil
	}
	if err != nil {
		return fileInput{}, fmt.Errorf("fetch source: %w", err)
	}
	defer func() { _ = body.Close() }()
	info, err := deps.Files.Put(ctx, body, fo.MaxBytes)
	if err != nil {
		return fileInput{}, fmt.Errorf("store fetched file: %w", err)
	}
	f := store.File{
		SourceID: sourceID, RunID: runID, SHA256: info.SHA256, Size: info.Size,
		ETag: body.ETag, StorageRef: info.Ref,
	}
	if t, err := http.ParseTime(body.LastModified); err == nil {
		t = t.UTC().Truncate(time.Second)
		f.LastModified = &t
	}
	id, _, err := deps.Store.RecordFile(ctx, f)
	if err != nil {
		return fileInput{}, err
	}
	if err := deps.Store.SetRunFile(ctx, runID, id); err != nil {
		return fileInput{}, err
	}
	if prev != nil && cd(cdContentHash) && prev.SHA256 == info.SHA256 {
		return fileInput{unchanged: true, location: resolved}, nil
	}
	if err := verifyStored(ctx, deps.Files, info.SHA256); err != nil {
		return fileInput{}, err
	}
	return fileInput{open: storedOpener(ctx, deps.Files, fa, cfg, info.SHA256, loc)}, nil
}

// lastLocation returns the location recorded (source_meta._location) on the run that stored f, or "".
func lastLocation(ctx context.Context, deps Deps, f *store.File) string {
	g, ok := deps.Store.(interface {
		GetRun(ctx context.Context, runID int64) (store.Run, error)
	})
	if !ok {
		return ""
	}
	run, err := g.GetRun(ctx, f.RunID)
	if err != nil || len(run.SourceMeta) == 0 {
		return ""
	}
	var m map[string]string
	if json.Unmarshal(run.SourceMeta, &m) != nil {
		return ""
	}
	return m[source.ResolveKey]
}

// verifyStored reads the stored file once, streaming, and fails with
// store.ErrFileCorrupt (or ErrFileMissing) before anything is parsed or written.
func verifyStored(ctx context.Context, fs store.FileStore, sha string) error {
	rc, err := fs.Open(ctx, sha)
	if err != nil {
		return fmt.Errorf("stored file %s: %w", sha, err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("stored file %s: %w", sha, err)
	}
	return nil
}

func storedOpener(ctx context.Context, fs store.FileStore, fa source.FileAdapter, cfg *config.Source, sha, loc string) openFunc {
	return func() (source.Stream, func() error, error) {
		rc, err := fs.Open(ctx, sha)
		if err != nil {
			return nil, nil, fmt.Errorf("stored file %s: %w", sha, err)
		}
		s, err := fa.OpenReader(ctx, cfg, rc)
		if err != nil {
			_ = rc.Close()
			return nil, nil, err
		}
		// Parsers may stop before the end of the file: read the rest so the
		// SHA-256 is verified over the whole file.
		after := func() error {
			if _, err := io.Copy(io.Discard, rc); err != nil {
				return fmt.Errorf("stored file %s: %w", sha, err)
			}
			return nil
		}
		if loc != "" {
			s = source.WithLocation(s, cfg, loc)
		}
		return fileStream{Stream: s, rc: rc}, after, nil
	}
}

// fileStream closes the stored file together with the stream.
type fileStream struct {
	source.Stream
	rc io.ReadCloser
}

func (f fileStream) Close() error { return errors.Join(f.Stream.Close(), f.rc.Close()) }
