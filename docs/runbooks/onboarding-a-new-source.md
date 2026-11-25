# Runbook: Inspect a new data source before writing its config

**Owner role:** developer  |  **Severity if this happens:** low
**Last verified:** 2026-11-24, task 021 (schedule section: the invalid-schedule block re-run under `bash -e`, and `sources apply` on a throwaway Postgres 16 for a valid, an unchanged and an `enabled: false` schedule; steps 1 to 7 are from 2026-11-20, 027d2e2, the procedure used for the ONS, Bank of England, UK HPI and ASHE sources)

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

## Schedule (optional)
A source with no `schedule:` is manual only (`importer run`). Add the block to the source file; `docs/examples/uk-hpi-average-prices.source.yaml` is an example:
```
schedule: { every: monthly, day_of_month: 20, at: "10:00", timezone: Europe/London, on_missed: run_once }
```
| Field | Rule |
|---|---|
| `every` | required: `daily`, `weekly` or `monthly` |
| `at` | `HH:MM`, 24-hour, in `timezone`; default `00:00` |
| `day_of_week` | `monday` to `sunday`; required for `weekly`, rejected otherwise |
| `day_of_month` | `1` to `28`, or `last`; required for `monthly`, rejected otherwise (`29`-`31`: UNVERIFIED) |
| `timezone` | IANA name; default `Europe/London` |
| `on_missed` | `run_once` (default: one catch-up run for the latest missed slot) or `skip` |
| `jitter` | Go duration from `0` to `1h` (for example `5m`); default `0s` |
| `enabled` | `false` pauses the source and clears `next_run_at` |

`cron:` is not supported yet. `importer serve` starts scheduled runs (task 022): see [scheduled-imports.md](scheduled-imports.md) for `schedule list`, pausing with `enabled: false`, and the scheduler settings. `importer run` still works by hand.

1. Store the schedule: `bin/importer sources apply --dir <dir>`  *(expected for a valid schedule, real on a throwaway database: `uk-hpi-average-prices: source created, mapping v1 created, next run 2026-12-20T10:00:00Z`, exit 0. Re-applying the same file: `source unchanged, mapping v1 unchanged, next run ...`. With `enabled: false`: `source updated, mapping v1 unchanged, schedule cleared (manual only or disabled)`. A source with no schedule prints no `next run`: UNVERIFIED, not run.)*
2. Check the stored state: `psql "$DATABASE_URL" -Atc 'select id, enabled, next_run_at, schedule from sources'`  *(real after `enabled: false`: `uk-hpi-average-prices|f||{"at": "10:00", "every": "monthly", "jitter": "0s", "timezone": "Europe/London", "on_missed": "run_once", "day_of_month": 20}` (empty `next_run_at`; the schedule is kept))*
3. Invalid schedule: `sources apply` lists every `schedule.<field>` error together, prints `nothing was written` and exits 1. Real output (task 021, schedule `every: hourly`, `timezone: Mars/Base`):
```
importer sources apply: .../uk-hpi-average-prices.source.yaml: schedule.every: must be daily, weekly or monthly, got "hourly"
schedule.timezone: unknown time zone "Mars/Base" (use an IANA name such as Europe/London)
nothing was written
```
   With `every: monthly, day_of_month: last, jitter: 2h` the only error is `schedule.jitter: must be between 0 and 1h0m0s, got 2h0m0s` (exit 1, nothing written; `day_of_month: last` is accepted). With `every: daily, cron: "0 6 * * 1"`: `schedule.cron: not supported yet` (exit 1, nothing written). Real message: `schedule.cron: not supported yet (use every: daily, weekly or monthly)`. Before the rebuild in task 021 the same file failed with `field cron not found in type config.Schedule` (a stale binary). `cron: "0 6 * * 1"` gives `schedule.cron: not supported yet (use every: daily, weekly or monthly)` (exit 1, nothing written).

Verified block (no database; run from the repo root; the invalid schedule must exit 1 with the messages above):
<!-- verify -->
```bash
d=$(mktemp -d); trap 'rm -rf "$d"' EXIT
cp docs/examples/uk-hpi-average-prices.source.yaml docs/examples/uk-hpi-average-prices.mapping.yaml "$d"/
sed -i 's|^schedule: .*|schedule: { every: hourly, timezone: Mars/Base }|' "$d"/uk-hpi-average-prices.source.yaml
rc=0; DATABASE_URL=postgres://nobody:x@127.0.0.1:1/none go run ./cmd/importer sources apply --dir "$d" > "$d/out" 2>&1 || rc=$?
test "$rc" -eq 1
grep -q 'schedule.every: must be daily, weekly or monthly' "$d/out"
grep -q 'schedule.timezone: unknown time zone "Mars/Base"' "$d/out"
grep -q 'nothing was written' "$d/out"
```

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
