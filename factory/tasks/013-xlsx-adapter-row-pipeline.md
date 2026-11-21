# 013 – XLSX adapter and row pipeline (Bank of England base rate)

## Goal
Excel workbooks (including hidden sheets, several sheets per source) can be read and cleaned by the declarative row pipeline, proven on the Bank of England base rate workbook.

## Depends on
005

## Design refs
`docs/mapping-spec.md` (Source: XLSX, Row pipeline, Static context), `docs/adr/0004-structural-parsing-at-import.md`, `docs/sources/boe-baserate.md`, `docs/examples/boe-baserate.source.yaml`

## Scope
`internal/config/` (xlsx + `table:` + `context`), `internal/source/` (xlsx adapter, pipeline steps), `cmd/importer` (adapter registration line + test only), tests; add `github.com/xuri/excelize/v2`

## Out of scope
Archives (015), change detection/retention (014), the mapping engine (done in 006/007), ASHE specifics beyond what the BoE and generic steps need (017).

## Acceptance criteria (each must be checkable)
- [ ] xlsx config per spec: `sheet` / `sheet_index` / `sheets: [..]` / `sheets: {except: [..]}`, `start_row`, `end_row`, `header`, `columns` by letter, `use_cached_values`. Hidden sheets are readable; formulas are **never evaluated** (cached values only); unknown sheet gives an error naming available sheets. Each emitted record gets `_ctx.sheet` and `_source: {sheet,row}`.
- [ ] Pipeline steps implemented and unit-tested in isolation: `skip_rows`, `drop_if_empty`, `section` (with `default`), `fill_down` (with `reset_on: section`), `normalize` (`trim lower upper to_int to_string collapse_ws to_number`), `unpivot` (list or map columns, empty cells dropped), `split` (named groups, `optional`), `strip`. Steps apply in order; step errors are `RowError`s, never panics.
- [ ] `context:` values from the source config appear as `_ctx.<name>` on every record and are usable in `natural_key` as `@ctx.<name>`.
- [ ] Safety: workbook size, sheet/row counts and decompressed size are limited (default 100 MB file / 2M rows), exceeding them is a clear error; a crafted zip bomb test is rejected; no network access, macros or external links are followed.
- [ ] Golden test on `testdata/boe/baserate-trimmed.xlsx` with `docs/examples/boe-baserate.source.yaml` (location overridden): **854** records, 0 row errors; 5 distinct `_ctx.series_name` values; first record (series `Bank Rate`) has year `1694`, month `oct`, day empty, rate `6`; the record `{year:1695, month:"may"}` has year filled down to 1695; `Feb ` is normalised to `feb`; **17** records have an empty day; the `1933-1938 no changes` text rows are absent; all natural keys are unique.
- [ ] Mixed-type years (`'1694'` text and `2025` number) both normalise to integers.
- [ ] A second golden test reads `testdata/ashe/t20-7a-trimmed.xlsx` with the sheet/skip/split/strip pipeline from `docs/examples/ashe-t20-annual-pay.group.yaml` (no unpivot, no zip: location is the xlsx itself, archive ignored in this test): sheets `All` and `Male Full-Time` each give **217** rows; first row has empty `age_band`; the row for `18-21 ALL OCCUPATIONS` has `age_band=18-21`, `soc_name=ALL OCCUPATIONS`, empty `soc_code`; a two-digit SOC row has `soc_code` without the apostrophe.
- [ ] (added by the orchestrator, lesson from task 011) `kind: xlsx` is registered end to end: config validation accepts it (`sources apply` no longer says "supported: csv"), and the adapter registry used by `importer run` (cmd/importer registration line only, plus its test) includes it, so an xlsx source can be applied and run from the CLI.
- [ ] BoE end to end through the real pipeline: a test (real Postgres, importer.Run, `testdata/boe/baserate-trimmed.xlsx` with `docs/examples/boe-baserate.source.yaml`, location overridden) imports **854** records (0 row errors) and evaluating ALL 854 stored payloads with `docs/examples/boe-baserate.mapping.yaml` gives **zero** mapping errors, with the **17** day-less events mapped to `period_type: month` (e.g. year 1694 Oct: 1694-10-01..1694-10-31) and the others to `day`; series_id values are the five mapped BOE_* ids. Re-running the import reports `unchanged=854`.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/mapping-spec.md (XLSX and row pipeline as implemented); docs/sources/boe-baserate.md (verified counts); CHANGELOG.md

## Runbook impact
docs/runbooks/source-layout-changed.md (a publisher changed a workbook/CSV layout: how to spot it from row errors and run shape, and how to fix the config)

## Status
in-progress
## Log
