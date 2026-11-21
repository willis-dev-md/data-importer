# Roadmap

Status as of task 012. Milestones M0 to M5 are done; the task numbers are in the Status column (task specs in `factory/tasks/`).

| # | Milestone | Outcome | Status |
|---|---|---|---|
| M0 | Scaffold | Go module, CI, lint, migrations runner, docker-compose Postgres | done: task 001 |
| M1 | Store + model | Schema from [data-model](data-model.md), store package, integration tests | done: tasks 002, 003 |
| M2 | CSV adapter | Configurable parsing, headerless files, metadata preamble, natural key, error capture | done: tasks 004, 005 |
| M2b | XLSX adapter + row pipeline | Sheets/hidden sheets, column letters, section/fill_down/unpivot/normalize, file hash + conditional fetch, `source_files`, reparse. Acceptance fixture: BoE base rate | remaining: task 013 |
| M2c | Files and URLs | File retention + change detection + reparse (014), zip archives and `resolve` for changing URLs (015) | remaining: tasks 014, 015 |
| M3 | Importer core | Runs, chunked idempotent upsert, counts, shape tracking, advisory lock | done: task 008 (CLI in task 009) |
| M4 | Mapping engine | Parser, validator, transforms, `period` type, `@meta` refs, error model, dry-run | done: tasks 006 (canonical v2, parser, validator), 007 (evaluator) |
| M5 | gRPC read API | `DataService` with filters, keyset paging, mapping on read | done: tasks 010 (skeleton), 011 (DataService) |
| M6 | HTTP JSON adapter | Auth, pagination types, retries, rate limit | remaining (no task yet) |
| M7 | Scheduler + services | Schedule parsing/validation, `next_run_at`, SKIP LOCKED claiming, catch-up policy; `SourceService`, `ImportService`, mapping versioning | remaining (no task yet) |
| M7b | Source groups | Templates, member expansion, shared mappings, plan/apply, rate limit, jitter, concurrency pool, group health | remaining (no task yet) |
| M8 | Hardening | Auth interceptor, TLS, metrics, rollback, tombstones | remaining (no task yet) |
| M9 | Performance | Indexes, optional materialized views, load test | remaining (no task yet) |

Milestones M0 to M5 give the CSV -> Postgres -> gRPC vertical slice, the first demo: `scripts/demo.sh` and the README
Quick start (task 012). The CLI (task 009, `sources apply` and `run`) and the canonical v2 schema (task 006) are part of
that work.

Remaining work, in order of the table:
- M2b: XLSX adapter and row pipeline (task 013).
- M2c: fetched-file retention, change detection and reparse (task 014); zip archives and location resolution (task 015).
- M6: HTTP JSON adapter.
- M7: scheduler and the source and import services.
- M7b: source groups.
- M8: hardening, including the backlog below.
- M9: performance.

Data sources beyond the first slice (tasks 013 to 017): BoE base rate (013), UK HPI (016) and ASHE Table 20 age by
occupation (017); see `docs/sources/`. The Bank of England base rate workbook (XLSX, messy layout, several series per
file) is the acceptance fixture for M2b.

Parked (not in v1): [MCP server source](parked/mcp-source.md).

## Hardening backlog
Known limitations of the task 009 CLI, left as future work:
- `importer run --dry-run` prints `inserted=0 updated=0 unchanged=0` and no total; it parses and reports but writes nothing, so the zeros are not a count of what a real run would do.
- Files in `sources apply --dir` have no size cap.
- A YAML parse error on a garbage file may echo a snippet of its content.
- That a redirect to another host does not receive the auth token is covered by a unit test only, not by an end-to-end test.
- `missing_policy: tombstone` is accepted in source config but nothing applies it yet and no code sets `deleted_at`, so `include_deleted` has no effect on real data until tombstoning is implemented.
- The ephemeral test database is created with encoding SQL_ASCII (container locale), so tests do not exercise UTF-8 collation or encoding rules like production: create it with UTF8 (`initdb -E UTF8`) in `scripts/testdb.sh`.
