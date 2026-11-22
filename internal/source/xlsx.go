package source

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/xuri/excelize/v2"

	"github.com/willis-dev-md/data-importer/internal/config"
	"github.com/willis-dev-md/data-importer/internal/record"
)

// Default xlsx limits (see XLSXLimits).
const (
	DefaultXLSXMaxFileBytes  int64 = 100 << 20 // the workbook file as fetched
	DefaultXLSXMaxRows       int64 = 2_000_000 // rows walked over all selected sheets, skipped and blank rows included
	DefaultXLSXMaxSheets           = 256
	DefaultXLSXMaxUnzipBytes int64 = 1 << 30 // sum of the decompressed sizes of all parts of the workbook
	DefaultXLSXMaxZipEntries       = 10_000
	maxXLSXRowBytes                = 16 << 20 // text of one row, like maxRecordBytes for csv
	xlsxXMLMemoryLimit       int64 = 16 << 20 // parts above this are spooled to a temporary file by excelize
)

// Errors that end an xlsx stream or fail Open.
var (
	ErrXLSXTooManyRows = errors.New("xlsx exceeds the maximum number of rows")
	ErrXLSXUnzipLimit  = errors.New("xlsx exceeds the maximum decompressed size")
	ErrXLSXBadArchive  = errors.New("xlsx is not a valid workbook")
	ErrXLSXRowTooLarge = errors.New("xlsx row exceeds maximum size")
)

// XLSXLimits bound what one workbook may cost. Zero values mean the defaults.
type XLSXLimits struct {
	MaxFileBytes  int64
	MaxRows       int64
	MaxSheets     int
	MaxUnzipBytes int64
	MaxZipEntries int
}

func (l XLSXLimits) withDefaults() XLSXLimits {
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = DefaultXLSXMaxFileBytes
	}
	if l.MaxRows <= 0 {
		l.MaxRows = DefaultXLSXMaxRows
	}
	if l.MaxSheets <= 0 {
		l.MaxSheets = DefaultXLSXMaxSheets
	}
	if l.MaxUnzipBytes <= 0 {
		l.MaxUnzipBytes = DefaultXLSXMaxUnzipBytes
	}
	if l.MaxZipEntries <= 0 {
		l.MaxZipEntries = DefaultXLSXMaxZipEntries
	}
	return l
}

// XLSXAdapter reads `kind: xlsx` sources.
type XLSXAdapter struct {
	Fetch   FetchOptions
	Limits  XLSXLimits
	Archive ArchiveLimits // used when the source has `archive:`; zero values mean the defaults
}

// NewXLSX returns the xlsx adapter with the default limits.
func NewXLSX(opts FetchOptions) *XLSXAdapter { return &XLSXAdapter{Fetch: opts} }

// Open fetches cfg.Location, checks the workbook against the limits, resolves the configured sheets (and their
// headers) and returns a stream of cleaned rows. Nothing is evaluated: formulas yield their cached values.
func (a *XLSXAdapter) Open(ctx context.Context, cfg *config.Source) (Stream, error) {
	if cfg == nil || cfg.Kind != config.KindXLSX || cfg.XLSX == nil {
		return nil, errors.New("xlsx adapter: source kind is not xlsx")
	}
	lim := a.Limits.withDefaults()
	fo := a.FetchOptions(cfg)
	loc, err := ResolveLocation(ctx, cfg, fo)
	if err != nil {
		return nil, err
	}
	body, err := Fetch(ctx, loc, fo)
	if err != nil {
		return nil, err
	}
	var in io.Reader = body
	var m io.ReadCloser
	if cfg.Archive != nil {
		m, err = openArchiveMember(ctx, body, cfg.Archive, fo.MaxBytes, a.Archive)
		if err != nil {
			_ = body.Close()
			return nil, err
		}
		in = io.LimitReader(m, lim.MaxFileBytes+1)
	}
	data, err := io.ReadAll(in)
	sum := body.SHA256() // of the fetched file (the archive, if any)
	_ = body.Close()
	if m != nil {
		_ = m.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("read workbook: %w", err)
	}
	if cfg.Archive != nil && int64(len(data)) > lim.MaxFileBytes {
		return nil, fmt.Errorf("%w: workbook in the archive exceeds %d bytes", ErrTooLarge, lim.MaxFileBytes)
	}
	s, err := openXLSX(ctx, data, sum, cfg, lim)
	if err != nil {
		return nil, err
	}
	return WithLocation(s, cfg, loc), nil
}

// FetchOptions implements FileAdapter: the fetch options with the workbook size limit applied.
func (a *XLSXAdapter) FetchOptions(cfg *config.Source) FetchOptions {
	lim := a.Limits.withDefaults()
	fo := a.Fetch
	if cfg != nil && cfg.Archive != nil {
		// The fetched file is the archive; the workbook limit applies to the member (see OpenReader).
		return fo.WithDefaults()
	}
	if fo.MaxBytes <= 0 || fo.MaxBytes > lim.MaxFileBytes {
		fo.MaxBytes = lim.MaxFileBytes
	}
	return fo
}

// OpenReader implements FileAdapter. A workbook is a zip archive and is read into memory (bounded by the
// workbook size limit, as in Open); more than the limit fails with ErrTooLarge.
func (a *XLSXAdapter) OpenReader(ctx context.Context, cfg *config.Source, r io.Reader) (Stream, error) {
	if cfg == nil || cfg.Kind != config.KindXLSX || cfg.XLSX == nil {
		return nil, errors.New("xlsx adapter: source kind is not xlsx")
	}
	lim := a.Limits.withDefaults()
	max := a.FetchOptions(cfg).MaxBytes
	if cfg.Archive != nil {
		m, err := openArchiveMember(ctx, r, cfg.Archive, max, a.Archive)
		if err != nil {
			return nil, err
		}
		defer func() { _ = m.Close() }()
		r, max = m, lim.MaxFileBytes
	}
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, fmt.Errorf("read workbook: %w", err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%w (limit %d bytes)", ErrTooLarge, max)
	}
	return openXLSX(ctx, data, "", cfg, lim)
}

// NewXLSXReader reads an xlsx workbook held in memory (no fetching), for callers that already have the bytes.
func NewXLSXReader(ctx context.Context, data []byte, cfg *config.Source, lim XLSXLimits) (Stream, error) {
	if cfg == nil || cfg.Kind != config.KindXLSX || cfg.XLSX == nil {
		return nil, errors.New("xlsx reader: source kind is not xlsx")
	}
	lim = lim.withDefaults()
	if int64(len(data)) > lim.MaxFileBytes {
		return nil, fmt.Errorf("%w (limit %d bytes)", ErrTooLarge, lim.MaxFileBytes)
	}
	return openXLSX(ctx, data, "", cfg, lim)
}

type sheetPlan struct {
	name string
	cols []config.Col
}

type xlsxStream struct {
	ctx    context.Context
	cfg    *config.Source
	lim    XLSXLimits
	f      *excelize.File
	sum    string
	info   *zipInfo
	plans  []sheetPlan
	cur    int // index of the sheet being read; len(plans) when finished
	rows   *excelize.Rows
	rowNum int64 // 1-based row number in the current sheet
	total  int64
	pipe   *Pipeline
	base   map[string]string
	queue  []Item
	err    error
	done   bool
	closed bool
}

// safely runs fn and turns a panic (excelize has seen malformed input) into an error.
func safely(what string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %s: internal error: %v", ErrXLSXBadArchive, what, r)
		}
	}()
	return fn()
}

func openXLSX(ctx context.Context, data []byte, sum string, cfg *config.Source, lim XLSXLimits) (Stream, error) {
	info, err := checkZip(data, lim)
	if err != nil {
		return nil, err
	}
	xmlLimit := lim.MaxUnzipBytes
	if xmlLimit > xlsxXMLMemoryLimit {
		xmlLimit = xlsxXMLMemoryLimit
	}
	var f *excelize.File
	if err := safely("open", func() (err error) {
		f, err = excelize.OpenReader(bytes.NewReader(data), excelize.Options{UnzipSizeLimit: lim.MaxUnzipBytes, UnzipXMLSizeLimit: xmlLimit})
		return err
	}); err != nil {
		if errors.Is(err, ErrXLSXBadArchive) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrXLSXBadArchive, err)
	}
	s := &xlsxStream{ctx: ctx, cfg: cfg, lim: lim, f: f, sum: sum, info: info}
	if err := safely("sheets", s.plan); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

// zipInfo is what the archive pass learned: the last row number of every worksheet part and which part
// belongs to which sheet name. The reader uses it to detect a row stream that ends early.
type zipInfo struct {
	lastRow map[string]int64 // lower-case part name -> row number of the last <row>
	cur     string           // part being scanned
	maxSST  int64            // highest shared string index used by any cell (-1: none)
	siCount int64            // number of <si> in xl/sharedStrings.xml
	sheets  []struct{ name, rid string }
	rels    map[string]string // relationship id -> lower-case part name
}

// lastRowOf returns the last row number recorded for a sheet; ok is false when it cannot be determined.
func (z *zipInfo) lastRowOf(sheet string) (int64, bool) {
	for _, sh := range z.sheets {
		if sh.name == sheet {
			part, ok := z.rels[sh.rid]
			if !ok {
				return 0, false
			}
			n, ok := z.lastRow[part]
			return n, ok
		}
	}
	return 0, false
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// isCheckedXML reports whether the reader depends on this part, so it must be well-formed XML. excelize stops
// silently at the first XML error, which would drop the rest of a sheet without any error.
func isCheckedXML(lower string) bool {
	switch lower {
	case "[content_types].xml", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/sharedstrings.xml", "xl/styles.xml":
		return true
	}
	return strings.HasPrefix(lower, "xl/worksheets/") && strings.HasSuffix(lower, ".xml") && !strings.Contains(lower[len("xl/worksheets/"):], "/")
}

func xmlAttr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// structErr is a structural inconsistency in a part (as opposed to a syntax error): excelize would silently
// drop or move data there.
type structErr struct{ msg string }

func (e *structErr) Error() string { return e.msg }

var cellRefRe = regexp.MustCompile(`^([A-Z]{1,3})([0-9]+)$`)

// supportedCellTypes are the t="" values the reader understands; anything else is not read faithfully.
var supportedCellTypes = map[string]bool{"": true, "b": true, "d": true, "e": true, "inlineStr": true, "n": true, "s": true, "str": true}

func nonNegInt(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

// sheetScan checks the structure of one worksheet while its tokens stream by. excelize silently drops or
// moves rows and cells whose numbers are inconsistent, so those are errors here.
type sheetScan struct {
	inData, inRow, inCell, inV bool
	row                        int64
	col                        int
	cellS                      bool // the current cell is a shared string
	v                          strings.Builder
	info                       *zipInfo
}

func (w *sheetScan) start(se xml.StartElement, dec *xml.Decoder) error {
	at := func() string {
		_, off := dec.InputPos()
		return fmt.Sprintf("row %d, near line %d", w.row, off)
	}
	if w.inData && (se.Name.Local == "row" || se.Name.Local == "c") {
		for i, a := range se.Attr {
			for _, b := range se.Attr[:i] {
				if a.Name.Local == b.Name.Local {
					return &structErr{fmt.Sprintf("attribute %q is repeated on a %s element (%s); readers disagree on which value wins", a.Name.Local, se.Name.Local, at())}
				}
			}
		}
	}
	switch se.Name.Local {
	case "sheetData":
		w.inData = true
	case "row":
		if !w.inData {
			return nil
		}
		if w.inRow {
			return &structErr{fmt.Sprintf("row element inside a row (%s)", at())}
		}
		w.inRow, w.col = true, 0
		if rs := xmlAttr(se, "r"); hasAttr(se, "r") {
			n, ok := nonNegInt(rs)
			if !ok || n < 1 || n > int64(config.MaxSheetRows) {
				return &structErr{fmt.Sprintf("row number r=%q is not an integer from 1 to %d (after row %d)", rs, config.MaxSheetRows, w.row)}
			}
			if n <= w.row {
				return &structErr{fmt.Sprintf("row number %d is not greater than the previous row %d (duplicate or out-of-order rows would be dropped)", n, w.row)}
			}
			w.row = n
		} else {
			w.row++
			if w.row > int64(config.MaxSheetRows) {
				return &structErr{fmt.Sprintf("more than %d rows", config.MaxSheetRows)}
			}
		}
	case "c":
		if !w.inData {
			return nil
		}
		if !w.inRow || w.inCell {
			return &structErr{fmt.Sprintf("cell element outside a row or inside a cell (%s)", at())}
		}
		w.inCell, w.cellS = true, false
		if hasAttr(se, "r") {
			ref := xmlAttr(se, "r")
			m := cellRefRe.FindStringSubmatch(ref)
			if m == nil {
				return &structErr{fmt.Sprintf("cell reference r=%q is not like A1 (%s)", ref, at())}
			}
			c, ok := config.ColumnIndex(m[1])
			rn, ok2 := nonNegInt(m[2])
			if !ok || !ok2 {
				return &structErr{fmt.Sprintf("cell reference r=%q is outside the worksheet (%s)", ref, at())}
			}
			if rn != w.row {
				return &structErr{fmt.Sprintf("cell %s is in row %d: its row part does not match (cell would be misplaced)", ref, w.row)}
			}
			if c <= w.col {
				return &structErr{fmt.Sprintf("cell %s is not to the right of the previous cell in row %d (duplicate or out-of-order cells would be dropped)", ref, w.row)}
			}
			w.col = c
		} else {
			w.col++
			if w.col > config.MaxSheetColumns {
				return &structErr{fmt.Sprintf("more than %d cells in row %d", config.MaxSheetColumns, w.row)}
			}
		}
		if hasAttr(se, "s") {
			if _, ok := nonNegInt(xmlAttr(se, "s")); !ok {
				return &structErr{fmt.Sprintf("cell style s=%q is not a non-negative integer (the cell would be dropped; %s)", xmlAttr(se, "s"), at())}
			}
		}
		t := xmlAttr(se, "t")
		if !supportedCellTypes[t] {
			return &structErr{fmt.Sprintf("unsupported cell type t=%q (%s)", t, at())}
		}
		w.cellS = t == "s"
	case "v":
		if w.inCell && w.cellS {
			w.inV = true
			w.v.Reset()
		}
	}
	return nil
}

func (w *sheetScan) end(name string) error {
	switch name {
	case "sheetData":
		w.inData = false
	case "row":
		w.inRow = false
		if w.inData {
			w.info.recordLast(w)
		}
	case "c":
		w.inCell = false
	case "v":
		if w.inV {
			w.inV = false
			idx, ok := nonNegInt(strings.TrimSpace(w.v.String()))
			if !ok {
				return &structErr{fmt.Sprintf("shared string index %q is not a non-negative integer (row %d)", truncate(w.v.String()), w.row)}
			}
			if idx > w.info.maxSST {
				w.info.maxSST = idx
			}
		}
	}
	return nil
}

func hasAttr(se xml.StartElement, local string) bool {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return true
		}
	}
	return false
}

func (z *zipInfo) recordLast(w *sheetScan) { z.lastRow[z.cur] = w.row }

// scanXML reads every token of one part (streaming, nothing is kept) and fails on the first syntax error,
// such as an illegal control character or an unclosed element, and, for worksheets, on structural
// inconsistencies (see sheetScan).
func scanXML(r io.Reader, lower string, info *zipInfo) error {
	dec := xml.NewDecoder(r)
	var ws *sheetScan
	if strings.HasPrefix(lower, "xl/worksheets/") {
		ws = &sheetScan{info: info}
		info.cur = lower
	}
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if ws != nil {
				if err := ws.start(t, dec); err != nil {
					return err
				}
			}
			switch {
			case lower == "xl/workbook.xml" && t.Name.Local == "sheet":
				info.sheets = append(info.sheets, struct{ name, rid string }{xmlAttr(t, "name"), xmlAttr(t, "id")})
			case lower == "xl/_rels/workbook.xml.rels" && t.Name.Local == "Relationship":
				tg := strings.TrimPrefix(xmlAttr(t, "Target"), "/")
				if !strings.HasPrefix(strings.ToLower(tg), "xl/") {
					tg = "xl/" + tg
				}
				info.rels[xmlAttr(t, "Id")] = strings.ToLower(tg)
			case lower == "xl/sharedstrings.xml" && t.Name.Local == "si" && depth == 2:
				info.siCount++
			}
		case xml.EndElement:
			depth--
			if ws != nil {
				if err := ws.end(t.Name.Local); err != nil {
					return err
				}
			}
		case xml.CharData:
			if ws != nil && ws.inV {
				ws.v.Write(t)
			}
		}
	}
	return nil
}

// checkZip enforces the archive limits before excelize reads anything: entry count, declared sizes and the
// sizes the entries really decompress to (a header can lie), so a zip bomb is rejected without being expanded
// beyond MaxUnzipBytes. The XML parts the reader depends on are also token-checked while they are inflated.
func checkZip(data []byte, lim XLSXLimits) (*zipInfo, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: not a zip archive: %v", ErrXLSXBadArchive, err)
	}
	if len(zr.File) > lim.MaxZipEntries {
		return nil, fmt.Errorf("%w: %d archive entries (limit %d)", ErrXLSXBadArchive, len(zr.File), lim.MaxZipEntries)
	}
	info := &zipInfo{lastRow: map[string]int64{}, rels: map[string]string{}, maxSST: -1}
	var declared, actual int64
	for _, zf := range zr.File {
		if zf.UncompressedSize64 > uint64(lim.MaxUnzipBytes) {
			return nil, fmt.Errorf("%w: entry %q declares %d bytes (limit %d)", ErrXLSXUnzipLimit, truncate(zf.Name), zf.UncompressedSize64, lim.MaxUnzipBytes)
		}
		declared += int64(zf.UncompressedSize64)
		if declared > lim.MaxUnzipBytes {
			return nil, fmt.Errorf("%w: archive declares more than %d bytes", ErrXLSXUnzipLimit, lim.MaxUnzipBytes)
		}
	}
	corrupt := func(name string, err error) error {
		if errors.Is(err, zip.ErrFormat) || errors.Is(err, zip.ErrChecksum) || errors.Is(err, zip.ErrAlgorithm) || errors.Is(err, io.ErrUnexpectedEOF) {
			return fmt.Errorf("%w: entry %q: the archive is corrupt or its header contradicts its contents", ErrXLSXBadArchive, truncate(name))
		}
		return fmt.Errorf("%w: entry %q: %v", ErrXLSXBadArchive, truncate(name), err)
	}
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, corrupt(zf.Name, err)
		}
		cr := &countingReader{r: io.LimitReader(rc, int64(zf.UncompressedSize64)+1)}
		lower := strings.ToLower(strings.ReplaceAll(zf.Name, "\\", "/"))
		if isCheckedXML(lower) {
			if err := scanXML(cr, lower, info); err != nil {
				_ = rc.Close()
				if cr.n > int64(zf.UncompressedSize64) {
					return nil, fmt.Errorf("%w: entry %q is larger than the %d bytes its header declares: the archive is corrupt or its header contradicts its contents", ErrXLSXUnzipLimit, truncate(zf.Name), zf.UncompressedSize64)
				}
				var st *structErr
				if errors.As(err, &st) {
					return nil, fmt.Errorf("%w: part %q is inconsistent, rows or cells would be silently dropped or moved: %v", ErrXLSXBadArchive, truncate(zf.Name), err)
				}
				var se *xml.SyntaxError
				if errors.As(err, &se) {
					return nil, fmt.Errorf("%w: part %q is not well-formed XML (an illegal control character or an unclosed element; the rest of the sheet would be silently dropped): %v", ErrXLSXBadArchive, truncate(zf.Name), err)
				}
				return nil, corrupt(zf.Name, err)
			}
		}
		_, err = io.Copy(io.Discard, cr)
		_ = rc.Close()
		if err != nil {
			return nil, corrupt(zf.Name, err)
		}
		n := cr.n
		if n > int64(zf.UncompressedSize64) {
			return nil, fmt.Errorf("%w: entry %q is larger than the %d bytes its header declares: the archive is corrupt or its header contradicts its contents", ErrXLSXUnzipLimit, truncate(zf.Name), zf.UncompressedSize64)
		}
		actual += n
		if actual > lim.MaxUnzipBytes {
			return nil, fmt.Errorf("%w: archive expands beyond %d bytes", ErrXLSXUnzipLimit, lim.MaxUnzipBytes)
		}
	}
	if info.maxSST >= info.siCount {
		return nil, fmt.Errorf("%w: a cell uses shared string %d but xl/sharedStrings.xml has %d strings", ErrXLSXBadArchive, info.maxSST, info.siCount)
	}
	return info, nil
}

// plan resolves the sheet selection and the columns of every selected sheet.
func (s *xlsxStream) plan() error {
	x := s.cfg.XLSX
	all := s.f.GetSheetList()
	if len(all) > s.lim.MaxSheets {
		return fmt.Errorf("%w: workbook has %d sheets (limit %d)", ErrXLSXBadArchive, len(all), s.lim.MaxSheets)
	}
	has := map[string]bool{}
	for _, n := range all {
		has[n] = true
	}
	avail := func() string {
		parts := make([]string, len(all))
		for i, n := range all {
			parts[i] = fmt.Sprintf("%q", n)
			if v, err := s.f.GetSheetVisible(n); err == nil && !v {
				parts[i] += " (hidden)"
			}
		}
		return strings.Join(parts, ", ")
	}
	var names []string
	switch {
	case x.Sheet != "":
		if !has[x.Sheet] {
			return fmt.Errorf("xlsx.sheet: sheet %q not found; available sheets: %s", x.Sheet, avail())
		}
		names = []string{x.Sheet}
	case x.SheetIndex != nil:
		if *x.SheetIndex >= len(all) {
			return fmt.Errorf("xlsx.sheet_index: %d is out of range; available sheets (0-based): %s", *x.SheetIndex, avail())
		}
		names = []string{all[*x.SheetIndex]}
	case x.Sheets != nil && x.Sheets.Except != nil:
		skip := map[string]bool{}
		for _, n := range x.Sheets.Except {
			if !has[n] {
				return fmt.Errorf("xlsx.sheets.except: sheet %q not found; available sheets: %s", n, avail())
			}
			skip[n] = true
		}
		for _, n := range all {
			if !skip[n] {
				names = append(names, n)
			}
		}
		if len(names) == 0 {
			return fmt.Errorf("xlsx.sheets.except: every sheet is excluded; available sheets: %s", avail())
		}
	case x.Sheets != nil:
		seen := map[string]bool{}
		for _, n := range x.Sheets.Names {
			if !has[n] {
				return fmt.Errorf("xlsx.sheets: sheet %q not found; available sheets: %s", n, avail())
			}
			if seen[n] {
				return fmt.Errorf("xlsx.sheets: sheet %q is listed twice", n)
			}
			seen[n] = true
			names = append(names, n)
		}
	default:
		return errors.New("xlsx: no sheet selected")
	}
	for _, n := range names {
		cols, err := s.columnsFor(n)
		if err != nil {
			return err
		}
		s.plans = append(s.plans, sheetPlan{name: n, cols: cols})
	}
	return nil
}

// columnsFor returns the columns of one sheet: the configured letters, or the header row.
func (s *xlsxStream) columnsFor(sheet string) ([]config.Col, error) {
	x := s.cfg.XLSX
	if len(x.Columns) > 0 {
		return x.ColumnList(), nil
	}
	rows, err := s.f.Rows(sheet)
	if err != nil {
		return nil, fmt.Errorf("sheet %q: %w", sheet, err)
	}
	defer func() { _ = rows.Close() }()
	want := x.HeaderRowNumber()
	var cells []string
	for n := 1; rows.Next(); n++ {
		if int64(n) > s.lim.MaxRows {
			return nil, fmt.Errorf("sheet %q: %w (limit %d)", sheet, ErrXLSXTooManyRows, s.lim.MaxRows)
		}
		if n < want {
			continue
		}
		if cells, err = rows.Columns(excelize.Options{RawCellValue: true}); err != nil {
			return nil, fmt.Errorf("sheet %q header row %d: %w", sheet, want, err)
		}
		break
	}
	if err := rows.Error(); err != nil {
		return nil, fmt.Errorf("sheet %q: %w", sheet, err)
	}
	var cols []config.Col
	seen := map[string]int{}
	for i, c := range cells {
		name := strings.TrimSpace(c)
		if name == "" {
			continue // a column without a header is not read
		}
		if strings.HasPrefix(name, "_") {
			return nil, fmt.Errorf("sheet %q header row %d: column %d name %q starts with an underscore", sheet, want, i+1, truncate(name))
		}
		if j, dup := seen[name]; dup {
			return nil, fmt.Errorf("sheet %q header row %d: duplicate column name %q (columns %d and %d)", sheet, want, truncate(name), j, i+1)
		}
		seen[name] = i + 1
		cols = append(cols, config.Col{Index: i + 1, Letter: colLetters(i + 1), Name: name})
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("sheet %q: no header names found in row %d", sheet, want)
	}
	// Validate the pipeline and natural key against what the header gave us, with the same rules as config.
	c2 := *s.cfg
	x2 := *x
	f := false
	x2.Header, x2.HeaderRow, x2.StartRow = &f, 0, x.FirstRow()
	x2.Columns = map[string]string{}
	for _, c := range cols {
		x2.Columns[c.Letter] = c.Name
	}
	c2.XLSX = &x2
	if err := c2.Validate(); err != nil {
		return nil, fmt.Errorf("sheet %q: %w", sheet, err)
	}
	return cols, nil
}

func colLetters(n int) string {
	var b []byte
	for ; n > 0; n = (n - 1) / 26 {
		b = append([]byte{byte('A' + (n-1)%26)}, b...)
	}
	return string(b)
}

// Meta implements Stream (xlsx sources have no preamble metadata).
func (s *xlsxStream) Meta() map[string]string { return nil }

// Err implements Stream.
func (s *xlsxStream) Err() error { return s.err }

// SHA256 implements Hasher (empty when the workbook was not fetched).
func (s *xlsxStream) SHA256() string { return s.sum }

// Close implements Stream; it removes any temporary files excelize made.
func (s *xlsxStream) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	var err error
	if s.rows != nil {
		err = s.rows.Close()
		s.rows = nil
	}
	if s.f != nil {
		err = errors.Join(err, s.f.Close())
	}
	return err
}

func (s *xlsxStream) fail(err error) (Item, bool) {
	s.err = err
	s.done = true
	if s.rows != nil {
		_ = s.rows.Close()
		s.rows = nil
	}
	return Item{}, false
}

// Next implements Stream.
func (s *xlsxStream) Next() (it Item, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			it, ok = s.fail(fmt.Errorf("%w: internal error: %v", ErrXLSXBadArchive, r))
		}
	}()
	for {
		if len(s.queue) > 0 {
			it, s.queue = s.queue[0], s.queue[1:]
			return it, true
		}
		if s.done {
			return Item{}, false
		}
		if err := s.ctx.Err(); err != nil {
			return s.fail(err)
		}
		if s.rows == nil {
			if s.cur >= len(s.plans) {
				s.done = true
				return Item{}, false
			}
			if err := s.openSheet(); err != nil {
				return s.fail(err)
			}
		}
		pl := s.plans[s.cur]
		x := s.cfg.XLSX
		if !s.rows.Next() {
			if err := s.rows.Error(); err != nil {
				return s.fail(fmt.Errorf("sheet %q: %w", pl.name, err))
			}
			// Second line of defence: the archive pass saw the last <row> of this sheet; ending before it
			// means the XML reader stopped early, which must never look like a clean end.
			if last, ok := s.info.lastRowOf(pl.name); ok && s.rowNum < last {
				return s.fail(fmt.Errorf("%w: sheet %q ended at row %d but the worksheet has rows up to %d (unreadable content)", ErrXLSXBadArchive, pl.name, s.rowNum, last))
			}
			s.endSheet()
			continue
		}
		s.rowNum++
		s.total++
		if s.total > s.lim.MaxRows {
			return s.fail(fmt.Errorf("%w (limit %d rows)", ErrXLSXTooManyRows, s.lim.MaxRows))
		}
		if s.rowNum < int64(x.FirstRow()) {
			continue
		}
		if x.EndRow > 0 && s.rowNum > int64(x.EndRow) {
			s.endSheet()
			continue
		}
		cells, err := s.rows.Columns(excelize.Options{RawCellValue: true})
		if err != nil {
			return s.fail(fmt.Errorf("sheet %q row %d: %w", pl.name, s.rowNum, err))
		}
		size := 0
		for _, c := range cells {
			size += len(c)
		}
		if size > maxXLSXRowBytes {
			return s.fail(fmt.Errorf("sheet %q row %d: %w", pl.name, s.rowNum, ErrXLSXRowTooLarge))
		}
		s.handleRow(pl, cells)
	}
}

func (s *xlsxStream) endSheet() {
	if s.rows != nil {
		_ = s.rows.Close()
		s.rows = nil
	}
	s.cur++
}

func (s *xlsxStream) openSheet() error {
	pl := s.plans[s.cur]
	rows, err := s.f.Rows(pl.name)
	if err != nil {
		return fmt.Errorf("sheet %q: %w", pl.name, err)
	}
	pipe, err := NewPipeline(s.cfg.Table)
	if err != nil {
		_ = rows.Close()
		return err
	}
	s.rows, s.pipe, s.rowNum = rows, pipe, 0
	s.base = map[string]string{}
	for k, v := range s.cfg.Context {
		s.base[k] = v
	}
	s.base[config.SheetContextName] = pl.name
	return nil
}

// handleRow turns one worksheet row into queued items.
func (s *xlsxStream) handleRow(pl sheetPlan, cells []string) {
	fields := make(map[string]any, len(pl.cols))
	blank := true
	for _, c := range pl.cols {
		v := ""
		if c.Index-1 < len(cells) {
			v = cells[c.Index-1]
		}
		if strings.TrimSpace(v) != "" {
			blank = false
		}
		fields[c.Name] = v
	}
	if blank {
		return // spacer row: not data, not an error
	}
	raw := func() string {
		var b strings.Builder
		fmt.Fprintf(&b, "%s!%d:", pl.name, s.rowNum)
		for _, c := range pl.cols {
			if c.Index-1 < len(cells) {
				b.WriteString(" | ")
				b.WriteString(cells[c.Index-1])
			}
			if b.Len() > 400 {
				break
			}
		}
		return truncate(b.String())
	}
	out, err := s.pipe.Run(pipeRow{fields: fields, ctx: s.base})
	if err != nil {
		s.queue = append(s.queue, Item{Error: &RowError{Position: s.rowNum, Raw: raw(), Reason: err.Error()}})
		return
	}
	for _, r := range out {
		parts := make([]string, len(s.cfg.NaturalKey))
		allEmpty := true
		for i, k := range s.cfg.NaturalKey {
			var v any
			if name, ok := strings.CutPrefix(k, config.CtxPrefix); ok {
				if cv, ok := r.ctx[name]; ok {
					v = cv
				}
			} else if fv, ok := r.fields[k]; ok {
				v = fv
			} else if cv, ok := r.ctx[k]; ok {
				v = cv
			}
			t, _ := textOf(v)
			if strings.TrimSpace(t) != "" {
				allEmpty = false
			}
			parts[i] = t
		}
		if allEmpty {
			s.queue = append(s.queue, Item{Error: &RowError{Position: s.rowNum, Raw: raw(), Reason: "natural key is empty (every natural key field is empty)"}})
			continue
		}
		ctxMap := make(map[string]any, len(r.ctx))
		for k, v := range r.ctx {
			ctxMap[k] = v
		}
		f := r.fields
		f["_ctx"] = ctxMap
		f["_source"] = map[string]any{"sheet": pl.name, "row": s.rowNum}
		s.queue = append(s.queue, Item{Record: &Record{Position: s.rowNum, Fields: f, NaturalKey: record.Key(parts...)}})
	}
}
