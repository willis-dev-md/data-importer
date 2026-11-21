# 004 – Source config and CSV adapter (plain files)

## Goal
A `csv` source can be read from a file or HTTPS URL into a stream of records, with bad rows captured instead of aborting.

## Depends on
003 (uses `record`); can run in parallel with 002 and 003 if `record` is stubbed, otherwise after.

## Design refs
`docs/mapping-spec.md` (Source: CSV), `docs/design.md` (components, §9), `docs/testing.md`

## Scope
`internal/config/` (source config types, YAML load, validation), `internal/source/` (adapter interface, CSV reader, fetcher), `testdata/csv/*`, tests

## Out of scope
Preamble/headerless handling (task 005), XLSX, HTTP JSON, mapping, DB writes, retry/backoff for HTTP beyond a single timeout.

## Acceptance criteria (each must be checkable)
- [ ] `config.Source` parses/validates YAML for `kind: csv` (fields per `docs/mapping-spec.md`: `id`, `location`, `csv.delimiter/header/encoding/columns`, `natural_key`, `max_error_ratio`). Validation errors name the field path and are tested; unknown YAML keys are rejected (strict decoding); `docs/examples/acme-csv.source.yaml` loads.
- [ ] `source.Adapter` interface: `Open(ctx, cfg) (Stream, error)`; `Stream.Next() (Item, bool)` / `Err()`/`Close()`, where an `Item` is either a `Record{Position, Fields map[string]any, NaturalKey}` or a `RowError{Position, Raw, Reason}`. Streaming: the whole file is never held as `[]Record`.
- [ ] Fetch supports local paths, `file://` and `https://` (timeout, max size default 256 MB, follows redirects, sets a descriptive `User-Agent`) and returns bytes streamed plus SHA-256 of what was read. Tests use `httptest` servers and `file://`; no real network.
- [ ] CSV features, each covered by a fixture + test: delimiters (`,` `;` tab `|`), header row and explicit `columns`, quoted fields with embedded delimiters/newlines/quotes, UTF-8 BOM, `windows-1252` and `utf-16` via `golang.org/x/text`, CRLF vs LF, blank lines skipped.
- [ ] A row with the wrong field count, or a bad quote, yields a `RowError` (with line number and raw text) and the stream **continues**.
- [ ] Natural key is built with `record.Key` from the configured fields; a missing/empty key field yields a `RowError`.
- [ ] A fuzz test (`go test -fuzz` seed corpus runs in normal `go test`) shows the reader never panics on arbitrary bytes.
- [ ] `scripts/validate.sh` passes.

## Docs impact
docs/mapping-spec.md (CSV options as implemented, strict config validation); CHANGELOG.md

## Runbook impact
none: adapter library; operators meet its failures through `importer run`, whose runbook is written in task 009

## Status
validated
## Log
- Worker round 1: config in `internal/config/source.go`; adapter, fetch and csv reader in `internal/source/`; fixtures in `testdata/csv/`.
- Decisions: own CSV row scanner (not `encoding/csv`) so `RowError.Raw` is exact and a bad row does not hide later rows; row positions are the 1-based starting line.
- Only `kind: csv` validates for now (other kinds are rejected with a clear message until their tasks); `preamble` is not accepted yet (task 005). `schedule` and `change_detection` are accepted and carried, not interpreted.
- `https://` only for URLs (plain `http://` is rejected); default timeout 5 min covers the body; max size default 256 MB enforced on Content-Length and while reading.
- Encodings: utf-8 (BOM stripped), windows-1252, utf-16/utf-16le (BOM honoured, LE default), utf-16be. Invalid UTF-8 becomes U+FFFD. A row over 16 MiB is a fatal stream error. NUL bytes: task 008.
- Natural key fields must be columns (checked at Open, or on the first row for headerless files without `columns`); "empty" means blank after trimming whitespace.
- Added direct dependencies `golang.org/x/text` v0.28.0 and `gopkg.in/yaml.v3` v3.0.1 (both approved, previously indirect).
- 2026-11-21 validator r1: PASS (own row scanner probed against Python csv; 100 MB streaming test; fuzz 20 s). Runbook `none` accepted, so runbook-refiner skipped. documentor ran; deterministic docs check: PASS (no agent docs-check pass needed).
- Notes carried forward: https->http redirect is followed (added to task 015 criteria); CRLF in a quoted field normalised to LF in Raw; unterminated quote swallows the rest of the file (matches Python); lone CR not a line end. All stated in docs/mapping-spec.md.
