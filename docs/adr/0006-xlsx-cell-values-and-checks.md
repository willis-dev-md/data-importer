# ADR-0006: XLSX cell values as stored text, and structural checks before parsing

Status: accepted (task 013; refines ADR-0004)

## Context
The Bank of England workbook needs an xlsx adapter with a row pipeline (ADR-0004). Two choices had to be made while
implementing it:
- how cell values are typed: excelize can give a formatted display value, a typed value per cell, or the raw stored value;
- how far a malformed workbook is trusted. excelize can stop reading a sheet early without an error (found in validation
  for a raw control character in sheet XML), and that would silently drop rows.

## Decision
1. **Cells are read as their stored text.** Excelize `Rows` with `RawCellValue` gives each cell as stored: text as text,
   a number as its saved literal (no display rounding), a boolean as `1`/`0`, an error as its literal, an empty or missing
   cell as `""`. Formulas are never evaluated; a formula cell gives its cached value. Typed values appear only through the
   `normalize` steps `to_int` and `to_number`.
2. **Excel date serials are not converted to dates.** The streaming API gives no per-cell type or style, and
   `GetCellType` loads whole sheets into memory, which the size limits reject. A date-formatted cell arrives as its serial
   number text (e.g. `45293`). The mapping `type: date` path must convert it when a source needs dates. This replaces the
   earlier sentence in [mapping-spec](../mapping-spec.md) that serials are returned as dates.
3. **Size limits are checked before excelize reads anything.** Defaults: 100 MiB file, 2,000,000 rows, 256 sheets, 1 GiB
   decompressed, 10,000 zip entries, 16 MiB per row. A declared size over the limit is rejected, and each zip entry is
   inflated against its declared size plus one byte, so a lying header is caught and a bomb is never expanded past the limit.
4. **Sheet XML is checked in a streaming pass before it is trusted.** `encoding/xml` walks the workbook parts while
   inflating (nothing buffered). Malformed XML, control characters and structurally inconsistent sheets (row and cell numbers
   not positive, not strictly increasing or not matching; bad references; invalid `s`/`t`; repeated attributes; bad nesting;
   shared-string index out of range) fail with `ErrXLSXBadArchive` naming the part and row. A stream that ends before the
   last row of its sheet is also an error.

## Consequences
- Adapter output is exact and typed only where a source asks for it; the mapping layer must convert dates and decimals.
- The streaming checks cost time (the BoE golden test stays around 0.03 s; the property test makes
  `go test -race ./internal/source` take about 19 s).
- Some damage cannot be seen from the XML structure alone. These are known and listed in the
  [roadmap](../roadmap.md) hardening backlog: a `<c>` outside `<sheetData>` is ignored, a cell relocated to another valid
  column or a changed value is not detected, and a `t` that does not match the content reads as empty.
- Dependencies: `github.com/xuri/excelize/v2` pinned to v2.10.0 (needs Go 1.24); `golang.org/x/text` moved from 0.28.0 to 0.30.0.
  Vulnerability scan not run (offline).
