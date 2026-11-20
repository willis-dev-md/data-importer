# Testing strategy

| Layer | Approach |
|---|---|
| Source adapters | Golden-file tests: several CSVs (different delimiters/encodings/BOM/quoted newlines) and recorded API fixtures (pagination types, 429/5xx retry) |
| XLSX / row pipeline | Golden tests on `testdata/boe/baserate-trimmed.xlsx`: 854 events, year fill-down, mixed text/number years and days, `Feb `/`may` months, section switches (rows after `Repo Rate` get that series), `1933-1938 no changes` dropped, hidden-sheet read, unchanged-file skip, reparse equals fresh import. Security: oversized/zip-bomb workbooks rejected |
| Mapping engine | Table-driven unit tests per transform and error code; property test: mapping never panics on arbitrary payloads |
| Store | Integration tests against real Postgres via `testcontainers-go` (upsert idempotency, tombstones, advisory lock) |
| Source groups | Expansion and placeholder validation, override deep-merge, idempotent apply (add/update/disable), 200-member scheduling spread, per-host rate limiting, one failing member does not affect others |
| Scheduler | Injected fake clock: daily/weekly/monthly next-run maths, DST, month-end, `on_missed`, no drift, two replicas never double-run, overlap skip |
| gRPC | In-process server + client; filter translation tests; pagination stability under concurrent import |
| End to end | Import sample sources, then query through gRPC and compare to golden output |
| Contract | `buf lint` and `buf breaking` on protos |

## Quality gates (CI)
`gofmt`, `go vet`, `staticcheck`, `go test -race ./...`, `buf lint`, migration up/down check.

## Definition of done per task
Spec acceptance criteria met, gates green, and independently reviewed.
