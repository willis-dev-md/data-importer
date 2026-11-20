# 016 – UK House Price Index sources

## Goal
UK HPI average prices and average prices by property type import and are served end to end from the fixtures, with configuration only.

## Depends on
015, 011

## Design refs
`docs/sources/uk-hpi.md`, `docs/examples/uk-hpi-*.yaml`, `docs/mapping-spec.md`

## Scope
`docs/examples/uk-hpi-*` (fixes only), `internal/e2e/` tests, any bug fixes in the owning packages (noted in the log)

## Out of scope
The full-file source, other attribute files, scheduling behaviour (007's scheduler is a later milestone).

## Acceptance criteria (each must be checkable)
- [ ] Both example sources and mappings validate (`importer mapping validate`) and `sources apply` accepts them.
- [ ] E2E with `file://` fixtures (location overridden, no `resolve`) against a real Postgres: `uk-hpi-average-prices` inserts **3,402** records (2,487 price + 915 seasonally adjusted) with 0 row errors; a second run is `unchanged=3402`; `uk-hpi-property-type` inserts **4,152**.
- [ ] Via gRPC `ListRecords` (source `uk-hpi-average-prices`): `measure = average_price` returns **2,487**; filtering `dimensions.area_code = K02000001` (United Kingdom) and `measure = average_price` returns **702**; the record for `2026-09` has value **272642**, unit `GBP`, period_type `month`, `period_start 2026-09-01`, `period_end 2026-09-30`.
- [ ] Filter support for `dimensions.<key>` (equality and IN) and `measure`/`statistic` is implemented in the DataService (extend task 011's filter set; note in the log if 011 already covers it).
- [ ] Resolution is exercised separately: a test serves `Average-prices-2026-09.csv` from an `httptest` server with `resolve` (fake clock set to 2026-11-20) and the run uses `2026-09` (candidates tried: `2026-10` (404), `2026-09` (200)).
- [ ] `scripts/validate.sh` passes.

## Status
pending
## Log
