# Source: HM Land Registry UK House Price Index (UK HPI)

Monthly aggregated house price statistics as CSV, published by HM Land Registry on GOV.UK:
[data downloads page](https://www.gov.uk/government/statistical-data-sets/uk-house-price-index-data-downloads-september-2026)
(September 2026 edition, published 18 November 2026). Files live on `publicdata.landregistry.gov.uk`.
Fixtures: `testdata/hpi/average-prices-trimmed.csv` and `average-prices-property-type-trimmed.csv`
(4 areas: United Kingdom, England, Scotland, City of London; all months). Inspected 2026-11-20.

## Files (all CSV, one new file per month)

| File | Content | Used |
|---|---|---|
| `Average-prices-YYYY-MM.csv` | average price, monthly/annual change, seasonally adjusted price | **yes** |
| `Average-prices-Property-Type-YYYY-MM.csv` | average price and index by detached / semi / terraced / flat | **yes** |
| `UK-HPI-full-file-YYYY-MM.csv` | all attributes, 54 columns | no (different date format and column names) |
| `Indices`, `Sales`, `Cash-mortgage-sales`, `First-Time-Buyer-Former-Owner-Occupied`, `New-and-Old`, `Repossession`, `Revisions-*` | other attributes | later, one source each |

## Observed structure (`Average-prices-2026-09.csv`)
- 152,730 rows, 405 areas, 702 months (1968-04 to 2026-09). `(Date, Area_Code)` is unique.
- Columns: `Date` (ISO, always the 1st), `Region_Name`, `Area_Code`, `Average_Price`, `Monthly_Change`,
  `Annual_Change`, `Average_Price_SA` (empty for 147,351 rows; only some areas are seasonally adjusted).
- Already **long format**: one row per area and month, so no unpivot is needed to get observations; we unpivot only
  to separate the price and seasonally adjusted price into two series.
- Areas mix levels: countries (`E92`, `W92`, `S92`, `N92`), UK/GB (`K02`, `K03`), regions, counties, unitary
  authorities and local authorities (`E06 E07 E08 E09 E10 E11 E12 E13 W06 S12 N09`, `K04`).
- No preamble, no footnotes, clean UTF-8 CSV.
- Quirks to design around:
  - **The file name carries the data month** (`-2026-09`) and the file for month M appears about two months later,
    so a fixed URL does not work. Implemented (task 015): a standalone source can use `resolve: {ym: {kind: month, layout: "2006-01", ...}}`
    (see *Location resolution* in [mapping-spec](../mapping-spec.md)). The two examples are standalone sources (task 016), one per file pattern; group templating is not implemented, so they are not one group.
    The 2026-09 file was fetched from this URL pattern on 2026-11-22. How the real URLs behave month by month is `UNVERIFIED`.
  - The full file uses a **different date format** (`01/01/2004`, day first) and column names (`RegionName`,
    `AveragePrice`) from the attribute files (ISO date, `Region_Name`, `Average_Price`). We use the attribute files.
  - The download page lists the files as "34KB", "7.4KB" and so on, but the real files are 35 MB, 7.6 MB, etc.
    The page's units are wrong: do not size limits from it (default max size of 256 MB is fine).
  - Historical months are revised in later releases; upsert plus `content_hash` handles it (open question on history).
- Licence and attribution: the page links to republishing terms; I have not verified them. Record attribution per
  source (open question 9d).

## Mapping
Average prices become observations with `measure = average_price` (or `average_price_sa`), `statistic = mean`,
`unit = GBP`, `period_type = month`, `dimensions = {area_code, area_name, property_type: all}`. The property type
file uses `dimensions.property_type` = detached / semi_detached / terraced / flat.
Examples: [average prices](../examples/uk-hpi-average-prices.source.yaml) /
[mapping](../examples/uk-hpi-average-prices.mapping.yaml),
[property type](../examples/uk-hpi-property-type.source.yaml) /
[mapping](../examples/uk-hpi-property-type.mapping.yaml).

## Verified counts
Checked with the built `importer` binary and the tests named below (counts are from test and command output).

- Run 1 of `uk-hpi-average-prices` inserted 3,402 records with 0 row errors; `uk-hpi-property-type` inserted 4,152.
  Natural keys are unique in both fixtures (`TestHPIUnpivotCounts`, `internal/source/csv_table_test.go`).
- Run 2 of `uk-hpi-average-prices` is `status=unchanged_file`, not a row count: the examples list `content_hash` in
  `change_detection`, so an identical file ends the run before parsing. The row-level `unchanged=3402` (and `4152`) is
  asserted by `TestUKHPIEndToEnd` in `internal/importer/hpi_test.go`.
- `TestUKHPIEndToEnd` in `internal/e2e` runs the real binary (`mapping validate`, `sources apply`, `run`, `serve`, gRPC).
- gRPC `ListRecords` on `uk-hpi-average-prices`: total 3,402; `measure = average_price` 2,487; `measure = average_price_sa` 915;
  `dimensions.area_code = K02000001` (United Kingdom) with `measure = average_price`: 702.
- The 2026-09 UK record: value `272642`, unit `GBP`, period_type `month`, period_start `2026-09-01`, period_end `2026-09-30`,
  series_id `ukhpi:average_price:K02000001`, dimensions `area_code=K02000001`, `area_name=United Kingdom`, `property_type=all`.
- `uk-hpi-property-type`: 4,152 records, split 915 / 915 / 1,026 / 1,296 (detached / semi_detached / terraced / flat).
- Zero mapping errors over all records. Record ids and `(series_id, period_start)` are unique, so the duplicate natural key
  rule of task 018 does not fire.

Real files (not in the repo; the September 2026 files were fetched from `publicdata.landregistry.gov.uk` on 2026-11-22).
A one-off manual check, not a test. Counts are from Python on the file and from the importer:

| File | File rows | Observations | Import | Time, peak RSS |
|---|---|---|---|---|
| `Average-prices-2026-09.csv` | 152,730 | 158,109 (152,730 price + 5,379 SA) | inserted 158,109, failed 0 | 10.9 s, 28.9 MB |
| `Average-prices-Property-Type-2026-09.csv` | 146,202 | 583,776 (145,821 detached + 145,821 semi + 145,932 terraced + 146,202 flat) | inserted 583,776, failed 0 | 39.1 s, 28.8 MB |

- The average prices file has 405 areas, no empty natural-key parts, no duplicate (area, date) and only numeric prices.
  Zero mapping errors on either file; no duplicate natural keys; 702 UK price rows.
- Paging through all records over gRPC: 7.5 s for the average prices (page size 1000, serve RSS about 33 MB) and 31.5 s for
  the property type file. Single measurements, not a performance target.
- Not verified: the full file (`UK-HPI-full-file`, not imported by design), the other attribute files, how the real
  publication timing and URLs behave over time, and any gRPC paging speed target.

Fixtures (`testdata/hpi/`, `file://` location, `resolve` not used):
- `average-prices-trimmed.csv`: 2,487 rows; observations after unpivot: **3,402** (2,487 price + 915 seasonally adjusted).
- `average-prices-property-type-trimmed.csv`: 1,296 rows; observations: **4,152** (detached 915, semi 915, terraced 1,026, flat 1,296).

## Schedule
Monthly. The dataset is published around the 18th to 20th; `change_detection` makes extra polls cheap. Use
`on_missed: run_once`.

Resolver order: `resolve` tries its candidates newest first and the first URL that answers wins. The examples use
`start: -1m, back: 6`, so on 2026-11-22 the six candidates are 2026-10 down to 2026-05 (derived from the config; the printed
candidate list is `UNVERIFIED`). The publication lag is about two months: `2026-10` answers 404 until its file exists and
`2026-09` answers 200 (`TestHPIResolveMonth`, `httptest` over TLS with a fake clock set to 2026-11-20).

## Decisions (task 016)
- The CSV row pipeline is the same `Pipeline` as XLSX (`internal/source/pipeline.go`). The examples need `table:` on `kind: csv`.
- `Date` stays ISO text through the pipeline. `type: period` with `precision: month` turns it into a month, so the mapping
  evaluator did not change and the examples needed no fixes.
- Filters on `dimensions.<key>`, `measure` and `statistic` were already implemented by task 011 (`internal/server/filter.go`).
- The CSV natural-key rule is unchanged: any empty part is a row error. XLSX differs (see [mapping-spec](../mapping-spec.md)).
