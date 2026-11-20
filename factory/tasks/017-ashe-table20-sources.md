# 017 – ASHE Table 20 (age x occupation) annual pay source group

## Goal
ASHE annual gross pay by age band and SOC occupation imports from the real workbook layout and is served with suppression markers preserved as flags.

## Depends on
015, 016

## Design refs
`docs/sources/ons-ashe-table20.md`, `docs/examples/ashe-t20-annual-pay.group.yaml`, `docs/examples/ashe-t20-annual-pay.mapping.yaml`, `docs/source-groups.md`

## Scope
`docs/examples/ashe-t20-*` (fixes only), group expansion support for `context`, `internal/e2e/` tests, bug fixes in the owning packages (noted in the log)

## Out of scope
Other ASHE tables and measures, the `b` coefficient-of-variation workbooks, backfilling editions before 2021, percentiles.

## Acceptance criteria (each must be checkable)
- [ ] Source group expansion yields members `ashe-t20-2026p` and `ashe-t20-2025r` with `location` placeholders filled (`2026provisional/ashetable202026provisional.zip`) and `context.year/status` set; unresolved placeholders are a validation error.
- [ ] E2E (no network): a zip built in the test from `testdata/ashe/t20-7a-trimmed.xlsx` using the real member name `PROV - Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2026.xlsx` is served over `httptest`; the group member `ashe-t20-2026p` imports with 0 row errors.
- [ ] Record counts: for sheet `All`, **650** records (620 numeric + 30 marker cells), for `Male Full-Time` **597 + marker cells** (compute and assert the exact marker count from the fixture in the test, do not hard-code a guess); natural keys unique across both sheets.
- [ ] Via gRPC: `dimensions.population = All`, `dimensions.age_band = 22-29`, `dimensions.soc_code = 11`... pick two real fixture rows and assert exact `value`, `unit`, `statistic`, `status = provisional`, `period_type = tax_year`, `period_start = 2025-04-06`, `period_end = 2026-04-05`; the `All employees` median is **34229.1** (GBP) and `jobs` is **25156** (`thousand_jobs`).
- [ ] A suppressed cell (e.g. `18-21`, SOC `1`, median `x`) is returned with `value` absent/null, `quality_flag = x`, **no** `mapping_errors`.
- [ ] Filtering `statistic = median AND dimensions.age_band = 22-29` returns one record per SOC group (+ the all-occupations row) per population sheet imported; `dimensions.population` distinguishes `All` from `Male Full-Time`.
- [ ] `scripts/validate.sh` passes.

## Status
pending
## Log
