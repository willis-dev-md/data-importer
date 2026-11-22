# 016 – UK House Price Index sources

## Goal
UK HPI average prices and average prices by property type import and are served end to end from the fixtures, with configuration only.

## Depends on
015, 011

## Design refs
`docs/sources/uk-hpi.md`, `docs/examples/uk-hpi-*.yaml`, `docs/mapping-spec.md`

## Scope
`docs/examples/uk-hpi-*` (fixes only), `internal/e2e/` tests, `internal/config` + `internal/source` (the CSV row pipeline: see the added criterion), any bug fixes in the owning packages (noted in the log)

## Out of scope
The full-file source, other attribute files, scheduling behaviour (007's scheduler is a later milestone).

## Acceptance criteria (each must be checkable)
- [ ] Both example sources and mappings validate (`importer mapping validate`) and `sources apply` accepts them.
- [ ] E2E with `file://` fixtures (location overridden, no `resolve`) against a real Postgres: `uk-hpi-average-prices` inserts **3,402** records (2,487 price + 915 seasonally adjusted) with 0 row errors; a second run is `unchanged=3402`; `uk-hpi-property-type` inserts **4,152**.
- [ ] Via gRPC `ListRecords` (source `uk-hpi-average-prices`): `measure = average_price` returns **2,487**; filtering `dimensions.area_code = K02000001` (United Kingdom) and `measure = average_price` returns **702**; the record for `2026-09` has value **272642**, unit `GBP`, period_type `month`, `period_start 2026-09-01`, `period_end 2026-09-30`.
- [ ] Filter support for `dimensions.<key>` (equality and IN) and `measure`/`statistic` is implemented in the DataService (extend task 011's filter set; note in the log if 011 already covers it).
- [ ] Resolution is exercised separately: a test serves `Average-prices-2026-09.csv` from an `httptest` server with `resolve` (fake clock set to 2026-11-20) and the run uses `2026-09` (candidates tried: `2026-10` (404), `2026-09` (200)).
- [ ] (added by the orchestrator: gap found while planning) The CSV adapter supports the row pipeline: task 013 built `table:` and `context:` for XLSX and deliberately REJECTS them on `kind: csv`, but `docs/examples/uk-hpi-*.source.yaml` need `table: [unpivot ...]` on a CSV. Allow `table:` (and `context:`) on csv sources and run the SAME pipeline steps (skip_rows, drop_if_empty, section, fill_down, normalize, unpivot incl. the map form `columns: {Average_Price: average_price, ...}` with empty cells dropped, split, strip) on CSV rows; strict config validation stays (unknown steps/keys rejected, field paths); tests: each step on CSV, the unpivot of `testdata/hpi/average-prices-trimmed.csv` gives exactly **3,402** observations (2,487 price + 915 seasonally adjusted) and of `average-prices-property-type-trimmed.csv` **4,152** (detached 915, semi 915, terraced 1,026, flat 1,296); natural keys `[Area_Code, Date, measure_col]` / `[Area_Code, Date, property_type]` are unique (the task 018 duplicate-key rule must not fire); CSV sources without `table:` behave exactly as before (ONS 642 unchanged).
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/sources/uk-hpi.md (verified counts and behaviour); CHANGELOG.md

## Runbook impact
docs/runbooks/uk-hpi-monthly-update.md (what happens each month, how to check it, what to do if a month is missing)

## Status
in-progress
## Log
