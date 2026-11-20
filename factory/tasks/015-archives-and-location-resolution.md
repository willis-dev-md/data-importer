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
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/mapping-spec.md (Archives, Location resolution as implemented); CHANGELOG.md

## Runbook impact
docs/runbooks/source-url-not-found.md (resolve failures, the URLs tried, blocked network hosts; link to blocked-network-host.md)

## Status
pending
## Log
