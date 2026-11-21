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
- **17 early events have no day** (month precision only, e.g. `Oct 1694`; blank in column B). They must map to
  `period_type: month`, not fail. The mapping supports this with a list of layouts (`2 Jan 2006`, then `Jan 2006`).
- Values are floats or ints (e.g. `13.8438`, `4`).
- One file yields **several series**, unlike ONS where one file is one series.

## Consequences for the design
1. A new **`xlsx` adapter** (sheet by name, hidden sheets allowed, cached formula values, column letters).
2. A **row pipeline** for structural clean-up that needs row order and so must run at import:
   section tracking, fill-down, dropping non-data rows ([ADR-0004](../adr/0004-structural-parsing-at-import.md)).
3. **Change detection** on the file (ETag/Last-Modified/hash) so a 1 MB download is parsed only when it changed.
4. The same canonical time-series observation fits: `period_type = day` (or `month` for the 17 day-less events), `period_start = period_end =` the
   effective date, `series_id` from the section. Because values are change events, "rate on date D" is the latest
   event on or before D (see `GetValueAsOf` in [api](../api.md)).
5. Cadence: the file changes only on rate decisions, so a cheap `daily` schedule with change detection is
   better than a fixed monthly date.

## Verified by tests (task 013)

Fixture `testdata/boe/baserate-trimmed.xlsx`, sheet `HISTORICAL SINCE 1694`, read with
`docs/examples/boe-baserate.source.yaml` (location overridden). These numbers are asserted by tests, not estimates:

- `TestXLSXBoEGolden` (`internal/source`): **854** records, **0** row errors; five distinct `_ctx.series_name` values with
  counts Bank Rate 573, Min.Band 1 Dealing Rate 127, Min Lending Rate 82, Official Bank Rate 40, Repo Rate 32
  (573 + 127 + 82 + 40 + 32 = 854); first record is `Bank Rate`, year 1694, month `oct`, day empty, rate 6; the
  `{year: 1695, month: may}` record has its year filled down; `Feb ` is normalised to `feb`; **17** records have an empty
  day; the `1933-1938 no changes` rows are absent; all natural keys are unique.
- `TestXLSXASHEGolden` (`internal/source`, ASHE fixture `testdata/ashe/t20-7a-trimmed.xlsx`): sheets `All` and
  `Male Full-Time` give 217 rows each (434 in total); the footers and six age-only duplicate rows are dropped.
- `TestBoEBaseRateEndToEnd` (`internal/importer`, real Postgres): `importer.Run` inserts 854 records with 0 row errors.
  Evaluating all 854 stored payloads with `docs/examples/boe-baserate.mapping.yaml` gives zero mapping errors: 17 events
  are `period_type: month` (1694 Oct is 1694-10-01 to 1694-10-31) and 837 are `day`; `series_id` takes five BOE_* values;
  a re-run reports `unchanged=854`.
- `TestCLIXLSXSource` (`cmd/importer`): `importer sources apply` then `importer run` for `kind: xlsx` gives `inserted=854`,
  then `unchanged=854`.

Config: [`../examples/boe-baserate.source.yaml`](../examples/boe-baserate.source.yaml) and
[`../examples/boe-baserate.mapping.yaml`](../examples/boe-baserate.mapping.yaml).
