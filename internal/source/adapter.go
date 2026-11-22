package source

import (
	"context"
	"io"

	"github.com/willis-dev-md/data-importer/internal/config"
)

// Record is one parsed source row.
type Record struct {
	Position   int64 // 1-based line number where the row starts
	Fields     map[string]any
	NaturalKey string // record.Key of the configured natural key fields
}

// RowError is a row that could not be turned into a Record. It is data, not a failure of the stream.
type RowError struct {
	Position int64
	Raw      string
	Reason   string
}

// Item is either a Record or a RowError (exactly one is non-nil).
type Item struct {
	Record *Record
	Error  *RowError
}

// Stream yields items one at a time; the source is never held in memory as a whole.
type Stream interface {
	// Next returns the next item, or false at the end or after a fatal error (see Err).
	Next() (Item, bool)
	// Err returns the fatal error that ended the stream, or nil after a clean end.
	Err() error
	// Meta returns the preamble metadata (key to value), available after Open; nil without a preamble.
	Meta() map[string]string
	Close() error
}

// Hasher is implemented by streams that can report the SHA-256 (hex) of the bytes read so far;
// after the stream has ended cleanly it is the checksum of the whole input.
type Hasher interface {
	SHA256() string
}

// Adapter opens a source as a Stream.
type Adapter interface {
	Open(ctx context.Context, cfg *config.Source) (Stream, error)
}

// FileAdapter is implemented by adapters whose input is one file that can be
// fetched separately from parsing (csv, xlsx). The importer then fetches the file
// itself (conditional request, hashing, retention in the file store) and hands the
// stored bytes to OpenReader, so a stored file can be parsed again without the
// network. Adapters that are not FileAdapters (for example an http_json API) are
// opened with Open and keep no file.
type FileAdapter interface {
	Adapter
	// FetchOptions returns the effective options (client, timeout, size limit) the
	// adapter would use to fetch cfg.Location. MaxBytes is always resolved (> 0).
	FetchOptions(cfg *config.Source) FetchOptions
	// OpenReader parses r (the bytes of the file) as Open would parse the fetched
	// file. It never touches the network. The caller closes r after Close.
	OpenReader(ctx context.Context, cfg *config.Source, r io.Reader) (Stream, error)
}
