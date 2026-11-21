# ADR-0005: DataService read path (mapping per call, filters on mapped values, scan-limited keyset pages)

Status: accepted (task 011)

## Context
ADR-0001 and ADR-0004 keep raw rows and map them on read. The read API has to filter on canonical values, which only
exist after mapping, and must stay correct for any mapping and any filter. Task 011 had to choose how paging, the scan
bound, invalid input and errors behave. Each choice below was made while implementing task 011 and is what the code does.

## Decision
- **No mapping cache.** `GetRecord` and `ListRecords` load the source and its active mapping on every call (two cheap
  queries) and parse it. A newly activated mapping applies to the next call; there is nothing to invalidate.
- **Filters run in Go on mapped values.** The server walks `raw_records` in natural-key order, maps each row, and tests
  the filters on the mapped result. Only `source_id` and `include_deleted` are pushed down to SQL. This is exact for any
  mapping; SQL pushdown over JSONB can come later with materialised views (roadmap M9) without changing the API.
- **Filterable fields** are the canonical fields (kinds fixed in code and checked against `canonical-schema.yaml`) and
  `dimensions.<key>` (strings, EQ/NEQ/IN only). Wrong types, unknown fields and disallowed operators are `INVALID_ARGUMENT`
  naming the field. An unknown dimension key matches nothing (it is not an error).
- **A mapping error on a filtered field never matches.** The record is still returned when the other filters pass.
- **Nulls:** `EQ null` matches a null or absent canonical field, `NEQ null` a non-null one; any other operator never
  matches a null or absent value. Null is not allowed on a dimension filter.
- **Page size:** 0 means 50; a negative value is `INVALID_ARGUMENT`; a value above 1000 is **clamped** to 1000 rather than
  rejected.
- **Scan limit (`SCAN_LIMIT`, default 50,000 raw rows per call).** When it is reached before the page is full, the response
  is a short page with a non-empty `next_page_token`. A short page is not the end of the walk; clients follow the token
  until it is empty. Checked at startup: an invalid value exits 2 before the database is opened.
- **Continuation token:** the base64 (URL-safe, unpadded) of the last scanned natural key. It is opaque to clients. A token
  that does not decode, is empty, is not valid UTF-8, contains NUL or is longer than 4096 bytes is `INVALID_ARGUMENT`.
  An empty token means no further rows; when a page fills on the last row, one more row is looked up to decide.
- **Record id:** `<source_id>:<natural key>`. `GetRecord` removes the exact prefix `<source_id>:`, so ids stay unambiguous
  when a source id contains a colon. A tombstoned record is `NOT_FOUND`.
- **Sort:** any `sort` entry is `INVALID_ARGUMENT`; the order is always natural-key order.
- **`group_id` alone** is `UNIMPLEMENTED` ("groups arrive in a later milestone"); with `source_id` as well it is `INVALID_ARGUMENT`.
- **Errors:** argument errors come before any source lookup. A database error whose cause is the input (Postgres SQLSTATE
  class 22) is `INVALID_ARGUMENT`; any other failure is `INTERNAL` with a generic `<operation> failed`. Database text is
  written to the server log only and never reaches the client.
- **Decimals are `float64`** (inherited from the mapping evaluator; see [mapping-spec](../mapping-spec.md#implemented-behaviour-task-007)).

## Consequences
- Every call costs work proportional to the rows it scans, bounded by `SCAN_LIMIT`; a sparse filter on a large source
  returns short pages more often. Performance on large sources is UNVERIFIED (not benchmarked).
- Clients must handle short pages and empty pages that carry a token. This is documented in [api](../api.md#listrecords).
- Changing `SCAN_LIMIT` or the page maximum needs no data change.
