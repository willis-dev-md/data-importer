# Source and mapping specification

Two YAML documents per setup: a **source** (how to fetch/parse) and a **mapping** (how to shape).
Both are validated against JSON Schema before being stored. Mappings are versioned; saving creates a new
version and `active_mapping_version` is switched explicitly.

## Many datasets
Shared structure across many datasets is handled with [source groups](source-groups.md): one template plus a list
of members, expanded to one source each. A mapping can be shared as `name@version` so it is written once.

## Schedule

Every source (any kind) defines when it is imported:

```yaml
schedule:
  every: daily            # daily | weekly | monthly   (or `cron: "0 6 * * 1"`)
  at: "06:00"             # local time in `timezone`
  day_of_week: monday     # weekly only
  day_of_month: 18        # monthly only; 1-28, or `last`
  timezone: Europe/London
  on_missed: run_once     # run_once | skip
  jitter: 5m              # optional random delay to spread load
enabled: true
```

Rules: exactly one of `every` or `cron`; `day_of_month` above 28 is rejected (use `last`); DST transitions
follow the timezone database (a non-existent local time runs at the next valid instant). Schedule changes take
effect on save. A source with no `schedule` is manual-only.

## Source: CSV

```yaml
id: acme-csv
kind: csv
location: file:///data/acme/members.csv     # or s3://, https://
csv:
  delimiter: ","
  header: true            # false => columns named c1..cn or via `columns`
  encoding: utf-8
  columns: []             # optional explicit names
natural_key: [member_id]  # source field(s) forming the record key
missing_policy: keep      # keep | tombstone
max_error_ratio: 0.05
```

`missing_policy: tombstone` is accepted in source config but nothing applies it yet and no code sets `deleted_at`, so `include_deleted` has no effect on real data until tombstoning is implemented.

#### Implemented behaviour (task 004)

- **Strict config:** YAML is decoded strictly. Unknown keys are errors and validation errors name the field path
  (e.g. `csv.delimiter`). Only `kind: csv` validates today.
  [examples/ons-l55o.source.yaml](examples/ons-l55o.source.yaml) loads and parses the fixture (task 005).
- **Options:** `delimiter` is one character (`tab` or `\t` for a tab). `encoding` is `utf-8` (default, BOM stripped),
  `windows-1252`, `utf-16`/`utf-16le` (BOM honoured, little-endian by default) or `utf-16be`. `natural_key` fields must be columns.
- **Fetch:** `location` is a local path, `file://` or `https://`; plain `http://` is rejected. Defaults: 256 MB size
  limit, 5 minute timeout (whole fetch, body included), a `User-Agent` is sent. The SHA-256 of the bytes read is recorded.
- **Parsing:** an own row scanner is used instead of `encoding/csv`, so `RowError.Raw` and line numbers are exact.
  Blank lines are skipped. A row with the wrong field count, a bad quote or an empty natural key field yields a recorded
  `RowError` (line number and raw text) and the stream continues. A row over 16 MiB is a fatal stream error.

Known limitations:
- An unterminated quote produces one `RowError` that swallows the rest of the file.
- CRLF inside a multi-line quoted field is normalised to LF, so `Raw` is not byte-exact there.
- A lone CR is not a line end.
- Redirects from `https` to `http` are currently followed (to be refused in task 015).

### CSV with a metadata preamble and no header row

Some publishers (e.g. the ONS, see [sources/ons-l55o.md](sources/ons-l55o.md)) put `key,value` metadata lines
above the data and have no header row.

```yaml
csv:
  header: false
  columns: [period, value]       # names for headerless data
  preamble:
    lines: 8                     # or `until: <regex matching first data row>`
    as: metadata                 # parse as key,value pairs -> available as @meta.<Key>
natural_key: ["@meta.CDID", period]   # may mix metadata and row fields
```

Preamble metadata is stored on `import_runs.source_meta` and copied into each raw payload under `_meta`, so
mappings can reference it (`from: "@meta.Unit"`) and the data stays self-describing.

Implemented behaviour (task 005):
- `preamble` takes exactly one of `lines: N` (physical rows, blank lines count) or `until: <regex>` (matched against the raw
  text of each row; the first matching row is the first row after the preamble), plus `as: metadata`. With `header: true`
  the header row follows the preamble. A preamble shorter than declared (or an `until` that never matches) fails `Open`.
- Each preamble row is `key,value`; a missing or empty value is `""`. More than two fields, an empty key, a bad quote or a
  duplicate key fail `Open`. Metadata is returned by `Stream.Meta()` (nil without a preamble).
- `natural_key` entries `@meta.<Key>` are mixed with column names; an unknown key fails at open time naming the known keys.
  `@meta.` needs `csv.preamble`. An empty metadata value gives an empty-key `RowError` per row.
- Every record's fields also hold `_meta` (map of metadata, empty without a preamble) and `_source` (`{line: n}`, the
  1-based physical line where the row starts). With `header: false` and `columns`, a row with another field count is a `RowError`.
- The source config also accepts `enabled` (carried, not interpreted yet).
- Known limitation: `FuzzCSVReader` does not cover the preamble (verified in `internal/source/csv_test.go`; only header/headerless modes).

## Source: XLSX workbook

```yaml
kind: xlsx
location: https://example.org/file.xlsx
change_detection: [etag, last_modified, content_hash]
xlsx:
  sheet: "Sheet name"        # exact name; hidden sheets are allowed; or `sheet_index`; see `sheets` for several
  start_row: 8
  end_row: null              # default: last row
  header: false              # default true (names from `header_row`, default 1); false => `columns` required
  sheets: ["Sheet name"]    # or { except: [Notes] } for every other sheet; the sheet name is available as @ctx.sheet
  columns: { A: year, B: day, C: month, D: rate }   # letters -> names (required when header: false)
  use_cached_values: true    # formulas are read as last saved values; formulas are never evaluated
```
Implemented behaviour (task 013, `internal/config/xlsx.go`, `internal/source/xlsx.go`):
- **Cell values are text as stored.** Every cell is read as its saved text: a number as its saved literal (`13.8438`,
  no display rounding), a boolean as `1`/`0`, an error cell as its literal (`#N/A`), an empty or missing cell as `""`.
  **Dates are not converted:** a date-formatted cell arrives as its Excel serial number text (e.g. `45293`). Typed values
  appear only through the `normalize` steps `to_int` and `to_number` (see the row pipeline below).
- **Formulas are never evaluated.** A formula cell gives its cached value, or `""` if the file saved none.
  `use_cached_values: false` is rejected.
- **Sheets:** hidden sheets are read like any other; `sheet_index` is 0-based. `sheets: {except: [...]}` keeps workbook
  order. An unknown name in `sheet`, `sheets` or `except` is an error that lists the available sheets (hidden ones are
  marked). Excluding every sheet is an error. Each record gets `_ctx.sheet` and `_source: {sheet, row}`.
- **Rows and header:** `header` defaults to `true`. Without `columns` the names come from row `header_row` (default 1),
  and `start_row` defaults to the row after the header. Rows whose configured columns are all empty are skipped before the
  pipeline. `_source.row` is the real 1-based row number. Merged cells give only the top-left value (covered cells are
  empty). Columns not listed in `columns` are not read.
- **Limits** (adapter defaults): file 100 MiB, 2,000,000 rows walked over the selected sheets (sheets hold at most
  1,048,576 rows each), 256 sheets, 1 GiB decompressed, 10,000 zip entries, 16 MiB of text in one row. Each zip entry is
  inflated against its declared size, so a lying header or a zip bomb is rejected. A breached limit is an error
  (`ErrTooLarge`, `ErrXLSXTooManyRows`, `ErrXLSXUnzipLimit` or `ErrXLSXRowTooLarge`).
- **Corrupt files:** a file that is not a workbook, is truncated or garbled, is encrypted, or whose sheet XML is not
  well-formed or is structurally inconsistent (row or cell numbers out of order, bad cell references, shared-string index
  out of range) fails with `ErrXLSXBadArchive` naming the part and row. A stream never ends early without an error.
  Decision and reasons: [ADR-0006](adr/0006-xlsx-cell-values-and-checks.md).
- No network access, macros (`vbaProject` is never read) or external links are followed.

Known limits, not detected by the adapter (also in the [roadmap](roadmap.md) hardening backlog): a `<c>` placed outside
`<sheetData>` is ignored; a cell whose `t` does not match its `<is>`/`<v>` content reads as empty; a cell moved to another
valid column of the same row, or a changed value, cannot be detected.

## Static context
A source (usually a group member) can carry static values that mappings read as `@ctx.<name>`:

```yaml
context: { year: 2026, status: provisional }
```
Values set by pipeline `section` steps and the sheet name (`@ctx.sheet`) live in the same namespace.

## Archives
A file can be a zip containing the real file(s); pick the member by regex. Archives are read with limits on
member size, ratio and count (zip-bomb protection) and member names are never used as filesystem paths.

```yaml
location: https://example.org/data.zip
archive: { type: zip, member: '(?i)Table 20\.7a .*\.xlsx$' }   # exactly one member must match
```

## Location resolution (files whose URL changes)
Publishers often put the date in the file name (`Average-prices-2026-09.csv`). `resolve` generates candidate
values for a placeholder, newest first, and the first URL that answers (HEAD, falling back to a ranged GET) wins:

```yaml
location: https://example.org/files/Average-prices-{ym}.csv
resolve:
  ym: { kind: month, layout: "2006-01", start: "-1m", back: 6 }   # this month minus 1, then 5 earlier
```
If no candidate answers, the run fails with the list of URLs tried. The chosen URL is recorded on the run.

## Row pipeline (`table:`) for XLSX

Structural, order-dependent clean-up that runs **at import** ([ADR-0004](adr/0004-structural-parsing-at-import.md)).
Steps run in order; the output rows are what is stored. Closed set:

| Step | Effect |
|---|---|
| `skip_rows: {where: <regex on column>}` | drop matching rows |
| `drop_if_empty: [cols]` | drop rows where all listed columns are empty |
| `section: {column, match, set, default}` | rows whose `column` matches the regex are headings: they set a context variable (available as `@ctx.<set>`, and stored in `_ctx`) and are dropped; `default` applies before the first heading |
| `fill_down: {columns, reset_on}` | carry the last non-empty value down; optionally reset on a section change |
| `normalize: {col: transforms}` | `trim`, `lower`, `upper`, `to_int`, `to_string`, `collapse_ws`, `to_number` (turns numeric text into numbers; leaves markers such as `x` untouched) |
| `unpivot: {id, columns, name, value}` | wide to long: each listed column becomes a row with `name`/`value`. `columns` is a list, or a map `column: label` (the label is stored in `name`). Rows whose value is empty are dropped. Columns not listed are not carried over (the stored source file keeps everything, see ADR-0004) |
| `split: {column, regex, optional}` | named capture groups become new columns (e.g. `age_band` and `soc_name` out of `18-21   Corporate managers`); a non-matching row gets empty values when `optional: true`, else is a `RowError` |
| `strip: {column: [chars]}` | remove characters (e.g. the leading `'` in ASHE SOC codes) |

Every stored row also gets `_source: {sheet, row}` (or line number) for traceability.

Implemented for `kind: xlsx` (task 013, `internal/source/pipeline.go`). `table:` and `context:` on a `kind: csv` source
are rejected at validation, not ignored, until CSV support lands.
- Steps apply in the order listed. A step failure on a row is a `RowError` (the row is not stored and the stream
  continues); steps never panic. Each step has its own unit test (`TestPipelineSkipRows` to `TestPipelineStrip`).
- Section and `fill_down` state is per sheet. Headings are trimmed; `section.match` is tested only against text;
  `fill_down` with `reset_on: section` forgets the carried value at each heading.
- `normalize` on mixed types: text transforms leave non-text values alone. `to_int` gives int64 for text or a number that
  holds a whole number (`" 1694 "`, `1694.0`, `1e3`); anything else (`abc`, `1.5`, `1_000`, hex, `inf`, `NaN`, 1e15 or more)
  is a `RowError` naming the column. `to_number` gives int64, or float64 when needed, for plain decimal text; markers
  (`x`, `..`, `1,234`) stay as they are; a literal with more than 15 significant digits that does not fit int64 stays text.
  A column absent from the row is skipped.
- `natural_key` parts are a field name, a section or context variable name (falling back to `_ctx`), or `@ctx.<name>`.
  An **empty part is allowed** (a BoE event without a day); only a key whose parts are all empty is a `RowError`.
  `@meta.` is rejected for xlsx.
- `context:` scalars are stored as strings. `_ctx` holds the context values, `sheet`, and the section variables; a section
  variable overrides a context value, and `sheet` is reserved.

## Source: HTTP JSON API

```yaml
id: partner-api
kind: http_json
location: https://api.example.com/v2/people
auth: { type: bearer, token_env: PARTNER_API_TOKEN }   # or api_key + header
pagination:
  type: cursor            # none | page | offset | cursor
  cursor_path: $.meta.next
  cursor_param: after
records_path: $.data[*]   # JSONPath to the record array
rate_limit_per_sec: 5
retry: { max_attempts: 5, backoff: exponential }
natural_key: [id]
```

## Mapping

```yaml
source: acme-csv
version: 3
fields:
  full_name:                       # canonical field
    from: ["First Name", "Last Name"]
    transform: concat(" ")
    required: true
  email:
    from: Email
    transform: [trim, lower]
    validate: email
  joined_on:
    from: Joined
    type: date
    format: "02/01/2006"           # Go reference-time layout
  active:
    from: Status
    type: bool
    map: { Active: true, Lapsed: false }
    default: false
extra: keep                        # keep | drop: unmapped source fields into Record.extra
```

### Dimensions
Free-form descriptors of what an observation is about. Each entry is a normal field spec; empty results are
omitted. Output as `Record.fields.dimensions` (a map of strings).

```yaml
dimensions:
  area_code: { from: Area_Code }
  age_band:  { from: age_band, default: all }
```

### Field options
| Option | Meaning |
|---|---|
| `from` | source field name(s) or JSONPath (`$.a.b`); list = multiple inputs |
| `type` | `string` `int` `decimal` `bool` `date` `timestamp` `period` (default `string`) |
| `const` | fixed value instead of `from` (e.g. `unit: "%"`) |
| `template` | build a string from placeholders, e.g. `"ukhpi:{measure_col}:{Area_Code}"`; `{@ctx.x}` and `{@meta.x}` work too; any empty placeholder is an error unless `empty_ok: true` |
| `null_markers` | list of publisher markers (`["x","..",":","-"]`) that mean "no value": the field becomes null (not an error) |
| `flag` | canonical field that receives the matched marker (e.g. `flag: quality_flag`) |
| `precision` | force period precision (`year\|quarter\|month\|day`) when the layout carries more (e.g. an ISO date that is always the 1st of a month) |
| `@ctx.<name>` | in `from`: a context variable set by a `section` step |
| `format` | parse layout (or a list of layouts tried in order) for dates/timestamps/periods. With several `from` inputs the non-empty ones are joined with spaces in the order listed, so list them in layout order (`[day, month, year]` for `"2 Jan 2006"`). Month names are case-insensitive |
| `transform` | ordered list: `trim` `lower` `upper` `concat(sep)` `split(sep)` `regex(pat,repl)` `coalesce` |
| `map` | value lookup table |
| `default` | used when input is missing/empty |
| `required` | missing => `FieldError` in `mapping_errors` |
| `validate` | `email` `url` `regex(pat)` `range(min,max)` |

Transforms are a closed, pure set (no scripting) so mappings stay safe and testable.

### Period type
`type: period` parses publisher period labels into a normalised value with three outputs:
`period_type` (`year|quarter|month|day`), `period_start` (date) and `period_end` (date).
Built-in layouts: `YYYY`, `tax_year_ending` (input is the year in which the tax year ends: `2026` gives 2025-04-06 to 2026-04-05, `period_type: tax_year`), `YYYY Qn`, `YYYY MON` (English month abbreviations). With an explicit `format`, the
resulting precision follows the layout that matched: a layout with a day gives `period_type: day`
(start = end = that date), a layout with only month and year gives `month`. This is how rate events with and
without a day (BoE) are handled. Unparseable labels give `PARSE_FAILED`.

### Validation on save
Implemented (task 006, `internal/mapping`, `importer mapping validate`, see [operations](operations.md)):
`mapping.Parse` reads strict YAML (an unknown key is a `PARSE_ERROR`), then `mapping.Validate` checks the mapping
against `canonical-schema.yaml` and returns every finding as `{path, code, message, severity}`. Only
`NULL_MARKERS_WITHOUT_FLAG` is a warning; every other code is an error.

| Code | Severity | Meaning |
|---|---|---|
| `PARSE_ERROR` | error | not an issue from `Validate`: the file is not valid strict YAML for the mapping (syntax error or unknown key); printed by the CLI as `<file>: PARSE_ERROR: ...` |
| `MISSING_SOURCE` | error | `source` is empty |
| `BAD_EXTRA` | error | `extra` is not `keep` or `drop` |
| `UNKNOWN_FIELD` | error | a key under `fields` is not a canonical field |
| `DIMENSIONS_AS_FIELD` | error | `dimensions` appears under `fields`; use the top-level `dimensions:` block |
| `REQUIRED_NOT_COVERED` | error | a required canonical field is not mapped |
| `UNKNOWN_TYPE` | error | `type` is not one of `string int decimal bool date timestamp period` |
| `TYPE_MISMATCH` | error | `type` cannot feed the canonical type (see below), `type: period` on a field other than `period_type`/`period_start`/`period_end`, or a direct mapping of the `dimensions` map |
| `DIMENSION_TYPE` | error | a `dimensions:` entry has a `type` other than string |
| `SOURCE_CONFLICT` | error | more than one of `from`, `const`, `template` |
| `SOURCE_MISSING` | error | none of `from`, `const`, `template` |
| `BAD_FROM` | error | a `from` entry is empty, or is not a field name, a JSONPath (`$.a.b`, `$.a[0]`, `$.a[*]`), `@meta.X` or `@ctx.X` |
| `CONST_NOT_SCALAR` | error | `const` is a list or map |
| `CONST_NOT_IN_ENUM` | error | `const` on an enum field (`period_type`) is not one of its values |
| `UNKNOWN_TRANSFORM` | error | transform is not `trim lower upper coalesce concat(sep) split(sep) regex(pat,repl)` (or is not `name` / `name(args)`) |
| `TRANSFORM_ARGS` | error | wrong number of transform arguments (`trim lower upper coalesce` 0; `concat split` 1; `regex` 2) |
| `UNKNOWN_VALIDATOR` | error | `validate` is not `email url regex(pat) range(min,max)` |
| `VALIDATOR_ARGS` | error | wrong arguments: `email`/`url` take none, `regex` one, `range` two numbers with min <= max |
| `FORMAT_REQUIRED` | error | `type` is `date` or `timestamp` and `format` is missing, or a `format` entry is empty |
| `BAD_REGEX` | error | the pattern of `regex(...)` (transform or validator) does not compile |
| `OUTPUT_NOT_PERIOD` | error | `output` on a field that is not `type: period` |
| `BAD_OUTPUT` | error | `output` is not `period_type`, `period_start` or `period_end` |
| `OUTPUT_MISMATCH` | error | `output` differs from the canonical field name it is mapped to (e.g. `output: period_end` under `period_start`) |
| `BAD_TEMPLATE` | error | `template` has an unbalanced, nested or empty `{}` placeholder, or an `@` placeholder that is not `{@ctx.x}` / `{@meta.x}` |
| `UNKNOWN_FLAG_FIELD` | error | `flag` names a field that is not canonical |
| `NULL_MARKERS_WITHOUT_FLAG` | warning | `null_markers` without `flag`: the matched marker is discarded |
| `BAD_PRECISION` | error | `precision` is not `year quarter month day` |

Rules the implementation adds beyond the original list:
- `output` must equal the canonical field name (`OUTPUT_MISMATCH`); `type: period` feeds only `period_type`,
  `period_start` and `period_end`.
- An enum `const` must be one of the enum values (`CONST_NOT_IN_ENUM`).
- `from` syntax is checked (`BAD_FROM`); transform and validator argument counts are checked (`TRANSFORM_ARGS`,
  `VALIDATOR_ARGS`). Arguments may be quoted with `"` or `'` (needed when a regex contains a comma).
- An untyped `const` takes its type from the YAML scalar, so `const: 1` can feed a decimal. Any other field without
  `type` is a string, so a decimal field fed by `from` needs `type: decimal`.
- Type compatibility (mapping type to canonical type): string to string; string to enum; int to int; decimal or int
  to decimal; bool to bool; date to date; timestamp or date to timestamp.

Not implemented yet:
- referenced source fields exist in the source's recent `shape` (warning, not error): needs stored shapes
- dry-run against N sample raw records, returning error rate before activation (`Eval` exists since task 007, see below; the dry-run command does not)

Note on the examples: `docs/examples/acme-csv.mapping.yaml` and `partner-api.mapping.yaml` were person-shaped and
invalid against the observation schema. They were rewritten as observation mappings and now validate with zero
errors; their column names (for example `Fee`, `Status`, `$.attributes.country`) are illustrative, not real publisher
columns. The person-shaped snippet under "Mapping" above is likewise only an illustration of the field options.

### Errors
Mapping errors are per record and per field: `{field, code, message}` with codes such as
`MISSING_REQUIRED`, `TYPE_MISMATCH`, `PARSE_FAILED`, `VALIDATION_FAILED` (details below).

### Implemented behaviour (task 007)
`mapping.Eval(m, payload) Result{Fields, Extra, Errors}` (`internal/mapping`, library only, no CLI yet) maps one raw
payload. Behaviour checked with a throwaway program against the code:

- **Errors never abort.** Each field is evaluated on its own; a failing field adds a `FieldError{Field, Code, Message}`
  (`dimensions.<name>` for a dimension) and is absent from `Fields`. Errors are ordered by canonical field name, then
  dimensions by name. A panic inside a field becomes `PARSE_FAILED`.
- **Codes:** `MISSING_REQUIRED` (`required` field empty after `default`; a `template` placeholder is empty unless `empty_ok`);
  `TYPE_MISMATCH` (value of the wrong kind, e.g. a list or map for a string, a bool for an int/decimal; `range` on a non-number);
  `PARSE_FAILED` (text does not parse as the type or layout, non-finite decimal, bad transform, unclosed `{` in a template);
  `VALIDATION_FAILED` (`email`, `url`, `regex`, `range` rejected the converted value).
- **Output Go types** in `Fields`: `string`, `int` as `int64`, `decimal` as `float64`, `bool`, `date` and `timestamp` as
  `time.Time` (dates at UTC midnight, timestamps converted to UTC), `period` as `time.Time` for `period_start`/`period_end`
  and a string for `period_type`. `dimensions` is always present, a `map[string]string`; empty values are omitted.
- **Known limitation: decimal is `float64`.** Values with more than about 15-17 significant digits lose precision
  (`0.1234567890123456789` becomes `0.12345678901234568`). `13.8438`, `272642` and `34229.1` round-trip exactly. The exact
  text stays in the raw payload. `-0` is emitted as `-0`. The `decimal` type rejects `NaN` and `Inf` (`PARSE_FAILED`), but
  `validate: range(min,max)` on a non-decimal field accepts the string `"NaN"` (`ParseFloat` accepts it and the comparison is false).
- **Inputs:** several `from` entries are joined with single spaces (non-empty ones, trimmed). An empty or blank string counts
  as missing, for `required` and `default` alike. `default` is used instead of `map` (a defaulted value is not looked up).
  `map` passes an unmapped value through unchanged.
- **Null markers:** a value equal to a `null_markers` entry makes the field present with a nil value; the marker goes to the
  `flag` field unless that field has its own value. `template` placeholders use the same references as `from`.
- **Extra:** `extra: keep` collects payload keys no field read; `drop` (or empty) leaves `Extra` empty. Keys starting with
  `_` (`_meta`, `_ctx`, `_source`) are never in `Extra`.
- **Periods:** layouts `YYYY`, `tax_year_ending`, `YYYY Qn`, `YYYY MON` or Go layouts, tried in order. `MON` is the English
  three-letter abbreviation, case-insensitive; `Sept` or `June` give `PARSE_FAILED` (also with the Go layout `2006 Jan`).
  `precision` only coarsens: `precision: month` on a day layout gives `period_type: month` and the start is the 1st, while
  `precision: day` on a month layout changes nothing. A `time.Time` input counts as a day.

Remaining: the dry-run against stored samples and the `importer` command that uses `Eval` are later tasks.
