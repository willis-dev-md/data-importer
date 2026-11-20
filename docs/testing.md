# Testing strategy

| Layer | Approach |
|---|---|
| Source adapters | Golden-file tests: several CSVs (different delimiters/encodings/BOM/quoted newlines) and recorded API fixtures (pagination types, 429/5xx retry) |
| Mapping engine | Table-driven unit tests per transform and error code; property test: mapping never panics on arbitrary payloads |
| Store | Integration tests against real Postgres via `testcontainers-go` (upsert idempotency, tombstones, advisory lock) |
| gRPC | In-process server + client; filter translation tests; pagination stability under concurrent import |
| End to end | Import sample sources, then query through gRPC and compare to golden output |
| Contract | `buf lint` and `buf breaking` on protos |

## Quality gates (CI)
`gofmt`, `go vet`, `staticcheck`, `go test -race ./...`, `buf lint`, migration up/down check.

## Definition of done per task
Spec acceptance criteria met, gates green, and independently reviewed.
