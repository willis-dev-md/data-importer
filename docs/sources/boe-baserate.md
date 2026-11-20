# Source: Bank of England official Bank Rate (workbook)

    https://www.bankofengland.co.uk/-/media/boe/files/monetary-policy/baserate.xlsx

An Excel workbook (about 1.1 MB). The fixture `testdata/boe/baserate-trimmed.xlsx` keeps the two sheets we care
about (all of `HISTORICAL SINCE 1694`, the first and last rows of `Raw Data`); the full file was inspected on
2026-11-20.

## Sheets

| Sheet | State | Content | Use |
|---|---|---|---|
| `HISTORICAL SINCE 1694` | visible | 854 rate-**change events**, Oct 1694 to 5 Nov 2026, in 5 sections: Bank Rate, Min Lending Rate, Min Band 1 Dealing Rate, Repo Rate, Official Bank Rate | **Import this** (authoritative, sparse) |
| `Raw Data` | hidden | daily table 1973 to 2026-12-16, one column per series, forward-filled, includes future-dated rows | Do not import: derived, hidden, contains projections. Daily values can be derived on read from events |
| `BOEBASERATE` | visible | presentation layout: 5 side-by-side blocks | Ignore |
| `Cover`, `Chart`, `FAME Persistence` | mixed | cover, chart, hidden marker | Ignore |

## Quirks of `HISTORICAL SINCE 1694`
- Columns: A year, B day, C month (text), D rate. Header text spans rows 4 to 6; data starts row 8.
- The **year appears only on the first event of each year**: it must be filled down.
- Year is text (`'1694'`) in some rows and a number in others; the day is text in dozens of rows.
- Month labels are inconsistent: `Feb `, `may`, `Aug ` (whitespace/case).
- **Section headings** (`Min Lending Rate`, `Min.Band 1 Dealing Rate`, `Repo Rate`, `Official Bank Rate`) appear in
  column A mid-sheet and change which series the following rows belong to. The first section (`Bank Rate`) has its
  heading above the data start.
- Text rows such as `1933-1938 no changes`, and blank spacer rows, must be dropped.
- Values are floats or ints (e.g. `13.8438`, `4`).
- One file yields **several series**, unlike ONS where one file is one series.

## Consequences for the design
1. A new **`xlsx` adapter** (sheet by name, hidden sheets allowed, cached formula values, column letters).
2. A **row pipeline** for structural clean-up that needs row order and so must run at import:
   section tracking, fill-down, dropping non-data rows ([ADR-0004](../adr/0004-structural-parsing-at-import.md)).
3. **Change detection** on the file (ETag/Last-Modified/hash) so a 1 MB download is parsed only when it changed.
4. The same canonical time-series observation fits: `period_type = day`, `period_start = period_end =` the
   effective date, `series_id` from the section. Because values are change events, "rate on date D" is the latest
   event on or before D (see `GetValueAsOf` in [api](../api.md)).
5. Cadence: the file changes only on rate decisions, so a cheap `daily` schedule with change detection is
   better than a fixed monthly date.

Config: [`../examples/boe-baserate.source.yaml`](../examples/boe-baserate.source.yaml) and
[`../examples/boe-baserate.mapping.yaml`](../examples/boe-baserate.mapping.yaml).
