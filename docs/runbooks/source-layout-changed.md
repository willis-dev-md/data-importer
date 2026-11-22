# Runbook: a publisher changed the file layout (xlsx or csv)

**Owner role:** on-call engineer (data onboarding)  |  **Severity if this happens:** medium (a run is `aborted` or `failed`, or it "succeeds" with the wrong rows, so data is stale or wrong for that source)
**Last verified:** 2026-11-22, d8652aa (case 2 re-run on a COPY of `testdata/boe/baserate-trimmed.xlsx` with a throwaway Postgres: `make build`, `migrate up`, `sources apply`, dry run, real run, `\d import_errors`, the duplicate query and the counts; cases 1, 3, 4, 5 from bf33011 on modified copies; the verify blocks re-run the read-only checks)

## When to use
A run of an xlsx or csv source is `aborted` or `failed` with row errors that were not there before, or the record count changed without a config change (for example 854 became 862 rejected, or 642 became 643), or a new file from the publisher no longer matches the source config (sheet name, column letters, preamble length, title rows).

## Prerequisites
- `bin/importer` (`make build`), `psql`, `DATABASE_URL` set (never paste it).
- `python3` with `openpyxl` to read workbooks (read-only; never save over the publisher's file).
- The old and the new file. Never edit the file the source reads from: copy it into a scratch directory first.

## Steps
1. Keep the previous file and copy the new one into an empty scratch directory. Point a copy of the source config at the copy (`location:`) and do not change the live config yet.
2. Compare sheet names and states (xlsx). Expected output for the BoE fixture (the verify block below):
   ```
   [('HISTORICAL SINCE 1694', 'visible'), ('Raw Data', 'hidden')]
   ```
3. Compare the top rows and column letters of the main sheet (xlsx), read-only. The BoE layout the config expects: row 7 is `Bank Rate`, row 8 is the first data row (`1694`, `Oct`, `6`), columns A year, B day, C month, D rate:
   ```bash
   python3 -I -c "import openpyxl; wb=openpyxl.load_workbook('<new file>.xlsx', read_only=True); ws=wb['<sheet>']; [print(i, r) for i, r in enumerate(ws.iter_rows(min_row=1, max_row=12, max_col=4, values_only=True), 1)]"
   ```
   *(run with the fixture: row 8 printed `('1694', None, 'Oct', 6)`)*
4. Compare the csv preamble (csv): the config says `preamble: { lines: 8 }`, so line 9 must be the first data row. Expected for `testdata/ons/l55o.csv`:
<!-- verify -->
```bash
sed -n '8,9p' testdata/ons/l55o.csv
```
   *(expected: `"Important notes",` then `"1989","5.7"`)*
5. Compare the record count with the last good run: `inserted + unchanged` of the last `succeeded` run must match the new count, or the reason must be known (the publisher added history).
6. Check the rejects and the counts after a test run (step "Read the result" below). Find the row in the Troubleshooting table; if none fits, go to Escalate.

**Read the result** (from the run that used the copy; `<id>` is the source id):
```
psql "$DATABASE_URL" -c "SELECT id, source_id, status, inserted, updated, unchanged, failed, left(error,70) AS err, shape::text FROM import_runs WHERE source_id = '<id>' ORDER BY id DESC LIMIT 3"
psql "$DATABASE_URL" -c "SELECT position, raw, reason FROM import_errors WHERE run_id = <run id> ORDER BY id LIMIT 10"
psql "$DATABASE_URL" -c "SELECT source_id, count(*) FROM raw_records WHERE source_id = '<id>' GROUP BY 1"
```
*(real output for these shapes, case 2 on the copy: run 1 `status=aborted inserted=773 failed=89`; `import_errors` rows like `579 | HISTORICAL SINCE 1694!579: | 1915 | no |  | changes | normalize: column "day": "no" is not a number`; `raw_records` count `773`)*

**Duplicate keys (case 2):** when key columns shift, rows collapse onto one natural key. The duplicate rows are the ones to read (`position` = the later record):
```
psql "$DATABASE_URL" -c "SELECT position, reason FROM import_errors WHERE run_id = <run id> AND reason LIKE 'duplicate natural key%' ORDER BY id LIMIT 10"
```
*(real, run 1 on the copy: `11 | duplicate natural key (first seen at 10)`, `26 | duplicate natural key (first seen at 25)`, `31 | duplicate natural key (first seen at 30)`, ... 10 rows. `\d import_errors` (real) lists the columns `id, run_id, position, raw, reason`. `ORDER BY id` was added to the query so the rows come in order.)*
`<run id>` is the `run=N` value of the summary line. Compare the first and the later record of a pair: the first one is the one stored.

`shape` lists, per field, the observed value types and counts. It only shows type changes. In these xlsx cells every value is stored as text, so a text column that moves into another text field gives the same shape (see case 2).

## Troubleshooting
Every message below is real output from a run on a modified copy (the copies were made from the fixture with openpyxl; the fixture was not changed). The fix is a config change; apply it with `bin/importer sources apply --dir <dir>` (see [add-a-source.md](add-a-source.md)), then the dry run and the real run in Verify.

| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| Case 1 (sheet renamed). `importer run --source boe-sheet` prints `importer run: import "boe-sheet" run 2: open source: xlsx.sheet: sheet "HISTORICAL SINCE 1694" not found; available sheets: "HISTORY SINCE 1694", "Raw Data" (hidden)`, exit 1; the run row is `failed` with `inserted=0` | The error lists the sheet names the file has now; hidden ones are marked `(hidden)`. Confirm with step 2. | Set `xlsx.sheet` to the listed name (or `sheet_index`, 0-based, if the position did not change), then re-apply and run. |
| Case 2 (column inserted before the rate column; reproduced with openpyxl `insert_cols(3)` on a copy of the fixture, `docs/examples/boe-baserate.source.yaml` unchanged except `location`, source id `boe-bank-rate`). Dry run: `source=boe-bank-rate status=aborted inserted=0 updated=0 unchanged=0 failed=89 run=dry-run duration=52ms`, stderr `importer run: import "boe-bank-rate" run 0: import aborted: 89 of 862 rows failed, above max_error_ratio 0`, exit 1, nothing written. Real run: `status=aborted inserted=773 updated=0 unchanged=0 failed=89 run=1`, same stderr with `run 1`. Rejects: 81 × `duplicate natural key (first seen at 10)` (the later record, position 11), and 8 × `normalize: column "day": "no" is not a number` (raw `HISTORICAL SINCE 1694!579: \| 1915 \| no \|  \| changes`: the text `no changes` is split, `no` lands in the day column and `changes` in the rate position) | Letters shifted: column C (month) is now blank and D (rate) holds the old month text. So records for different months share one natural key (`series_name, year, month, day` with an empty month). Only the first record of each key is stored; each later one is rejected as a duplicate. Check: 773 stored + 81 duplicates + 8 normalize errors = 862 rows read (the 81 + 8 = 89 `failed`). Confirm with the duplicate query above and compare step 3: the first data row must have the rate in column E. `shape` did NOT show it (same as the good run). | Change `columns` to the new letters: for one column inserted at C: `columns: { A: year, B: day, D: month, E: rate }`. Dry run until `failed=0`, then the real run. Keep `max_error_ratio: 0.0` so the next change is seen. |
| Case 3 (one title row added above the table; reproduced with openpyxl `insert_rows(1)` and text in A1). `status=succeeded inserted=854 failed=0`, same counts as before, no error | Silent. The shift moved the data down one row, so `start_row: 8` now reads the old `Bank Rate` heading row, which the `section` step consumes. Counts stay right by chance: one heading row is shifted into `start_row`. Check the first record's `_source.row` (should be the real row of `1694`; 8 before the change, 9 after): UNVERIFIED (the query was not run, see below). | Set `xlsx.start_row` to the first data row of the new file (9 here), dry-run, real run. Counts are not proof: check step 3 for every change. A title row shift larger than the heading block may drop rows silently: compare counts with the publisher's own total. |
| Case 4 (month spelled `Sept` in 67 cells, reproduced with openpyxl). `status=succeeded inserted=854 failed=0` | The import stores the text as is; the spelling is only checked when the mapping parses the period (`PARSE_FAILED` on `Eval`, see [mapping-validation-errors.md](mapping-validation-errors.md)). The mapping `format` list (`"2 Jan 2006"`, `"Jan 2006"`) has no `Sept`. Mapping `PARSE_FAILED`: UNVERIFIED (not run; the mapping was not evaluated on this data). | Add the new spelling to the mapping's `period` `format:` list if Go layout can express it (UNVERIFIED), or fix the source file. Then `bin/importer mapping validate <mapping>` (exit 0), then re-apply and run. If the spelling cannot be expressed, escalate (new task). |
| Case 5 (csv: one line added above the preamble; `testdata/ons/l55o.csv` copy with the line `"Notice","..."` at line 1). `status=succeeded inserted=643` where the good file gives 642 | The preamble is `lines: 8` (config), so line 8 of the new file (`"Important notes",` of the original) is read as a data row: one extra record and no error. Metadata (CDID, Unit) still parse because they are above line 8. Contents of the extra record: UNVERIFIED (not queried). | Set `csv.preamble.lines` to the new preamble length (9 here), dry-run, real run, expect 642 `inserted`/`unchanged` again (`UNVERIFIED`: the fixed config was not run). |
| `importer run: import "<id>" run N: open source: ... ` (not a layout error) | not this runbook | see [import-failures.md](import-failures.md) |

Abort rule for all of these: `max_error_ratio: 0.0` (BoE) aborts on the first reject. Do not raise it to get the run through; fix the layout first.

**Explained in case 2 (was UNVERIFIED):** the lower count `773` (not 854) is the 81 duplicate records. Real run and count check: 773 + 81 + 8 = 862. That the 81 collapsed records have different content is from the validator's evidence (task 018 log), not re-checked here.

**Not reproduced (UNVERIFIED):** the `_source.row` check of case 3; the `raw_records.payload` of cases 2-5. An earlier query failed with the real error `column "id" does not exist` (the table has no `id`; its columns are `source_id, natural_key, payload, content_hash, first_run_id, last_run_id, imported_at, deleted_at`). For the payload, use `SELECT natural_key, payload FROM raw_records WHERE source_id = '<id>' ORDER BY natural_key LIMIT 3` (UNVERIFIED: not run).

## Verify
1. Dry run, then real run on the fixed config (dry-run prints zeros; the real run prints the counts):
   ```
   bin/importer run --source <id> --dry-run
   bin/importer run --source <id>
   ```
   *(UNVERIFIED for the fixed configs. Expected for the BoE fixed config on the fixture: `inserted=854 failed=0`; a repeat gives `unchanged=854`, from [add-a-source.md](add-a-source.md) and task 013.)*
2. The status is `succeeded`, `failed = 0` and the count equals the expected count from step 5 of Steps.
3. The verify blocks below still hold (the fixture and the sheet name).

<!-- verify -->
```bash
python3 -I -c "import openpyxl; wb=openpyxl.load_workbook('testdata/boe/baserate-trimmed.xlsx', read_only=True); print([(ws.title, ws.sheet_state) for ws in wb.worksheets])"
```
*(expected: `[('HISTORICAL SINCE 1694', 'visible'), ('Raw Data', 'hidden')]`)*

<!-- verify -->
```bash
test -n "$(grep -e 'available sheets' internal/source/xlsx.go)" && test -n "$(grep -e 'ErrXLSXBadArchive' internal/source/xlsx.go)" && echo ok
```
*(expected: `ok`)*

## Roll back / undo
- A config change: restore the previous source/mapping content and re-apply (`bin/importer sources apply --dir <dir>`). It becomes a new active version (see [add-a-source.md](add-a-source.md)).
- Rows written by a bad run stay in `raw_records`, tagged with `first_run_id`/`last_run_id`. Do not delete them by hand; a later good run updates them (idempotent). Find what a run touched with `SELECT ... FROM raw_records WHERE last_run_id = <run id>`.

## Escalate
Escalate (a new task, not a config fix) when:
- the publisher changes the structure in a way the config cannot express: columns swapped or moved per section, a second table in the same sheet, a changed number of columns per row, date serials that must become dates, new spellings that the mapping `format` cannot parse;
- a count changes with no visible row change, or the first-row check cannot be made;
- the same run gives a new message not in this table.

Send: the old and new file (or their first 12 rows and sheet names), the source id, run id(s), the `import_runs` row, the `import_errors` rows (first 10), and the config diff. Do not send the database URL or any secret.
