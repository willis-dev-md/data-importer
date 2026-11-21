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

## Hardening backlog
Known limitations of the task 009 CLI, left as future work:
- `importer run --dry-run` prints `inserted=0 updated=0 unchanged=0` and no total; it parses and reports but writes nothing, so the zeros are not a count of what a real run would do.
- Files in `sources apply --dir` have no size cap.
- A YAML parse error on a garbage file may echo a snippet of its content.
- That a redirect to another host does not receive the auth token is covered by a unit test only, not by an end-to-end test.
- `missing_policy: tombstone` is accepted in source config but nothing applies it yet and no code sets `deleted_at`, so `include_deleted` has no effect on real data until tombstoning is implemented.
- The ephemeral test database is created with encoding SQL_ASCII (container locale), so tests do not exercise UTF-8 collation or encoding rules like production: create it with UTF8 (`initdb -E UTF8`) in `scripts/testdb.sh`.
