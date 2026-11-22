package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
)

// Errors of the file store.
var (
	// ErrFileTooLarge means a file exceeded the size limit given to Put.
	ErrFileTooLarge = errors.New("file exceeds maximum size")
	// ErrFileCorrupt means a stored file no longer matches its SHA-256 or size.
	ErrFileCorrupt = errors.New("stored file is corrupt (SHA-256 mismatch)")
	// ErrFileMissing means the bytes of a stored file are gone.
	ErrFileMissing = errors.New("stored file is missing")
)

// FileInfo describes bytes held by a FileStore.
type FileInfo struct {
	SHA256 string // lower-case hex
	Size   int64
	Ref    string // informational reference (not a path to trust), stored in source_files.storage_ref
}

// FileStore keeps fetched files by content hash. Files are addressed ONLY by the
// SHA-256 of their bytes (64 lower-case hex digits): no caller-supplied name or
// path ever reaches the backing store, so there is nothing to traverse.
type FileStore interface {
	// Put streams r into the store (never holding the whole file in memory) and
	// returns its SHA-256 and size. At most maxBytes (> 0) are accepted: more fails
	// with ErrFileTooLarge and stores nothing. If the bytes are already stored they
	// are not stored twice. Safe for concurrent use.
	Put(ctx context.Context, r io.Reader, maxBytes int64) (FileInfo, error)
	// Open returns the stored bytes. The reader re-hashes while it is read and
	// fails with ErrFileCorrupt at the end of the file if the SHA-256 or the size
	// differs from what was stored; a missing file is ErrFileMissing.
	Open(ctx context.Context, sha256hex string) (io.ReadCloser, error)
}

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// DirFileStore is a FileStore on a local directory: <dir>/<first two hex
// digits>/<sha256>. Content addressing means the same bytes fetched by two
// sources are held once on disk (source_files still has one row per source).
type DirFileStore struct {
	dir string
}

// NewDirFileStore returns a store rooted at dir, creating it (mode 0700) if needed.
// dir must be a directory that can be written.
func NewDirFileStore(dir string) (*DirFileStore, error) {
	if dir == "" {
		return nil, errors.New("file store directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("file store directory %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("file store directory %q: %w", dir, err)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("file store directory %q: %w", dir, err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("file store directory %q: not a directory", dir)
	}
	// Prove it is writable now rather than at the first import.
	tmp, err := os.CreateTemp(abs, ".probe-*")
	if err != nil {
		return nil, fmt.Errorf("file store directory %q is not writable: %w", dir, err)
	}
	_ = tmp.Close()
	_ = os.Remove(tmp.Name())
	return &DirFileStore{dir: abs}, nil
}

// Dir returns the absolute root directory.
func (d *DirFileStore) Dir() string { return d.dir }

func (d *DirFileStore) path(sha string) string {
	return filepath.Join(d.dir, sha[:2], sha)
}

// Put implements FileStore. The bytes go to a temporary file in the store, are
// synced, and are renamed to their final name (atomic, so concurrent Puts of the
// same bytes and readers never see a partial file).
func (d *DirFileStore) Put(ctx context.Context, r io.Reader, maxBytes int64) (FileInfo, error) {
	if maxBytes <= 0 {
		return FileInfo{}, errors.New("file store put: maxBytes must be positive")
	}
	tmp, err := os.CreateTemp(d.dir, ".incoming-*")
	if err != nil {
		return FileInfo{}, fmt.Errorf("file store put: %w", err)
	}
	defer func() { // no-op after a successful rename
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	h := sha256.New()
	buf := make([]byte, 64<<10)
	var n int64
	for {
		if err := ctx.Err(); err != nil {
			return FileInfo{}, fmt.Errorf("file store put: %w", err)
		}
		m, rerr := r.Read(buf)
		if m > 0 {
			n += int64(m)
			if n > maxBytes {
				return FileInfo{}, fmt.Errorf("%w (limit %d bytes)", ErrFileTooLarge, maxBytes)
			}
			h.Write(buf[:m])
			if _, err := tmp.Write(buf[:m]); err != nil {
				return FileInfo{}, fmt.Errorf("file store put: %w", err)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return FileInfo{}, fmt.Errorf("file store put: read: %w", rerr)
		}
	}
	if err := tmp.Sync(); err != nil {
		return FileInfo{}, fmt.Errorf("file store put: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return FileInfo{}, fmt.Errorf("file store put: %w", err)
	}
	sha := hex.EncodeToString(h.Sum(nil))
	final := d.path(sha)
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return FileInfo{}, fmt.Errorf("file store put: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return FileInfo{}, fmt.Errorf("file store put: %w", err)
	}
	// Always (re)write the final name: the bytes just hashed are known good, so a
	// corrupted earlier copy is healed instead of kept.
	if err := os.Rename(tmp.Name(), final); err != nil {
		return FileInfo{}, fmt.Errorf("file store put: %w", err)
	}
	return FileInfo{SHA256: sha, Size: n, Ref: "dir:" + sha[:2] + "/" + sha}, nil
}

// Open implements FileStore.
func (d *DirFileStore) Open(ctx context.Context, sha string) (io.ReadCloser, error) {
	if !sha256Hex.MatchString(sha) {
		return nil, fmt.Errorf("file store open: %q is not a lower-case hex SHA-256", sha)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("file store open: %w", err)
	}
	f, err := os.Open(d.path(sha))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrFileMissing, sha)
	}
	if err != nil {
		return nil, fmt.Errorf("file store open %s: %w", sha, err)
	}
	return &verifyReader{f: f, h: sha256.New(), want: sha}, nil
}

// verifyReader hashes what is read and checks the digest at EOF. A reader that is
// closed before EOF cannot vouch for the file; callers that must know drain it.
type verifyReader struct {
	f    *os.File
	h    hash.Hash
	want string
	done bool
}

func (v *verifyReader) Read(p []byte) (int, error) {
	n, err := v.f.Read(p)
	v.h.Write(p[:n])
	if err == io.EOF && !v.done {
		v.done = true
		if got := hex.EncodeToString(v.h.Sum(nil)); got != v.want {
			return n, fmt.Errorf("%w: expected %s, found %s", ErrFileCorrupt, v.want, got)
		}
	}
	return n, err
}

func (v *verifyReader) Close() error { return v.f.Close() }

// File is a row of source_files.
type File struct {
	ID           int64
	SourceID     string
	RunID        int64
	SHA256       string // hex
	Size         int64
	ETag         string     // "" = none; exactly as the server sent it
	LastModified *time.Time // nil = none
	StorageRef   string
	FetchedAt    time.Time
}

// LastModifiedHeader renders LastModified as an HTTP date (IMF-fixdate, the only
// format a server may send), or "" if there is none.
func (f File) LastModifiedHeader() string {
	if f.LastModified == nil {
		return ""
	}
	return f.LastModified.UTC().Format(http.TimeFormat)
}

// ErrNoFile means there is no stored file for the request (see LastFile, FileForReparse).
var ErrNoFile = errors.New("no stored file")

const fileColumns = `f.id, f.source_id, f.run_id, encode(f.sha256,'hex'), f.size_bytes, f.etag, f.last_modified, f.storage_ref, f.fetched_at`

func scanFile(row pgx.Row) (File, error) {
	var f File
	var etag *string
	if err := row.Scan(&f.ID, &f.SourceID, &f.RunID, &f.SHA256, &f.Size, &etag, &f.LastModified, &f.StorageRef, &f.FetchedAt); err != nil {
		return File{}, err
	}
	if etag != nil {
		f.ETag = *etag
	}
	return f, nil
}

// RecordFile registers the file fetched by run f.RunID for f.SourceID and returns
// its id. There is one row per (source, sha256): if the bytes were seen before
// the existing row is kept (its run_id stays the run that first stored it) and
// only etag, last_modified and fetched_at are refreshed from this response, so
// the next conditional request uses the latest validators. created tells whether
// the row is new.
func (s *Store) RecordFile(ctx context.Context, f File) (id int64, created bool, err error) {
	if !sha256Hex.MatchString(f.SHA256) {
		return 0, false, fmt.Errorf("record file: %q is not a lower-case hex SHA-256", f.SHA256)
	}
	var etag *string
	if f.ETag != "" {
		etag = &f.ETag
	}
	err = s.pool.QueryRow(ctx, `
INSERT INTO source_files (source_id, run_id, sha256, size_bytes, etag, last_modified, storage_ref)
VALUES ($1, $2, decode($3,'hex'), $4, $5, $6, $7)
ON CONFLICT (source_id, sha256) DO UPDATE SET
  etag = EXCLUDED.etag, last_modified = EXCLUDED.last_modified, fetched_at = now()
RETURNING id, (xmax = 0)`,
		f.SourceID, f.RunID, f.SHA256, f.Size, etag, f.LastModified, f.StorageRef).Scan(&id, &created)
	if err != nil {
		return 0, false, fmt.Errorf("record file for source %q run %d: %w", f.SourceID, f.RunID, err)
	}
	return id, created, nil
}

// SetRunFile sets import_runs.file_id: the file this run parsed (or found unchanged).
func (s *Store) SetRunFile(ctx context.Context, runID, fileID int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE import_runs SET file_id = $2 WHERE id = $1`, runID, fileID)
	if err != nil {
		return fmt.Errorf("set file of run %d: %w", runID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("run %d: %w", runID, ErrNotFound)
	}
	return nil
}

// LastFile returns the file of the source's latest successful run (status
// succeeded or unchanged_file, with a file), or ErrNoFile. Its etag and
// last_modified drive the conditional request of the next run.
func (s *Store) LastFile(ctx context.Context, sourceID string) (File, error) {
	f, err := scanFile(s.pool.QueryRow(ctx, `
SELECT `+fileColumns+` FROM source_files f
JOIN import_runs r ON r.file_id = f.id
WHERE r.source_id = $1 AND f.source_id = $1 AND r.status IN ('succeeded','unchanged_file')
ORDER BY r.id DESC LIMIT 1`, sourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return File{}, fmt.Errorf("source %q: no successful run with a stored file: %w", sourceID, ErrNoFile)
	}
	if err != nil {
		return File{}, fmt.Errorf("last file of source %q: %w", sourceID, err)
	}
	return f, nil
}

// FileForReparse returns the stored file of run runID of the source, or, when
// runID is 0, of the source's latest run that has one (any status). A run of
// another source, an unknown run, or a run without a file is ErrNoFile.
func (s *Store) FileForReparse(ctx context.Context, sourceID string, runID int64) (File, error) {
	var row pgx.Row
	if runID > 0 {
		row = s.pool.QueryRow(ctx, `
SELECT `+fileColumns+` FROM source_files f JOIN import_runs r ON r.file_id = f.id
WHERE r.id = $2 AND r.source_id = $1 AND f.source_id = $1`, sourceID, runID)
	} else {
		row = s.pool.QueryRow(ctx, `
SELECT `+fileColumns+` FROM source_files f JOIN import_runs r ON r.file_id = f.id
WHERE r.source_id = $1 AND f.source_id = $1 ORDER BY r.id DESC LIMIT 1`, sourceID)
	}
	f, err := scanFile(row)
	if errors.Is(err, pgx.ErrNoRows) {
		if runID > 0 {
			return File{}, fmt.Errorf("source %q: run %d does not exist or has no stored file: %w", sourceID, runID, ErrNoFile)
		}
		return File{}, fmt.Errorf("source %q: no run with a stored file: %w", sourceID, ErrNoFile)
	}
	if err != nil {
		return File{}, fmt.Errorf("file for reparse of source %q: %w", sourceID, err)
	}
	return f, nil
}
