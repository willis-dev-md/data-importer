# gRPC API

Package `dataimporter.v1`. Reflection and `grpc.health.v1` enabled.

```proto
service DataService {
  rpc GetRecord(GetRecordRequest) returns (Record);
  rpc ListRecords(ListRecordsRequest) returns (ListRecordsResponse);
  rpc ExportRecords(ExportRecordsRequest) returns (stream Record);   // server streaming
  rpc GetRawRecord(GetRawRecordRequest) returns (RawRecord);         // restricted scope
}

service SourceService {
  rpc ListSources(ListSourcesRequest) returns (ListSourcesResponse); // includes inferred shape
  rpc PutSource(PutSourceRequest) returns (Source);
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

message Record {
  string id = 1;
  string source_id = 2;
  google.protobuf.Struct fields = 3;
  google.protobuf.Struct extra = 4;
  repeated FieldError mapping_errors = 5;
  google.protobuf.Timestamp imported_at = 6;
}

message ListRecordsRequest {
  string source_id = 1;
  repeated Filter filters = 2;      // field, op (EQ, NEQ, GT, LT, CONTAINS, IN), value
  repeated Sort sort = 3;
  int32 page_size = 4;              // default 50, max 1000
  string page_token = 5;            // opaque keyset cursor
  bool include_deleted = 6;
}
```

Conventions
- Keyset pagination (stable under concurrent imports), never offset.
- Filters/sorts only on canonical fields; the server rejects unknown fields with `INVALID_ARGUMENT`.
- Errors use canonical gRPC codes with `google.rpc.ErrorInfo` details.
- Versioning by package (`v1`); additive changes only within a version. Linted with `buf`.
- Auth: bearer token metadata, scoped to source ids and roles (`reader`, `admin`, `raw_reader`).
