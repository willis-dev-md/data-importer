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
- [ ] `store.New(pool)`; methods with `context.Context`: `UpsertSource`, `GetSource`, `CreateRun(sourceID, trigger) (runID)`, `FinishRun(runID, status, counts, shape, sourceMeta, errText)`, `GetRun`, `UpsertRaw(ctx, runID, sourceID, []record.Record) (Counts{Inserted,Updated,Unchanged})`, `RecordErrors(ctx, runID, []RowError)`, `GetRaw(ctx, sourceID, key)`, `ListRaw(ctx, sourceID, afterKey, limit)` (keyset by `natural_key`), `TryAdvisoryLock(ctx, sourceID) (unlock func(), ok bool, err error)` (`err != nil` means the attempt itself failed, e.g. the database is down; `ok == false` with a nil error means the lock is held elsewhere; `unlock` is never nil and safe to call).
- [ ] `UpsertRaw` runs in one transaction, uses a single batched statement per call (no per-row round trips), and: a new key is `Inserted` (sets `first_run_id=last_run_id=runID`); same key + same hash is `Unchanged` (row not rewritten, `last_run_id` still updated or not: choose and document in the log); same key + different hash is `Updated`.
- [ ] Integration tests (real Postgres): insert/update/unchanged counts across two runs; 2,000-record batch; keyset pagination returns every row exactly once in order; two concurrent `TryAdvisoryLock` for one source: exactly one gets `ok`; lock released after `unlock()` and after connection loss.
- [ ] (added in round 2 after validator notes) the canonical JSON of a number is identical before and after a jsonb round trip for **all** numbers, including integral floats in [2^63, 2^64) (e.g. `9.3e18`, `1.8446744073709552e19`) and above (`1e21`): tests assert `Hash(p) == Hash(readBack(p))` for these and `UpsertRaw` of the read-back payload reports `Unchanged`; `docs/data-model.md` states the guarantee accurately.
- [ ] A database error in `TryAdvisoryLock` is returned as `err` (tested, e.g. with a closed pool), distinct from the lock being held.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/data-model.md (natural-key encoding, content hash, upsert counting rules); docs/design.md Import flow if wording changed

## Runbook impact
none: internal library with no operator-facing behaviour yet; lock and run behaviour become operational in tasks 008 and 009, which own those runbooks

## Status
validated
## Log
- Worker round 1: added `internal/record` (Key, Canonical, Sum, Hash) and `internal/store/store.go` (+ tests). No migration changes; no new dependencies.
- Decision: `UpsertRaw` sets `last_run_id` for `Unchanged` rows (makes "seen in run N" queryable, needed later for tombstoning) but does not rewrite payload/content_hash/imported_at.
- Decision: `Hash` returns nil on unencodable payloads (NaN/Inf); `record.Sum` returns the error and `UpsertRaw` fails the batch with it.
- Decision: duplicate keys in one batch: last wins, counted once (ON CONFLICT cannot touch a row twice). Reappearing tombstoned rows get `deleted_at = NULL`.
- Decision (superseded in round 2): `TryAdvisoryLock` returned `(unlock, ok)`. Extra helpers beyond the spec: `Store.ListErrors`, `Store.RunCounts` type, `ErrNotFound`.
- Docs updated: docs/data-model.md "Decisions (task 003)". design.md wording unchanged.
- 2026-11-21 validator r1: PASS with notes. Orchestrator applies two notes as round 2: (1) TryAdvisoryLock returns an error (a DB outage must not look like "already running"); (2) hash canonical form must survive jsonb for integral floats in [2^63,2^64). Note 3 (NUL byte in a string fails the whole batch) is assigned to task 008.
- Worker round 2: (1) `TryAdvisoryLock` now returns `(unlock, ok, err)`: acquire/query failure is an error (connection discarded), held elsewhere is `ok=false, err=nil`; unlock never nil. Tests updated, new test for closed pool and cancelled context vs held.
- Worker round 2: (2) number canonicalisation rewritten. jsonb stores numeric exactly and prints plain decimal with no exponent (it keeps trailing zeros, e.g. 1.0). Canonical form is now the exact value as plain decimal with leading/trailing zeros stripped and -0 as 0; float64 go through shortest digits then the same expansion, so 9.3e18, 1e21, 1e-7, 5e-324, MaxFloat64 match their jsonb read-back (json.Number). Expansion above 20000 digits is rejected. This changes hashes of non-integers with exponents and large floats vs round 1 (no data exists yet). Tests: record unit tests plus a real-jsonb round trip (Hash equal, UpsertRaw of read-back is Unchanged). docs/data-model.md corrected (preserved vs not preserved).
- 2026-11-21 validator r2: PASS with minor notes (documentation of the asymmetric numeric limit, assigned to the documentor). documentor + runbook-refiner ran (runbook impact `none` confirmed; seeds for tasks 008/009 added to docs/runbooks/README.md); validator docs-check: PASS (the Postgres jsonb numeric limit was verified: 1e-16383 accepted, 1e-16384 rejected with "value overflows numeric format").
- Rounds: 2 (round 2 was a deliberate orchestrator choice to apply two round-1 notes: TryAdvisoryLock error return, and number canonicalisation stable across jsonb). Note 3 (NUL byte in a string fails the whole batch) added to task 008's criteria.
