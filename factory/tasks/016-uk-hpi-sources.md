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
validated
## Log
- Worker round 1. CSV row pipeline: `table:` and `context:` are now accepted on `kind: csv` (still rejected for other kinds; strict keys/steps and field-path errors unchanged). One pipeline implementation: `internal/source/pipeline.go` (`Pipeline`, steps, and the new shared `keyValue` natural-key lookup, now also used by xlsx). `csvStream` builds one `Pipeline` for the file (state spans the file; csv has a single "sheet"), queues the 0..n output rows of an input row (`queue`), and sets `_ctx`, `_meta`, `_source: {line}` on them. Config: `validateCSVTable` (internal/config/source.go) reuses `Table.CheckTable` and the new shared `validateContext` (internal/config/xlsx.go); columns are known statically from `csv.columns`, otherwise `newCSVStreamP` re-validates the config against the header at open (as xlsx does per sheet). Natural key with a table: field after the pipeline, section/context variable, `@ctx.<name>` or `@meta.<key>`.
- Decision: csv natural-key policy is unchanged (any empty part is a RowError, unlike xlsx where only all-empty is); `context.sheet` stays reserved for csv too (same validator).
- Decision: `Date` stays an ISO text cell through the pipeline (no normalize step); `type: period` with `format: "2006-01-02"`, `precision: month` parses it and widens to the month, so no mapping/Eval change was needed. No mapping or example bug found; the example sources/mappings were not changed.
- Filters on `dimensions.<key>` (EQ, NEQ, IN) and `measure`/`statistic` were already implemented by task 011 (internal/server/filter.go); exercised by the e2e (including IN).
- Tests: internal/config/csvtable_test.go; internal/source/csv_table_test.go (each step on CSV, no-table csv unchanged, unpivot counts 3402 = 2487+915 and 4152 = 915/915/1026/1296 with unique natural keys, resolve test via httptest TLS with clock 2026-11-20: HEAD 2026-10 404, HEAD 2026-09 200, GET 2026-09); internal/importer/hpi_test.go (real Postgres, file:// location, Run twice: inserted 3402/4152, unchanged 3402/4152, zero mapping errors over every payload, 702 UK price rows, no duplicate series/period); internal/e2e `TestUKHPIEndToEnd` (real binary: mapping validate, sources apply, run, serve, gRPC counts incl. 2,487/915/702, 2026-09 UK record). e2e refactored: `startServe`/`listSource` helpers shared with the ONS test.
- Note: in the binary e2e the second run is `status=unchanged_file` (the examples set `change_detection` with `content_hash`, so an identical file ends the run before parsing); the row-level `unchanged=3402` is asserted in the importer test (no file store). Docs not updated by the worker: docs/mapping-spec.md still says table/context on csv are rejected (lines 235-236).
- 2026-11-22 validator r1: PASS first time. Fixture counts re-derived with Python (3,402 / 4,152), real binary + gRPC (3,402 and 4,152 records, zero mapping errors, ids unique, second run `unchanged_file`), a REAL-DATA run on the actual HM Land Registry files (Average-prices-2026-09.csv: 158,109 observations in 10.9 s at 28.9 MB peak RSS; Property-Type: 583,776 in 39.1 s; paging all through ListRecords 7.5 s and 31.5 s; failed=0, no duplicates). One shared pipeline implementation for csv and xlsx (mutation on the unpivot map form fails three tests).
- documentor and runbook-refiner (budgets, evidence packs; the runbook-refiner also ran the real Average-prices file: 158,109 rows in 12.0 s) ran in parallel (no database for the documentor); deterministic docs check PASS first time.
- Known inconsistency (documented): CSV treats ANY empty natural-key part as a RowError, XLSX only when ALL parts are empty.
