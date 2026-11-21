# gRPC API

Package `dataimporter.v1`. Reflection and `grpc.health.v1` are enabled. The contract is
[`proto/dataimporter/v1/data.proto`](../proto/dataimporter/v1/data.proto); the Go code in `gen/` is generated from it
(see [Proto workflow](operations.md#proto-workflow)).

## Implemented (task 010)

**All `DataService` methods currently return `Unimplemented`** (real output:
`rpc error: code = Unimplemented desc = method GetRecord not implemented`). The behaviour arrives with task 011.

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
- Filters/sorts only on canonical fields; the server rejects unknown fields with `INVALID_ARGUMENT`.
- Errors use canonical gRPC codes with `google.rpc.ErrorInfo` details.
- Versioning by package (`v1`); additive changes only within a version. Linted with `buf` (`go tool buf lint`).
- Auth: bearer token metadata, scoped to source ids and roles (`reader`, `admin`, `raw_reader`).

## Filtering semantics (mapping on read)
Filters and sorts apply to **canonical** fields, which only exist after mapping. v1 handles this honestly rather than
approximating:
- The server walks `raw_records` in key order (keyset), maps each row, applies the filters to the mapped values and
  collects matches until `page_size` is reached.
- A per-call **scan limit** (default 50,000 rows, `SCAN_LIMIT`) bounds the work. If it is hit before the page is
  full, the response returns the matches so far with a non-empty `next_page_token`, so clients simply continue.
- Cheap pushdown applies where it is exact: `source_id` and `deleted`/`include_deleted`.
- This is correct for any mapping and any filter. Speed for hot sources comes later from materialised typed views
  (roadmap M9); the API does not change when that happens.
- `sort` is limited in v1 to natural-key order (the stable keyset order); other sorts return `INVALID_ARGUMENT`.
