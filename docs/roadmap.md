# Roadmap

Status as of task 012. Milestones M0 to M5 are done; the task numbers are in the Status column (task specs in `factory/tasks/`).

| # | Milestone | Outcome | Status |
|---|---|---|---|
| M0 | Scaffold | Go module, CI, lint, migrations runner, docker-compose Postgres | done: task 001 |
| M1 | Store + model | Schema from [data-model](data-model.md), store package, integration tests | done: tasks 002, 003 |
| M2 | CSV adapter | Configurable parsing, headerless files, metadata preamble, natural key, error capture | done: tasks 004, 005 |
| M2b | XLSX adapter + row pipeline | Sheets/hidden sheets, column letters, section/fill_down/unpivot/normalize, file hash + conditional fetch, `source_files`, reparse. Acceptance fixture: BoE base rate | remaining: task 013 |
| M2c | Files and URLs | File retention + change detection + reparse (014), zip archives and `resolve` for changing URLs (015) | partly done: tasks 014, 015 (file retention still open, see backlog) |
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
- M2c: fetched-file retention is still open (backlog). Zip archives and location resolution are done (task 015, see [mapping-spec](mapping-spec.md#archives)).
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
- Concurrent runs deadlocked on pool exhaustion, because each run held a work-pool connection for its source advisory lock. Fixed by task 019 (`factory/tasks/019-lock-pool-starvation.md`): lock connections are separate ([data model](data-model.md#decisions-task-019), [operations](operations.md#connection-pool-and-source-locks)).
- TLS for lock connections is not tested (the work pool's TLS is not changed by task 019).
- The abort and panic lock-release paths (task 019) are covered by unit tests only (`TestLockSlotFreedOnEveryExit`), not through the real binary.
- `importer run` prints `failed=N`, which now includes duplicate natural keys, but not the sample reasons in `RunResult.DuplicateSamples` (task 018 follow-up: print them after the summary line in `cmd/importer/runcmd.go`).
- No retention or cleanup for fetched files (`source_files`, the `FILE_STORE_DIR` directory): every fetched file is kept. Task 014 left it out ([data model](data-model.md#decisions-task-014)).
- `FILE_STORE_DIR` defaults to `data/files` relative to the working directory: a run from another directory silently starts an empty store, and a later `reparse` fails with `stored file is missing`. Use an absolute path, or make the default absolute.

Zip archives and location resolution (task 015), not verified (`UNVERIFIED`):
- A 256 MB member at the 100:1 ratio limit is not tested (the limits are tested with smaller and bomb cases).
- Fuzzing beyond the seed corpora of the zip and resolver parsers.
- A redirect loop is not tested.
- The fallback used when unlinking the spool file fails (the name is kept and removed on Close) is not tested.
- The 0600 mode of the spool file is not tested; concurrent-reader tests are missing.
- URL query tokens and userinfo are not redacted in `_location`, the run summary or resolver errors. Configs must not contain secrets; a redaction rule is open.
- Group templating (`{year}{status}`) is not implemented; `resolve` and `archive` work for standalone sources only (M7b).

XLSX adapter (task 013), known limits that the adapter does not detect:
- A `<c>` placed outside `<sheetData>` is silently ignored.
- A cell whose `t` attribute does not match its `<is>`/`<v>` content reads as empty.
- A cell relocated to another valid column of the same row, or a changed value, cannot be detected.
- Two guards (a row without `r` beyond 1,048,576, and a cell without `r` beyond the column limit) have no test that fails when they are removed.
- The vulnerability scan (`govulncheck`) was not run (not available offline); run it before release.
- The LibreOffice prefixed-namespace test files were hand-built, not produced by LibreOffice.

## Resume here (updated 2026-11-24)
- **Validated and pushed:** tasks 001 to 013 (M0 to M5, CSV/ONS, mapping, importer, CLI, gRPC read API, e2e demo, XLSX adapter with the Bank of England fixture end to end).
- **Validated:** task 018 (duplicate natural keys within a run are row errors; dry run detects them too). Docs were updated by the documentor ([data-model](data-model.md#decisions-task-018), [design](design.md#9-failure-handling)). 
- **Validated:** task 019 (source locks on their own connections; the pool cannot deadlock on concurrent runs). Docs were updated by the documentor ([operations](operations.md#connection-pool-and-source-locks), [design](design.md#9-failure-handling), [data model](data-model.md#decisions-task-019)). 
- **Validated:** task 014 (fetched files kept by SHA-256, conditional fetch with `unchanged_file`, `importer reparse`; no retention yet). Docs were updated by the documentor ([data model](data-model.md#decisions-task-014), [design](design.md#import), [operations](operations.md#importer-reparse)).
- **Validated:** task 015 (zip archives with zip-safety limits, `resolve` for monthly URLs, `_location` on the run and in `importer run`, https-to-http redirect refused). Docs were updated by the documentor ([mapping-spec](mapping-spec.md#archives), [roadmap](roadmap.md#hardening-backlog)); the unverified items are in the Hardening backlog.
- **Validated:** task 016 (UK HPI average prices and property type import and serve from the fixtures; CSV sources accept `table:` and `context:` and run the same pipeline as xlsx). Docs were updated by the documentor ([UK HPI](sources/uk-hpi.md), [mapping-spec](mapping-spec.md)); the real-file checks and the unverified items are listed there.
- **Validated:** task 017 (ASHE Table 20.7a imports from the real ONS zips as standalone sources `ashe-t20-2026p` and `ashe-t20-2025r`; group templating is not implemented). Docs were updated by the documentor ([ASHE Table 20](sources/ons-ashe-table20.md), [source groups](source-groups.md) marked not implemented).
- **Validated:** task 020 (a run whose lock session is lost stops as `failed` with `lost source lock (session terminated)`; `DB_LOCK_CHECK_INTERVAL`). Docs were updated by the documentor ([design](design.md#9-failure-handling), [operations](operations.md#lost-source-lock), [data model](data-model.md#decisions-task-020)).
- **Next task:** not chosen yet.
- **Housekeeping for you:** fill `factory/metrics/prices.json` (cost shows n/a); decide whether to keep the docs-role budget (documentor 20, runbook-refiner 25 tool calls); the Hardening backlog above lists known limits.
