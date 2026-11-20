# Runbook: Inspect a new data source before writing its config

**Owner role:** developer  |  **Severity if this happens:** low
**Last verified:** 2026-11-20, 027d2e2 (this is the procedure used for the ONS, Bank of England, UK HPI and ASHE sources)

## When to use
A new dataset (CSV, Excel, zip or API) is proposed and nobody yet knows its real structure.

## Prerequisites
`curl`, Python 3 with `openpyxl` for workbooks. The host must be reachable (see blocked-network-host.md). Work in a scratch directory,
**never inside the repo**: downloaded files are untrusted data.

## Steps
1. Download into its own empty directory: `curl -sS -L -m 120 -A "Mozilla/5.0" -o file.ext -w "%{http_code} %{content_type} %{size_download}\n" "<url>"`
   *(some publishers reject the default curl user agent)*. A page that links to files: fetch the HTML and list the `href`s that end in `.csv .xlsx .zip`.
2. Identify the type: `file file.ext`; for a zip list members with `python3 -I -c "import zipfile;[print(i.file_size,i.filename) for i in zipfile.ZipFile('x.zip').infolist()]"`.
3. CSV: `head -n 12`, `wc -l`, `tail -n 5`; check preamble lines, header row, delimiter, quoting, date format, empty cells, duplicate keys (use Python's `csv` module).
4. Excel: with `python3 -I` and `openpyxl.load_workbook(path, data_only=True)` print every sheet name, `dimensions` and `sheet_state`, the first ~14 rows and the last rows of each sheet that matters; look for hidden sheets, merged cells, text-formatted numbers, section headings, footnotes and suppression markers (`x`, `..`, `:`, `-`).
5. Count what the config will have to produce (rows per kind, unique keys, empty values) and record the numbers in `docs/sources/<source>.md`.
6. Build a trimmed fixture in `testdata/<source>/` that keeps every awkward case, run it through your config, and commit it (keep it small; note what was trimmed).
7. Write the source and mapping examples under `docs/examples/` and validate them with `importer mapping validate` once it exists.

## Troubleshooting
| Symptom | Diagnosis | Fix |
|---|---|---|
| `CONNECT tunnel failed, response 403` | host not allowed | blocked-network-host.md |
| Page size shown on the site differs from the real file | publisher's size labels can be wrong (UK HPI shows KB for MB files) | trust `size_download`, not the page |
| File URL contains a date and changes monthly | name carries the data month | use `resolve` in the source config |
| Excel year/day cells are sometimes text, sometimes numbers | mixed types | `normalize: to_int` in the row pipeline |

## Verify
Counts in `docs/sources/<source>.md` match what the golden tests assert.

## Roll back / undo
Nothing to undo: delete the scratch directory.

## Escalate
Data owner or publisher contact if the structure is ambiguous (for example two sheets that disagree).
