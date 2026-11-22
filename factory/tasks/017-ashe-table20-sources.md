# 017 – ASHE Table 20 (age x occupation) annual pay as standalone sources

## Goal
ASHE annual gross pay by age band and SOC occupation imports from the real workbook layout (a workbook inside a zip) and is served with suppression markers preserved as flags, using standalone sources (source groups are NOT implemented: the orchestrator found that out while planning).

## Depends on
015, 016

## Design refs
`docs/sources/ons-ashe-table20.md`, `docs/examples/ashe-t20-annual-pay.group.yaml` and `.mapping.yaml` (the group file is a FUTURE design: use it as the template for the standalone files), `docs/mapping-spec.md` (Archives, Row pipeline, implemented behaviour for tasks 013 and 015), `docs/source-groups.md` (planned, not implemented)

## Scope
`docs/examples/ashe-t20-2026p.*` and `ashe-t20-2025r.*` (new standalone source + mapping files, each mapping's `source:` equal to its source id), `internal/e2e/` tests, bug fixes in the owning packages (noted in the log)

## Out of scope
Source groups/templating, shared mappings, other ASHE tables and measures, the `b` coefficient-of-variation workbooks, backfilling editions before 2021, percentiles.

## Acceptance criteria (each must be checkable)
- [ ] Two standalone examples exist and pass `importer mapping validate` and `importer sources apply`: `ashe-t20-2026p` (location `https://www.ons.gov.uk/file?uri=/employmentandlabourmarket/peopleinwork/earningsandworkinghours/datasets/agegroupbyoccupation2digitsocashetable20/2026provisional/ashetable202026provisional.zip`, `context: {year: 2026, status: provisional}`) and `ashe-t20-2025r` (`.../2025revised/ashetable202025revised.zip`, `context: {year: 2025, status: revised}`); kind xlsx, `archive` member regex for Table 20.7a, `sheets: {except: [Notes]}`, the pipeline from the group example (skip_rows, split, strip, normalize, unpivot of jobs/median/mean), natural key `["@ctx.sheet", "@ctx.year", "@ctx.status", age_band, soc_code, statistic]`; no `{placeholders}` remain; the group example file is kept but its header comment says it is a future design.
- [ ] E2E (no network): a zip built in the test from `testdata/ashe/t20-7a-trimmed.xlsx` using the real member name `PROV - Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2026.xlsx` is served over httptest TLS (location overridden); `ashe-t20-2026p` imports with 0 row errors and no duplicate-key errors; the archive (zip + SHA-256) is stored in source_files; a second run is `unchanged_file`.
- [ ] Record counts: for sheet `All`, **650** records (620 numeric + 30 marker cells), for `Male Full-Time` **597 + marker cells** (compute and assert the exact marker count from the fixture in the test, do not hard-code a guess); natural keys unique across both sheets.
- [ ] Via gRPC, all with `status = provisional`, `period_type = tax_year`, `period_start = 2025-04-06`, `period_end = 2026-04-05`, `dimensions.population = All`: age 22-29 + SOC `11` (Corporate managers and directors): median **35763.7**, mean **44541.8**, jobs **97** (`thousand_jobs`); age 30-39 + SOC `2` (Professional occupations): median **47066**, mean **52704.8**; age 22-29 all occupations (empty SOC code, `soc_code` dimension `total`): median **30737.9**, jobs **3438**; `All employees` (age_band `all`): median **34229.1** (GBP) and jobs **25156**. Units are `GBP` for median/mean.
- [ ] A suppressed cell (e.g. `18-21`, SOC `1`, median `x`) is returned with `value` absent/null, `quality_flag = x`, **no** `mapping_errors`.
- [ ] Filtering `statistic = median AND dimensions.age_band = 22-29` returns one record per SOC group (+ the all-occupations row) per population sheet imported; `dimensions.population` distinguishes `All` from `Male Full-Time`.
- [ ] Both editions coexist: the same trimmed workbook zipped under the 2025 revised name imports as source `ashe-t20-2025r`: its records have `status = revised`, `period_start = 2024-04-06`, `period_end = 2025-04-05`, and filtering `status` separates the two sources' records; nothing from one source appears under the other (the series_id includes sheet/age/soc/statistic, not the source: judge and document how a client picks "best" per year).
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/sources/ons-ashe-table20.md (verified counts and behaviour); CHANGELOG.md

## Runbook impact
docs/runbooks/ashe-annual-release.md (October release: new provisional and revised editions, adding a source for a new edition, checking the load)

## Status
in-progress
## Log
