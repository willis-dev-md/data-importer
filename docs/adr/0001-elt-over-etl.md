# ADR-0001: ELT (load raw, map on read) over ETL

Status: accepted

## Context
Source structure differs per setup and is not fully known when a setup is added. Mapping before load couples
ingestion to a correct mapping and drops or rejects whatever does not fit.

## Decision
Store every record as received (JSONB) and apply a versioned per-source mapping in the gRPC read path. Offer
optional materialisation to typed tables/views for stable, hot sources.

## Consequences
+ Mapping changes need no re-import; no data loss; easy audit/debugging; simpler importer.
- Read-time cost and weaker DB-level typing; bad mappings surface at read time.
Mitigations: validate and dry-run mappings on save, return per-record mapping errors, add indexes and
materialised views where needed.

## Alternatives
- ETL with typed tables: best query speed and integrity, but requires known schema and re-import on change.
- Fully generic API (Struct only): max flexibility, but pushes interpretation to every client.
