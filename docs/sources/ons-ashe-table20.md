# Source: ONS ASHE Table 20, age group by occupation (2-digit SOC)

Annual Survey of Hours and Earnings. Dataset page:
[Earnings and hours worked, age group by occupation by two-digit SOC: ASHE Table 20](https://www.ons.gov.uk/employmentandlabourmarket/peopleinwork/earningsandworkinghours/datasets/agegroupbyoccupation2digitsocashetable20)
(2026 provisional edition, released 22 October 2026). Inspected 2026-11-20; real zips re-checked for task 017 on 2026-11-22.
Fixtures: `testdata/ashe/t20-7a-trimmed.xlsx` (sheets `Notes`, `All`, `Male Full-Time`) and
`t20-7b-cv-trimmed.xlsx` (coefficients of variation, same layout).

"Socio-economic type" is interpreted as **occupation group (SOC)**: ASHE publishes pay by SOC, not by NS-SeC
(decision confirmed with the user; true NS-SeC earnings would need a different source or a bespoke ONS request).

## Sources in this repository (task 017)
Two standalone sources, one per edition, imported from the real ONS zips:

| Source id | Edition | Context | Period | Config |
|---|---|---|---|---|
| `ashe-t20-2026p` | provisional (zip `2026provisional`) | `year: 2026`, `status: provisional` | 2025-04-06 to 2026-04-05 | [source](../examples/ashe-t20-2026p.source.yaml), [mapping](../examples/ashe-t20-2026p.mapping.yaml) |
| `ashe-t20-2025r` | revised (zip `2025revised`) | `year: 2025`, `status: revised` | 2024-04-06 to 2025-04-05 | [source](../examples/ashe-t20-2025r.source.yaml), [mapping](../examples/ashe-t20-2025r.mapping.yaml) |

The group file [ashe-t20-annual-pay.group.yaml](../examples/ashe-t20-annual-pay.group.yaml) is a **future design**:
source groups and `{year}{status}` templating are not implemented ([source groups](../source-groups.md)). Each
new edition needs its own source file until then.

Load (real ONS zips, validator evidence for task 017, run through `importer run`; not re-run for this doc):
```
ashe-t20-2026p: total 5812 (All 650, Male 648, Female 650, Full-Time 650, Part-Time 647, Male Full-Time 646, Male Part-Time 643, Female Full-Time 646, Female Part-Time 632)
ashe-t20-2025r: total 5816 (All 650, Male 649, Female 649, Full-Time 650, Part-Time 649, Male Full-Time 647, Male Part-Time 643, Female Full-Time 644, Female Part-Time 635)
failed=0, import_errors=0, mapping_errors=0, duplicates=0; about 0.9 s, 35 MB peak RSS
```
The per-sheet counts are below 650 where blank cells were dropped by the unpivot (see the expected counts). A second run of
either source closes as `unchanged_file` (no parsing). The archive (zip) and its SHA-256 are stored in `source_files`.
The counts were checked against an independent Python parse of the same zips.

## Packaging
- One **zip per edition** (reference year and status), e.g. `…/datasets/agegroupbyoccupation2digitsocashetable20/2026provisional/ashetable202026provisional.zip` (4.4 MB). Editions: provisional (October of year Y) then
  revised (October of Y+1). The URL pattern `{year}{status}/ashetable20{year}{status}.zip` holds for 2021 onwards;
  older editions are irregular and need explicit locations.
  Implemented (task 015): `archive: {type: zip, member: ...}` reads the workbook from the zip, with an explicit `location` per edition
  (see [mapping-spec](../mapping-spec.md#archives)). Not implemented: the `{year}{status}` template (group templating, M7b), so each
  edition's source is written by hand until then.
- The zip holds **22 workbooks** named `Table 20.<n><a|b>   <measure> <year>.xlsx` (the 2026 provisional names start with `PROV - `):
  `a` = estimates, `b` = coefficients of variation (CV, a reliability measure). Measures: weekly pay (20.1 to 20.4),
  hourly pay (20.5, 20.6), **annual pay gross (20.7)**, annual incentive pay (20.8), paid hours (20.9 to 20.11).
- Real member names (task 017, checked on the zips):
  - 2026 provisional: `PROV - Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2026.xlsx`
  - 2025 revised: `Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2025.xlsx` (no `PROV - ` prefix)

  Names contain irregular spaces, so members are matched by the regex `(?i)Table 20\.7a .*\.xlsx$`, which matches
  exactly one member in each zip and not the `20.7b` CV workbook. The import fails if the match is not unique.
- We import **Table 20.7a** (annual gross pay). Weekly and hourly pay are the same layout: add as further sources.

## Workbook structure (`Table 20.7a`)
- **10 sheets**: `Notes` plus 9 population slices: `All`, `Male`, `Female`, `Full-Time`, `Part-Time`,
  `Male Full-Time`, `Male Part-Time`, `Female Full-Time`, `Female Part-Time`. The sheet name is a dimension
  (`population`) and the same pipeline applies to each. `Notes` is excluded.
- Header spans rows 3 to 5; data starts row 6. Columns: A description, B SOC code, C number of jobs (thousand),
  D median, E annual % change, F mean, G annual % change, H to Q percentiles 10, 20, 25, 30, 40, 60, 70, 75, 80, 90.
- Row kinds in `All` (223 data rows):
  - `All employees` (1; no age, no code);
  - age bands alone, `18-21` ... `60+` (6), **exact duplicates** of the `<age> ALL OCCUPATIONS` rows (6): drop one set;
  - 216 age x occupation rows = 6 age bands x (1 all-occupations + 9 one-digit + 26 two-digit SOC groups).
  The age band is **inside the description text** (`'  18-21   Corporate managers and directors'`, with irregular
  leading spaces), and the SOC code is text with a leading apostrophe (`'11`).
- **Suppression markers** in value cells: `x` (CV above 20%, unreliable, 327 cells in `All`), `..` (disclosive),
  `:` (not applicable), `-` (nil or negligible). They are meaningful, not errors: map to null plus `quality_flag`.
  In the 2026 provisional and 2025 revised workbooks, the cells seen in the real files are `x` and `:` only.
- Footer: footnotes a and b, a colour key, the CV legend and a source line, all in column A; also a stray
  `Not Classified` row.
- Merged cells in the header and footer; counts of jobs are "for indicative purposes only".
- Time: annual estimates cover **the tax year ended 5 April of the reference year** (so `2026` is 2025-04-06 to
  2026-04-05); hourly/weekly estimates refer to a pay period in April. The year is not in the sheet: it comes from the
  edition (`context`).

## Mapping
`measure = annual_pay_gross`, `statistic` = median / mean / jobs (percentiles optional), `unit` GBP or thousand_jobs,
`period_type = tax_year`, `status` = provisional / revised,
`dimensions = {population, age_band, soc_code, soc_name}`. Suppressed cells get `value = null` and
`quality_flag` (the marker, e.g. `x`) with no `mapping_errors`.

`series_id` = `ashe20.7a:<sheet>:<age_band>:<soc_code>:<statistic>`. It has **no source, year or status**, so the same
series from the two editions has the same `series_id`. Record ids are prefixed by the source. Empty SOC codes
(all occupations, `All employees`) stay empty in `series_id` (for example `ashe20.7a:All:22-29::median`); the
dimension defaults are `soc_code = total` and `age_band = all`.

## Expected counts on the fixtures (used by tests)
After dropping footers and the duplicate age-only rows: 217 rows per sheet. Numeric values for jobs + median + mean:
`All` **620** (206 + 205 + 209) and `Male Full-Time` **597** (194 + 197 + 206); marker cells are kept as flagged
nulls (`All`: 11 + 12 + 7 = 30; `Male Full-Time`: 49 markers). Rows emitted by the pipeline for the 3 statistics: `All` **650** (620 numeric + 30 flagged;
1 of the 651 cells is empty and dropped). `Male Full-Time` emits 646 (597 + 49). The fixture therefore gives
1,296 records per edition. The e2e test (`internal/e2e`, `TestASHETable20EndToEnd`) asserts these counts from the fixture.

## Decision: picking the value for a reference year (task 017)
- **Both editions are stored and neither supersedes the other.** Each edition is its own source, so a query on one
  source never returns the other's records, and the two editions cannot collide in `raw_records`.
- The importer does **not** choose a "best" value. A client that wants one value per reference year does this:
  1. filter `period_start = <Y-1>-04-06` (the same series and period in both editions);
  2. prefer the record with `status = revised` if it exists, otherwise `status = provisional`.
- Selection by source id (`ashe-t20-2025r` or `ashe-t20-2026p`) is also possible.
- This answers open question 9f for ASHE only; see [open questions](../open-questions.md).

## Open points
- Annual figures are for employees on adult rates who have been in the same job for more than a year (ASHE note):
  carry this as source documentation, not a field.
- Quality (CV) from the `b` files can be loaded as a second source with `statistic = median_cv` and joined by
  series id.
- Percentiles, the `b` workbooks and the other measures are out of scope for task 017.
