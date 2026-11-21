package source

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/willis-dev-md/data-importer/internal/config"
)

// sheetSpec describes one worksheet of a hand-built workbook. Cells are "s:text" (inline string),
// "n:12" (number), "b:1" (boolean), "e:#N/A" (error), "f:formula|cached" (formula with a cached value; an empty
// cache means none was saved) or "" (no cell).
type sheetSpec struct {
	name   string
	hidden bool
	rows   [][]string
	merges []string
}

func colName(i int) string { return colLetters(i + 1) }

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func cellXML(ref, spec string) string {
	kind, val, _ := strings.Cut(spec, ":")
	switch kind {
	case "s":
		return fmt.Sprintf(`<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, esc(val))
	case "n":
		return fmt.Sprintf(`<c r="%s"><v>%s</v></c>`, ref, esc(val))
	case "b":
		return fmt.Sprintf(`<c r="%s" t="b"><v>%s</v></c>`, ref, esc(val))
	case "e":
		return fmt.Sprintf(`<c r="%s" t="e"><v>%s</v></c>`, ref, esc(val))
	case "f":
		f, cached, _ := strings.Cut(val, "|")
		if cached == "" {
			return fmt.Sprintf(`<c r="%s"><f>%s</f></c>`, ref, esc(f))
		}
		return fmt.Sprintf(`<c r="%s"><f>%s</f><v>%s</v></c>`, ref, esc(f), esc(cached))
	}
	panic("bad cell spec " + spec)
}

func buildXLSX(t testing.TB, sheets ...sheetSpec) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	var ct, wb, rels strings.Builder
	for i, sh := range sheets {
		n := i + 1
		fmt.Fprintf(&ct, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, n)
		state := ""
		if sh.hidden {
			state = ` state="hidden"`
		}
		fmt.Fprintf(&wb, `<sheet name="%s" sheetId="%d"%s r:id="rId%d"/>`, esc(sh.name), n, state, n)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, n, n)
		var sd strings.Builder
		for r, row := range sh.rows {
			var cells strings.Builder
			for c, spec := range row {
				if spec != "" {
					cells.WriteString(cellXML(colName(c)+fmt.Sprint(r+1), spec))
				}
			}
			if cells.Len() > 0 {
				fmt.Fprintf(&sd, `<row r="%d">%s</row>`, r+1, cells.String())
			}
		}
		merge := ""
		if len(sh.merges) > 0 {
			merge = fmt.Sprintf(`<mergeCells count="%d">`, len(sh.merges))
			for _, m := range sh.merges {
				merge += fmt.Sprintf(`<mergeCell ref="%s"/>`, m)
			}
			merge += `</mergeCells>`
		}
		add(fmt.Sprintf("xl/worksheets/sheet%d.xml", n), `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`+sd.String()+`</sheetData>`+merge+`</worksheet>`)
	}
	add("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`+ct.String()+`</Types>`)
	add("_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`)
	add("xl/workbook.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`+wb.String()+`</sheets></workbook>`)
	add("xl/_rels/workbook.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+rels.String()+`</Relationships>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// xcfg parses a source config from YAML (location is irrelevant for NewXLSXReader).
func xcfg(t testing.TB, y string) *config.Source {
	t.Helper()
	cfg, err := config.ParseSource([]byte("id: t\nkind: xlsx\nlocation: unused.xlsx\n" + y))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return cfg
}

type xres struct {
	recs []*Record
	errs []*RowError
	err  error
}

func drainX(t testing.TB, st Stream) xres {
	t.Helper()
	var g xres
	for {
		it, ok := st.Next()
		if !ok {
			break
		}
		switch {
		case it.Record != nil && it.Error == nil:
			g.recs = append(g.recs, it.Record)
		case it.Error != nil && it.Record == nil:
			g.errs = append(g.errs, it.Error)
		default:
			t.Fatalf("item with both or neither of record and error: %+v", it)
		}
	}
	g.err = st.Err()
	if err := st.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	return g
}

func readXLSX(t testing.TB, data []byte, y string) xres {
	t.Helper()
	st, err := NewXLSXReader(context.Background(), data, xcfg(t, y), XLSXLimits{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return drainX(t, st)
}

func fixtureXLSX(t testing.TB, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func ctxOf(r *Record) map[string]any { return r.Fields["_ctx"].(map[string]any) }
