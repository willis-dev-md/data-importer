package source

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/willis-dev-md/data-importer/internal/config"
)

// Default archive limits (see ArchiveLimits).
const (
	DefaultArchiveMaxEntries           = 1000
	DefaultArchiveMaxMemberBytes       = 256 << 20 // uncompressed size of the chosen member
	DefaultArchiveMaxRatio             = 100       // uncompressed : compressed
	archiveRatioSlack            int64 = 1 << 20   // output allowed on top of the ratio (tiny members compress oddly)
	archiveMaxDirBytes           int64 = 4 << 20   // bytes read to parse the zip directory
	maxListedMembers                   = 20
	maxListedNameLen                   = 120
)

// Errors of the archive reader. All are wrapped with detail.
var (
	// ErrArchiveBad means the file is not a usable zip (corrupt, truncated, encrypted, unsupported method, a lying header).
	ErrArchiveBad = errors.New("archive is not a valid zip")
	// ErrArchiveLimit means a safety limit (member count, size, ratio) was breached.
	ErrArchiveLimit = errors.New("archive exceeds a safety limit")
	// ErrArchiveMember means the member regex matched no member, more than one, or a member that cannot be used.
	ErrArchiveMember = errors.New("archive member selection failed")
)

// ArchiveLimits bound what one zip may cost. Zero values mean the defaults.
type ArchiveLimits struct {
	MaxEntries     int   // entries in the zip directory
	MaxMemberBytes int64 // uncompressed size of the chosen member, declared and actually inflated
	MaxRatio       int   // uncompressed : compressed of the chosen member
}

func (l ArchiveLimits) withDefaults() ArchiveLimits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = DefaultArchiveMaxEntries
	}
	if l.MaxMemberBytes <= 0 {
		l.MaxMemberBytes = DefaultArchiveMaxMemberBytes
	}
	if l.MaxRatio <= 0 {
		l.MaxRatio = DefaultArchiveMaxRatio
	}
	return l
}

// openArchiveMember reads the zip in r (at most maxArchive bytes) and returns the one member whose name matches
// a.Member, as a stream of its uncompressed bytes. The zip needs random access, so r is first spooled to an
// unnamed-by-content temporary file (never beyond maxArchive); the member itself is inflated on the fly, never
// extracted. Member names are only matched, never used as paths. Nested archives are not expanded: a member that
// is itself a zip is just bytes. Closing the result removes the temporary file; r is not closed.
func openArchiveMember(ctx context.Context, r io.Reader, a *config.Archive, maxArchive int64, lim ArchiveLimits) (io.ReadCloser, error) {
	if a == nil || a.Type != config.ArchiveZip {
		return nil, errors.New("archive: only type zip is supported")
	}
	re, err := regexp.Compile(a.Member)
	if err != nil {
		return nil, fmt.Errorf("archive.member: %w", err)
	}
	lim = lim.withDefaults()
	if maxArchive <= 0 {
		maxArchive = DefaultMaxBytes
	}

	tmp, err := os.CreateTemp("", "importer-archive-*")
	if err != nil {
		return nil, fmt.Errorf("archive: spool: %w", err)
	}
	// Unlink at once and keep the descriptor: no name stays on disk even if the process is killed or
	// panics. Where an open file cannot be unlinked (e.g. Windows) the name is kept and removed on Close.
	unlinked := os.Remove(tmp.Name()) == nil
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			if !unlinked {
				_ = os.Remove(tmp.Name())
			}
		}
	}()
	n, err := io.Copy(tmp, io.LimitReader(&ctxReader{ctx: ctx, r: r}, maxArchive+1))
	if err != nil {
		return nil, fmt.Errorf("archive: read: %w", err)
	}
	if n > maxArchive {
		return nil, fmt.Errorf("%w (archive limit %d bytes)", ErrTooLarge, maxArchive)
	}

	ra := &capReaderAt{ra: tmp, left: archiveMaxDirBytes}
	zr, err := zip.NewReader(ra, n)
	if err != nil && !(errors.Is(err, zip.ErrInsecurePath) && zr != nil) {
		// ErrInsecurePath only says a name has .. or is absolute; names are never used as paths here.
		if errors.Is(err, errDirTooLarge) {
			return nil, fmt.Errorf("%w: zip directory larger than %d bytes", ErrArchiveLimit, archiveMaxDirBytes)
		}
		return nil, fmt.Errorf("%w: %v", ErrArchiveBad, err)
	}
	ra.off = true
	if len(zr.File) > lim.MaxEntries {
		return nil, fmt.Errorf("%w: %d members, limit %d", ErrArchiveLimit, len(zr.File), lim.MaxEntries)
	}

	var matches []*zip.File
	names := make([]string, 0, len(zr.File))
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") { // directory entry
			continue
		}
		names = append(names, f.Name)
		if re.MatchString(f.Name) {
			matches = append(matches, f)
		}
	}
	switch {
	case len(matches) == 0:
		return nil, fmt.Errorf("%w: no member matches %q; members: %s", ErrArchiveMember, a.Member, listNames(names))
	case len(matches) > 1:
		got := make([]string, len(matches))
		for i, f := range matches {
			got[i] = f.Name
		}
		return nil, fmt.Errorf("%w: %d members match %q (exactly one must); matching: %s", ErrArchiveMember, len(matches), a.Member, listNames(got))
	}
	f := matches[0]
	if f.Mode()&os.ModeSymlink != 0 || f.Mode()&os.ModeType != 0 {
		return nil, fmt.Errorf("%w: member %s is not a regular file (symlink or special entry)", ErrArchiveMember, listNames([]string{f.Name}))
	}
	if f.Flags&0x1 != 0 {
		return nil, fmt.Errorf("%w: member %s is encrypted", ErrArchiveBad, listNames([]string{f.Name}))
	}
	if f.UncompressedSize64 > uint64(lim.MaxMemberBytes) {
		return nil, fmt.Errorf("%w: member %s declares %d uncompressed bytes, limit %d", ErrArchiveLimit, listNames([]string{f.Name}), f.UncompressedSize64, lim.MaxMemberBytes)
	}
	if ratioExceeded(int64(f.UncompressedSize64), int64(f.CompressedSize64), lim.MaxRatio) {
		return nil, fmt.Errorf("%w: member %s declares compression ratio above %d:1", ErrArchiveLimit, listNames([]string{f.Name}), lim.MaxRatio)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: member %s: %v", ErrArchiveBad, listNames([]string{f.Name}), err)
	}
	ok = true
	return &memberReader{
		rc: rc, tmp: tmp, keepName: !unlinked, name: f.Name, declared: int64(f.UncompressedSize64), compressed: int64(f.CompressedSize64), lim: lim, ctx: ctx,
	}, nil
}

func ratioExceeded(uncompressed, compressed int64, maxRatio int) bool {
	return uncompressed > archiveRatioSlack+int64(maxRatio)*compressed
}

// listNames renders member names for an error: quoted (odd characters are escaped), long names cut,
// and at most maxListedMembers of them.
func listNames(names []string) string {
	var b strings.Builder
	for i, n := range names {
		if i == maxListedMembers {
			fmt.Fprintf(&b, ", ... and %d more", len(names)-i)
			break
		}
		if i > 0 {
			b.WriteString(", ")
		}
		if r := []rune(n); len(r) > maxListedNameLen {
			n = string(r[:maxListedNameLen]) + "..."
		}
		fmt.Fprintf(&b, "%q", n)
	}
	return b.String()
}

// memberReader streams the chosen member and checks it against the real stream, not the header: it fails when
// more bytes come out than declared (a lying header or a bomb), above the size limit, above the ratio, or when the
// stream ends with fewer bytes than declared.
type memberReader struct {
	ctx        context.Context
	rc         io.ReadCloser
	tmp        *os.File
	keepName   bool // the spool file could not be unlinked while open: remove it on Close
	name       string
	declared   int64
	compressed int64
	lim        ArchiveLimits
	n          int64
	err        error
}

func (m *memberReader) Read(p []byte) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	if err := m.ctx.Err(); err != nil {
		m.err = err
		return 0, err
	}
	// Never ask for more than one byte past what is allowed, so a bomb is stopped at the limit.
	allowed := m.declared
	if m.lim.MaxMemberBytes < allowed {
		allowed = m.lim.MaxMemberBytes
	}
	if room := allowed - m.n + 1; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := m.rc.Read(p)
	m.n += int64(n)
	switch {
	case m.n > m.declared:
		m.err = fmt.Errorf("%w: member %s inflates past its declared size of %d bytes (lying header or zip bomb)", ErrArchiveLimit, listNames([]string{m.name}), m.declared)
	case m.n > m.lim.MaxMemberBytes:
		m.err = fmt.Errorf("%w: member %s inflates past %d bytes", ErrArchiveLimit, listNames([]string{m.name}), m.lim.MaxMemberBytes)
	case ratioExceeded(m.n, m.compressed, m.lim.MaxRatio):
		m.err = fmt.Errorf("%w: member %s inflates past a ratio of %d:1", ErrArchiveLimit, listNames([]string{m.name}), m.lim.MaxRatio)
	case err == io.EOF && m.n != m.declared:
		m.err = fmt.Errorf("%w: member %s ended after %d bytes, header declares %d", ErrArchiveBad, listNames([]string{m.name}), m.n, m.declared)
	case err != nil && err != io.EOF:
		m.err = fmt.Errorf("%w: member %s: %v", ErrArchiveBad, listNames([]string{m.name}), err)
	default:
		return n, err
	}
	return 0, m.err
}

// Close closes the member and removes the temporary spool file.
func (m *memberReader) Close() error {
	err := m.rc.Close()
	err = errors.Join(err, m.tmp.Close())
	if m.keepName {
		if rerr := os.Remove(m.tmp.Name()); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			err = errors.Join(err, rerr)
		}
	}
	return err
}

var errDirTooLarge = errors.New("zip directory too large")

// capReaderAt fails once more than `left` bytes have been read while parsing the zip directory, so a
// directory with millions of entries cannot use unbounded memory. After parsing (off) it is transparent.
type capReaderAt struct {
	ra   io.ReaderAt
	left int64
	off  bool
}

func (c *capReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if !c.off {
		c.left -= int64(len(p))
		if c.left < 0 {
			return 0, errDirTooLarge
		}
	}
	return c.ra.ReadAt(p, off)
}

// ctxReader stops a copy when the context is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

// locStream adds the chosen location to the metadata of a stream.
type locStream struct {
	Stream
	loc string
}

func (l locStream) Meta() map[string]string {
	m := map[string]string{}
	for k, v := range l.Stream.Meta() {
		m[k] = v
	}
	m[ResolveKey] = l.loc
	return m
}

// SHA256 forwards to the wrapped stream when it can report one.
func (l locStream) SHA256() string {
	if h, ok := l.Stream.(Hasher); ok {
		return h.SHA256()
	}
	return ""
}

// WithLocation wraps s so Meta carries the chosen location when `resolve` is configured.
func WithLocation(s Stream, cfg *config.Source, loc string) Stream {
	if len(cfg.Resolve) == 0 {
		return s
	}
	return locStream{Stream: s, loc: loc}
}
