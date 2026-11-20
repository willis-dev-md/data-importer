# 003 – Record hashing and store layer

## Goal
A tested Go API for creating import runs and idempotently upserting raw records with accurate inserted/updated/unchanged counts.

## Depends on
002

## Design refs
`docs/design.md` (Import flow, §5a), `docs/data-model.md`

## Scope
`internal/record/`, `internal/store/` (not `migrate.go`), tests

## Out of scope
Adapters, mapping, gRPC, tombstoning of missing rows, scheduling.

## Acceptance criteria (each must be checkable)
- [ ] `record.Record{Key string, Payload map[string]any}` and `record.Hash(payload) []byte`: SHA-256 of a **canonical JSON** encoding (sorted keys at every level, no insignificant whitespace, numbers formatted deterministically). Tests: key order does not change the hash; any value change does; nested maps/arrays are covered; stable known-answer vector.
- [ ] `record.Key(parts ...string) string` encodes a natural key as a JSON array string (`["L55O","1989"]`), unambiguous even if parts contain `|` or quotes. Property/table tests.
- [ ] `store.New(pool)`; methods with `context.Context`: `UpsertSource`, `GetSource`, `CreateRun(sourceID, trigger) (runID)`, `FinishRun(runID, status, counts, shape, sourceMeta, errText)`, `GetRun`, `UpsertRaw(ctx, runID, sourceID, []record.Record) (Counts{Inserted,Updated,Unchanged})`, `RecordErrors(ctx, runID, []RowError)`, `GetRaw(ctx, sourceID, key)`, `ListRaw(ctx, sourceID, afterKey, limit)` (keyset by `natural_key`), `TryAdvisoryLock(ctx, sourceID) (unlock func(), ok bool)`.
- [ ] `UpsertRaw` runs in one transaction, uses a single batched statement per call (no per-row round trips), and: a new key is `Inserted` (sets `first_run_id=last_run_id=runID`); same key + same hash is `Unchanged` (row not rewritten, `last_run_id` still updated or not: choose and document in the log); same key + different hash is `Updated`.
- [ ] Integration tests (real Postgres): insert/update/unchanged counts across two runs; 2,000-record batch; keyset pagination returns every row exactly once in order; two concurrent `TryAdvisoryLock` for one source: exactly one gets `ok`; lock released after `unlock()` and after connection loss.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/data-model.md (natural-key encoding, content hash, upsert counting rules); docs/design.md Import flow if wording changed

## Runbook impact
none: internal library with no operator-facing behaviour yet; lock and run behaviour become operational in tasks 008 and 009, which own those runbooks

## Status
pending
## Log
