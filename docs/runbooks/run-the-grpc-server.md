# Runbook: run the gRPC server (`importer serve`)

**Owner role:** on-call engineer  |  **Severity if this happens:** medium (the server is down or `NOT_SERVING`; a client gets wrong or empty results)
**Last verified:** 2026-11-21, task 011 (ran `make build`, `importer migrate up`, `sources apply` and `run` for the ONS example, `importer serve` plaintext with `SCAN_LIMIT` unset and `SCAN_LIMIT=100`, a Go gRPC client for `ListRecords` and `GetRecord`, a `psql` UPDATE to set `deleted_at` in the throwaway database, a `pg_ctl` immediate stop and restart of that database, and the startup errors below; throwaway Postgres from `scripts/testdb.sh`)

## When to use
Start, check or stop `importer serve`; health is `NOT_SERVING`; the process exits at startup; a client cannot connect; a client calls `DataService` (`ListRecords`, `GetRecord`) and gets an error, fewer records than expected, or a short page.
Reference: the `importer serve` section of [operations.md](../operations.md).

## Prerequisites
- `bin/importer` built (`make build`) and the schema applied (`importer migrate up`, see [database-migrations.md](database-migrations.md)).
- `DATABASE_URL` set. Optional: `GRPC_ADDR` (default `:8443`), `TLS_CERT_FILE` + `TLS_KEY_FILE` (both or neither), `SCAN_LIMIT` (default `50000`, see step 8).
- For data calls: the source applied with a mapping and imported (`sources apply`, `run`; see [add-a-source.md](add-a-source.md)). A source without an active mapping answers `FailedPrecondition`.
- A health client and a data client. Neither `grpc_health_probe` nor `grpcurl` was installed when this was verified, so small Go clients were used (see Verify). `openssl` was installed and is used for the certificate.

## Steps
1. Build and migrate. *(expected: `version 2` from `migrate up` on a fresh database; your version may differ)*
```bash
make build
eval "$(scripts/testdb.sh start)"   # local only; in production set DATABASE_URL yourself
bin/importer migrate up
```
2. Start plaintext (local use only). *(expected log: `level=WARN msg="TLS_CERT_FILE and TLS_KEY_FILE are not set: serving plaintext gRPC" addr=127.0.0.1:18443`)*
```bash
GRPC_ADDR=127.0.0.1:18443 bin/importer serve
```
3. Or start with TLS. Make a self-signed certificate (1 day, valid for `localhost` and `127.0.0.1`), then start. *(expected log: `level=INFO msg="serving gRPC with TLS" addr=127.0.0.1:18443`)*
<!-- verify -->
```bash
d=$(mktemp -d) && openssl req -x509 -newkey rsa:2048 -nodes -keyout $d/key.pem -out $d/cert.pem -days 1 -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" 2>/dev/null && ls $d | tr '\n' ' '; rm -rf $d
```
```bash
GRPC_ADDR=127.0.0.1:18443 TLS_CERT_FILE=cert.pem TLS_KEY_FILE=key.pem bin/importer serve
```
4. Check health (see Verify). `SERVING` means the database answered a ping; `NOT_SERVING` means it did not.
5. Stop with `Ctrl-C` (SIGINT) or `kill -TERM <pid>`. In-flight calls get up to 10 s. *(expected log: `level=INFO msg="server stopped"`, exit code 0)*
6. Load the ONS example once (skip if `sources` already has `ons-l55o`). It uses `testdata/ons/l55o.csv` from the repository, so run it from the repo root. Writes only to the database in `DATABASE_URL`. *(real output: `ons-l55o: source created, mapping v1 created`, then `source=ons-l55o status=succeeded inserted=642 ...`; the second run prints `source unchanged, mapping v1 unchanged` and `unchanged=642`; the count line is `642`)*
<!-- verify: db -->
```bash
set -e
make build >/dev/null
d=$(mktemp -d)
sed "s#^location: .*#location: $PWD/testdata/ons/l55o.csv#" docs/examples/ons-l55o.source.yaml > $d/ons-l55o.source.yaml
cp docs/examples/ons-l55o.mapping.yaml $d/
bin/importer migrate up >/dev/null
bin/importer sources apply --dir $d
bin/importer run --source ons-l55o
psql "$DATABASE_URL" -tAc "SELECT count(*) FROM raw_records WHERE source_id = 'ons-l55o'"
rm -rf $d
```
7. Check that the service answers. Start the server (step 2, a port of your choice) and run the data client (see Verify, "Data client"). *(real output, page size 100, all pages: `page 1: records=100 ...` through `page 7: records=42 next_page_token_empty=true`, then `TOTAL records=642 pages=7 unique=642 duplicates=0`)*
   ```
   rbclient -addr 127.0.0.1:18455 -source ons-l55o -page-size 100 -all
   rbclient -addr 127.0.0.1:18455 -source ons-l55o -get 'ons-l55o:["L55O","1989 DEC"]'
   ```
   `-get` prints `id=ons-l55o:["L55O","1989 DEC"] source_id=ons-l55o`, then `fields={...}` with `"value": 5.8`, `"period_type": "month"`, `"period_start": "1989-12-01"`. A record id is `<source_id>:<natural key as a JSON array>`; copy it from a `ListRecords` result.
8. Read a short page and continue. A page is **not** the end just because it is short. Pass `next_page_token` back unchanged in `page_token` until the token is empty.
   - Why a page is short: each call maps at most `SCAN_LIMIT` raw rows (default `50000`). With a filter, the rows that do not match still count. When the limit is reached before the page is full, the response has fewer records than `page_size` and a non-empty token.
   - Real run, `SCAN_LIMIT=100`, filter `period_type = year`, `page_size` 50 (`rbclient ... -all -page-size 50 -f 'period_type|EQ|"year"'`):
     ```
     page 1: records=5 next_page_token_empty=false
     page 2: records=6 next_page_token_empty=false
     ... (pages 3-6: records=6 each)
     page 7: records=2 next_page_token_empty=true
     TOTAL records=37 pages=7 unique=37 duplicates=0
     ```
     The same filter with the default `SCAN_LIMIT` gives `TOTAL records=37 pages=1 unique=37 duplicates=0`.
   - The token is base64 of the last natural key scanned. Do not decode or edit it. Start a new walk by sending no token.
9. Tune `SCAN_LIMIT` (environment variable, read at start; restart `importer serve` after a change). *(real: `SCAN_LIMIT=abc` and `SCAN_LIMIT=0` and `SCAN_LIMIT=-5` each exit 2 with `importer serve: SCAN_LIMIT must be a positive integer, got "abc"`; the server does not connect to the database)*
   ```bash
   SCAN_LIMIT=100 GRPC_ADDR=127.0.0.1:18455 bin/importer serve
   ```
   - Too low (signs: many short pages with filters, clients make many calls, each with few records): raise it. The value to choose is UNVERIFIED: none was measured.
   - Too high (sign: a filtered call is slow and its latency grows with the source size, since each call maps up to `SCAN_LIMIT` rows): lower it. UNVERIFIED: no timing was measured at `50000` (the ONS fixture has 642 rows, so the limit never bit at the default).
   - Unset or empty `SCAN_LIMIT` means the default. Do not set it per request; it is a server setting.

## Troubleshooting
All messages below were produced by the real binary or the data client. Startup errors exit 1 unless noted. The client prints `ERR code=<Code> msg="<message>"`; codes use Go names (`FailedPrecondition` is gRPC `FAILED_PRECONDITION`).

### Startup and connection

| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| `importer serve: listen tcp 127.0.0.1:18443: bind: address already in use` | another process (often a second `serve`) holds the port: `ss -ltnp \| grep 18443` | stop it, or set another `GRPC_ADDR`. Note: on the test machine the default `:8443` was already taken by another process. |
| `importer serve: DATABASE_URL is not set` | env var missing in the service environment | export it (the DSN, never commit it) and restart |
| `importer serve: TLS_CERT_FILE is set but TLS_KEY_FILE is not: set both or neither` (and the mirror message `TLS_KEY_FILE is set but TLS_CERT_FILE is not`) | only one of the two vars is set | set both (TLS) or unset both (plaintext) |
| `importer serve: load TLS key pair: open /x: no such file or directory` | path in `TLS_CERT_FILE` or `TLS_KEY_FILE` is wrong | `ls -l` both files; fix the path |
| `importer serve: load TLS key pair: tls: failed to find any PEM data in certificate input` | the file is not a PEM certificate (garbage, or key and cert swapped) | regenerate or re-copy the cert (step 3); cert file first, key file second |
| `importer serve: SCAN_LIMIT must be a positive integer, got "abc"` (also `"0"`, `"-5"`) (exit 2) | `SCAN_LIMIT` is not a whole number above 0 | set a positive integer or unset it (default `50000`) |
| `usage: importer serve` (exit 2) | an argument was given | run `importer serve` with no arguments; configure with env vars |
| Server starts, health `NOT_SERVING`, log `level=WARN msg="health check failed" err="database ping: failed to connect to ... connection refused"` | database unreachable at startup (wrong host/port in `DATABASE_URL` or database down). The server does not exit. | start the database / fix the DSN. Health became `SERVING` about 3 s after the database came back (verified, 5 s re-check). No restart needed if the DSN was right; if the DSN was wrong, fix it and restart. |
| Health goes `SERVING` to `NOT_SERVING` while running | the database went away. Verified: stopped the database, `NOT_SERVING` was seen within about 5 s, the same `health check failed` WARN was logged, the process kept running | restore the database; `SERVING` returns by itself within the next 5 s check |
| `level=WARN msg="TLS_CERT_FILE and TLS_KEY_FILE are not set: serving plaintext gRPC" addr=0.0.0.0:8443` | no TLS configured; with the default `:8443` the listener reports all interfaces (`0.0.0.0`). There is no authentication. | do not expose the port publicly: TLS enforcement and the auth interceptor are planned for M8. Bind to `127.0.0.1` via `GRPC_ADDR` or keep it on a private network. Set both TLS vars to silence it. (The `0.0.0.0:8443` text is quoted from operations.md; with `GRPC_ADDR=0.0.0.0:18444` the same WARN was reproduced with `addr=0.0.0.0:18444`.) |
| Client over TLS to a plaintext server: `transport: authentication handshake failed: tls: first record does not look like a TLS handshake` | client uses TLS, server runs without cert vars | start the server with both TLS vars, or connect without TLS |
| Plaintext client to a TLS server: `rpc error: code = Unavailable desc = connection error: desc = "error reading server preface: EOF"` | server runs TLS, client does not | connect with TLS and trust the certificate (CA file = the self-signed cert) |

### Data calls (`ListRecords`, `GetRecord`)

| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| `code=FailedPrecondition msg="source \"ons-nomap\" has no active mapping"` (same for `ListRecords` and `GetRecord`) | the source was applied without a mapping file. Real output of `sources apply` for it: `ons-nomap: source created, mapping none`. Check: `psql "$DATABASE_URL" -At -c "SELECT id, active_mapping_version FROM sources WHERE id = 'ons-nomap'"` shows an empty second column | write `<name>.mapping.yaml` with `source:` equal to the source `id:`, then `bin/importer sources apply --dir <dir>` (real: `ons-nomap: source unchanged, mapping v1 created`). Call again: `page 1: records=2 ...` |
| `code=FailedPrecondition msg="active mapping v<N> of source ... is not valid: ..."` | the active mapping in the database does not parse | UNVERIFIED (not produced). Fix the mapping file, `importer mapping validate <file>` ([mapping-validation-errors.md](mapping-validation-errors.md)), `sources apply` |
| `code=NotFound msg="source \"nope\" not found"` | source id unknown (typo, or never applied) | `psql "$DATABASE_URL" -At -c "SELECT id FROM sources"`; apply the directory |
| `code=NotFound msg="record \"ons-l55o:nope\" not found in source \"ons-l55o\""` | the id is not `<source_id>:<natural key JSON array>` of a row in that source; an id with another source prefix is also NOT_FOUND | copy the id from `ListRecords`, e.g. `ons-l55o:["L55O","1989 DEC"]` (quote it in the shell) |
| `code=NotFound msg="record \"ons-tomb:[\\\"L55O\\\",\\\"1989 JUL\\\"]\" not found in source \"ons-tomb\""` after `deleted_at` was set | the row is tombstoned (`raw_records.deleted_at` not null). Real: `SELECT count(*) ... AND deleted_at IS NOT NULL` gave `1`; `ListRecords` default gave `TOTAL records=641`, with `-include-deleted` `TOTAL records=642`. Note: no code in `internal/` sets `deleted_at` today (`grep -rn "deleted_at = " internal` finds none), so this only happens by hand or in a future task | `ListRecords` with `include_deleted=true` shows the row. `GetRecord` has no such flag: a reappearing row clears `deleted_at` on the next import that sees it |
| `code=InvalidArgument msg="filter on field \"value\": value has the wrong type, expected number"` | a filter on a number field sent a string, e.g. `"3.5"` | send a JSON number (`3.5`). The same for string fields: `period_type` with `5` gives `expected string` |
| `code=InvalidArgument msg="filter field \"nope\" is unknown or not filterable"` | field name typo or not canonical | use a canonical field: `series_id series_title measure statistic period_type period_start period_end value unit status quality_flag`, or `dimensions.<key>` (see [api.md](../api.md) "Filtering semantics") |
| `code=InvalidArgument msg="filter field \"dimensions\" is not filterable: use dimensions.<key>"` | `dimensions` alone | use `dimensions.<key>` |
| `code=InvalidArgument msg="filter field \"dimensions.\": a dimension key is required (dimensions.<key>)"` | `dimensions.` with an empty key | add the key |
| `code=InvalidArgument msg="filter on field \"period_start\": \"1.1.2020\" is not a date (YYYY-MM-DD)"` | date filter not in ISO form | send `"2020-01-01"` |
| `code=InvalidArgument msg="filter on field \"period_type\": IN needs a list value"` | `IN` with a scalar | send a list |
| `code=InvalidArgument msg="filter on field \"value\": CONTAINS is only supported on string fields"` | `CONTAINS` on a number, bool or date | use `EQ`, `GT`, ... on that field |
| `code=InvalidArgument msg="page_token is not valid"` | the token was edited, truncated, or is not from this server. Checked: `!!!`, `AA` (decodes to one NUL byte) and base64 of `a<NUL>b` all gave this | do not edit tokens. Send no token to start over, or pass the `next_page_token` from the last response unchanged |
| No error, empty page: `page 1: records=0 next_page_token_empty=true` after a hand-made token (checked: `eHl6`, base64 of `xyz`) | a valid key that sorts after every row of the source, so nothing is left to scan | not a fault; send no token to start over |
| `code=InvalidArgument msg="page_size must not be negative, got -1"` | negative `page_size`. Note `0` means the default 50, and above 1000 is clamped to 1000 (not an error) | send `0` or a positive number |
| `code=InvalidArgument msg="sort is not supported: records are returned in natural-key order"` | any `sort` entry is sent | remove `sort`. The order is always natural-key order |
| `code=InvalidArgument msg="set either source_id or group_id, not both"` | both were sent (checked with `source_id` plus `group_id`) | send `source_id` only |
| `code=Unimplemented msg="group_id alone is not implemented yet: groups arrive in a later milestone; pass source_id"` | `group_id` without `source_id` | normal until groups exist: send `source_id` |
| `filter` on `dimensions.<key>` returns `TOTAL records=0` with no error | the key is not in the record's dimensions (ONS has none). Unknown keys match nothing, also for `NEQ` | check the key with `psql "$DATABASE_URL" -c "SELECT ..."` on `raw_records`, or accept the empty result |
| Record in the response with `"mapping_errors"` (client print: `mapping_error field=value message="PARSE_FAILED: \"abc\" is not a number"`) and the field absent from `fields` | the record is still returned (one bad row does not fail the call). Mapping problem or source-data problem: see "Mapping problem or source-data problem?" in [mapping-validation-errors.md](mapping-validation-errors.md). A record with an error on a field never matches a filter on that field (checked: `value GTE 0` on a source with one bad value and one `x` marker gave 640 of 642: the bad row and the null row did not match) | if most records of the source have it, fix the mapping and re-import; if a few, report the raw value to the data owner. Do not hide it by changing the mapping |
| `"value": null` and `"quality_flag": "x"` in `fields`, no `mapping_errors` | not an error: the mapping has `null_markers: ["x"]` and `flag: quality_flag`, so the source's suppression marker is kept as the flag. Checked with `required: true` kept on `value` too: no `MISSING_REQUIRED` | nothing to fix. A new marker shows up as `PARSE_FAILED` on `value`: add it to `null_markers` |

### Server faults (`Internal`)

| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| `code=Internal msg="load source failed"` (other calls use `<operation> failed`; only `load source` was seen) | a server-side failure. The client sees only `<operation> failed`. The detail is in the server log: checked by stopping the database (`pg_ctl -m immediate`) while serving. Log line (real, `level=ERROR`): `msg="data service failure" op="load source" err="get source \"ons-l55o\": failed to connect to ... connection refused"` | restore the database. Real result: after `pg_ctl` restart on the same port, `ListRecords` answered again without restarting `importer serve`. If it persists while `psql "$DATABASE_URL" -c 'select 1'` works, escalate |
| `WARN msg="request rejected by the database" op=... sqlstate=22...` in the log, client `InvalidArgument msg="request contains a value the database cannot store or compare"` | a Postgres data error (SQLSTATE class 22) on input | UNVERIFIED (not reproduced: NUL bytes and bad UTF-8 in a token or id are rejected earlier with `page_token is not valid` or NOT_FOUND). Look at the logged `err` and the request |
| Client `Unavailable` during data calls | UNVERIFIED (not seen in the tests); usually the server stopped, see the startup rows | restart; check `SERVING` (health client) |

**Client error or server fault?** The code tells you: `InvalidArgument`, `NotFound`, `FailedPrecondition`, `Unimplemented` are caused by the request or the source setup, and the message names the field or the source. `Internal` is a server fault: the message is generic, so read the server log for the `data service failure` line (the server writes the `err` text there, never to the client).

## Verify
Health client (the only tool available for health). It imports the project's gRPC module, so it builds only inside the repository: copy the source below to a temporary directory in the repo, build it elsewhere, then delete the directory.
```bash
mkdir cmd/zz-hc-tmp && cp main.go cmd/zz-hc-tmp/ && go build -o /path/to/hc ./cmd/zz-hc-tmp; rm -rf cmd/zz-hc-tmp
```
Source (`main.go`): usage `hc ADDR [CA_FILE|-] [SERVICE]`; with `LIST=1` it lists services through reflection.
```go
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection/grpc_reflection_v1"
)

func main() {
	creds := credentials.TransportCredentials(insecure.NewCredentials())
	if len(os.Args) > 2 && os.Args[2] != "-" {
		pem, err := os.ReadFile(os.Args[2])
		if err != nil { fmt.Println(err); os.Exit(1) }
		p := x509.NewCertPool(); p.AppendCertsFromPEM(pem)
		creds = credentials.NewTLS(&tls.Config{RootCAs: p})
	}
	conn, err := grpc.NewClient(os.Args[1], grpc.WithTransportCredentials(creds))
	if err != nil { fmt.Println(err); os.Exit(1) }
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if os.Getenv("LIST") != "" {
		st, err := grpc_reflection_v1.NewServerReflectionClient(conn).ServerReflectionInfo(ctx)
		if err != nil { fmt.Println(err); os.Exit(1) }
		st.Send(&grpc_reflection_v1.ServerReflectionRequest{MessageRequest: &grpc_reflection_v1.ServerReflectionRequest_ListServices{ListServices: ""}})
		r, err := st.Recv()
		if err != nil { fmt.Println(err); os.Exit(1) }
		for _, s := range r.GetListServicesResponse().GetService() { fmt.Println(s.Name) }
		return
	}
	svc := ""
	if len(os.Args) > 3 { svc = os.Args[3] }
	r, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: svc})
	if err != nil { fmt.Println(err); os.Exit(1) }
	fmt.Println(r.Status)
}
```
1. Health, plaintext: `hc 127.0.0.1:18443` *(expected: `SERVING`; `NOT_SERVING` if the database does not answer)*. TLS: `hc 127.0.0.1:18443 cert.pem` *(expected: `SERVING`)*. A named service: `hc 127.0.0.1:18443 - dataimporter.v1.DataService`.
2. Reflection: `LIST=1 hc 127.0.0.1:18443` *(real output, 4 lines)*:
   ```
   dataimporter.v1.DataService
   grpc.health.v1.Health
   grpc.reflection.v1.ServerReflection
   grpc.reflection.v1alpha.ServerReflection
   ```
   With `grpcurl` installed, `grpcurl -plaintext 127.0.0.1:18443 list` should show the same: UNVERIFIED (`grpcurl` not installed).
3. Graceful shutdown: `kill -TERM <pid>` (and SIGINT with Ctrl-C): both logged `level=INFO msg="server stopped"` and exited 0, in about 4 ms with no open calls (measured). The 10 s limit and the `graceful shutdown timed out; forcing stop` message with a long-running call: UNVERIFIED (no long call was run). A second signal terminating immediately: UNVERIFIED.
4. Startup errors can be re-checked without a database (these need nothing running):
<!-- verify -->
```bash
env -u DATABASE_URL go run ./cmd/importer serve 2>&1 | grep -c "DATABASE_URL is not set"
DATABASE_URL=postgres://x TLS_CERT_FILE=/x go run ./cmd/importer serve 2>&1 | grep -c "TLS_CERT_FILE is set but TLS_KEY_FILE is not: set both or neither"
```
<!-- verify -->
```bash
make build >/dev/null
for v in abc 0; do
  if SCAN_LIMIT=$v DATABASE_URL=postgres://postgres@127.0.0.1:1/x bin/importer serve; then echo "SCAN_LIMIT=$v unexpectedly ok"; else echo "SCAN_LIMIT=$v exit $?"; fi
done
```

### Data client
A small Go client for `ListRecords` and `GetRecord` (no `grpcurl`). Same build rule as the health client: copy it into the repo, build, delete. Usage: `rbclient -addr ADDR -source ID [-page-size N] [-token T] [-all] [-f 'field|OP|jsonvalue']... [-include-deleted] [-group G] [-sort F] [-get ID]`. `-f` is repeatable and ANDed; `OP` is `EQ NEQ GT GTE LT LTE IN CONTAINS`; the value is JSON (`"month"`, `3.5`, `["month","year"]`). `-all` walks every page and prints one line per page.
```bash
mkdir cmd/zz-rb-tmp && cp rbclient/main.go cmd/zz-rb-tmp/ && go build -o /path/to/rbclient ./cmd/zz-rb-tmp; rm -rf cmd/zz-rb-tmp
```
Source (`rbclient/main.go`):
```go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	pb "github.com/willis-dev-md/data-importer/gen/dataimporter/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ";") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func fail(err error) {
	st, ok := status.FromError(err)
	if ok {
		fmt.Printf("ERR code=%s msg=%q\n", st.Code(), st.Message())
	} else {
		fmt.Printf("ERR %v\n", err)
	}
	os.Exit(1)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18455", "server address")
	source := flag.String("source", "", "source_id")
	pageSize := flag.Int("page-size", 0, "page_size")
	token := flag.String("token", "", "page_token (sent as is)")
	all := flag.Bool("all", false, "walk all pages")
	get := flag.String("get", "", "GetRecord id")
	inclDel := flag.Bool("include-deleted", false, "include_deleted")
	group := flag.String("group", "", "group_id")
	sortF := flag.String("sort", "", "sort field (ascending)")
	maxPages := flag.Int("max-pages", 1000, "stop after this many pages")
	var filters multi
	flag.Var(&filters, "f", "filter field|OP|jsonvalue (repeatable)")
	flag.Parse()

	conn, err := grpc.NewClient(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer conn.Close()
	cl := pb.NewDataServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if *get != "" {
		r, err := cl.GetRecord(ctx, &pb.GetRecordRequest{SourceId: *source, Id: *get})
		if err != nil {
			fail(err)
		}
		printRecord(r)
		return
	}

	var fs []*pb.Filter
	for _, f := range filters {
		parts := strings.SplitN(f, "|", 3)
		if len(parts) != 3 {
			fmt.Printf("bad -f %q\n", f)
			os.Exit(2)
		}
		var v any
		if err := json.Unmarshal([]byte(parts[2]), &v); err != nil {
			fmt.Printf("bad json value %q: %v\n", parts[2], err)
			os.Exit(2)
		}
		sv, err := structpb.NewValue(v)
		if err != nil {
			fmt.Println(err)
			os.Exit(2)
		}
		op, ok := pb.FilterOp_value["FILTER_OP_"+parts[1]]
		if !ok {
			fmt.Printf("unknown op %q\n", parts[1])
			os.Exit(2)
		}
		fs = append(fs, &pb.Filter{Field: parts[0], Op: pb.FilterOp(op), Value: sv})
	}
	var sorts []*pb.Sort
	if *sortF != "" {
		sorts = append(sorts, &pb.Sort{Field: *sortF})
	}

	tok := *token
	total, pages, dupes := 0, 0, 0
	seen := map[string]bool{}
	for {
		req := &pb.ListRecordsRequest{
			SourceId: *source, GroupId: *group, Filters: fs, Sort: sorts,
			PageSize: int32(*pageSize), PageToken: tok, IncludeDeleted: *inclDel,
		}
		r, err := cl.ListRecords(ctx, req)
		if err != nil {
			fail(err)
		}
		pages++
		for _, rec := range r.Records {
			if seen[rec.Id] {
				dupes++
			}
			seen[rec.Id] = true
		}
		total += len(r.Records)
		if !*all {
			fmt.Printf("page 1: records=%d next_page_token_empty=%t next_page_token=%q\n",
				len(r.Records), r.NextPageToken == "", r.NextPageToken)
			if n := countMapErr(r); n > 0 {
				fmt.Printf("records with mapping_errors on this page: %d\n", n)
			}
			return
		}
		fmt.Printf("page %d: records=%d next_page_token_empty=%t\n", pages, len(r.Records), r.NextPageToken == "")
		if r.NextPageToken == "" || pages >= *maxPages {
			break
		}
		tok = r.NextPageToken
	}
	fmt.Printf("TOTAL records=%d pages=%d unique=%d duplicates=%d\n", total, pages, len(seen), dupes)
}

func countMapErr(r *pb.ListRecordsResponse) int {
	n := 0
	for _, rec := range r.Records {
		if len(rec.MappingErrors) > 0 {
			n++
		}
	}
	return n
}

func printRecord(r *pb.Record) {
	fmt.Printf("id=%s source_id=%s\n", r.Id, r.SourceId)
	fmt.Printf("fields=%s\n", protojson.Format(r.Fields))
	for _, e := range r.MappingErrors {
		fmt.Printf("mapping_error field=%s message=%q\n", e.Field, e.Message)
	}
}
```
Real checks against the ONS source (server on `127.0.0.1:18455`, `SCAN_LIMIT` unset). Filters count the same rows as the task's test expectations:

| Call | Real result |
|---|---|
| `-source ons-l55o -all -page-size 100` | `TOTAL records=642 pages=7 unique=642 duplicates=0` |
| `-source ons-l55o -all` (default page size 50) | `TOTAL records=642 pages=13 unique=642 duplicates=0` |
| `-source ons-l55o -all -f 'period_type\|EQ\|"month"'` | `TOTAL records=454 pages=5 ...` |
| `-source ons-l55o -all -f 'period_type\|EQ\|"month"' -f 'value\|GTE\|3.5'` | `TOTAL records=108 pages=3 ...` |
| `-source ons-l55o -all -f 'period_type\|EQ\|"year"'` | `TOTAL records=37 pages=1 ...` |
| `-source ons-l55o -all -f 'dimensions.area\|EQ\|"x"'` | `TOTAL records=0 pages=1 ...` (no error) |

(In a Markdown table, `\|` is the escaped pipe; type a plain `|` in the shell.)

### Scan-limit check (optional)
Start a second server with `SCAN_LIMIT=100` on another port and run the year filter with `-all -page-size 50`: the page sizes are the ones shown in step 8. Stop it afterwards (`kill -TERM <pid>`; expected `level=INFO msg="server stopped"`).

## Roll back / undo
`importer serve` keeps no state: stop it (step 5) and start the previous binary. The server only reads the database; schema changes are in [database-migrations.md](database-migrations.md). To go back to the old `SCAN_LIMIT`, unset it and restart.

## Escalate
Escalate to the developer owning the server if: `NOT_SERVING` persists after the database ping works from the same host (`psql "$DATABASE_URL" -c 'select 1'`), the process exits after start without a message above, the process does not exit within 10 s of SIGTERM, or `Internal` errors persist while the database answers. Include the full log (the ping error shows the DSN user, host and port, never the password: UNVERIFIED with a password in the DSN, check before pasting), the env var names set (not values), the client's `ERR code=... msg=...` line, and the output of the health client.

Not verified: an unreadable (permission denied) certificate file (the test machine runs as root); a rotated certificate (needs a restart; not run); `grpc_health_probe` and `grpcurl` (not installed); a timing for `SCAN_LIMIT` at the default `50000` (a slow filtered call at scale); a long-running call during shutdown; a `FailedPrecondition` for an active mapping that does not parse; `Unavailable` during data calls; the SQLSTATE 22 log line.
