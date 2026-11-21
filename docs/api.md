# gRPC API

Package `dataimporter.v1`. Reflection and `grpc.health.v1` are enabled. The contract is
[`proto/dataimporter/v1/data.proto`](../proto/dataimporter/v1/data.proto); the Go code in `gen/` is generated from it
(see [Proto workflow](operations.md#proto-workflow)).

## Implemented (task 011)

`GetRecord` and `ListRecords` are implemented and served by `importer serve` ([operations](operations.md#importer-serve)).
Every call reads the raw rows and maps them with the source's **active mapping** (see [Mapping on read](#mapping-on-read)).
`ExportRecords`, `GetRawRecord` and `GetValueAsOf` are not in the proto yet (see [Planned](#planned-not-yet-in-the-proto)).
The proto below is unchanged from task 010; the behaviour is described in the sections that follow.

```proto
service DataService {
  rpc GetRecord(GetRecordRequest) returns (Record);
  rpc ListRecords(ListRecordsRequest) returns (ListRecordsResponse);
}

message Record {
  string id = 1;
  string source_id = 2;
  google.protobuf.Struct fields = 3;           // canonical (mapped) values
  google.protobuf.Struct extra = 4;            // source columns the mapping does not map
  repeated FieldError mapping_errors = 5;
  google.protobuf.Timestamp imported_at = 6;
}

message FieldError {
  string field = 1;
  string message = 2;
}

enum FilterOp {
  FILTER_OP_UNSPECIFIED = 0;
  FILTER_OP_EQ = 1;
  FILTER_OP_NEQ = 2;
  FILTER_OP_GT = 3;
  FILTER_OP_GTE = 4;
  FILTER_OP_LT = 5;
  FILTER_OP_LTE = 6;
  FILTER_OP_CONTAINS = 7;
  FILTER_OP_IN = 8;
}

message Filter {
  string field = 1;
  FilterOp op = 2;
  google.protobuf.Value value = 3;
}

message Sort {
  string field = 1;
  bool descending = 2;
}

message GetRecordRequest {
  string source_id = 1;
  string id = 2;
}

message ListRecordsRequest {
  string source_id = 1;
  string group_id = 7;              // alternative to source_id: query across a group's members
  repeated Filter filters = 2;
  repeated Sort sort = 3;
  int32 page_size = 4;              // default 50, max 1000
  string page_token = 5;            // opaque keyset cursor
  bool include_deleted = 6;
}

message ListRecordsResponse {
  repeated Record records = 1;
  string next_page_token = 2;       // empty when there are no more records
}
```

Notes
- `fields` and `extra` are `google.protobuf.Struct`; a `Filter.value` is a `google.protobuf.Value`, so it can be a string,
  number, bool, null or (for `FILTER_OP_IN`) a list. `imported_at` is a `google.protobuf.Timestamp`.
- The filter operators are the top-level enum `FilterOp`; `FILTER_OP_UNSPECIFIED = 0` is the proto3 default and is not a
  valid operator.
- **`GetRecord` returns `Record` directly**, not a `GetRecordResponse`. Adding a response wrapper later would be a
  breaking change. For that reason the `buf` lint rules `RPC_REQUEST_RESPONSE_UNIQUE` and `RPC_RESPONSE_STANDARD_NAME` are
  excluded in `buf.yaml`; all other `STANDARD` rules apply.

## Planned (not yet in the proto)

Sketch of later milestones; none of this exists in `data.proto` yet.

```proto
service DataService {
  rpc ExportRecords(ExportRecordsRequest) returns (stream Record);   // server streaming
  rpc GetRawRecord(GetRawRecordRequest) returns (RawRecord);         // restricted scope
  rpc GetValueAsOf(GetValueAsOfRequest) returns (Record);            // sparse/event series: latest event on or before a date
}

service SourceService {
  rpc ListSources(ListSourcesRequest) returns (ListSourcesResponse); // includes inferred shape
  rpc PutSource(PutSourceRequest) returns (Source);
  rpc PutSourceGroup(PutSourceGroupRequest) returns (SourceGroup);     // plan/apply, idempotent
  rpc ListGroupHealth(ListGroupHealthRequest) returns (GroupHealth);   // failing/stale/never-run members
  rpc PutMapping(PutMappingRequest) returns (MappingVersion);        // validates, dry-runs
  rpc ActivateMapping(ActivateMappingRequest) returns (Source);
  rpc InferMapping(InferMappingRequest) returns (MappingDraft);
}

service ImportService {
  rpc StartImport(StartImportRequest) returns (ImportRun);          // manual run
  rpc GetSchedule(GetScheduleRequest) returns (Schedule);           // schedule, next_run_at, last run
  rpc SetSchedule(SetScheduleRequest) returns (Schedule);           // validates, recomputes next_run_at
  rpc PauseSource(PauseSourceRequest) returns (Source);             // enabled=false
  rpc GetImportRun(GetImportRunRequest) returns (ImportRun);
  rpc ListImportRuns(ListImportRunsRequest) returns (ListImportRunsResponse);
  rpc WatchImportRun(GetImportRunRequest) returns (stream ImportRun);
}
```

## Conventions
- Keyset pagination (stable under concurrent imports), never offset.
- Filters only on canonical fields and `dimensions.<key>`; the server rejects unknown fields with `INVALID_ARGUMENT`. Sorts: natural-key order only.
- Errors use canonical gRPC codes with `google.rpc.ErrorInfo` details.
- Versioning by package (`v1`); additive changes only within a version. Linted with `buf` (`go tool buf lint`).
- Auth: bearer token metadata, scoped to source ids and roles (`reader`, `admin`, `raw_reader`).

## Mapping on read
- Every `GetRecord` and `ListRecords` call loads the source's active mapping (two small queries: the source row and the
  active mapping version), parses it and maps each raw row with the mapping evaluator. **There is no mapping cache**: the
  active version is read on every call, so a newly activated mapping applies to the next call. ([ADR-0005](adr/0005-data-service-read-path.md))
- A source with no active mapping, or whose active mapping does not parse, gives `FAILED_PRECONDITION`.
- A record whose mapping has errors is still returned, with `mapping_errors`; one bad row never fails a call.

## Record
- `id` is `<source_id>:<natural key>`; the natural key is a JSON array of strings. Real example from the ONS source:
  `ons-l55o:["L55O","1989"]`. Passing this id back to `GetRecord` with the same `source_id` returns the same record
  (checked: the two JSON renderings were equal).
- `fields` holds the mapped (canonical) values:

| Canonical type | In `fields` |
|---|---|
| `string` | string |
| `int`, `decimal` | number. Decimals are `float64` (see [mapping-spec](mapping-spec.md#implemented-behaviour-task-007) and the note below); integers beyond 2^53 lose precision |
| `bool` | bool |
| `date`, `period_start`, `period_end` | string `YYYY-MM-DD` |
| `timestamp` | string, RFC 3339, UTC |
| null marker | `null` (UNVERIFIED in a real run: the ONS fixture has no null markers; covered by unit tests only) |
| `dimensions` | nested struct of strings; always present, `{}` when empty |

- `extra` holds the source columns the mapping does not map (JSON numbers become numbers).
- `imported_at` is when the raw row was last imported. `mapping_errors` lists `{field, message}`; `message` is `CODE: text`.

Real record from a `ListRecords` call on the ONS source (`importedAt` differs per run):

    {
      "id": "ons-l55o:[\"L55O\",\"1989\"]",
      "sourceId": "ons-l55o",
      "fields": {
        "dimensions": {},
        "measure": "cpih_annual_rate",
        "period_end": "1989-12-31",
        "period_start": "1989-01-01",
        "period_type": "year",
        "series_id": "L55O",
        "series_title": "CPIH ANNUAL RATE 00: ALL ITEMS 2015=100",
        "unit": "%",
        "value": 5.7
      },
      "extra": {},
      "importedAt": "2026-11-21T11:00:10.145778Z"
    }

## GetRecord
- `source_id` and `id` are required (`INVALID_ARGUMENT`).
- Order of checks: unknown source `NOT_FOUND`; no active mapping `FAILED_PRECONDITION`; then the id. An id that does not
  start with exactly `<source_id>:`, an unknown key, and a **tombstoned** (deleted) record all give `NOT_FOUND`. The
  `Record` message has no deleted marker, so a deleted record cannot be read back.

Real output (ONS run): `GetRecord` of an id returned by `ListRecords` gave the same record; a key that does not exist
gave `NOT_FOUND: record "ons-l55o:[\"nope\"]" not found in source "ons-l55o"`.

## ListRecords
Checks, in this order (argument errors come before any source lookup):

| Request | Result |
|---|---|
| `source_id` and `group_id` both set | `INVALID_ARGUMENT` |
| neither set | `INVALID_ARGUMENT` (`source_id is required`) |
| `group_id` alone | `UNIMPLEMENTED`: `group_id alone is not implemented yet: groups arrive in a later milestone; pass source_id` |
| any `sort` entry | `INVALID_ARGUMENT`: records are returned in natural-key order |
| bad filter (see [Filters](#filters)) | `INVALID_ARGUMENT`, naming the field |
| `page_token` not decodable, empty payload, not valid UTF-8, contains NUL, or longer than 4096 bytes | `INVALID_ARGUMENT`: `page_token is not valid` |
| unknown `source_id` | `NOT_FOUND` |
| source without an active mapping | `FAILED_PRECONDITION` |

**Page size (chosen rule):** `page_size` 0 means the default **50**; a negative value is `INVALID_ARGUMENT`; a value
above **1000** is **clamped to 1000**, not rejected. The real ONS run gave 642 records for `page_size` 100 (7 pages),
for the default (13 pages), and for `page_size` 5000 (1 page). The clamp itself is not observable with 642 rows (UNVERIFIED
beyond 1000 rows).

**Order:** natural-key order always. There is no other sort in v1.

**Paging and tokens**
- `next_page_token` is the **opaque** base64 (URL-safe, unpadded) of the last natural key the server **scanned**. Clients
  must treat it as opaque and pass it back unchanged in `page_token`.
- An empty `next_page_token` means the source has no further rows. When a page fills on the last row the server looks
  one row ahead, so a full last page has an empty token. With filters, a page can be empty and still carry a token.
- The walk is keyset, so it is stable when an import adds rows mid-walk: no pre-existing row is duplicated or lost
  (test `TestONSPaginationStableDuringImport`).
- `include_deleted` defaults to `false`: tombstoned rows are hidden and not counted towards the scan limit.

**Scan limit.** A call maps at most `SCAN_LIMIT` raw rows (environment variable, default **50,000**; see
[operations](operations.md#importer-serve)). When the limit is reached before the page is full, the response has
**fewer records than `page_size`** and a **non-empty** `next_page_token`. A short page is not the end: clients keep
calling with the token until it is empty. Real run with `SCAN_LIMIT=100` and the filter `period_type = year`, `page_size` 50:

    page 1: 5 records, token set
    pages 2-6: 6 records each, token set
    page 7: 2 records, token empty
    total 37, duplicates 0

## Filters
A filter is `{field, op, value}`; all filters in a request are ANDed. Filters apply to **mapped** values, so the field
must be a canonical field (or `dimensions.<key>`). The server maps each raw row and tests the filters on the mapped values.

**Filterable fields and their kinds** (from `canonical-schema.yaml`; a test keeps the list in step with it):

| Field | Kind |
|---|---|
| `series_id`, `series_title`, `measure`, `statistic`, `period_type`, `unit`, `status`, `quality_flag` | string |
| `value` | number |
| `period_start`, `period_end` | date (string `YYYY-MM-DD`; any other text is `INVALID_ARGUMENT`) |
| `dimensions.<key>` | string (see [Dimensions](#dimensions)) |

No canonical field is a bool today, so bool comparisons are implemented but cannot be reached by a filter yet.

**Operators per kind** (anything else is `INVALID_ARGUMENT` naming the field):

| Kind | EQ, NEQ | GT, GTE, LT, LTE | IN (list) | CONTAINS (substring) |
|---|---|---|---|---|
| string | yes | yes (byte order) | yes | yes, case-sensitive |
| number | yes | yes | yes | no |
| date | yes | yes (dates compare as `YYYY-MM-DD`) | yes | no |
| bool | yes | no | yes | no |
| `dimensions.<key>` | yes | no | yes | no |

A value of the wrong type for the field, an unknown or non-filterable field, an operator that is not allowed for the
kind, a missing value, `IN` without a list and an unparseable date are all `INVALID_ARGUMENT`. Real messages from the
ONS run:

    value EQ "abc"            -> INVALID_ARGUMENT: filter on field "value": value has the wrong type, expected number
    bogus EQ "abc"            -> INVALID_ARGUMENT: filter field "bogus" is unknown or not filterable
    period_start EQ "2024-02-30" -> INVALID_ARGUMENT: filter on field "period_start": "2024-02-30" is not a date (YYYY-MM-DD)
    value CONTAINS "1"        -> INVALID_ARGUMENT: filter on field "value": CONTAINS is only supported on string fields
    page_size -1              -> INVALID_ARGUMENT: page_size must not be negative, got -1

**Null.** `EQ null` matches a canonical field that is null or absent; `NEQ null` matches one that is not null. Every
other operator with a null value is `INVALID_ARGUMENT`, and so is any null on a `dimensions.<key>` filter. Any other
operator never matches a null or absent value (so `NEQ "x"` does not match a null).

**Mapping errors.** A record with a mapping error on a filtered field **never matches**, whatever the operator (also
`NEQ` and `EQ null`). The record is still returned when it passes the other filters.

**Real counts** (ONS source `ons-l55o`, 642 records, paged with `page_size` 100 and checked for duplicates):

| Filter | Records |
|---|---|
| none | 642 |
| `period_type = month` | 454 |
| `period_type = month` AND `value >= 3.5` | 108 |
| `period_type = year` | 37 |
| `period_type = quarter` | 151 |
| `period_type IN [month, year]` | 491 |
| `period_start >= 2000-01-01` | 455 |

The ONS fixture has no mapping errors, so the "never matches" rule is covered by tests only.

## Dimensions
- `dimensions.<key>` filters the `dimensions` map by key. Values are strings; the operators are `EQ`, `NEQ` and `IN`.
- An **unknown key matches nothing**, for `EQ`, `NEQ` and `IN` alike (not an error). A record without that key does not match either.
- `dimensions.` (empty key) is `INVALID_ARGUMENT`; so is `dimensions` alone (`filter field "dimensions" is not filterable: use dimensions.<key>`).
- Real run (the ONS mapping has no dimensions): `dimensions.area EQ "x"` and `dimensions.area NEQ "x"` each returned 0 records, not an error.

## Errors and internal failures
- A failed database call whose cause is the input (Postgres SQLSTATE class 22, a data exception) is `INVALID_ARGUMENT`:
  `request contains a value the database cannot store or compare`. (UNVERIFIED in a run: no request reached this path;
  NUL bytes are rejected before the query.)
- Any other failure is `INTERNAL` with the message `<operation> failed` (for example `read records failed`).
  **The database error text never reaches the client**; it is written to the server log only.

## Notes and known limits
- **Decimals are `float64`.** Values with more than about 15 to 17 significant digits lose precision; see the limitation in
  [mapping-spec](mapping-spec.md#implemented-behaviour-task-007).
- **Two cheap queries per call** (source and active mapping) on top of the raw-row reads; `ListRecords` may read one more row
  to decide whether the token is empty.
- **Performance was not benchmarked.** Every call maps up to `SCAN_LIMIT` rows in Go. A 50,000-row walk is UNVERIFIED.
- **TLS:** the checks for this task ran over plaintext gRPC (no `TLS_CERT_FILE`/`TLS_KEY_FILE`); TLS mode was not verified here.
- Pushdown to SQL is limited to `source_id` and `include_deleted`; filters on mapped values run in Go. Speed for hot
  sources comes later from materialised typed views (roadmap M9); the API does not change when that happens.
- `sort` is limited to natural-key order (the stable keyset order); other sorts return `INVALID_ARGUMENT`.
