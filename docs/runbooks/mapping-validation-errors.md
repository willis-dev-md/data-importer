# Runbook: mapping validation errors

**Owner role:** on-call engineer (data onboarding)  |  **Severity if this happens:** low (a mapping is rejected before it is used)
**Last verified:** 2026-11-21, ran the validate commands and snippets with `go run ./cmd/importer`, and the runtime-error examples via a throwaway `mapping.Eval` Go test in a scratch copy (task 007)

## When to use
`importer mapping validate` prints issues or exits non-zero for a mapping file (`docs/examples/*.mapping.yaml` or a new source's mapping).

## Prerequisites
- Go toolchain; run from the repo root. The canonical schema defaults to `canonical-schema.yaml` relative to the CURRENT directory, so
  from any other directory pass `--schema /path/to/canonical-schema.yaml`.
- No database or network needed.

## Steps
1. Build once (`make build` gives `bin/importer`) or use `go run ./cmd/importer` instead of `bin/importer` below.
2. Validate a file:

<!-- verify -->
```bash
go run ./cmd/importer mapping validate docs/examples/ons-l55o.mapping.yaml; echo "exit $?"
```
   *(expected: no issue lines, then `exit 0`)*
3. Read the output. Each issue is one line on stdout: `path: code: message` (path is e.g. `fields.unit.transform[0]`).
   There is no severity column; the only warning code is `NULL_MARKERS_WITHOUT_FLAG`, every other code is an error.
4. Read the exit code:

| Exit | Meaning |
|---|---|
| 0 | no errors (warnings may still be printed) |
| 1 | at least one error, or the file is not valid YAML for the mapping format (`PARSE_ERROR`) |
| 2 | usage problem or unreadable mapping file or schema (message on stderr, nothing on stdout) |

5. With both errors and warnings the lines are mixed in one list (sorted by field name, not by severity). Exit 1 means look for
   non-warning codes first; fix those, re-run, and only then decide about the warning. A warning-only run exits 0.
   Real output with one error and one warning:
   ```
   fields.nope: UNKNOWN_FIELD: "nope" is not a canonical field
   fields.value.null_markers: NULL_MARKERS_WITHOUT_FLAG: null_markers without flag: the matched marker is discarded
   ```
   (exit 1)
6. Fix, re-run until exit 0. Check all shipped examples still pass:

<!-- verify -->
```bash
for f in docs/examples/*.mapping.yaml; do go run ./cmd/importer mapping validate "$f" || exit 1; done; echo ok
```
   *(expected: `ok`)*

Try a snippet without creating a file (reads stdin; expect exit 1 and the issue lines):

<!-- verify -->
```bash
printf 'source: demo\nfields:\n  nope: { from: x }\n' | go run ./cmd/importer mapping validate /dev/stdin 2>&1 | grep UNKNOWN_FIELD
```
*(expected: `fields.nope: UNKNOWN_FIELD: "nope" is not a canonical field`)*

## Troubleshooting

Snippets below are lines added to (or removed from) an otherwise valid mapping. Each output line was produced by the real binary.

### Command-level problems (stderr, exit 2 unless stated)

| Symptom | Diagnosis | Fix |
|---|---|---|
| `importer mapping validate: canonical schema: open canonical-schema.yaml: no such file or directory` (exit 2) | You are not in the repo root | `cd` to the repo root or add `--schema /path/to/canonical-schema.yaml` |
| `importer mapping validate: open nofile.yaml: no such file or directory` (exit 2) | Mapping path wrong | Fix the path |
| `usage: importer mapping validate [--schema canonical-schema.yaml] <file>` (exit 2) | No file, more than one file, or `validate` missing | Pass exactly one file; flags go before it |
| `<file>: PARSE_ERROR: mapping: yaml: unmarshal errors:` then `  line 10: field colour not found in type mapping.Field` (exit 1, stdout) | Strict YAML: unknown key (here `colour: red` inside a field), or malformed YAML. The line number is in the file | Remove or correct the key. Allowed field keys: from, const, template, empty_ok, type, format, precision, transform, map, default, required, validate, null_markers, flag, output |
| `<file>: PARSE_ERROR: mapping: empty document` (exit 1) | File is empty or only comments | Write the mapping |

### Issue codes (all exit 1 except the warning)

| Code | Snippet that triggers it | Real output line | Meaning | Fix |
|---|---|---|---|---|
| MISSING_SOURCE | no `source:` key | `source: MISSING_SOURCE: source is required` | mapping has no source name | add `source: <source id>` |
| BAD_EXTRA | `extra: maybe` | `extra: BAD_EXTRA: extra must be keep or drop, got "maybe"` | `extra` only accepts keep or drop | use `extra: keep` or `extra: drop` |
| UNKNOWN_FIELD | `fields:` `nope: { from: x }` | `fields.nope: UNKNOWN_FIELD: "nope" is not a canonical field` | key under `fields` is not in `canonical-schema.yaml` | fix the spelling, or add the field to the canonical schema (design change) |
| DIMENSIONS_AS_FIELD | `fields:` `dimensions: { from: x }` | `fields.dimensions: DIMENSIONS_AS_FIELD: dimensions are declared in the top-level dimensions block` | dimensions are not mapped as a normal field | move to a top-level `dimensions:` block, one entry per dimension |
| REQUIRED_NOT_COVERED | drop the `measure` line | `fields.measure: REQUIRED_NOT_COVERED: required canonical field "measure" is not mapped` | a required canonical field has no mapping | add the field (`const`, `from` or `template`) |
| UNKNOWN_TYPE | `unit: { from: u, type: money }` | `fields.unit.type: UNKNOWN_TYPE: unknown type "money"` | type not one of string, int, decimal, bool, date, timestamp, period | use an allowed type |
| TYPE_MISMATCH | `value: { from: value, type: string }` | `fields.value.type: TYPE_MISMATCH: type string is not compatible with canonical decimal field "value"` | mapping type cannot feed that canonical type | change `type` to match the canonical field |
| TYPE_MISMATCH (period) | `unit: { from: u, type: period }` | `fields.unit.type: TYPE_MISMATCH: type period can only feed period_type, period_start or period_end, not "unit"` | `period` type only for the three period fields. Also raised when the target is the map field | use another type, or map to a period field |
| DIMENSION_TYPE | `dimensions:` `region: { from: r, type: int }` | `dimensions.region.type: DIMENSION_TYPE: dimension values are strings, got type "int"` | dimension values must be strings | drop `type` or use `string` |
| SOURCE_CONFLICT | `unit: { from: u, const: GBP }` | `fields.unit: SOURCE_CONFLICT: from, const and template are mutually exclusive (exactly one is allowed)` | more than one of from/const/template | keep one |
| SOURCE_MISSING | `unit: { type: string }` | `fields.unit: SOURCE_MISSING: exactly one of from, const or template is required` | no value source | add one of from/const/template |
| BAD_FROM | `unit: { from: "@foo.x" }` | `fields.unit.from[0]: BAD_FROM: "@foo.x" is not a field name, JSONPath ($.a.b), @meta.X or @ctx.X` | a `from` starting with `@` or `$` is malformed | use a plain column/key name, `$.a.b`, `@meta.X` or `@ctx.X` |
| CONST_NOT_SCALAR | `unit: { const: [a, b] }` | `fields.unit.const: CONST_NOT_SCALAR: const must be a scalar` | const is a list or map | use a single string/number/bool |
| CONST_NOT_IN_ENUM | `period_type: { const: decade }` | `fields.period_type.const: CONST_NOT_IN_ENUM: "decade" is not one of year\|tax_year\|quarter\|month\|day` | const not in the canonical enum | use one of the listed values |
| UNKNOWN_TRANSFORM | `unit: { from: u, transform: reverse }` | `fields.unit.transform[0]: UNKNOWN_TRANSFORM: unknown transform "reverse" (allowed: coalesce, concat(sep), lower, regex(pat,repl), split(sep), trim, upper)` | transform name not supported | use an allowed transform |
| TRANSFORM_ARGS | `unit: { from: u, transform: "split()" }` | `fields.unit.transform[0]: TRANSFORM_ARGS: transform split takes 1 argument(s), got 0` | wrong number of arguments | pass the arguments shown in the allowed list |
| UNKNOWN_VALIDATOR | `unit: { from: u, validate: phone }` | `fields.unit.validate: UNKNOWN_VALIDATOR: unknown validator "phone" (allowed: email, url, regex(pat), range(min,max))` | validator name not supported | use an allowed validator |
| VALIDATOR_ARGS | `unit: { from: u, validate: "range(5,1)" }` | `fields.unit.validate: VALIDATOR_ARGS: range(min,max) needs numbers with min <= max` | wrong argument count or values (also: validator that takes no arguments was given some) | fix the arguments |
| FORMAT_REQUIRED | `period_end: { from: d, type: date }` | `fields.period_end.format: FORMAT_REQUIRED: type date needs a format` | date and timestamp need `format`; an empty format string also raises it | add e.g. `format: "2006-01-02"` (Go layout) |
| BAD_REGEX | `unit: { from: u, transform: "regex([a, x)" }` | `fields.unit.transform[0]: BAD_REGEX: bad regex "[a": error parsing regexp: missing closing ]: [a` (backticks around `[a` in the real output) | regex in a transform or `regex(pat)` validator does not compile | fix the pattern (Go RE2 syntax) |
| OUTPUT_NOT_PERIOD | `unit: { from: u, output: period_start }` | `fields.unit.output: OUTPUT_NOT_PERIOD: output is only allowed on type period fields` | `output` used on a non-period field | remove `output` or set `type: period` |
| BAD_OUTPUT | `period_start: { from: period, type: period, output: period_begin }` | `fields.period_start.output: BAD_OUTPUT: output must be period_type, period_start or period_end, got "period_begin"` | invalid output name | use period_type, period_start or period_end |
| OUTPUT_MISMATCH | `period_start: { from: period, type: period, output: period_end }` | `fields.period_start.output: OUTPUT_MISMATCH: output "period_end" does not match canonical field "period_start"` | `output` differs from the field it is mapped to | make `output` equal the field name |
| BAD_PRECISION | `period_type: { from: period, type: period, output: period_type, precision: decade }` | `fields.period_type.precision: BAD_PRECISION: precision must be year, quarter, month or day, got "decade"` | invalid precision | use year, quarter, month or day |
| BAD_TEMPLATE | `unit: { template: "{@ctx.a" }` | `fields.unit.template: BAD_TEMPLATE: unclosed or nested '{' at position 0` | unbalanced/nested/empty braces, or `{@x.y}` that is not `@ctx.`/`@meta.`. Also `unknown placeholder {@bogus.x}: use {@ctx.name} or {@meta.name}` (run) | balance the braces; placeholders are `{name}`, `{@ctx.name}`, `{@meta.name}` |
| UNKNOWN_FLAG_FIELD | `value: { from: value, type: decimal, null_markers: ["-"], flag: nope }` | `fields.value.flag: UNKNOWN_FLAG_FIELD: flag names "nope", which is not a canonical field` | `flag` must name a canonical field (usually `quality_flag`) | name an existing canonical field |
| NULL_MARKERS_WITHOUT_FLAG (warning, exit stays 0) | `value: { from: value, type: decimal, null_markers: ["-"] }` | `fields.value.null_markers: NULL_MARKERS_WITHOUT_FLAG: null_markers without flag: the matched marker is discarded` | markers turn into null but the reason (e.g. `x`, `..`) is lost | add `flag: quality_flag` to keep the marker, or accept the loss on purpose |

UNVERIFIED: behaviour on a mapping where several validation codes combine was only run for UNKNOWN_FIELD + NULL_MARKERS_WITHOUT_FLAG.

### Runtime field errors (the evaluator, `internal/mapping/eval.go`)

These are NOT printed by `importer mapping validate`: a mapping can validate clean and still produce them for a bad record.
`mapping.Eval(mapping, payload)` returns `Result{Fields, Extra, Errors}`. Each error is `{Field, Code, Message}` (`Field` is the
canonical field name, or `dimensions.<name>`; errors are ordered canonical fields by name, then dimensions by name).
A record with errors is still returned: the failing field is simply absent from `Fields`, every other field is evaluated normally,
and Eval never panics (an internal panic is reported as `PARSE_FAILED`).

There is no CLI for evaluation yet. Examples below were run with a throwaway Go test calling `mapping.Parse` and `mapping.Eval`
on a scratch copy of the repo (not part of the repo). Each mapping is `source: demo`, `extra: keep` plus the fields shown.

| Code | Triggers | Mapping + payload | Real result |
|---|---|---|---|
| MISSING_REQUIRED | `required: true` field resolves to nothing/blank and has no `default`; or a `template` placeholder is empty and `empty_ok` is not set | `series_id: { from: id, required: true }`, `unit: { from: u }`; `{"id": "", "u": "GBP"}` | Fields `{unit: GBP, dimensions: {}}`; Errors `[{series_id MISSING_REQUIRED "required field has no value"}]` |
| MISSING_REQUIRED (template) | as above | `series_id: { template: "s:{a}:{b}" }`; `{"a": "1"}` | Errors `[{series_id MISSING_REQUIRED "template placeholder {b} is empty"}]` |
| TYPE_MISMATCH | the value has the wrong shape for the field `type` (object where a number is expected, bool for a number/int) | `value: { from: value, type: decimal }`; `{"value": {"a": 1}, "u": "GBP"}` | Errors `[{value TYPE_MISMATCH "expected a number, got map[string]interface {}"}]`; with `{"value": true}`: `"expected a number, got a bool"` |
| PARSE_FAILED | the text does not parse as the type (number, int, bool, date/timestamp format, period layout) or a transform fails | `value: { from: value, type: decimal }`; `{"value": "12.3.4"}` | Errors `[{value PARSE_FAILED "\"12.3.4\" is not a number"}]` |
| PARSE_FAILED (date) | as above | `period_end: { from: d, type: date, format: "2006-01-02" }`; `{"d": "31/12/2024"}` | Errors `[{period_end PARSE_FAILED "\"31/12/2024\" does not match format 2006-01-02"}]` |
| VALIDATION_FAILED | the value converted fine but the `validate` rule rejects it (`email`, `url`, `regex(..)`, `range(min,max)`) | `value: { from: value, type: decimal, validate: "range(0,100)" }`; `{"value": "250"}` | Errors `[{value VALIDATION_FAILED "250 is outside 0..100"}]` |

Mixed record, real result (`value` bad, `unit` fine): `{"value": "abc", "u": "GBP"}` gives Fields `{unit: GBP}` and Errors
`[{value PARSE_FAILED "\"abc\" is not a number"}]`: one bad field never fails the others.
Note: an empty list or blank string counts as "no value", so it gives MISSING_REQUIRED only when the field is `required`, otherwise
the field is just omitted with no error.

**Mapping problem or source-data problem?**
1. Does the same error appear for (almost) every record of the source? Mapping problem: wrong `type`, wrong `format`/layout, wrong
   `from` column name (the field resolves to nothing, so `required` gives MISSING_REQUIRED), or a `validate` rule that is too strict. Fix
   the mapping file, run `importer mapping validate`, re-import or re-read.
2. Does it appear for a few records only, and does the Message quote a value that really is bad in the raw payload (`"abc"`, a
   `31/12/2024` in an ISO-date column, `250` outside the range)? Source-data problem: do not change the mapping to hide it. The
   record stays readable without that field; report the values to the data owner. If the value is a known "no data" symbol, add it to
   `null_markers` (see below) instead.
3. Confirm by looking at the raw JSONB payload of one failing record and comparing it with the Message.

**Not errors (operators will see these)**
- ASHE suppression marker: with `value: { from: value, type: decimal, null_markers: ["x", ".."], flag: quality_flag }` and
  `{"value": "x"}`, real result: Fields `{value: nil, quality_flag: "x"}`, Errors empty. The value is null on purpose and the marker is
  kept in `quality_flag`. Without `flag` the marker is silently discarded (warning `NULL_MARKERS_WITHOUT_FLAG`). If a new symbol (e.g.
  `[c]`) shows up as PARSE_FAILED on `value`, add it to `null_markers`.
- BoE month-only dates (early events such as Oct 1694 have no day): with the shipped `boe-baserate.mapping.yaml` layouts
  `["2 Jan 2006", "Jan 2006"]` and inputs `from: [day, month, year]`, `{"day": "", "month": "Oct", "year": "1694"}` gives
  `period_type: month`, `period_start: 1694-10-01`, `period_end: 1694-10-31`, no errors; with a day (`"5"`) it gives `period_type: day`
  and `1694-10-05`. If the mapping lists only the day layout, the month-only row fails:
  `[{period_start PARSE_FAILED "period \"Oct 1694\" does not match layout 2 Jan 2006"}]`. Fix: add the month-only layout.
  Layouts are tried in order and the first match wins.

**Future / UNVERIFIED:** mapping errors are meant to reach API clients as `mapping_errors` on a record once the DataService exists
(task 011). That path does not exist yet and was not run; until then these errors are only visible to Go callers of `mapping.Eval`.
Also UNVERIFIED (not run): `email`/`url`/`regex` validator failures and TYPE_MISMATCH for `int`, `date` and period inputs (messages are
in `internal/mapping/convert.go`, `period.go`, `transform.go`); PARSE_FAILED from a failing transform.

## Verify
Re-run step 2 for the file: no issue lines (or only `NULL_MARKERS_WITHOUT_FLAG` you accept) and `exit 0`.

## Roll back / undo
The command only reads files. Restore the mapping with `git checkout -- <file>` if an edit made it worse.

## Escalate
A mapping that looks right but is rejected, or an accepted mapping that later fails at import: open an issue with the mapping file,
the full validate output, the exit code and `canonical-schema.yaml` version, to the owner of `internal/mapping`.
