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

#### Implemented behaviour (task 004)

- **Strict config:** YAML is decoded strictly. Unknown keys are errors and validation errors name the field path
  (e.g. `csv.delimiter`). Only `kind: csv` validates today. `csv.preamble` is rejected as an unknown key until task 005,
  so [examples/ons-l55o.source.yaml](examples/ons-l55o.source.yaml) does not load yet.
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

## Source: XLSX workbook

```yaml
kind: xlsx
location: https://example.org/file.xlsx
change_detection: [etag, last_modified, content_hash]
xlsx:
  sheet: "Sheet name"        # exact name; hidden sheets are allowed; or `sheet_index`; see `sheets` for several
  start_row: 8
  end_row: null              # default: last row
  header: false              # true => header row index in `header_row`
  sheets: ["Sheet name"]    # or { except: [Notes] } for every other sheet; the sheet name is available as @ctx.sheet
  columns: { A: year, B: day, C: month, D: rate }   # letters -> names (required when header: false)
  use_cached_values: true    # formulas are read as last saved values; formulas are never evaluated
```
Merged cells are read as their top-left value only; dates stored as Excel serial numbers are returned as dates.
Macros, external links and embedded content are ignored. Files are parsed with limits on size, rows and
uncompressed size (zip-bomb protection).

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

## Row pipeline (`table:`) for CSV and XLSX

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
- every canonical field referenced exists in `canonical-schema.yaml` and types are compatible
- required canonical fields are covered
- referenced source fields exist in the source's recent `shape` (warning, not error)
- dry-run against N sample raw records, returning error rate before activation

### Errors
Mapping errors are per record and per field: `{field, code, message}` with codes such as
`MISSING_REQUIRED`, `TYPE_MISMATCH`, `PARSE_FAILED`, `VALIDATION_FAILED`.
