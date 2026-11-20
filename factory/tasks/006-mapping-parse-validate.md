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
- [ ] `canonical-schema.yaml` declares the time-series observation: `series_id` (string, required), `series_title` (string), `period_type` (enum year|quarter|month|day, required), `period_start` (date, required), `period_end` (date, required), `value` (decimal, required), `unit` (string); loaded by `mapping.LoadCanonical`.
- [ ] `mapping.Parse([]byte) (*Mapping, error)` with strict YAML (unknown keys rejected). Supports: `source`, `version`, `fields` (`from` as string, list, JSONPath `$.a.b`, `@meta.X`, `@ctx.X`; `const`; `type`; `format` as string or list; `transform` string or list; `map`; `default`; `required`; `validate`; `output`), `extra: keep|drop`.
- [ ] `mapping.Validate(m, canonical) []Issue` (each Issue has a field path, code, message, severity error|warning) covers: unknown canonical field; wrong/incompatible `type`; required canonical fields not covered; `from` and `const` together or neither; unknown transform or validator; `format` missing for `date`/`timestamp`; bad regex; `output` used on a non-`period` field; `output` not one of period_type/period_start/period_end; non-closed-set transform names.
- [ ] Table-driven tests for every rule (one valid and one invalid case each), plus: `docs/examples/ons-l55o.mapping.yaml` and `docs/examples/boe-baserate.mapping.yaml` validate with **zero errors**; `docs/examples/acme-csv.mapping.yaml` / `partner-api.mapping.yaml` are validated and any real errors are fixed in the example files.
- [ ] `importer mapping validate <file>` prints issues as `path: code: message`, exits 0 when no errors (warnings allowed), 1 on errors, 2 on unreadable file.
- [ ] `scripts/validate.sh` passes.

## Status
pending
## Log
