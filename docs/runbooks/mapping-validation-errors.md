# Runbook: mapping validation errors

**Owner role:** on-call engineer (data onboarding)  |  **Severity if this happens:** low (a mapping is rejected before it is used)
**Last verified:** 2026-11-21, ran `make build`, every command and snippet below with `bin/importer` and `go run ./cmd/importer` (task 006 working tree)

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

Runtime row errors (MISSING_REQUIRED, TYPE_MISMATCH at read time, PARSE_FAILED, VALIDATION_FAILED) are not validation
codes; task 007 adds them here. UNVERIFIED: none of the above is unrun, but behaviour on a mapping where several codes combine was only run for UNKNOWN_FIELD + NULL_MARKERS_WITHOUT_FLAG.

## Verify
Re-run step 2 for the file: no issue lines (or only `NULL_MARKERS_WITHOUT_FLAG` you accept) and `exit 0`.

## Roll back / undo
The command only reads files. Restore the mapping with `git checkout -- <file>` if an edit made it worse.

## Escalate
A mapping that looks right but is rejected, or an accepted mapping that later fails at import: open an issue with the mapping file,
the full validate output, the exit code and `canonical-schema.yaml` version, to the owner of `internal/mapping`.
