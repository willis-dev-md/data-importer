# 010 – Protobuf contract and server skeleton

## Goal
A running gRPC server with the `DataService` contract, health and reflection, generated code committed and linted.

## Depends on
001

## Design refs
`docs/api.md`, `docs/operations.md`, `docs/design.md` §10

## Scope
`proto/`, `buf.yaml`, `buf.gen.yaml`, generated code under `gen/`, `internal/server/`, `cmd/importer` (`serve`), tests

## Out of scope
Implementing `GetRecord`/`ListRecords` (task 011); `SourceService`, `ImportService`, auth interceptor, TLS enforcement (M8).

## Acceptance criteria (each must be checkable)
- [ ] `proto/dataimporter/v1/data.proto` defines `DataService` with `GetRecord`, `ListRecords`; messages `Record`, `FieldError`, `Filter` (field, op EQ/NEQ/GT/GTE/LT/LTE/CONTAINS/IN, value as `google.protobuf.Value`), `Sort`, `ListRecordsRequest/Response` exactly as sketched in `docs/api.md` (including `page_token`, `page_size`, `include_deleted`), `Record.fields/extra` as `google.protobuf.Struct`.
- [ ] `go tool buf lint` and `go tool buf generate` pass; generated Go code is committed and a check (script or test) fails if regenerating produces a diff.
- [ ] `internal/server` serves `DataService` (methods return `codes.Unimplemented` for now), `grpc.health.v1` (SERVING after the DB ping succeeds, NOT_SERVING otherwise) and server reflection.
- [ ] `importer serve` reads `GRPC_ADDR` (default `:8443`), optional `TLS_CERT_FILE`/`TLS_KEY_FILE` (both or neither; plaintext only if neither, logged as a warning), shuts down gracefully on SIGINT/SIGTERM within 10 s.
- [ ] Tests (in-process via `bufconn` and one real TCP listener): health responds, reflection lists `dataimporter.v1.DataService`, unimplemented methods return the right code, graceful shutdown completes in-flight calls, TLS config validation errors for half-configured TLS.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/api.md (as generated); docs/operations.md (GRPC_ADDR, TLS vars, shutdown); CHANGELOG.md

## Runbook impact
docs/runbooks/run-the-grpc-server.md (start, health, reflection, TLS, graceful shutdown)

## Status
pending
## Log
