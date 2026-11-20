# Roadmap

| # | Milestone | Outcome |
|---|---|---|
| M0 | Scaffold | Go module, CI, lint, migrations runner, docker-compose Postgres |
| M1 | Store + model | Schema from [data-model](data-model.md), store package, integration tests |
| M2 | CSV adapter | Configurable parsing, headerless files, metadata preamble, natural key, error capture |
| M3 | Importer core | Runs, chunked idempotent upsert, counts, shape tracking, advisory lock |
| M4 | Mapping engine | Parser, validator, transforms, `period` type, `@meta` refs, error model, dry-run |
| M5 | gRPC read API | `DataService` with filters, keyset paging, mapping on read |
| M6 | HTTP JSON adapter | Auth, pagination types, retries, rate limit |
| M7 | Source/Import services | `SourceService`, `ImportService`, mapping versioning |
| M8 | Hardening | Auth interceptor, TLS, metrics, rollback, tombstones |
| M9 | Performance | Indexes, optional materialized views, load test |

Each milestone is split into small tasks with explicit acceptance criteria. M1 to M5 give a working
CSV -> Postgres -> gRPC vertical slice, which is the first demo.

First real source: ONS `l55o` (CPIH annual rate). It is the acceptance fixture for M2 to M5.
