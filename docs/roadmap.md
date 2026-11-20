# Roadmap

| # | Milestone | Outcome |
|---|---|---|
| M0 | Scaffold | Go module, CI, lint, migrations runner, docker-compose Postgres |
| M1 | Store + model | Schema from [data-model](data-model.md), store package, integration tests |
| M2 | CSV adapter | Configurable parsing, headerless files, metadata preamble, natural key, error capture |
| M2b | XLSX adapter + row pipeline | Sheets/hidden sheets, column letters, section/fill_down/unpivot/normalize, file hash + conditional fetch, `source_files`, reparse. Acceptance fixture: BoE base rate |
| M2c | Files and URLs | File retention + change detection + reparse (014), zip archives and `resolve` for changing URLs (015) |
| M3 | Importer core | Runs, chunked idempotent upsert, counts, shape tracking, advisory lock |
| M4 | Mapping engine | Parser, validator, transforms, `period` type, `@meta` refs, error model, dry-run |
| M5 | gRPC read API | `DataService` with filters, keyset paging, mapping on read |
| M6 | HTTP JSON adapter | Auth, pagination types, retries, rate limit |
| M7 | Scheduler + services | Schedule parsing/validation, `next_run_at`, SKIP LOCKED claiming, catch-up policy; `SourceService`, `ImportService`, mapping versioning |
| M7b | Source groups | Templates, member expansion, shared mappings, plan/apply, rate limit, jitter, concurrency pool, group health |
| M8 | Hardening | Auth interceptor, TLS, metrics, rollback, tombstones |
| M9 | Performance | Indexes, optional materialized views, load test |

Each milestone is split into small tasks with explicit acceptance criteria. M1 to M5 give a working
CSV -> Postgres -> gRPC vertical slice, which is the first demo.

Real sources: ONS `l55o` (CSV, preamble, one series per file) is the fixture for M2 to M5; the Bank of England
base rate workbook (XLSX, messy layout, several series per file) is the fixture for M2b.

Data sources beyond the first slice (tasks 013 to 017): BoE base rate (013), UK HPI (016) and ASHE Table 20 age by
occupation (017); see `docs/sources/`.

Parked (not in v1): [MCP server source](parked/mcp-source.md).
