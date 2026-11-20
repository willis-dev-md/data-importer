# Source: ONS ASHE Table 20, age group by occupation (2-digit SOC)

Annual Survey of Hours and Earnings. Dataset page:
[Earnings and hours worked, age group by occupation by two-digit SOC: ASHE Table 20](https://www.ons.gov.uk/employmentandlabourmarket/peopleinwork/earningsandworkinghours/datasets/agegroupbyoccupation2digitsocashetable20)
(2026 provisional edition, released 22 October 2026). Inspected 2026-11-20.
Fixtures: `testdata/ashe/t20-7a-trimmed.xlsx` (sheets `Notes`, `All`, `Male Full-Time`) and
`t20-7b-cv-trimmed.xlsx` (coefficients of variation, same layout).

"Socio-economic type" is interpreted as **occupation group (SOC)**: ASHE publishes pay by SOC, not by NS-SeC
(decision confirmed with the user; true NS-SeC earnings would need a different source or a bespoke ONS request).

## Packaging
- One **zip per edition** (reference year and status), e.g. `…/datasets/agegroupbyoccupation2digitsocashetable20/2026provisional/ashetable202026provisional.zip` (4.4 MB). Editions: provisional (October of year Y) then
  revised (October of Y+1). The URL pattern `{year}{status}/ashetable20{year}{status}.zip` holds for 2021 onwards;
  older editions are irregular and need explicit locations.
- The zip holds **22 workbooks** named `PROV - Age by Occupation SOC20 (2) Table 20.<n><a|b>   <measure> 2026.xlsx`:
  `a` = estimates, `b` = coefficients of variation (CV, a reliability measure). Measures: weekly pay (20.1 to 20.4),
  hourly pay (20.5, 20.6), **annual pay gross (20.7)**, annual incentive pay (20.8), paid hours (20.9 to 20.11).
  File names contain irregular spaces and a `PROV - ` prefix, so members are matched by regex, not by name.
- We import **Table 20.7a** (annual gross pay). Weekly and hourly pay are the same layout: add as further sources.

## Workbook structure (`Table 20.7a`)
- **10 sheets**: `Notes` plus 9 population slices: `All`, `Male`, `Female`, `Full-Time`, `Part-Time`,
  `Male Full-Time`, `Male Part-Time`, `Female Full-Time`, `Female Part-Time`. The sheet name is a dimension
  (`population`) and the same pipeline applies to each.
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
`quality_flag`.
Config: [group](../examples/ashe-t20-annual-pay.group.yaml) and
[mapping](../examples/ashe-t20-annual-pay.mapping.yaml).

## Expected counts on the fixtures (used by tests)
After dropping footers and the duplicate age-only rows: 217 rows per sheet. Numeric values for jobs + median + mean:
`All` **620** (206 + 205 + 209) and `Male Full-Time` **597** (194 + 197 + 206); marker cells are kept as flagged
nulls (`All`: 11 + 12 + 7 = 30). Rows emitted by the pipeline for the 3 statistics: `All` **650** (620 numeric + 30 flagged;
1 of the 651 cells is empty and dropped).

## Open points
- **Supersession:** a revised edition replaces the provisional one for the same reference year. Both are stored (they
  are different sources); readers choose by `status`, and a "prefer revised" rule is an open question.
- Annual figures are for employees on adult rates who have been in the same job for more than a year (ASHE note):
  carry this as source documentation, not a field.
- Quality (CV) from the `b` files can be loaded as a second source with `statistic = median_cv` and joined by
  series id.
