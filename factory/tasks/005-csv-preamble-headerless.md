# 005 – CSV metadata preamble and headerless files (ONS)

## Goal
The real ONS CSV (`testdata/ons/l55o.csv`) can be read: metadata preamble captured, headerless data named, key built from metadata plus a column.

## Depends on
004

## Design refs
`docs/mapping-spec.md` ("CSV with a metadata preamble and no header row"), `docs/sources/ons-l55o.md`, `docs/examples/ons-l55o.source.yaml`

## Scope
`internal/config/`, `internal/source/` (CSV), tests

## Out of scope
Mapping, period parsing, DB, groups/templating.

## Acceptance criteria (each must be checkable)
- [ ] `csv.preamble: {lines: N, as: metadata}` and `{until: <regex>, as: metadata}` supported and validated (exactly one of `lines`/`until`); preamble lines are parsed as `key,value` pairs (value may be empty or missing, as in `"Important notes",`) and exposed as `Stream.Meta() map[string]string` (available after `Open`).
- [ ] `header: false` with `columns: [period, value]` names the data columns; wrong column counts are `RowError`s.
- [ ] `natural_key` entries may reference metadata as `@meta.<Key>`, mixed with column names; unknown `@meta` keys fail validation at open time with a clear message.
- [ ] Each emitted record's `Fields` includes the row fields plus `_meta` (map of metadata) and `_source` (`{line: n}`) so stored payloads are self-describing (`docs/adr/0004-structural-parsing-at-import.md`).
- [ ] Golden test on `testdata/ons/l55o.csv` with `docs/examples/ons-l55o.source.yaml` (location overridden to the fixture): exactly **642** records and **0** row errors; `Meta()` has the 8 keys with `CDID=L55O`, `Unit=%`, `Release date=18-11-2026`; first record key `["L55O","1989"]` and fields `{period:"1989", value:"5.7"}`; last record key `["L55O","2026 OCT"]`; every record key is unique.
- [ ] Preamble shorter than declared (`lines` larger than the file) is an error, not a panic or a silent empty import.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/mapping-spec.md (preamble / headerless / @meta key); docs/sources/ons-l55o.md (verified counts); CHANGELOG.md

## Runbook impact
none: library-level; the ONS source failure modes are documented in the task 012 smoke-test runbook

## Status
in-progress
## Log
