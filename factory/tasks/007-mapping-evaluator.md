# 007 – Mapping evaluator

## Goal
Given a raw payload and a validated mapping, produce canonical `fields`, `extra` and per-field errors, deterministically and without panics.

## Depends on
006

## Design refs
`docs/mapping-spec.md` (transforms, period type, errors, field options), `docs/design.md` (Read flow), `docs/sources/ons-l55o.md`, `docs/sources/boe-baserate.md`

## Scope
`internal/mapping/` (evaluator, transforms, types, period parsing), tests

## Out of scope
DB access, gRPC, caching, filtering across records.

## Acceptance criteria (each must be checkable)
- [ ] `mapping.Eval(m, payload map[string]any) Result{Fields, Extra map[string]any, Errors []FieldError}`; `FieldError{Field, Code, Message}` with codes `MISSING_REQUIRED`, `TYPE_MISMATCH`, `PARSE_FAILED`, `VALIDATION_FAILED`. A failing field never aborts evaluation of the other fields.
- [ ] Input resolution: column name, list of names, JSONPath subset (`$.a.b`, `$.a[0]`), `@meta.X` (from `payload["_meta"]`), `@ctx.X` (from `payload["_ctx"]`), `const`, and `template` with `{col}`, `{@ctx.x}`, `{@meta.x}` placeholders (empty placeholder is a `MISSING_REQUIRED` error unless `empty_ok`).
- [ ] `Result.Fields` also carries `dimensions` (map[string]string, empty values omitted). `null_markers` turn a listed marker into a nil value with no error and write the marker to the field named by `flag`; a non-marker, non-numeric string is still `PARSE_FAILED`.
- [ ] Types: `string`, `int`, `decimal` (emitted as float64 in `Fields`, with an exact-string round trip kept out of scope: log the limitation), `bool`, `date`, `timestamp`, `period`.
- [ ] Transforms (closed set): `trim lower upper concat(sep) split(sep) regex(pat,repl) coalesce`; `map` (lookup), `default`, `required`, `validate` (`email`, `url`, `regex(pat)`, `range(min,max)`). Empty string counts as missing for `required`/`default`.
- [ ] Period type per `docs/mapping-spec.md`: built-in layouts `YYYY` (year: Jan 1 to Dec 31), `YYYY Qn` (quarter, end of quarter), `YYYY MON` (English abbreviations, case-insensitive; month start to month end, leap years correct) with `output` selecting `period_type`/`period_start`/`period_end`. With `format` as a list of Go layouts and several `from` inputs (non-empty joined with spaces): `["2 Jan 2006","Jan 2006"]` gives day precision when a day exists, otherwise month precision. Unmatched labels: `PARSE_FAILED`.
- [ ] Period `precision` forces month/year/etc. from a fuller layout (`2006-01-02` with `precision: month` gives 1st to last day of the month), and the `tax_year_ending` layout maps `2026` to `tax_year`, 2025-04-06 to 2026-04-05.
- [ ] `extra: keep` returns all payload keys not consumed by any `from`, excluding keys starting with `_`; `extra: drop` returns none.
- [ ] Golden tests: every one of the 642 ONS fixture rows (via a small CSV read in the test) maps with zero errors using `docs/examples/ons-l55o.mapping.yaml` plus a `_meta` from the preamble; spot checks (also `measure` is `cpih_annual_rate`): `"1989"` -> year, 1989-01-01 to 1989-12-31, value 5.7; `"1989 Q1"` -> quarter, 1989-01-01 to 1989-03-31; `"2026 OCT"` -> month, 2026-10-01 to 2026-10-31, value 3.7, unit `%`, series_id `L55O`.
- [ ] HPI and ASHE cases (hand-built post-pipeline payloads): `{Date:"2026-09-01",Region_Name:"United Kingdom",Area_Code:"K02000001",measure_col:"average_price",value:"272642"}` with `docs/examples/uk-hpi-average-prices.mapping.yaml` gives measure `average_price`, month, 2026-09-01..2026-09-30, value 272642, unit GBP, series_id `ukhpi:average_price:K02000001`, dimensions `{area_code:K02000001, area_name:United Kingdom, property_type:all}`; an ASHE row `{age_band:"18-21",soc_code:"1",soc_name:"Managers, directors and senior officials",statistic:"median",value:"x",_ctx:{sheet:"All",year:2026,status:"provisional"}}` gives value nil, `quality_flag` `x`, no errors, period_type `tax_year`, status `provisional`, unit GBP, dimensions with `population: All`; the `All employees` row (empty age_band/soc_code) gets `age_band: all`, `soc_code: total`; statistic `jobs` gets unit `thousand_jobs`.
- [ ] BoE cases (hand-built payloads, no xlsx reading): `{year:2025,day:6,month:"Feb ",rate:4.5,_ctx:{series_name:"Bank Rate"}}` -> day, 2025-02-06; `{year:1694,day:nil,month:"Oct",rate:6}` -> month, 1694-10-01..1694-10-31 (note year before 1900 works); `"may"` and `"Aug "` normalise; bad month -> `PARSE_FAILED`.
- [ ] A fuzz test shows `Eval` never panics for arbitrary JSON-like payloads; tests are `-race` clean.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/mapping-spec.md (transforms, period types, FieldError codes, known limitation: decimal as float64); CHANGELOG.md

## Runbook impact
docs/runbooks/mapping-validation-errors.md (add runtime FieldError codes MISSING_REQUIRED, TYPE_MISMATCH, PARSE_FAILED, VALIDATION_FAILED with fixes)

## Status
pending
## Log
