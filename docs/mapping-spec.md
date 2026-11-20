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

### Field options
| Option | Meaning |
|---|---|
| `from` | source field name(s) or JSONPath (`$.a.b`); list = multiple inputs |
| `type` | `string` `int` `decimal` `bool` `date` `timestamp` `period` (default `string`) |
| `format` | parse layout for dates/timestamps |
| `transform` | ordered list: `trim` `lower` `upper` `concat(sep)` `split(sep)` `regex(pat,repl)` `coalesce` |
| `map` | value lookup table |
| `default` | used when input is missing/empty |
| `required` | missing => `FieldError` in `mapping_errors` |
| `validate` | `email` `url` `regex(pat)` `range(min,max)` |

Transforms are a closed, pure set (no scripting) so mappings stay safe and testable.

### Period type
`type: period` parses publisher period labels into a normalised value with three outputs:
`period_type` (`year|quarter|month`), `period_start` (date) and `period_end` (date).
Recognised layouts: `YYYY`, `YYYY Qn`, `YYYY MON` (English month abbreviations). Custom layouts can be added
with `formats:`. Unparseable labels give `PARSE_FAILED`.

### Validation on save
- every canonical field referenced exists in `canonical-schema.yaml` and types are compatible
- required canonical fields are covered
- referenced source fields exist in the source's recent `shape` (warning, not error)
- dry-run against N sample raw records, returning error rate before activation

### Errors
Mapping errors are per record and per field: `{field, code, message}` with codes such as
`MISSING_REQUIRED`, `TYPE_MISMATCH`, `PARSE_FAILED`, `VALIDATION_FAILED`.
