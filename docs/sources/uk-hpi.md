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
    (see *Location resolution* in [mapping-spec](../mapping-spec.md)). Not verified against the real HPI URLs (`UNVERIFIED`).
    Not implemented: group templating, so the source is not configured here yet (task 016).
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

## Expected counts on the fixtures (used by tests)
- `average-prices-trimmed.csv`: 2,487 rows; observations after unpivot: **3,402** (2,487 price + 915 seasonally adjusted).
- `average-prices-property-type-trimmed.csv`: 1,296 rows; observations: **4,152** (detached 915, semi 915, terraced 1,026, flat 1,296).

## Schedule
Monthly. The dataset is published around the 18th to 20th; `change_detection` makes extra polls cheap. Use
`on_missed: run_once`.
