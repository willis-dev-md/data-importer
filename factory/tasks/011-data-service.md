# 011 – DataService with mapping on read

## Goal
`GetRecord` and `ListRecords` return canonical, mapped records with correct filtering and stable keyset pagination.

## Depends on
007, 009, 010

## Design refs
`docs/api.md` (including "Filtering semantics"), `docs/design.md` (Read flow), `docs/mapping-spec.md`

## Scope
`internal/server/`, `internal/store/` (read queries, active-mapping lookup), `cmd/importer/serve.go` (wiring, round 2), tests

## Out of scope
Auth, `GetRawRecord`, `ExportRecords`, `GetValueAsOf`, caching beyond a simple in-memory cache of active mappings, materialised views.

## Acceptance criteria (each must be checkable)
- [ ] `GetRecord(source_id, id)` loads the raw row and the source's active mapping and returns `Record{id, source_id, fields, extra, mapping_errors, imported_at}`; unknown source/record gives `NOT_FOUND`; source without an active mapping gives `FAILED_PRECONDITION`.
- [ ] `ListRecords` implements the semantics in `docs/api.md`: keyset walk, map each row, apply filters to **mapped** values, fill the page, honour the scan limit (`SCAN_LIMIT`, default 50,000) and return a continuation `next_page_token` (opaque, base64 of the last scanned natural key) even when the page is short; `page_size` default 50, max 1000 (above is clamped or `INVALID_ARGUMENT`: choose, document, test); `include_deleted=false` hides rows with `deleted_at`.
- [ ] Filter ops EQ NEQ GT GTE LT LTE IN CONTAINS work on string, int, decimal, bool and date fields; comparing against a wrongly typed value, or an unknown/unfilterable field, is `INVALID_ARGUMENT` naming the field. Records with a mapping error on a filtered field never match.
- [ ] A record that fails mapping is still returned (when it passes the filters that don't involve the failed field) with `mapping_errors` populated: one bad row does not fail the call.
- [ ] Unsupported `sort` gives `INVALID_ARGUMENT`; default order is natural-key order.
- [ ] Tests (real Postgres, ONS fixture imported through `importer.Run`, mapping `docs/examples/ons-l55o.mapping.yaml`): total records for `source_id=ons-l55o` via paging with `page_size=100` equals **642** with no duplicates; filter `period_type = month` returns **454**; `period_type = month AND value >= 3.5` returns **108**; `period_type = year` returns **37**; a tiny `SCAN_LIMIT` still returns all matches across pages; invalid filters give the right codes; pagination is stable when a new import adds rows mid-walk (no duplicate, no loss of pre-existing rows).
- [ ] Filters also work on `dimensions.<key>` (EQ, NEQ, IN; values are strings) and on `measure` / `statistic` / `series_id` / `unit` / `status` (added now so task 016 needs no rework): an unknown dimension key matches nothing (not an error); `dimensions.` alone is `INVALID_ARGUMENT`. Test with a hand-made mapping and rows.
- [ ] (added in round 2) `importer serve` really serves it: `cmd/importer/serve.go` wires the DataService (scope widened to that file and its test). Test with the REAL binary and a real database: import the ONS fixture (migrate, sources apply, run), start `importer serve`, call `ListRecords` over gRPC (a small Go client in the test) and get 642 records for the ONS source; `GetRecord` returns one; `importer serve` without a mapping for a source returns FAILED_PRECONDITION for it. The test must not leave processes running.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/api.md (filtering semantics, page size rule chosen, SCAN_LIMIT); CHANGELOG.md

## Runbook impact
update docs/runbooks/run-the-grpc-server.md (short pages and continuation tokens, SCAN_LIMIT tuning, mapping errors in responses)

## Status
validated
## Log

### Worker round 1 (decisions; docs/api.md, CHANGELOG and the runbook are for the documentor)
Code: `internal/server/data.go` (DataService, `NewDataService`, `ScanLimitFromEnv`, `RecordID`), `internal/server/filter.go`,
`internal/store/store.go` (`ListRawPage`: keyset read with `includeDeleted` pushdown), tests `internal/server/data_test.go`,
`internal/store/store_test.go`.
- **Wiring not done:** `cmd/importer/serve.go` is outside this task's Scope. It still needs
  `Data: server.NewDataService(store.New(pool), server.DataOptions{ScanLimit: n})` with `n` from `server.ScanLimitFromEnv(os.Getenv)`
  (SCAN_LIMIT; an invalid value should be a usage error).
- **Record.id** = `source_id + ":" + natural_key` (natural key is the JSON array from `record.Key`). `GetRecord` strips the exact
  prefix `request.source_id + ":"`, so ids stay unambiguous even if a source id contains a colon; an id with another prefix is NOT_FOUND.
- **fields mapping:** string -> string; int and decimal -> number (float64; ints beyond 2^53 lose precision); bool -> bool; date and
  `period_start`/`period_end` -> `"YYYY-MM-DD"`; a field of type `timestamp` -> RFC 3339 UTC; null marker -> Null; `dimensions` -> nested
  Struct of strings (always present, may be empty). `extra`: json.Number -> number, other JSON types as is. `mapping_errors[].message` is `"CODE: text"`.
- **Filterable fields** are the canonical fields (hard-coded kinds, a test checks them against `canonical-schema.yaml`) plus
  `dimensions.<key>`. Kinds: string/enum -> string value; int/decimal -> number; bool -> bool; date -> string `YYYY-MM-DD`
  (unparseable = INVALID_ARGUMENT). Wrong value type, unknown field, `dimensions` alone, `dimensions.` alone, unspecified op,
  missing value, IN without list, CONTAINS on non-string, GT/GTE/LT/LTE on bool: INVALID_ARGUMENT naming the field. Dimensions allow only EQ/NEQ/IN with string values.
- **Null/absent semantics:** `EQ null` matches a null or omitted canonical field, `NEQ null` a non-null one; every other op never matches
  a null/omitted value (so `NEQ x` does not match null). A field with a mapping error never matches, whatever the op. An absent dimension never matches (also not NEQ). Filters are ANDed.
- **page_size:** 0 -> 50; negative -> INVALID_ARGUMENT; above 1000 is **clamped** to 1000.
- **Token:** base64 (URL-safe, unpadded) of the last scanned natural key; undecodable/empty -> INVALID_ARGUMENT. Empty token only when the
  source has no further rows: when the page fills on the last row of a batch the server peeks one row. With filters the next page may still be empty.
- **Sort:** any `sort` entry -> INVALID_ARGUMENT. `group_id` alone -> UNIMPLEMENTED; with `source_id` -> INVALID_ARGUMENT.
- **Order of checks (ListRecords):** argument validation first (INVALID_ARGUMENT even for an unknown source), then NOT_FOUND (no source) / FAILED_PRECONDITION (no active mapping, or an active spec that does not parse).
- **GetRecord of a tombstoned row** is NOT_FOUND (the Record has no deleted marker).
- **No mapping cache:** the active mapping is loaded once per call (two cheap queries); always current, nothing to invalidate.
- Batch size per store read: clamp(page space left, 32, 500), never more than the scan limit left. Scan limit counts mapped rows; tombstoned rows hidden by `include_deleted=false` are not scanned.
- No proto change was needed. No new dependencies.
- 2026-11-21 orchestrator: worker r1 did not wire the service into `importer serve` (cmd/importer/serve.go was out of scope). Scope widened and a wiring criterion added; round 2 wires it before validation.

### Worker round 2
- `cmd/importer/serve.go`: `serve` reads `SCAN_LIMIT` via `server.ScanLimitFromEnv` (invalid -> stderr message, exit 2, before touching the DB) and sets `Options.Data = server.NewDataService(store.New(pool), DataOptions{ScanLimit, Logger})`. Health, TLS and shutdown unchanged.
- New `cmd/importer/serve_binary_test.go`: real binary; migrate, `sources apply` (ONS source with absolute fixture path + mapping, plus a mapping-less `nomap` source), `run`, `importer serve` on `127.0.0.1:0` (address polled from the log, deadlines only); `ListRecords` paging 642 unique records, `GetRecord`, FAILED_PRECONDITION for `nomap` (List and Get), UNIMPLEMENTED for `group_id` alone; SIGTERM, asserts clean exit (kill on 30s timeout). Also a bad `SCAN_LIMIT` test.
- `group_id` alone: message now "group_id alone is not implemented yet: groups arrive in a later milestone; pass source_id"; the existing test asserts the text.
- validate.sh passes.

### Worker round 3
- Defect fix (`internal/server/data.go`): a page token must decode to a key that is valid UTF-8, has no NUL and is at most 4096 bytes, else INVALID_ARGUMENT `page_token is not valid` (same check makes a GetRecord id with such a key NOT_FOUND). Store errors now go through `DataService.internal`: a Postgres data exception (SQLSTATE class 22) becomes INVALID_ARGUMENT with a generic message; anything else is INTERNAL `<op> failed`; the DB detail goes to the server log only.
- Minor (`internal/server/filter.go`): Eval keys dimension errors per dimension (`dimensions.<key>`), which the filter already matched; a mapping error on `dimensions` as a whole now also blocks every dimension filter (defensive). New test `TestDimensionMappingErrorNeverMatches` (required dimension missing).
- Tests: tokens `AA`, `AAAA`, `AGE`, invalid UTF-8, oversized, garbage; NUL in a GetRecord id; a token of another key space gives an empty page without error.
- 2026-11-21 validator r2 (round 1 of validation): FAIL on one defect: a tampered page_token decoding to bytes with a NUL reached Postgres and returned INTERNAL with a database error text. Worker round 3 fixed it (UTF-8/no-NUL/<=4096 bytes, class-22 errors -> INVALID_ARGUMENT, other failures generic with detail only in the log). validator r3: PASS (3,000-call fuzz: zero INTERNAL/UNKNOWN or database-text leaks; ONS numbers 642/454/108/37/151 through the real server; SCAN_LIMIT=7 short pages lose nothing).
- Rounds: 3 worker rounds (r1 service, r2 wiring into `importer serve`, r3 token defect). Wiring was outside the original scope (cmd/importer/serve.go): scope widened by the orchestrator.
- documentor and runbook-refiner ran sequentially (shared test database). docs-check FAIL: `docs/operations.md: verify block failed (exit 2)`: a tagged block documented a command that is supposed to exit 2 and aborted under `bash -e`. Fixed first with the strong model (documentor on sonnet): the block now asserts the exit code. Deterministic check PASS afterwards.
- Known notes (docs): decimals float64; no 50k-row benchmark; TLS not verified for this task; the filter.go dimension-error guards are redundant (nit); `missing_policy: tombstone` is accepted but never applied (backlog); test database is SQL_ASCII (backlog).
