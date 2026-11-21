package source

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/record"
)

// maxRecordBytes bounds one logical row (a quoted field may span lines); a longer row is a fatal error.
const maxRecordBytes = 16 << 20

// ErrRecordTooLarge is the fatal error for a row longer than maxRecordBytes.
var ErrRecordTooLarge = errors.New("csv row exceeds maximum size")

// CSVAdapter reads `kind: csv` sources.
type CSVAdapter struct {
	Fetch FetchOptions
}

// NewCSV returns the csv adapter.
func NewCSV(opts FetchOptions) *CSVAdapter { return &CSVAdapter{Fetch: opts} }

// Open fetches cfg.Location and returns a stream of its rows. The header (if any) is read eagerly.
func (a *CSVAdapter) Open(ctx context.Context, cfg *config.Source) (Stream, error) {
	if cfg == nil || cfg.Kind != config.KindCSV {
		return nil, errors.New("csv adapter: source kind is not csv")
	}
	body, err := Fetch(ctx, cfg.Location, a.Fetch)
	if err != nil {
		return nil, err
	}
	var opts config.CSV
	if cfg.CSV != nil {
		opts = *cfg.CSV
	}
	s, err := newCSVStream(ctx, body, body, opts, cfg.NaturalKey)
	if err != nil {
		_ = body.Close()
		return nil, err
	}
	return s, nil
}

// NewCSVReader reads CSV rows from r (no fetching, no closing of r).
func NewCSVReader(ctx context.Context, r io.Reader, opts config.CSV, naturalKey []string) (Stream, error) {
	return newCSVStream(ctx, r, nil, opts, naturalKey)
}

type csvStream struct {
	ctx    context.Context
	br     *bufio.Reader
	closer io.Closer
	sum    interface{ SHA256() string }
	delim  rune
	names  []string // column names; nil until known (headerless without columns)
	nkey   []string
	line   int64 // physical lines consumed
	first  bool  // nothing consumed yet (BOM stripping)
	err    error
	done   bool
}

func decoderFor(r io.Reader, enc string) (io.Reader, error) {
	switch enc {
	case "utf-8":
		return r, nil
	case "windows-1252":
		return transform.NewReader(r, charmap.Windows1252.NewDecoder()), nil
	case "utf-16", "utf-16le":
		return transform.NewReader(r, unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder()), nil
	case "utf-16be":
		return transform.NewReader(r, unicode.UTF16(unicode.BigEndian, unicode.UseBOM).NewDecoder()), nil
	}
	return nil, fmt.Errorf("csv.encoding: unsupported encoding %q", enc)
}

func newCSVStream(ctx context.Context, r io.Reader, closer io.Closer, opts config.CSV, nkey []string) (*csvStream, error) {
	dr, err := decoderFor(r, opts.EncodingName())
	if err != nil {
		return nil, err
	}
	s := &csvStream{
		ctx:    ctx,
		br:     bufio.NewReaderSize(dr, 64<<10),
		closer: closer,
		delim:  opts.DelimiterRune(),
		nkey:   nkey,
		first:  true,
	}
	if h, ok := r.(interface{ SHA256() string }); ok {
		s.sum = h
	}
	if len(opts.Columns) > 0 {
		s.names = append([]string(nil), opts.Columns...)
	}
	if opts.HasHeader() {
		raw, pos, fields, bad, err := s.readRecord()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("csv: no header row (empty input)")
			}
			return nil, fmt.Errorf("csv header: %w", err)
		}
		if bad != "" {
			return nil, fmt.Errorf("csv header (line %d): %s: %q", pos, bad, truncate(raw))
		}
		if s.names == nil {
			seen := map[string]bool{}
			for i, f := range fields {
				if strings.TrimSpace(f) == "" {
					return nil, fmt.Errorf("csv header (line %d): column %d has an empty name", pos, i+1)
				}
				if seen[f] {
					return nil, fmt.Errorf("csv header (line %d): duplicate column %q", pos, f)
				}
				seen[f] = true
			}
			s.names = fields
		}
	}
	if s.names != nil {
		if err := s.checkKey(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *csvStream) checkKey() error {
	have := map[string]bool{}
	for _, n := range s.names {
		have[n] = true
	}
	for _, k := range s.nkey {
		if !have[k] {
			return fmt.Errorf("natural_key: field %q is not a column (columns: %s)", k, strings.Join(s.names, ", "))
		}
	}
	return nil
}

// Next implements Stream.
func (s *csvStream) Next() (Item, bool) {
	if s.done {
		return Item{}, false
	}
	for {
		if err := s.ctx.Err(); err != nil {
			return s.fail(err)
		}
		raw, pos, fields, bad, err := s.readRecord()
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.done = true
				return Item{}, false
			}
			return s.fail(err)
		}
		if raw == "" && len(fields) == 1 && fields[0] == "" && bad == "" {
			continue // blank line
		}
		if bad != "" {
			return Item{Error: &RowError{Position: pos, Raw: raw, Reason: bad}}, true
		}
		if s.names == nil { // headerless, no explicit columns: name by the first row's width
			s.names = make([]string, len(fields))
			for i := range fields {
				s.names[i] = "c" + strconv.Itoa(i+1)
			}
			if err := s.checkKey(); err != nil {
				return Item{Error: &RowError{Position: pos, Raw: raw, Reason: err.Error()}}, true
			}
		}
		if len(fields) != len(s.names) {
			return Item{Error: &RowError{Position: pos, Raw: raw, Reason: fmt.Sprintf("expected %d fields, got %d", len(s.names), len(fields))}}, true
		}
		m := make(map[string]any, len(fields))
		for i, f := range fields {
			m[s.names[i]] = f
		}
		parts := make([]string, len(s.nkey))
		for i, k := range s.nkey {
			v, _ := m[k].(string)
			if strings.TrimSpace(v) == "" {
				return Item{Error: &RowError{Position: pos, Raw: raw, Reason: fmt.Sprintf("natural key field %q is empty", k)}}, true
			}
			parts[i] = v
		}
		return Item{Record: &Record{Position: pos, Fields: m, NaturalKey: record.Key(parts...)}}, true
	}
}

func (s *csvStream) fail(err error) (Item, bool) {
	s.err = err
	s.done = true
	return Item{}, false
}

// Err implements Stream.
func (s *csvStream) Err() error { return s.err }

// Close implements Stream.
func (s *csvStream) Close() error {
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}

// SHA256 implements Hasher (empty when the input is not a fetched Body).
func (s *csvStream) SHA256() string {
	if s.sum == nil {
		return ""
	}
	return s.sum.SHA256()
}

const (
	stStart = iota // at the start of a field
	stPlain
	stQuoted
	stQuoteEnd // just saw a quote inside a quoted field
)

// readRecord reads one logical row. bad is non-empty for a malformed row (fields are then unreliable).
// It returns io.EOF when no rows remain.
func (s *csvStream) readRecord() (raw string, pos int64, fields []string, bad string, err error) {
	var rawB strings.Builder
	var cur strings.Builder
	state := stStart
	started := false
	for {
		line, rerr := s.readLine()
		if rerr != nil {
			if !started || !errors.Is(rerr, io.EOF) {
				return "", 0, nil, "", rerr
			}
			// EOF inside a quoted field.
			return rawB.String(), pos, nil, "unterminated quoted field", nil
		}
		if !started {
			started = true
			pos = s.line
		} else {
			rawB.WriteByte('\n')
		}
		if rawB.Len()+len(line) > maxRecordBytes {
			return "", 0, nil, "", ErrRecordTooLarge
		}
		rawB.WriteString(line)
		for _, r := range line {
			switch state {
			case stStart:
				switch {
				case r == '"':
					state = stQuoted
				case r == s.delim:
					fields = append(fields, "")
				default:
					cur.WriteRune(r)
					state = stPlain
				}
			case stPlain:
				switch {
				case r == s.delim:
					fields = append(fields, cur.String())
					cur.Reset()
					state = stStart
				case r == '"':
					if bad == "" {
						bad = "bare quote in unquoted field"
					}
					cur.WriteRune(r)
				default:
					cur.WriteRune(r)
				}
			case stQuoted:
				if r == '"' {
					state = stQuoteEnd
				} else {
					cur.WriteRune(r)
				}
			case stQuoteEnd:
				switch {
				case r == '"':
					cur.WriteRune('"')
					state = stQuoted
				case r == s.delim:
					fields = append(fields, cur.String())
					cur.Reset()
					state = stStart
				default:
					if bad == "" {
						bad = "text after closing quote"
					}
					cur.WriteRune(r)
					state = stPlain
				}
			}
		}
		if state == stQuoted {
			cur.WriteByte('\n')
			if cur.Len() > maxRecordBytes {
				return "", 0, nil, "", ErrRecordTooLarge
			}
			continue
		}
		fields = append(fields, cur.String())
		return rawB.String(), pos, fields, bad, nil
	}
}

// readLine returns the next physical line without its line terminator (LF or CRLF),
// with invalid UTF-8 replaced and a leading BOM removed.
func (s *csvStream) readLine() (string, error) {
	var buf []byte
	for {
		chunk, err := s.br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxRecordBytes {
			return "", ErrRecordTooLarge
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(buf) > 0 {
				break // final line without terminator
			}
			return "", err
		}
		break
	}
	s.line++
	line := string(buf)
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if s.first {
		s.first = false
		line = strings.TrimPrefix(line, "\xef\xbb\xbf")
	}
	if !utf8.ValidString(line) {
		line = strings.ToValidUTF8(line, "\xef\xbf\xbd")
	}
	return line, nil
}

func truncate(s string) string {
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
