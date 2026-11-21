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

## Docs impact
docs/mapping-spec.md (validation rules as implemented); docs/design.md section 7 (canonical schema); docs/operations.md (`importer mapping validate`); CHANGELOG.md

## Runbook impact
docs/runbooks/mapping-validation-errors.md (each issue code: meaning and fix)

## Status
validated
## Log

### Worker round 1
- Added `canonical-schema.yaml` (v2), `internal/mapping/{types,parse,canonical,validate}.go`, tests, and `importer mapping validate [--schema file] <file>`.
- Decisions:
  - `--schema` defaults to `canonical-schema.yaml` in the working directory (the file is not embeddable from the repo root into `internal/`); an unreadable schema exits 2.
  - A parse error (strict YAML) is printed as `<file>: PARSE_ERROR: ...` and exits 1.
  - Issue codes: MISSING_SOURCE, BAD_EXTRA, UNKNOWN_FIELD, DIMENSIONS_AS_FIELD, REQUIRED_NOT_COVERED, UNKNOWN_TYPE, TYPE_MISMATCH, DIMENSION_TYPE,
    SOURCE_CONFLICT, SOURCE_MISSING, BAD_FROM, CONST_NOT_SCALAR, CONST_NOT_IN_ENUM, UNKNOWN_TRANSFORM, TRANSFORM_ARGS, UNKNOWN_VALIDATOR,
    VALIDATOR_ARGS, FORMAT_REQUIRED, BAD_REGEX, OUTPUT_NOT_PERIOD, BAD_OUTPUT, OUTPUT_MISMATCH, BAD_TEMPLATE, UNKNOWN_FLAG_FIELD,
    NULL_MARKERS_WITHOUT_FLAG (warning), BAD_PRECISION.
  - Extra rules beyond the spec list: `output` must equal the canonical field name (OUTPUT_MISMATCH); `type: period` only feeds period_type/start/end;
    an enum const must be one of the values; `from` syntax is checked (`$.a.b`, `@ctx.x`, `@meta.x`); transform/validator argument counts;
    an untyped `const` takes its type from the YAML scalar (so `const: 1` feeds a decimal).
  - Type compatibility: string<-string; enum<-string; int<-int; decimal<-decimal|int; bool<-bool; date<-date; timestamp<-timestamp|date. A `from` with no `type` is a string, so a decimal field needs `type: decimal`.
  - Transform/validator args: `name(a,b)`, args may be quoted with `"` or `'` (needed when a regex contains a comma).
  - Source-shape warnings ("referenced source fields exist in the shape") are not implemented (needs stored shapes).
- The five required examples validated with zero errors unchanged.
- `acme-csv.mapping.yaml` and `partner-api.mapping.yaml` were person-shaped (name/email/date), not valid against the observation schema
  (unknown fields, required fields not covered, `timestamp` without `format`). Rewritten as observation mappings (the acme one uses columns Joined/Fee/First Name/Last Name/Status/Email-free; the partner one uses `$.attributes.*`); the column names beyond the original ones are illustrative.
- 2026-11-21 validator r1: PASS (own table of invalid mappings per rule; 7 example mappings give 0 issues; CLI exit codes 0/1/2). documentor + runbook-refiner ran in parallel; deterministic docs check: PASS (3 verify blocks re-run).
- Decisions: `--schema` defaults to ./canonical-schema.yaml relative to the CURRENT directory (documented: run from the repo root or pass --schema); strict-YAML errors print PARSE_ERROR (exit 1); extra rules beyond the spec (output must equal the canonical field name, enum const check, `from` syntax, transform arity, untyped const infers its type); acme/partner example mappings rewritten as observation mappings (illustrative columns). Source-shape warnings (referenced source fields vs recent run shape) are NOT implemented yet.
