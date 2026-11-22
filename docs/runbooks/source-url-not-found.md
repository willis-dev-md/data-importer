# Runbook: source URL not found, resolve failures, archive member errors

**Owner role:** on-call engineer (source owner for the config change)  |  **Severity if this happens:** medium (the source does not import until fixed; a failed run changes no stored data)
**Last verified:** 2026-11-22, task 015 (binary built from the task 015 working tree; config error and `source not found` are real output, the rest is marked in Evidence)

## When to use
A source with `resolve:` (the URL changes every month, for example `Average-prices-2026-09.csv`) or `archive:` (the file is a member of a zip) fails in `importer run`, or `importer sources apply` rejects its `resolve` or `archive` fields.

Example config (from [mapping-spec](../mapping-spec.md#location-resolution-files-whose-url-changes)):
```yaml
location: "https://example.org/files/Average-prices-{ym}.csv"
resolve:
  ym: { kind: month, layout: "2006-01", start: "-1m", back: 6 }   # last month, then 5 earlier
```

## Prerequisites
- `bin/importer` (`make build`) and `DATABASE_URL` (see [local-development.md](local-development.md)). Config errors are only reported when `DATABASE_URL` is set (see Verify).
- Never put tokens in the `location` URL. Query tokens and userinfo are NOT redacted in `_location`, the summary or the errors.
- For the database queries: `psql` and the `import_runs` table ([database-migrations.md](database-migrations.md)).

## Steps
1. Check the config. Nothing is written on error:
   ```
   bin/importer sources apply --dir ./sources
   ```
   *(real output for `back: 0`: `importer sources apply: <dir>/hpi-test.source.yaml: resolve.ym.back: must be between 1 and 120, got 0` then `nothing was written`, exit 1)*
2. Dry run. It reads the source and writes nothing; the URL it chose should appear in the summary as `location="..."` *(evidence pack, not seen in this session)*:
   ```
   bin/importer run --source <id> --dry-run
   ```
3. Real run. On success the summary ends with the chosen URL, `location="<url>"`:
   ```
   bin/importer run --source <id>
   ```
4. Read the chosen URL of the last runs (UNVERIFIED: this SQL was not run in this session):
   ```
   psql "$DATABASE_URL" -c "SELECT id, status, source_meta->>'_location' AS location FROM import_runs WHERE source_id = '<id>' ORDER BY id DESC LIMIT 3"
   ```
   A new month's URL sends no ETag or If-Modified-Since. A `304` closes the run as `unchanged_file`.
5. If the file is not found, check the publisher's names (see the section below), then fix the `location` or `resolve` fields and go back to step 1.

## How candidates are tried
- Candidates are generated from the UTC calendar month, newest first: `start` is the first candidate (`0m` or `-Nm`), `back` is how many (1 to 120). Example: `start: "-1m"`, `back: 3`, `layout: "2006-01"` on 2026-11-22 gives `2026-10`, `2026-09`, `2026-08` (computed from the rule, not run).
- Each candidate: HEAD first; on 405 or 501 a GET with `Range: bytes=0-0`. The first 200 wins. A 404 or 410 moves to the next candidate.
- Any other answer (5xx, 401, 403, timeout, EOF, connection error) is an error for the whole run. The run never falls back to an older file.
- An `https://` redirect to `http://` is refused.
- Local paths and `file://` are checked with a stat (no HTTP).

## Troubleshooting
Labels: **(run)** = real output seen in this session; **(code)** = text copied from the source (`internal/source`, `internal/config`, `cmd/importer`), not seen in a run here; **(evidence)** = validator evidence pack.

| Symptom (exact message) | Diagnosis | Fix |
|---|---|---|
| `importer sources apply: ... resolve.ym.back: must be between 1 and 120, got 0` (exit 1, `nothing was written`) **(run)** | `back` out of range | Set `back` to 1 to 120, re-run step 1 |
| `resolve: no candidate location exists; tried, newest first: <urls>` (URLs at most 20 of 120 characters, then `and N more`) **(code, evidence)** | every candidate answered 404 or 410: the publisher has not published the months tried, or the naming changed | Check the publisher names (section below). If only the newest month is missing, the publisher lags: raise `start` (for example `-2m`) or `back`. If the names changed, change `location`/`layout` |
| `resolve: probing a candidate failed: <url>: <error> (tried, newest first: ...)` **(code, evidence)** | a 5xx, 401/403, timeout or network error on one candidate: an outage or a blocked host. The run stops; it does not use an older file | Retry later. If the error shows `CONNECT tunnel failed, response 403`, go to [blocked-network-host.md](blocked-network-host.md). Do not widen `back` to get around it |
| `refusing redirect from https to a non-https URL: <from> -> <to>` **(code, evidence)** | the publisher redirects the file to `http://` | Do not allow the downgrade. Set `location` to the final `https://` URL (check it in a browser), or escalate to the publisher's owner |
| `location: unresolved placeholder {ym} (no group template value and no resolve entry)` **(code, spec)** | a `{name}` in `location` has no `resolve` entry (or a typo) | Add the `resolve` entry or fix the name. A `resolve` name missing from `location` is also an error |
| `importer run: source "<id>": not found` (exit 1) **(run)** | the source was never applied (or the id is misspelt) | Run `bin/importer sources apply --dir ./sources` first (see [add-a-source.md](add-a-source.md)) |
| `importer sources apply: DATABASE_URL is not set` (exit 2) **(run)** | no database URL: config checks do not run | Export `DATABASE_URL` and repeat step 1 |
| `archive member selection failed: no member matches "<regex>"; members: <names>` **(code)** | `archive.member` matches no name in the zip. The names are listed, quoted, at most 20 of 120 characters | Copy the exact name from the list and fix the regex (unanchored; escape `(` `)` `.`). Note names with repeated spaces |
| `archive member selection failed: <n> members match "<regex>" (exactly one must); matching: <names>` **(code)** | the regex matches more than one member | Make the regex exact, for example with `^` and `$` |
| `archive member selection failed: member <name> is not a regular file (symlink or special entry)` **(code)** | the chosen entry is a symlink or special entry | Publisher problem: escalate with the file name |
| `archive is not a valid zip: ...` or `archive exceeds a safety limit: ...` **(code)** | corrupt, encrypted or truncated zip, or a limit hit (1,000 members, 256 MB per member, ratio 100:1) | Download the file and check it (UNVERIFIED: `python3 -I -m zipfile -l <file>` was not run here). Limits are not raised here: escalate |

## Checking the publisher's file names
Needs network: run these from the operator's machine. Not run in this session (UNVERIFIED).
1. Open the landing page of the publisher (URL in `docs/sources/<name>.md`) and list the file links.
2. Compare the newest name with the pattern: for example `Average-prices-2026-09.csv` matches `Average-prices-{ym}.csv` with `layout: "2006-01"`.
3. UK HPI files are named `<Name>-YYYY-MM.csv` and appear about two months after the data month (task spec 016 and the evidence pack; not checked against the publisher in this session). A 404 for the newest month is then normal: `start` and `back` must reach the month that exists.
4. If the names changed, update `location` and `layout`, re-apply, and run the dry run (steps 1 and 2).

## Behind a blocked host
- Symptom: a candidate probe fails with `CONNECT tunnel failed, response 403` (inside the probe error).
- Follow [blocked-network-host.md](blocked-network-host.md): add the exact host to the environment's allowed domains. Do not replace the URL with a local copy in production configs.
- A file may be on a different host than the landing page: allow both.

## Verify
After a fix, the config check and the dry run must pass. The tagged block below shows the usage rule: with `DATABASE_URL` unset, `sources apply` exits 2 before it checks the files.

<!-- verify -->
```bash
make build >/dev/null
rc=0; env -u DATABASE_URL bin/importer sources apply --dir docs/examples >/dev/null 2>&1 || rc=$?
test "$rc" -eq 2
```

Then, with `DATABASE_URL` set: `bin/importer sources apply --dir ./sources` prints one line per source and exits 0, and `bin/importer run --source <id> --dry-run` shows `location="<url>"`.

## Roll back / undo
1. Restore the previous source file (remove the `resolve` or `archive` block if it was added by the change).
2. Re-apply: `bin/importer sources apply --dir ./sources`.
A failed run stores nothing, so no data needs to be removed.

## Escalate
Source owner (for `location` and `resolve` decisions) or the developer (for limits and the code). Include: the source id, the full error line, the landing-page URL, the last good `_location` from step 4, and the output of `bin/importer sources apply`. Never include a token or a URL with a token.

## Evidence (task 015)
- **Run (real, this session):** `sources apply` with `back: 0` (exit 1, `nothing was written`); `importer run --source hpi-test` for a source that was not applied (`source "hpi-test": not found`, exit 1); `DATABASE_URL is not set` (exit 2, `sources apply`); the tagged verify block.
- **Evidence pack (validator, not repeated here):** candidate order, HEAD/GET fallback, 404 moves on, 5xx is an error, the tried list, `_location` in `source_meta`, 304 handling, the redirect refusal, the archive limits.
- **UNVERIFIED (not run here):** a real run where every candidate returns 404 (the apply step was blocked by a mapping error in my test config, and the database was not started again); the `_location` SQL in step 4; the redirect refusal with a real https server; the member-regex mismatch with a real run (a test zip was built, but the run needs an applied source); the wording of the probe error with `CONNECT tunnel failed`; the landing-page check (no network).
