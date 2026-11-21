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
validated
## Log
- Round 1 (worker): proto, buf.yaml (lint STANDARD minus RPC_REQUEST_RESPONSE_UNIQUE and RPC_RESPONSE_STANDARD_NAME because docs/api.md returns `Record` from GetRecord), buf.gen.yaml with local plugins via `go tool` (protoc-gen-go v1.36.8, protoc-gen-go-grpc v1.5.1; grpc v1.75.0 and protobuf v1.36.8 now direct deps, all Go 1.24 compatible). Generated code in gen/dataimporter/v1; `scripts/checks.d/15-proto-generated.sh` fails on a diff after offline `buf generate`.
- Decisions: `FilterOp` is a top-level enum with `FILTER_OP_*` values (buf lint prefix rule); `GetRecordRequest{source_id,id}` and `Sort{field,descending}` added (docs/api.md does not define them); `Filter` includes GTE/LTE as the spec requires (docs/api.md sketch lists only EQ, NEQ, GT, LT, CONTAINS, IN: docs need updating). Health: NOT_SERVING until the first DB ping succeeds, re-pinged every 5 s; service names "" and dataimporter.v1.DataService. `server.Options.Data` lets task 011 inject the real DataService. `importer serve` requires DATABASE_URL; the "scheduler" part of the old usage text was dropped. Existing cmd test for "serve not implemented" replaced.
- Round 2 (worker): fixed validator FAIL. `Server.Run` now runs the first `CheckHealth` BEFORE `grpc.Serve` (connections queue in the listener meanwhile), so SERVING is deterministic once the DB is up; if the DB is down the server still starts as NOT_SERVING and the 5 s loop recovers it (new test `TestHealthRecoversAfterStartupOutage`, polls with a deadline). Each ping is capped at 5 s so an unreachable DB cannot delay startup indefinitely. Health loop is joined (`loopDone`) before Run returns: no goroutine leak. Why it passed for me and not the validator: round 1 ran Check after Serve but the ping (local Postgres, sub-ms) usually finished first; on the validator's loaded machine the ping lost the race. Real-binary checks found a real defect: an open `Health.Watch` stream held `GracefulStop` for the full 10 s then forced stop. Fixed with `watchEnder` (ends Watch streams at shutdown; test `TestShutdownWithOpenWatch`); shutdown is now immediate. Also: after the first SIGINT/SIGTERM default signal handling is restored so a second signal kills the process (previously swallowed). Verified with the binary: plaintext start + health SERVING, SIGTERM/SIGINT exit 0, port in use -> "address already in use" exit 1, half TLS and unreadable cert files -> clear error exit 1, plaintext warning logged (addr=0.0.0.0:port for `:port`), DB down -> NOT_SERVING. In-flight unary call on shutdown is covered by the test (no slow RPC exists in the binary yet). `-race -count=20 -run TestHealth`, `-count=3 -p 8 ./...`, and validate.sh x3 pass.
- 2026-11-21 validator r1: FAIL (deterministic health race: the first CheckHealth ran after grpc.Serve, so a prompt Check saw NOT_SERVING; the worker's own run passed by timing luck). r2 (worker): first CheckHealth before Serve; ping capped at 5 s; loop joined on shutdown; an open Health.Watch stream blocked GracefulStop for 10 s (fixed); a swallowed second signal fixed. validator r2: PASS (TestHealth x30 under CPU load; mutation fails the test again; real binary: plaintext + TLS, health, reflection, SIGINT/SIGTERM exit 0, port in use, bad TLS).
- documentor then runbook-refiner (sequential: both use the shared test database) ran; deterministic docs check PASS.
- Not verified (labelled UNVERIFIED in docs): second-signal behaviour (code reading only), unreadable cert file (tests ran as root), goroutine leaks at runtime, grpcurl.
- Process lessons: (1) a worker reporting a green gate is not evidence: the validator caught a timing race; (2) an agent left an orphaned `importer serve` running (stopped by the orchestrator): roles that start servers must stop them.
