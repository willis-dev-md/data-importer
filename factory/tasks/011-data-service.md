# 011 – DataService with mapping on read

## Goal
`GetRecord` and `ListRecords` return canonical, mapped records with correct filtering and stable keyset pagination.

## Depends on
007, 009, 010

## Design refs
`docs/api.md` (including "Filtering semantics"), `docs/design.md` (Read flow), `docs/mapping-spec.md`

## Scope
`internal/server/`, `internal/store/` (read queries, active-mapping lookup), tests

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
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/api.md (filtering semantics, page size rule chosen, SCAN_LIMIT); CHANGELOG.md

## Runbook impact
update docs/runbooks/run-the-grpc-server.md (short pages and continuation tokens, SCAN_LIMIT tuning, mapping errors in responses)

## Status
in-progress
## Log
