# Runbook: UK House Price Index monthly update

**Owner role:** data operations (owner of the `uk-hpi-*` sources)  |  **Severity if this happens:** low (a missed month leaves the previous month's data in place; nothing is deleted)
**Last verified:** 2026-11-24, task 021 (`sources apply` for `uk-hpi-average-prices` on a throwaway Postgres 16: `next run 2026-12-20T10:00:00Z`, re-apply, and `enabled: false` cleared it; the load figures below are from 2026-11-22, task 016: fixture runs and one real-file run, figures marked "pack" come from the validator evidence pack)

## When to use
Once a month, after the Land Registry publishes the new UK HPI files. They appear about two months after the data month (the 2026-09 files were published 18 November 2026). Also use it when someone asks whether the UK HPI data is current.

## What the two sources do
- `uk-hpi-average-prices`: file `Average-prices-<YYYY-MM>.csv`. Columns `Average_Price` and `Average_Price_SA` are unpivoted to the measures `average_price` and `average_price_sa`. Empty cells (for example SA before 2004) are dropped.
- `uk-hpi-property-type`: file `Average-prices-Property-Type-<YYYY-MM>.csv`. Four property types: detached, semi_detached, terraced, flat.
- Files are read from `https://publicdata.landregistry.gov.uk/market-trend-data/house-price-index-data/`.
- Each run tries the previous 6 months, newest first. Example: a run on 2026-11-20 tries `2026-10` (404), then `2026-09` (200) and imports `2026-09`.
- If the newest file is byte-identical to the last import (`etag`, `last_modified` or `content_hash`), the run ends with `status=unchanged_file` and exit 0. Nothing is parsed.
- The publisher revises earlier months in later releases. A changed file upserts on `(source_id, natural_key)`, so the summary reports `updated=` for old months. This is normal.
- **Scheduling:** the source files carry `schedule: { every: monthly, day_of_month: 20, at: "10:00", timezone: Europe/London }`. `sources apply` stores it and prints `next run <time>` (task 021, throwaway database: `next run 2026-12-20T10:00:00Z`). Nothing runs at that time yet: the loop that starts scheduled imports is task 022 and NOT built. Until then start each month's run by hand, or by a cron job that calls `importer run` (UNVERIFIED: no cron entry was installed or tested). Schedule rules: [onboarding-a-new-source.md](onboarding-a-new-source.md#schedule-optional).

## Prerequisites
- A built binary: `go build -o bin/importer ./cmd/importer` (or `make build`).
- `DATABASE_URL` exported, and the schema migrated: `bin/importer migrate up` (see [database-migrations.md](database-migrations.md)).
- Both sources applied: `bin/importer sources apply --dir <dir>` (see [add-a-source.md](add-a-source.md)). The sources table is referenced by `raw_records.source_id`.
- Optional: `FILE_STORE_DIR` (where fetched files are kept; see [reparse-and-file-retention.md](reparse-and-file-retention.md)).

## Steps
1. Check the example mapping files still validate (no database needed):
<!-- verify -->
```bash
go run ./cmd/importer mapping validate docs/examples/uk-hpi-average-prices.mapping.yaml
go run ./cmd/importer mapping validate docs/examples/uk-hpi-property-type.mapping.yaml
```
   *(expected: no output and exit 0 for both; measured 2026-11-22, `rc=0` each)*

2. Run both sources (the real URLs need network access):
```bash
bin/importer run --source uk-hpi-average-prices
bin/importer run --source uk-hpi-property-type
```
   *(expected per source: one line `source=<id> status=succeeded inserted=<n> updated=<n> unchanged=<n> failed=0 run=<id> duration=<d>`, exit 0; or `status=unchanged_file` if nothing changed)*

3. Check the result (see Verify below).

## Troubleshooting
| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| `status=unchanged_file` (exit 0) | the newest file is byte-identical to the last import, or no newer month is published | Nothing to do. Check the latest month in Verify. If a newer month should exist, wait and re-run. |
| Run fails: `resolve: no candidate location exists` (or every candidate returned 404) | none of the last 6 months is published (the pack reports a run fails with the list of tried URLs) | Wait; check the landing page above. Do not hand-edit data. See [source-url-not-found.md](source-url-not-found.md). |
| `updated=` is large and `inserted=` small | the publisher revised older months | Normal. No action. |
| `failed=` above 0, or `status=aborted` (`max_error_ratio: 0.0`) | rows failed mapping or parsing | See [import-failures.md](import-failures.md). Do not edit stored rows. |
| Run fails or `failed=` rises after a new publication | the publisher changed the CSV layout (column names, column order, preamble) | See [source-layout-changed.md](source-layout-changed.md). The config fix is a source-owner change. |
| `duplicate natural key` in errors | two rows map to the same `(Area_Code, Date, measure or property type)` | See [import-failures.md](import-failures.md) (first record wins, later ones are rejected). |
| `importer run: DATABASE_URL is not set` (exit 2) | env var missing | `export DATABASE_URL=...` from your secret store. Do not paste the value into shared history. |
| `importer run: source "uk-hpi-average-prices": not found` (exit 1) | sources not applied to this database | Run `bin/importer sources apply --dir <dir>`. |
| `import "...": import already running for this source` (exit 3) | another run holds the source lock | Wait. Do not kill it. See [import-failures.md](import-failures.md). |

## Sizing (for planning)
| Source | Input | Result | Time | Memory |
|---|---|---|---|---|
| `uk-hpi-average-prices` 2026-09 (real file, this task) | 7,600,567 bytes download | `inserted=154707 updated=3402`; 158,109 rows in total | 12.0 s wall (measured) | not measured (`/usr/bin/time` is not installed here) |
| `uk-hpi-average-prices` 2026-09 (pack) | 152,730 rows | 158,109 observations (152,730 price + 5,379 SA), failed=0 | 10.9 s | 28.9 MB peak RSS |
| `uk-hpi-property-type` 2026-09 (pack) | 146,202 rows | 583,776 observations | 39.1 s | not in pack |
| Paging all records through gRPC `ListRecords` (pack) | | | 7.5 s (average prices) and 31.5 s (property type) | |

## Verify
1. Row counts per source (after a run; the fixture run in this task gave `uk-hpi-average-prices` 3402 and `uk-hpi-property-type` 4152):
```
psql "$DATABASE_URL" -c "SELECT source_id, count(*) FROM raw_records WHERE source_id LIKE 'uk-hpi-%' GROUP BY 1 ORDER BY 1"
```
   *(measured on the fixtures: `uk-hpi-average-prices | 3402` and `uk-hpi-property-type | 4152`; after the real 2026-09 file: `uk-hpi-average-prices | 158109`)*
2. Latest month. Each month the rows for the new `Date` should be present (the pack says about 405 areas per month; the real file gave 420 rows for `2026-09-01` across both measures):
```
psql "$DATABASE_URL" -c "SELECT payload->>'Date' AS d, count(*) FROM raw_records WHERE source_id='uk-hpi-average-prices' GROUP BY 1 ORDER BY 1 DESC LIMIT 2"
```
   *(measured after the real 2026-09 file: `2026-09-01 | 420` and `2026-08-01 | 420`)*
3. Count growth: after a new month the count should grow by about one row per area (about 405 to 420 rows). UNVERIFIED: a second consecutive monthly update was not run.
4. Served data (UNVERIFIED on this run): query `ListRecords` for source `uk-hpi-average-prices` with the filter on `dimensions.area_code` and `measure` (see [run-the-grpc-server.md](run-the-grpc-server.md)). The task 016 e2e test asserts 2,487 `average_price` records, 915 `average_price_sa` records and 702 UK rows (pack).

## Missing month: what to do
1. Do not hand-edit or delete rows.
2. Check the landing page `https://publicdata.landregistry.gov.uk/market-trend-data/house-price-index-data/` for the expected file (UNVERIFIED: not opened in this task).
3. Wait and re-run in a few days. No config change is needed: the run picks the newest published month within the last 6.
4. If a file is published but the run still fails, follow the troubleshooting table.

## Roll back / undo
- There is no undo command for a run. Do not delete rows by hand.
- After a pipeline fix, re-parse the stored file without downloading it: `bin/importer reparse --source <id>` (see [reparse-and-file-retention.md](reparse-and-file-retention.md)). UNVERIFIED for the UK HPI sources.

## Escalate
Send: the source id, the full summary line (or the error text), the exit code, the output of `SELECT * FROM import_runs ORDER BY id DESC LIMIT 1` and the first few lines of `import_errors` for that run. Escalate to the source owner (data operations) for layout or mapping changes, and to the on-call engineer for database or binary problems.
