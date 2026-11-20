# 013 – XLSX adapter and row pipeline (Bank of England base rate)

## Goal
Excel workbooks (including hidden sheets, several sheets per source) can be read and cleaned by the declarative row pipeline, proven on the Bank of England base rate workbook.

## Depends on
005

## Design refs
`docs/mapping-spec.md` (Source: XLSX, Row pipeline, Static context), `docs/adr/0004-structural-parsing-at-import.md`, `docs/sources/boe-baserate.md`, `docs/examples/boe-baserate.source.yaml`

## Scope
`internal/config/` (xlsx + `table:` + `context`), `internal/source/` (xlsx adapter, pipeline steps), tests; add `github.com/xuri/excelize/v2`

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
- [ ] `scripts/validate.sh` passes.

## Status
pending
## Log
