package source

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/willis-dev-md/data-importer/internal/config"
)

type zent struct {
	name  string
	data  []byte
	mode  os.FileMode // 0 = regular
	store bool        // stored instead of deflated
	flags uint16
}

func buildZip(t testing.TB, ents ...zent) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range ents {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.store {
			h.Method = zip.Store
		}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		h.Flags |= e.flags
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rawZip writes one deflated member whose header declares declared bytes while the stream holds data.
func rawZip(t testing.TB, name string, data []byte, declared uint64) []byte {
	t.Helper()
	var comp bytes.Buffer
	fw, _ := flate.NewWriter(&comp, flate.BestCompression)
	_, _ = fw.Write(data)
	_ = fw.Close()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name: name, Method: zip.Deflate, CRC32: crc32.ChecksumIEEE(data),
		CompressedSize64: uint64(comp.Len()), UncompressedSize64: declared,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(comp.Bytes())
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func csvArchiveCfg(member string) *config.Source {
	return &config.Source{
		ID: "a", Kind: config.KindCSV, Location: "unused", NaturalKey: []string{"id"},
		Archive: &config.Archive{Type: "zip", Member: member},
	}
}

func readMember(t testing.TB, z []byte, member string, lim ArchiveLimits) (string, error) {
	t.Helper()
	m, err := openArchiveMember(context.Background(), bytes.NewReader(z), &config.Archive{Type: "zip", Member: member}, 0, lim)
	if err != nil {
		return "", err
	}
	defer func() { _ = m.Close() }()
	b, err := readAllBytes(m)
	return string(b), err
}

func readAllBytes(r io.Reader) ([]byte, error) {
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, err
		}
	}
}

func TestArchiveMemberSelection(t *testing.T) {
	const odd = "PROV - Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2026.xlsx"
	many := []zent{}
	for i := 0; i < 30; i++ {
		many = append(many, zent{name: fmt.Sprintf("file-%02d.txt", i), data: []byte("x")})
	}
	tests := []struct {
		name    string
		ents    []zent
		member  string
		want    string // member content
		errIs   error
		errHas  []string
		errNot  []string
		maxErrL int
	}{
		{name: "odd name with repeated spaces", member: `(?i)Table 20\.7a .*\.xlsx$`, want: "A",
			ents: []zent{{name: "PROV - Age by Occupation SOC20 (2) Table 20.1a   Weekly pay - Gross 2026.xlsx", data: []byte("B")}, {name: odd, data: []byte("A")}}},
		{name: "exact odd name", member: `^` + strings.ReplaceAll(strings.NewReplacer("(", `\(`, ")", `\)`, ".", `\.`).Replace(odd), " ", `\s+`) + `$`, want: "A",
			ents: []zent{{name: odd, data: []byte("A")}}},
		{name: "traversal name is matched, never used", member: `evil\.csv$`, want: "T",
			ents: []zent{{name: "../../etc/evil.csv", data: []byte("T")}}},
		{name: "absolute name", member: `abs\.csv$`, want: "S",
			ents: []zent{{name: "/var/abs.csv", data: []byte("S")}}},
		{name: "windows separators and odd characters", member: `x`, want: "W",
			ents: []zent{{name: `..\..\x yé;$(id)'.csv`, data: []byte("W")}}},
		{name: "directories are not members", member: `data`, want: "D",
			ents: []zent{{name: "data/", mode: os.ModeDir | 0o755}, {name: "data/d.csv", data: []byte("D")}}},
		{name: "no match lists names", member: `nope`, errIs: ErrArchiveMember, errHas: []string{`"a.csv"`, `"b.csv"`, "nope"},
			ents: []zent{{name: "a.csv"}, {name: "b.csv"}}},
		{name: "two match lists the matches", member: `\.csv$`, errIs: ErrArchiveMember, errHas: []string{"2 members match", `"a.csv"`, `"b.csv"`}, errNot: []string{"c.txt"},
			ents: []zent{{name: "a.csv"}, {name: "b.csv"}, {name: "c.txt"}}},
		{name: "same name twice is ambiguous", member: `^a\.csv$`, errIs: ErrArchiveMember, errHas: []string{"2 members match"},
			ents: []zent{{name: "a.csv", data: []byte("1")}, {name: "a.csv", data: []byte("2")}}},
		{name: "name list is capped", member: `nope`, errIs: ErrArchiveMember, errHas: []string{"and 10 more", `"file-00.txt"`}, errNot: []string{"file-29.txt"}, maxErrL: 2500, ents: many},
		{name: "long name is cut", member: `nope`, errIs: ErrArchiveMember, errHas: []string{"..."}, maxErrL: 600,
			ents: []zent{{name: strings.Repeat("n", 5000)}}},
		{name: "stored member", member: `s`, want: "plain", ents: []zent{{name: "s.csv", data: []byte("plain"), store: true}}},
		{name: "empty member", member: `e`, want: "", ents: []zent{{name: "e.csv"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readMember(t, buildZip(t, tc.ents...), tc.member, ArchiveLimits{})
			if tc.errIs != nil || len(tc.errHas) > 0 {
				if !errors.Is(err, tc.errIs) {
					t.Fatalf("err = %v, want %v", err, tc.errIs)
				}
				for _, s := range tc.errHas {
					if !strings.Contains(err.Error(), s) {
						t.Errorf("error %q lacks %q", err, s)
					}
				}
				for _, s := range tc.errNot {
					if strings.Contains(err.Error(), s) {
						t.Errorf("error %q must not contain %q", err, s)
					}
				}
				if tc.maxErrL > 0 && len(err.Error()) > tc.maxErrL {
					t.Errorf("error is %d bytes, want <= %d", len(err.Error()), tc.maxErrL)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestArchiveNeverTouchesFilesystemByMemberName(t *testing.T) {
	root := t.TempDir()
	spool := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", spool)
	z := buildZip(t,
		zent{name: "../../escape.csv", data: []byte("id\n1\n")},
		zent{name: "/abs-escape.csv", data: []byte("x")},
		zent{name: "../../../etc/passwd", data: []byte("x")})
	m, err := openArchiveMember(context.Background(), bytes.NewReader(z), &config.Archive{Type: "zip", Member: `^\.\./\.\./escape\.csv$`}, 0, ArchiveLimits{})
	if err != nil {
		t.Fatal(err)
	}
	// While open: no name on disk at all (the spool file is unlinked at once, so a kill leaves nothing).
	if ents, _ := os.ReadDir(spool); len(ents) != 0 {
		t.Fatalf("spool dir holds %v while the reader is open", ents)
	}
	if b, err := readAllBytes(m); err != nil || string(b) != "id\n1\n" {
		t.Fatalf("read %q %v", b, err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(spool); len(ents) != 0 {
		t.Errorf("spool file left behind: %v", ents)
	}
	for _, p := range []string{filepath.Join(root, "escape.csv"), filepath.Join(root, "a", "escape.csv"), "/abs-escape.csv"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was created", p)
		}
	}
}

func TestResolveTriedListIsCapped(t *testing.T) {
	p := newProbeSrv(t)
	cfg := resolveCfg(p.URL+"/f/"+strings.Repeat("x", 200)+"-{ym}.csv", 60)
	_, err := ResolveLocation(context.Background(), cfg, FetchOptions{Client: p.Client(), Now: fakeClock(2026, 11, 22)})
	if !errors.Is(err, ErrNoCandidate) {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "and 40 more") || !strings.Contains(msg, "...") {
		t.Errorf("marker/first entry missing: %.300s", msg)
	}
	if len(msg) > 20*(120+3+2)+300 {
		t.Errorf("error is %d bytes", len(msg))
	}
}

func TestArchiveFailuresLeaveNoSpoolFile(t *testing.T) {
	spool := t.TempDir()
	t.Setenv("TMPDIR", spool)
	for _, z := range [][]byte{buildZip(t, zent{name: "a.csv"}), []byte("not a zip at all"), nil} {
		if _, err := openArchiveMember(context.Background(), bytes.NewReader(z), &config.Archive{Type: "zip", Member: `nope`}, 0, ArchiveLimits{}); err == nil {
			t.Fatal("want an error")
		}
	}
	if ents, _ := os.ReadDir(spool); len(ents) != 0 {
		t.Errorf("spool files left: %v", ents)
	}
}

func TestArchiveSafety(t *testing.T) {
	zeros := func(n int) []byte { return make([]byte, n) }
	small := ArchiveLimits{MaxMemberBytes: 1 << 20}
	tests := []struct {
		name  string
		zip   []byte
		lim   ArchiveLimits
		errIs error
		alt   error // also acceptable: archive/zip itself may catch the lie first (as a bad zip)
		has   string
	}{
		{name: "zip bomb: honest header, ratio far above 100:1",
			zip: buildZip(t, zent{name: "b.csv", data: zeros(20 << 20)}), lim: ArchiveLimits{MaxMemberBytes: 100 << 20}, errIs: ErrArchiveLimit, has: "ratio"},
		{name: "zip bomb: honest header, above the size limit",
			zip: buildZip(t, zent{name: "b.csv", data: zeros(2 << 20)}), lim: ArchiveLimits{MaxMemberBytes: 1 << 20, MaxRatio: 100000}, errIs: ErrArchiveLimit, has: "declares"},
		{name: "lying header: declares 10 bytes, stream inflates to 8 MB",
			zip: rawZip(t, "b.csv", zeros(8<<20), 10), lim: ArchiveLimits{MaxMemberBytes: 100 << 20, MaxRatio: 1 << 20}, errIs: ErrArchiveLimit, alt: ErrArchiveBad},
		{name: "lying header: declares small, stream inflates past the size limit",
			zip: rawZip(t, "b.csv", zeros(8<<20), 500<<10), lim: ArchiveLimits{MaxMemberBytes: 1 << 20, MaxRatio: 1 << 20}, errIs: ErrArchiveLimit, alt: ErrArchiveBad},
		{name: "lying header: declares more than the stream holds",
			zip: rawZip(t, "b.csv", []byte("short"), 100), lim: small, errIs: ErrArchiveBad},
		{name: "lying header: declares above the limit",
			zip: rawZip(t, "b.csv", []byte("short"), 1<<40), lim: small, errIs: ErrArchiveLimit},
		{name: "too many members", zip: buildZip(t, zent{name: "a"}, zent{name: "b"}, zent{name: "c"}, zent{name: "d"}),
			lim: ArchiveLimits{MaxEntries: 3}, errIs: ErrArchiveLimit, has: "4 members"},
		{name: "symlink entry is rejected", zip: buildZip(t, zent{name: "link.csv", data: []byte("/etc/passwd"), mode: os.ModeSymlink | 0o777}),
			errIs: ErrArchiveMember, has: "not a regular file"},
		{name: "encrypted member", zip: buildZip(t, zent{name: "a.csv", data: []byte("x"), flags: 0x1}), errIs: ErrArchiveBad, has: "encrypted"},
		{name: "not a zip", zip: []byte("id,name\n1,x\n"), errIs: ErrArchiveBad},
		{name: "empty input", zip: nil, errIs: ErrArchiveBad},
		{name: "truncated zip", zip: func() []byte { z := buildZip(t, zent{name: "a.csv", data: []byte("hello")}); return z[:len(z)-30] }(), errIs: ErrArchiveBad},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readMember(t, tc.zip, `\.csv$|^a$|^b$`, tc.lim)
			if tc.name == "too many members" {
				_, err = readMember(t, tc.zip, `a`, tc.lim)
			}
			if !errors.Is(err, tc.errIs) && !(tc.alt != nil && errors.Is(err, tc.alt)) {
				t.Fatalf("err = %v, want %v", err, tc.errIs)
			}
			if tc.has != "" && !strings.Contains(err.Error(), tc.has) {
				t.Errorf("error %q lacks %q", err, tc.has)
			}
		})
	}
}

func TestArchiveBombIsStoppedWhileStreaming(t *testing.T) {
	// The reader must stop at the limit, not inflate everything first.
	z := rawZip(t, "b.csv", make([]byte, 8<<20), 64)
	m, err := openArchiveMember(context.Background(), bytes.NewReader(z), &config.Archive{Type: "zip", Member: `b`}, 0, ArchiveLimits{MaxRatio: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()
	b, err := readAllBytes(m)
	if !errors.Is(err, ErrArchiveLimit) && !errors.Is(err, ErrArchiveBad) {
		t.Fatalf("err = %v", err)
	}
	if len(b) > 64 {
		t.Errorf("%d bytes came out past the declared 64", len(b))
	}
	if _, err := m.Read(make([]byte, 8)); err == nil {
		t.Errorf("error must be sticky")
	}
}

// memberReader checks the real stream itself, whatever archive/zip does: a source that inflates past the
// declared size, the limit or the ratio, or ends short, is an error and never more than one byte past the limit.
func TestMemberReaderChecksTheRealStream(t *testing.T) {
	mk := func(rc io.Reader, declared, compressed int64, lim ArchiveLimits) *memberReader {
		f, err := os.CreateTemp(t.TempDir(), "m-*")
		if err != nil {
			t.Fatal(err)
		}
		return &memberReader{ctx: context.Background(), rc: io.NopCloser(rc), tmp: f, name: "m", declared: declared, compressed: compressed, lim: lim.withDefaults()}
	}
	tests := []struct {
		name  string
		m     *memberReader
		errIs error
		max   int
	}{
		{"inflates past declared", mk(bytes.NewReader(make([]byte, 1<<20)), 10, 10, ArchiveLimits{MaxRatio: 1 << 20}), ErrArchiveLimit, 10},
		{"inflates past the limit", mk(bytes.NewReader(make([]byte, 1<<20)), 1<<30, 1<<30, ArchiveLimits{MaxMemberBytes: 1000}), ErrArchiveLimit, 1000},
		{"inflates past the ratio", mk(bytes.NewReader(make([]byte, 20<<20)), 20<<20, 10, ArchiveLimits{}), ErrArchiveLimit, 2 << 20},
		{"ends short", mk(strings.NewReader("abc"), 10, 10, ArchiveLimits{}), ErrArchiveBad, 3},
		{"read error", mk(errReader{}, 10, 10, ArchiveLimits{}), ErrArchiveBad, 0},
	}
	for _, tc := range tests {
		b, err := readAllBytes(tc.m)
		if !errors.Is(err, tc.errIs) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.errIs)
		}
		if len(b) > tc.max {
			t.Errorf("%s: %d bytes delivered, want <= %d", tc.name, len(b), tc.max)
		}
		_ = tc.m.Close()
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestArchiveNestedZipIsNotExpanded(t *testing.T) {
	inner := buildZip(t, zent{name: "deep.csv", data: []byte("id\n1\n")})
	outer := buildZip(t, zent{name: "inner.zip", data: inner}, zent{name: "top.csv", data: []byte("id\n2\n")})

	// The nested member is not listed or matchable.
	_, err := readMember(t, outer, `deep\.csv`, ArchiveLimits{})
	if !errors.Is(err, ErrArchiveMember) || !strings.Contains(err.Error(), `"inner.zip"`) {
		t.Fatalf("err = %v", err)
	}
	// Selecting the nested archive itself yields its raw bytes, untouched.
	got, err := readMember(t, outer, `inner\.zip`, ArchiveLimits{})
	if err != nil || got != string(inner) {
		t.Fatalf("nested member is not returned verbatim: %v", err)
	}
	// As a csv source it is just (bad) csv, never unpacked: no record "1" appears.
	st, err := NewCSV(FetchOptions{}).OpenReader(context.Background(), csvArchiveCfg(`inner\.zip`), bytes.NewReader(outer))
	if err == nil {
		recs, _ := drainTolerant(st)
		for _, r := range recs {
			if r.Fields["id"] == "1" {
				t.Errorf("nested archive was expanded: %+v", r)
			}
		}
		_ = st.Close()
	}
}

func drainTolerant(s Stream) (recs []*Record, errs []*RowError) {
	for {
		it, ok := s.Next()
		if !ok {
			return
		}
		if it.Record != nil {
			recs = append(recs, it.Record)
		} else {
			errs = append(errs, it.Error)
		}
	}
}

func TestArchiveDirectoryIsBounded(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i < 70000; i++ {
		if _, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("dir/sub-directory-with-a-longish-name/%06d.csv", i), Method: zip.Store}); err != nil {
			t.Fatal(err)
		}
	}
	_ = zw.Close()
	// Entry limit very high on purpose: the directory read cap must stop it.
	_, err := readMember(t, buf.Bytes(), `000001\.csv`, ArchiveLimits{MaxEntries: 10_000_000})
	if !errors.Is(err, ErrArchiveLimit) {
		t.Fatalf("err = %v", err)
	}
}

func TestArchiveTooLarge(t *testing.T) {
	z := buildZip(t, zent{name: "a.csv", data: bytes.Repeat([]byte("abcdefgh"), 1000)})
	_, err := openArchiveMember(context.Background(), bytes.NewReader(z), &config.Archive{Type: "zip", Member: `a`}, int64(len(z)-1), ArchiveLimits{})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	m, err := openArchiveMember(context.Background(), bytes.NewReader(z), &config.Archive{Type: "zip", Member: `a`}, int64(len(z)), ArchiveLimits{})
	if err != nil {
		t.Fatalf("exactly at the limit must work: %v", err)
	}
	_ = m.Close()
}

func TestArchiveCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	z := buildZip(t, zent{name: "a.csv", data: []byte("x")})
	if _, err := openArchiveMember(ctx, bytes.NewReader(z), &config.Archive{Type: "zip", Member: `a`}, 0, ArchiveLimits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestCSVAdapterArchive(t *testing.T) {
	z := buildZip(t,
		zent{name: "readme.txt", data: []byte("hi")},
		zent{name: "Data  2026   final.csv", data: []byte("id,name\n1,a\n2,b\n")})
	cfg := csvArchiveCfg(`(?i)data\s+2026\s+final\.csv$`)
	st, err := NewCSV(FetchOptions{}).OpenReader(context.Background(), cfg, bytes.NewReader(z))
	if err != nil {
		t.Fatal(err)
	}
	recs, errs := drain(t, st)
	_ = st.Close()
	if len(recs) != 2 || len(errs) != 0 {
		t.Fatalf("records %d, row errors %d", len(recs), len(errs))
	}

	// Open on a local archive file: the same.
	p := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(p, z, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Location = p
	st, err = NewCSV(FetchOptions{}).Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	recs, _ = drain(t, st)
	_ = st.Close()
	if len(recs) != 2 {
		t.Fatalf("records %d", len(recs))
	}

	// A corrupt member (CRC) is an error at the end of the stream, never a silent short read.
	bad := buildZip(t, zent{name: "d.csv", data: []byte("id,name\n1,a\n2,b\n"), store: true})
	bad[bytes.Index(bad, []byte("1,a"))] = '9'
	cfg.Archive.Member = `d\.csv`
	st, err = NewCSV(FetchOptions{}).OpenReader(context.Background(), cfg, bytes.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	drainTolerant(st)
	if err := st.Err(); !errors.Is(err, ErrArchiveBad) {
		t.Fatalf("corrupt member: stream err = %v", err)
	}
	_ = st.Close()
}

func TestXLSXAdapterArchive(t *testing.T) {
	wb := fixtureXLSX(t, "boe/baserate-trimmed.xlsx")
	const odd = "PROV - Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2026.xlsx"
	z := buildZip(t,
		zent{name: "PROV - Age by Occupation SOC20 (2) Table 20.7b   CV Annual pay - Gross 2026.xlsx", data: []byte("not a workbook")},
		zent{name: odd, data: wb})
	plain, err := config.LoadSource(filepath.Join("..", "..", "docs", "examples", "boe-baserate.source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := *plain
	cfg.Archive = &config.Archive{Type: "zip", Member: `(?i)Table 20\.7a .*\.xlsx$`}

	want := readXLSXConfig(t, plain, wb)
	ad := NewXLSX(FetchOptions{})
	if got := ad.FetchOptions(&cfg).MaxBytes; got != DefaultMaxBytes {
		t.Errorf("archive fetch limit = %d, want the archive default %d (the workbook limit applies to the member)", got, DefaultMaxBytes)
	}
	if got := ad.FetchOptions(plain).MaxBytes; got != DefaultXLSXMaxFileBytes {
		t.Errorf("plain workbook fetch limit = %d", got)
	}

	st, err := ad.OpenReader(context.Background(), &cfg, bytes.NewReader(z))
	if err != nil {
		t.Fatal(err)
	}
	g := drainX(t, st)
	if g.err != nil || len(g.errs) != 0 || len(g.recs) != want {
		t.Fatalf("OpenReader: %d records (want %d), %d row errors, err %v", len(g.recs), want, len(g.errs), g.err)
	}

	// Open from a local archive: change detection hashes the ARCHIVE.
	p := filepath.Join(t.TempDir(), "a.zip")
	if err := os.WriteFile(p, z, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Location = p
	st, err = ad.Open(context.Background(), &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if h, ok := st.(Hasher); !ok || h.SHA256() != sha(z) {
		t.Errorf("stream sha is not the archive's")
	}
	g = drainX(t, st)
	if g.err != nil || len(g.recs) != want {
		t.Fatalf("Open: %d records, err %v", len(g.recs), g.err)
	}

	// The workbook limit applies to the member, not to the archive.
	tight := &XLSXAdapter{Limits: XLSXLimits{MaxFileBytes: int64(len(wb) - 1)}}
	if _, err := tight.OpenReader(context.Background(), &cfg, bytes.NewReader(z)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("member above the workbook limit: err = %v", err)
	}
	// A member that is not a workbook fails clearly.
	cfg.Archive.Member = `Table 20\.7b`
	if _, err := ad.OpenReader(context.Background(), &cfg, bytes.NewReader(z)); !errors.Is(err, ErrXLSXBadArchive) {
		t.Errorf("bad workbook: err = %v", err)
	}
}

func readXLSXConfig(t *testing.T, cfg *config.Source, wb []byte) int {
	t.Helper()
	st, err := NewXLSXReader(context.Background(), wb, cfg, XLSXLimits{})
	if err != nil {
		t.Fatal(err)
	}
	g := drainX(t, st)
	if g.err != nil {
		t.Fatal(g.err)
	}
	return len(g.recs)
}
