package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// DefaultMaxBytes is the default limit on the size of a fetched source (256 MB).
	DefaultMaxBytes int64 = 256 << 20
	// DefaultTimeout is the default limit for a whole HTTPS fetch, body included.
	DefaultTimeout = 5 * time.Minute
	// UserAgent is sent with every HTTPS request.
	UserAgent = "data-importer/0.1 (+https://github.com/willis-dev-md/data-importer)"
)

// ErrTooLarge is returned when a source exceeds the size limit.
var ErrTooLarge = errors.New("source exceeds maximum size")

// ErrNotModified is returned by Fetch when the server answered 304 to a
// conditional request: nothing was downloaded and there is no body.
var ErrNotModified = errors.New("source not modified (HTTP 304)")

// FetchOptions tunes Fetch; zero values mean the defaults.
type FetchOptions struct {
	Timeout  time.Duration
	MaxBytes int64
	Client   *http.Client // optional; its Timeout is used if Timeout is zero

	// IfNoneMatch and IfModifiedSince, when not empty, make an https fetch
	// conditional: they are sent verbatim as If-None-Match / If-Modified-Since
	// (the ETag exactly as the server sent it, weak validators included). A 304
	// then returns ErrNotModified. They are ignored for local files.
	IfNoneMatch     string
	IfModifiedSince string
}

// Conditional reports whether the fetch sends a conditional header.
func (o FetchOptions) Conditional() bool { return o.IfNoneMatch != "" || o.IfModifiedSince != "" }

// WithDefaults returns o with the default size limit filled in.
func (o FetchOptions) WithDefaults() FetchOptions {
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	return o
}

// Body is a streamed source body that hashes what is read through it.
type Body struct {
	rc   io.ReadCloser
	h    hash.Hash
	n    int64
	max  int64
	over bool

	// ETag and LastModified are the response headers of an https fetch, exactly
	// as received ("" if absent or not https).
	ETag         string
	LastModified string
}

// Fetch opens location: a local path, a file:// URL or an https:// URL.
func Fetch(ctx context.Context, location string, opts FetchOptions) (*Body, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	var rc io.ReadCloser
	var err error
	var etag, lastMod string
	switch {
	case strings.HasPrefix(location, "https://"):
		rc, etag, lastMod, err = fetchHTTPS(ctx, location, opts)
		if errors.Is(err, ErrNotModified) {
			return nil, err
		}
	case strings.HasPrefix(location, "file://"):
		var u *url.URL
		u, err = url.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("fetch %q: %w", location, err)
		}
		rc, err = os.Open(u.Path)
	case strings.Contains(location, "://"):
		return nil, fmt.Errorf("fetch %q: unsupported scheme (use a path, file:// or https://)", location)
	default:
		rc, err = os.Open(location)
	}
	if err != nil {
		return nil, fmt.Errorf("fetch %q: %w", location, err)
	}
	return &Body{rc: rc, h: sha256.New(), max: opts.MaxBytes, ETag: etag, LastModified: lastMod}, nil
}

func fetchHTTPS(ctx context.Context, location string, opts FetchOptions) (rc io.ReadCloser, etag, lastMod string, err error) {
	client := opts.Client
	if client == nil {
		client = &http.Client{}
	}
	c := *client // copy so the caller's client is not modified
	if opts.Timeout > 0 {
		c.Timeout = opts.Timeout
	} else if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", UserAgent)
	if opts.IfNoneMatch != "" {
		req.Header.Set("If-None-Match", opts.IfNoneMatch)
	}
	if opts.IfModifiedSince != "" {
		req.Header.Set("If-Modified-Since", opts.IfModifiedSince)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	if resp.StatusCode == http.StatusNotModified && opts.Conditional() {
		_ = resp.Body.Close()
		return nil, "", "", ErrNotModified
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = resp.Body.Close()
		return nil, "", "", fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}
	if resp.ContentLength > opts.MaxBytes {
		_ = resp.Body.Close()
		return nil, "", "", fmt.Errorf("%w: Content-Length %d > %d", ErrTooLarge, resp.ContentLength, opts.MaxBytes)
	}
	return resp.Body, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), nil
}

// Read implements io.Reader; it fails with ErrTooLarge once more than the maximum has been read.
func (b *Body) Read(p []byte) (int, error) {
	if b.over {
		return 0, ErrTooLarge
	}
	n, err := b.rc.Read(p)
	b.n += int64(n)
	if b.n > b.max {
		b.over = true
		return 0, fmt.Errorf("%w (limit %d bytes)", ErrTooLarge, b.max)
	}
	b.h.Write(p[:n])
	return n, err
}

// Close closes the underlying source.
func (b *Body) Close() error { return b.rc.Close() }

// Size returns the number of bytes read so far.
func (b *Body) Size() int64 { return b.n }

// SHA256 returns the hex SHA-256 of the bytes read so far.
func (b *Body) SHA256() string { return hex.EncodeToString(b.h.Sum(nil)) }
