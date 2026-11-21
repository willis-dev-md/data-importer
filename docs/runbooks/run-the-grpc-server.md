# Runbook: run the gRPC server (`importer serve`)

**Owner role:** on-call engineer  |  **Severity if this happens:** medium (the server is down or `NOT_SERVING`)
**Last verified:** 2026-11-21, ran `make build`, `bin/importer migrate up`, `bin/importer serve` (plaintext and TLS), a Go health/reflection client, SIGINT/SIGTERM, database stop/start, and the startup errors below against a throwaway Postgres 16 from `scripts/testdb.sh`

## When to use
Start, check or stop `importer serve`; health is `NOT_SERVING`; the process exits at startup; a client cannot connect.
Reference: the `importer serve` section of [operations.md](../operations.md).

## Prerequisites
- `bin/importer` built (`make build`) and the schema applied (`importer migrate up`, see [database-migrations.md](database-migrations.md)).
- `DATABASE_URL` set. Optional: `GRPC_ADDR` (default `:8443`), `TLS_CERT_FILE` + `TLS_KEY_FILE` (both or neither).
- A health client. Neither `grpc_health_probe` nor `grpcurl` was installed when this was verified, so a small Go client was used (see Verify).
  `openssl` was installed and is used for the certificate.

## Steps
1. Build and migrate. *(expected: `version 1` from `migrate up` on a fresh database; your version may differ)*
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

## Troubleshooting
All messages below were produced by the real binary. Startup errors exit 1 unless noted.

| Symptom (exact message or behaviour) | Diagnosis | Fix |
|---|---|---|
| `importer serve: listen tcp 127.0.0.1:18443: bind: address already in use` | another process (often a second `serve`) holds the port: `ss -ltnp \| grep 18443` | stop it, or set another `GRPC_ADDR`. Note: on the test machine the default `:8443` was already taken by another process. |
| `importer serve: DATABASE_URL is not set` | env var missing in the service environment | export it (the DSN, never commit it) and restart |
| `importer serve: TLS_CERT_FILE is set but TLS_KEY_FILE is not: set both or neither` (and the mirror message `TLS_KEY_FILE is set but TLS_CERT_FILE is not`) | only one of the two vars is set | set both (TLS) or unset both (plaintext) |
| `importer serve: load TLS key pair: open /x: no such file or directory` | path in `TLS_CERT_FILE` or `TLS_KEY_FILE` is wrong | `ls -l` both files; fix the path |
| `importer serve: load TLS key pair: tls: failed to find any PEM data in certificate input` | the file is not a PEM certificate (garbage, or key and cert swapped) | regenerate or re-copy the cert (step 3); cert file first, key file second |
| `usage: importer serve` (exit 2) | an argument was given | run `importer serve` with no arguments; configure with env vars |
| Server starts, health `NOT_SERVING`, log `level=WARN msg="health check failed" err="database ping: failed to connect to ... connection refused"` | database unreachable at startup (wrong host/port in `DATABASE_URL` or database down). The server does not exit. | start the database / fix the DSN. Health became `SERVING` about 3 s after the database came back (verified, 5 s re-check). No restart needed if the DSN was right; if the DSN was wrong, fix it and restart. |
| Health goes `SERVING` to `NOT_SERVING` while running | the database went away. Verified: stopped the database, `NOT_SERVING` was seen within about 5 s, the same `health check failed` WARN was logged, the process kept running | restore the database; `SERVING` returns by itself within the next 5 s check |
| Client error `rpc error: code = Unimplemented desc = method ListRecords not implemented` | normal for now: all `DataService` methods return `Unimplemented` until task 011. Not a fault; health and reflection still work | nothing to fix; do not restart. Update this runbook when task 011 lands |
| Client over TLS to a plaintext server: `transport: authentication handshake failed: tls: first record does not look like a TLS handshake` | client uses TLS, server runs without cert vars | start the server with both TLS vars, or connect without TLS |
| Plaintext client to a TLS server: `rpc error: code = Unavailable desc = connection error: desc = "error reading server preface: EOF"` | server runs TLS, client does not | connect with TLS and trust the certificate (CA file = the self-signed cert) |
| `level=WARN msg="TLS_CERT_FILE and TLS_KEY_FILE are not set: serving plaintext gRPC" addr=0.0.0.0:8443` | no TLS configured; with the default `:8443` the listener reports all interfaces (`0.0.0.0`). There is no authentication. | do not expose the port publicly: TLS enforcement and the auth interceptor are planned for M8. Bind to `127.0.0.1` via `GRPC_ADDR` or keep it on a private network. Set both TLS vars to silence it. (The `0.0.0.0:8443` text is quoted from operations.md; with `GRPC_ADDR=0.0.0.0:18444` the same WARN was reproduced with `addr=0.0.0.0:18444`.) |

## Verify
Health client (the only tool available). It imports the project's gRPC module, so it builds only inside the repository: copy the source below to a temporary directory in the repo, build it elsewhere, then delete the directory.
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
3. Graceful shutdown: `kill -TERM <pid>` (and SIGINT with Ctrl-C): both logged `level=INFO msg="server stopped"` and exited 0, in about 4 ms with no open calls (measured). The 10 s limit and the `graceful shutdown timed out; forcing stop` message with a long-running call: UNVERIFIED (no long call exists until task 011). A second signal terminating immediately: UNVERIFIED.
4. Startup errors can be re-checked without a database (these need nothing running):
<!-- verify -->
```bash
env -u DATABASE_URL go run ./cmd/importer serve 2>&1 | grep -c "DATABASE_URL is not set"
DATABASE_URL=postgres://x TLS_CERT_FILE=/x go run ./cmd/importer serve 2>&1 | grep -c "TLS_CERT_FILE is set but TLS_KEY_FILE is not: set both or neither"
```
5. Stop the throwaway database when finished: `scripts/testdb.sh stop`.

## Roll back / undo
`importer serve` keeps no state: stop it (step 5) and start the previous binary. The server only reads the database; schema changes are in [database-migrations.md](database-migrations.md).

## Escalate
Escalate to the developer owning the server if: `NOT_SERVING` persists after the database ping works from the same host (`psql "$DATABASE_URL" -c 'select 1'`), the process exits after start without a message above, or the process does not exit within 10 s of SIGTERM. Include the full log (the ping error shows the DSN user, host and port, never the password: UNVERIFIED with a password in the DSN, check before pasting), the env var names set (not values), and the output of the health client.

Not verified: an unreadable (permission denied) certificate file (the test machine runs as root); a rotated certificate (needs a restart; not run); `grpc_health_probe` and `grpcurl` (not installed).
