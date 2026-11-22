# Runbook: ASHE Table 20 annual release (new provisional or revised edition)

**Owner role:** data source owner (on-call engineer for the run)  |  **Severity if this happens:** medium (the new edition does not import until fixed; the earlier editions keep serving)
**Last verified:** 2026-11-22, task 017 (round 2 state: `make build`, `importer mapping validate` on both example files, and a member-regex check on zips built in the test with the real 2026 and 2025 member names; the download, `sources apply`, `run` and gRPC steps are UNVERIFIED here, see the end)

## When to use
ONS publishes ASHE Table 20 (annual gross pay by age band and 2-digit SOC) once a year, around October: a new **provisional** edition (for example `2026p`) and, a year later, the **revised** edition of the same reference year (for example `2025r`). Use this when:
- a new edition is published and you must add it as a source, or
- a run of an ASHE source fails on the zip member name or the URL, or
- you must check that a new edition loaded correctly.

Each edition is its own standalone source (`ashe-t20-<year><p|r>`). Source groups are **not implemented**: do not use `docs/examples/ashe-t20-annual-pay.group.yaml` (a future design, see `docs/source-groups.md`).

## Prerequisites
- Repo checkout, Go 1.24, `python3`, `make build` (gives `bin/importer`).
- For the load: `DATABASE_URL` for a migrated database (`bin/importer migrate up`, see [database-migrations.md](database-migrations.md)) and `canonical-schema.yaml` in the repo root.
- Network access to `www.ons.gov.uk` for the download and the run (not available to the automated check).
- Never write a secret into a source file. ASHE needs none.

## Steps
1. **Get the new edition's zip** (the URL is the `location:` of the example file; for 2026p it is `https://www.ons.gov.uk/file?uri=/employmentandlabourmarket/peopleinwork/earningsandworkinghours/datasets/agegroupbyoccupation2digitsocashetable20/2026provisional/ashetable202026provisional.zip`). Save it in a new empty directory:
   ```
   d=$(mktemp -d) && curl -fsSL -o "$d/ashe.zip" "<location URL>"
   ```
   *(UNVERIFIED here: no network in this run.)*
2. **List the zip members and confirm exactly one match** for the member regex `(?i)Table 20\.7a .*\.xlsx$`:
   ```
   python3 -I - "$d/ashe.zip" <<'PY'
   import re, sys, zipfile
   rx = re.compile(r'(?i)Table 20\.7a .*\.xlsx$')
   hits = [n for n in zipfile.ZipFile(sys.argv[1]).namelist() if rx.search(n)]
   print("matches:", len(hits), hits)
   assert len(hits) == 1
   PY
   ```
   *(Real output, zips built in the test with the real member names: `member regex matches exactly 1: PROV - Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2026.xlsx` and `member regex matches exactly 1: Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2025.xlsx`. The real ONS zips were checked by the validator, not in this run.)* The 2026 zip holds 22 members; only the `Table 20.7a` one matches, not the `20.7b` CV file.
   If the assert fails, see the troubleshooting rows for the member name.
3. **Create the source and mapping files** in a `sources/` directory (copy the closest existing edition; the 2026p files are the template):
   ```
   mkdir -p sources
   cp docs/examples/ashe-t20-2026p.source.yaml sources/ashe-t20-2027p.source.yaml
   cp docs/examples/ashe-t20-2026p.mapping.yaml sources/ashe-t20-2027p.mapping.yaml
   ```
   Then edit the copies (all four places must agree):
   - source `id:` and mapping `source:` (the same new id; a mismatch is `SOURCE_MISMATCH`);
   - source `location:` (year folder `2027provisional`, zip name `ashetable202027provisional.zip`);
   - source `context: { year: 2027, status: provisional }` (year and status drive `period_start`, `period_end` and the natural key);
   - the comment on line 1-2 if it names the edition.
   Keep `archive.member` as it is unless step 2 failed. For a revised edition use `status: revised`, the `<year>revised` folder and the same pattern.
4. **Validate the mapping** (silent and exit 0 when fine; problems: [mapping-validation-errors.md](mapping-validation-errors.md)):
   ```
   bin/importer mapping validate sources/ashe-t20-2027p.mapping.yaml
   ```
   *(Real, for the example files: no output, exit 0 for `docs/examples/ashe-t20-2026p.mapping.yaml` and `docs/examples/ashe-t20-2025r.mapping.yaml`.)*
5. **Apply the directory** (all-or-nothing; the output format is in [add-a-source.md](add-a-source.md)):
   ```
   bin/importer sources apply --dir ./sources
   ```
   *(UNVERIFIED for ASHE here; expected `ashe-t20-2027p: source created, mapping v1 created`.)*
6. **Dry run** (reads the file, writes nothing):
   ```
   bin/importer run --source ashe-t20-2027p --dry-run
   ```
   *(UNVERIFIED here: needs network.)*
7. **Real run**:
   ```
   bin/importer run --source ashe-t20-2027p
   ```
   Expected (from the validator's run of the two editions): 2026p `inserted=5812`, 2025r `inserted=5816`, `failed=0`, `import_errors=0`. The new edition should be in the same range. *(Not repeated here.)*
8. **Repeat run**: same command again. Expected status `unchanged_file` (the file is skipped, exit 0; see [import-failures.md](import-failures.md)).
9. **Check the load** with the gRPC client in [run-the-grpc-server.md](run-the-grpc-server.md) (`rbclient`, copied into the repo for the check and then deleted): list `ashe-t20-2027p` with `statistic = median` AND `dimensions.age_band = 22-29`, `-f 'statistic|EQ|"median"' -f 'dimensions.age_band|EQ|"22-29"'`. Expected: **36 records per sheet** (validator evidence). `dimensions.population` tells the sheets apart (`All`, `Male Full-Time`, ...). *(UNVERIFIED in this run.)*

## Choosing between editions (series_id is shared)
`series_id` is `ashe20.7a:{sheet}:{age_band}:{soc_code}:{statistic}`: it has no year and no status, so the 2025 revised and 2026 provisional records share a series_id. The importer does no supersession. The client chooses:
1. Filter `period_start = <tax year start>` (for example `2024-04-06` for the tax year ending 2025, revised `2025r` has `period_start 2024-04-06`, `period_end 2025-04-05`).
2. Prefer `status = revised` if a record exists for that period, otherwise `status = provisional`. Or select by `source` id.

## Troubleshooting
| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| `archive member selection failed: no member matches "<regex>"; members: [...]` (code, exit 1) | ONS renamed the member (for example the `PROV - ` prefix changed, or the year or spacing changed). The member list is in the message | Copy the exact name from the list. Fix `archive.member` in every source file of the edition. Re-run step 2. See [source-url-not-found.md](source-url-not-found.md) |
| `archive member selection failed: 2 members match "<regex>" (exactly one must); matching: ...` (code) | regex too broad (a new member also contains `Table 20.7a `) | Anchor the regex to the exact name, for example `^PROV - .*Table 20\.7a .*2027\.xlsx$`, and re-run step 2 |
| The download is not a zip, or `location` returns 404 | ONS moved the file (the URL includes the folder `<year><status>` and the zip name `ashetable20<year><status>.zip`) | Fetch the publication page, copy the new URL into `location:`, then re-run step 2. Exact error text: UNVERIFIED |
| A marker in the workbook that is not `x` or `:` (for example a new symbol) and the run reports failed rows or `mapping_errors` | a new suppression marker form | Add it to `value.null_markers` in the mapping (the list has `"x", "..", ":", "-"`), `sources apply`, then re-run. The exact error for a new marker is UNVERIFIED. The `x` and `:` markers are verified: `value` null and `quality_flag` set, no `mapping_errors` |
| `<file>.mapping.yaml: SOURCE_MISMATCH: mapping says source "ashe-t20-2026p" ...` (exit 1) | the copied mapping still names the old id | Set `source:` to the new id (see [add-a-source.md](add-a-source.md)) |
| Rows rejected as `duplicate natural key` after copying a file | `year` or `status` in `context:` was not changed, so the natural key repeats (UNVERIFIED for ASHE) | Set `context:` to the new year and status. See [import-failures.md](import-failures.md) |
| You copied the group file `ashe-t20-annual-pay.group.yaml` or try `importer group` for ASHE | source groups are not implemented | Use one standalone source per edition. Exact message UNVERIFIED |

## Verify
- Step 2 prints exactly one member and the assert passes.
- Step 8 reports `unchanged_file` on the second run.
- Step 9 returns 36 `median` records for `22-29` per sheet.
- Record counts in step 7 are in the range above (2026p 5812, 2025r 5816 by the validator).

Self-contained check (no network, no database): builds two zips in a temporary directory with the real member names (plus a constructed decoy member), checks the regex, and validates both example mappings:
<!-- verify -->
```bash
set -e
make build >/dev/null
bin/importer mapping validate docs/examples/ashe-t20-2026p.mapping.yaml
bin/importer mapping validate docs/examples/ashe-t20-2025r.mapping.yaml
d=$(mktemp -d)
python3 -I - "$d" <<'PY'
import sys, zipfile, os
d = sys.argv[1]
editions = {
  "2026p.zip": "PROV - Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2026.xlsx",
  "2025r.zip": "Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2025.xlsx",
}
for zname, member in editions.items():
    with zipfile.ZipFile(os.path.join(d, zname), "w") as z:
        z.writestr(member, b"")
        z.writestr("Constructed decoy name - Table 20.7b CV.xlsx", b"")
PY
for z in 2026p 2025r; do
python3 -I - "$d/$z.zip" <<'PY'
import re, sys, zipfile
rx = re.compile(r'(?i)Table 20\.7a .*\.xlsx$')
hits = [n for n in zipfile.ZipFile(sys.argv[1]).namelist() if rx.search(n)]
assert len(hits) == 1, hits
print("member regex matches exactly 1:", hits[0])
PY
done
rm -r "$d"
```

## Roll back / undo
- A wrong mapping: mappings are versioned. Re-apply the previous content of the file (it becomes a new active version), see [add-a-source.md](add-a-source.md).
- A wrong load: `importer rollback --run <id>` exists in the CLI; its behaviour for ASHE is UNVERIFIED, see [import-failures.md](import-failures.md).
- Removing a source: UNVERIFIED, not recommended.

## Escalate
Escalate to the importer maintainers when the zip has a different layout than the Table 20.7a workbook (new sheets, columns or header rows: see [source-layout-changed.md](source-layout-changed.md)), when `archive member selection failed` persists after the regex fix, or when a run fails with a message not listed here. Include the edition (`2026p` or `2025r`), the member list from the error, the command and exit code, and the run id. Do not include the URL query secrets (there are none for ONS).
