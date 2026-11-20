# 006 – Canonical schema and mapping parser/validator

## Goal
Mappings can be loaded and validated against a canonical schema, with precise errors, from the CLI.

## Depends on
001 (independent of 002 to 005)

## Design refs
`docs/mapping-spec.md` (Mapping, field options, validation on save, period type), `docs/design.md` §7, `docs/adr/0003-declarative-mappings.md`

## Scope
`canonical-schema.yaml`, `internal/mapping/` (types, parse, validate), `cmd/importer` (`mapping validate`), tests

## Out of scope
Evaluating mappings against data (task 007); storing mappings in the DB.

## Acceptance criteria (each must be checkable)
- [ ] `canonical-schema.yaml` declares the observation (v2, see `docs/design.md` §7): `series_id` (string, required), `series_title`, `measure` (string, required), `statistic`, `dimensions` (map of strings), `period_type` (enum year|tax_year|quarter|month|day, required), `period_start` (date, required), `period_end` (date, required), `value` (decimal, **nullable**), `unit`, `status`, `quality_flag`; loaded by `mapping.LoadCanonical`.
- [ ] `mapping.Parse([]byte) (*Mapping, error)` with strict YAML (unknown keys rejected). Supports: `source`, `version`, `fields` (`from` as string, list, JSONPath `$.a.b`, `@meta.X`, `@ctx.X`; `const`; `template` (+`empty_ok`); `type`; `format` as string or list; `precision`; `transform` string or list; `map`; `default`; `required`; `validate`; `null_markers`; `flag`; `output`), a `dimensions:` block (same field specs, values are strings), `extra: keep|drop`.
- [ ] `mapping.Validate(m, canonical) []Issue` (each Issue has a field path, code, message, severity error|warning) covers: unknown canonical field; wrong/incompatible `type`; required canonical fields not covered; `from` and `const` together or neither; unknown transform or validator; `format` missing for `date`/`timestamp`; bad regex; `output` used on a non-`period` field; `output` not one of period_type/period_start/period_end; `from`, `const` and `template` used together (exactly one required); unknown placeholder syntax in `template`; `flag` naming a non-existent canonical field; `null_markers` without `flag` is allowed (warning only); `precision` not one of year|quarter|month|day; `dimensions` entries declaring a non-string `type`; non-closed-set transform names.
- [ ] Table-driven tests for every rule (one valid and one invalid case each), plus: `docs/examples/ons-l55o.mapping.yaml`, `boe-baserate.mapping.yaml`, `uk-hpi-average-prices.mapping.yaml`, `uk-hpi-property-type.mapping.yaml` and `ashe-t20-annual-pay.mapping.yaml` validate with **zero errors**; `docs/examples/acme-csv.mapping.yaml` / `partner-api.mapping.yaml` are validated and any real errors are fixed in the example files.
- [ ] `importer mapping validate <file>` prints issues as `path: code: message`, exits 0 when no errors (warnings allowed), 1 on errors, 2 on unreadable file.
- [ ] `scripts/validate.sh` passes.

## Status
pending
## Log
