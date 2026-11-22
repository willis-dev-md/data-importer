# 015 – Zip archives and location resolution

## Goal
Sources whose file sits inside a zip, or whose URL changes every month, can be configured without code.

## Depends on
013

## Design refs
`docs/mapping-spec.md` ("Archives", "Location resolution"), `docs/sources/uk-hpi.md`, `docs/sources/ons-ashe-table20.md`

## Scope
`internal/config/`, `internal/source/` (archive reader, resolver), tests, small zip fixtures built in the tests

## Out of scope
Other archive formats (tar, 7z), scraping landing pages for links (explicitly not in v1: the `resolve` candidate approach is the design).

## Acceptance criteria (each must be checkable)
- [ ] `archive: {type: zip, member: <regex>}`: exactly one member must match, otherwise an error listing the member names; the chosen member is streamed (not extracted to disk); member names containing `..`, absolute paths or odd characters (as in `PROV - Age by Occupation SOC20 (2) Table 20.7a   Annual pay - Gross 2026.xlsx`, with repeated spaces) are matched and never used as paths.
- [ ] Zip safety: limits on member count (default 1,000), uncompressed size (256 MB) and compression ratio (100:1); tests for a zip bomb, a nested zip (not expanded), a symlink entry (ignored/rejected), and path traversal names.
- [ ] `resolve: {<name>: {kind: month, layout, start, back}}`: generates candidates newest first (`start: "-1m"` is one month before now, `back: 6` gives six candidates), substitutes `{name}` in `location`, probes with HEAD (falling back to a ranged GET when HEAD is not allowed), and uses the first that returns 200. A fake clock is injected. Tests: newest candidate exists; only an older one exists; none exist (error lists all URLs tried); a 5xx on a candidate is an error, not a "not found".
- [ ] The chosen URL is stored on the run (`import_runs.source_meta._location`) and shown by `importer run`.
- [ ] Group templating (`{year}{status}`) and `resolve` placeholders coexist: unresolved `{...}` placeholders left in a location after expansion fail validation.
- [ ] Fetching refuses a redirect from `https://` to `http://` (downgrade), with a clear error and a test (found as a note in task 004's validation; the task 004 fetcher currently follows it).
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/mapping-spec.md (Archives, Location resolution as implemented); CHANGELOG.md

## Runbook impact
docs/runbooks/source-url-not-found.md (resolve failures, the URLs tried, blocked network hosts; link to blocked-network-host.md)

## Status
validated
## Log

### Worker round 1 (code)
Files: `internal/config/archive.go` (+ test), `internal/config/source.go` (fields `archive`, `resolve`, validation hook),
`internal/source/archive.go`, `resolve.go` (+ tests), `internal/source/fetch.go` (redirect rule, `FetchOptions.Now`),
`csv.go`/`xlsx.go` (archive + resolve in `Open`/`OpenReader`), and, beyond the literal scope because the acceptance
criteria need them: `internal/importer/files.go`, `importer.go` (resolve before fetch, `_location` on the run, 304 baseline;
test `internal/importer/archive_test.go`) and `cmd/importer/runcmd.go` (`summary` prints `location="..."`; test). No change to
`cmd/importer` registration was needed: `sources apply` and `run` parse configs with `config.ParseSource`, which now knows the keys.
No new dependency (stdlib `archive/zip`); go.mod untouched.

Decisions
- **Composition:** `ResolveLocation` (HEAD probes, no validators involved) picks the URL first, then the normal conditional fetch runs
  on that URL. The baseline for 304 is the validators of the file last fetched from the SAME URL: the importer reads
  `_location` from the run that stored the last file (`store.GetRun(file.RunID)`, via an optional interface so fakes keep working) and
  drops `If-None-Match`/`If-Modified-Since` when the chosen URL differs (a new month never gets a stale 304); `content_hash` still
  catches an identical file. An unchanged run (304 / same hash) records `_location` too. Reparse never touches the network and records no `_location`.
- **source_files for archives:** one row per (source, SHA-256 of the ARCHIVE file), `size_bytes` = archive size, etag/last_modified of
  the chosen URL; the file store keeps the archive, not the member. The member is picked again on every parse/reparse.
- **Archive reading:** a zip needs random access, so the (size-limited, default 256 MB `FetchOptions.MaxBytes`) archive is spooled to
  an anonymous temp file (`os.CreateTemp`, generated name, removed on Close/failure); the chosen member is inflated on the fly, never
  extracted; member names are only matched (regex on the raw name), never used as paths; `..`/absolute names are matched, the
  `zip.ErrInsecurePath` result is ignored on purpose. The xlsx adapter still reads the member into memory (its own 100 MiB workbook limit
  applies to the member; for archive sources the fetch limit is the archive limit).
- **Limits:** 1,000 members, 256 MB uncompressed per chosen member (declared AND actually inflated), ratio 100:1 (+1 MiB slack for tiny
  members), zip directory read capped at 4 MiB (bounds memory of a directory bomb), encrypted members and symlink/special entries
  rejected, nested zips never opened. The member stream is checked against the real bytes: more than declared, over limit, over
  ratio, or short at EOF is an error (archive/zip itself often reports a lying header first, as `ErrArchiveBad`). Errors: `ErrArchiveBad`, `ErrArchiveLimit`, `ErrArchiveMember`; member-name lists in errors: quoted, max 20 names, 120 chars each.
- **Resolve:** one placeholder per source (config error for more); UTC calendar months from `FetchOptions.Now` (nil = `time.Now`);
  `start` must be `0m` or `-Nm` (N 1..1200), `back` 1..120, `layout` must name year and month and render only `[A-Za-z0-9._-]`.
  Only 404/410 mean "not found"; 405/501 on HEAD falls back to `GET` with `Range: bytes=0-0` (200/206 = exists); any other
  status, a network error or a 5xx is `ErrResolveProbe` (never falls back to an older file; names the URL and those tried so far);
  nothing found is `ErrNoCandidate` listing every URL. Local paths/`file://` are probed with stat. The chosen URL goes to
  `import_runs.source_meta._location` (also for dry-run via the stream `Meta`) and `importer run` prints it.
- **Placeholders:** `{...}` left in `location` that is not a `resolve` name fails validation (`location: unresolved placeholder {year}`);
  a `resolve` name missing from the location also fails. Group templating itself does not exist in code yet; the check runs on the
  expanded source, so it covers it.
- **Redirects:** any redirect target that is not https is refused (`ErrRedirectDowngrade`) in fetch and probe; the caller's client is
  copied, its own `CheckRedirect` still runs.

Not done / for other roles: docs/mapping-spec.md, CHANGELOG, runbook (docs impact below). The docs examples `uk-hpi-*` use `table:` on csv, still rejected by validation (pre-existing).

### Worker round 2
Validator defect: `TestArchiveTooLarge` leaked a spool file per run. Fixed: the test closes the reader (other tests checked, none leaked);
`TestMain` in `internal/source` and `internal/importer` sets TMPDIR to a fresh dir and fails the package if an `importer-archive-*` file remains;
the spool file is unlinked right after creation (descriptor kept; fallback: name kept and removed on Close if unlink fails), test asserts
nothing is visible in TMPDIR while the reader is open; the "tried URLs" list in resolver errors is capped (20 entries x 120 chars, "and N more"), tested.
- 2026-11-22 validator r1: FAIL (one test leaked a 0600 `importer-archive-*` spool file in TMPDIR on every run; the implementation was otherwise sound: 40 hostile archives bounded, resolver edge cases, composition with change detection, https-to-http redirect refused). Worker r2: the test closes its reader, the spool is unlinked right after creation (kill -9, panic and SIGTERM leave nothing), a TestMain guard in internal/source and internal/importer fails on leftovers, the tried-URL list is capped (20 x 120 chars). validator r2: PASS (mutations: the guard fails when the leak or the unlink is re-introduced; the cap test fails when the cap is removed).
- documentor and runbook-refiner (budgets, evidence packs) ran; a gap-fix agent for two stale doc lines ran out of its small budget without editing, so the orchestrator made those three one-line edits directly (mapping-spec supported kinds; operations `location=` note). It also corrected an orchestrator error: `location=` prints only when a `resolve` chose a URL. Deterministic docs check PASS.
- Notes: URL query tokens/userinfo are not redacted in `_location`/summary/errors (policy: configs never hold secrets); `{{ym}}` passes validation; group templating does not exist (so the unresolved-placeholder check runs on the expanded source); not verified: a 256 MB member under 100:1, redirect loops, the unlink-failure fallback, 0600 and concurrent-reader tests, real HPI URLs.
