# ADR-0002: Go, Postgres, gRPC

Status: accepted

Go gives strong gRPC tooling, easy concurrency for streaming imports and a single deployable binary.
Postgres JSONB supports flexible payload storage with GIN indexing while keeping transactional upserts.
gRPC provides a typed contract, streaming and reflection. SQLite or a document DB were rejected: weaker
concurrent writes/ops story respectively.
