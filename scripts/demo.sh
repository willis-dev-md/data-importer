#!/usr/bin/env bash
# Demo: ONS L55O fixture -> Postgres -> gRPC, on a throw-away database.
# Run from anywhere:  scripts/demo.sh   (needs Go and Postgres binaries; no network)
# Starts a temp Postgres, imports testdata/ons/l55o.csv, serves gRPC, prints three
# example calls (grpcurl if installed, otherwise the tiny Go client), then cleans up.
# Cleanup also runs on Ctrl+C and on failure. Safe to re-run.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(pwd)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/data-importer-demo.XXXXXX")
SERVE_PID=""
STARTED_DB=0

cleanup() {
  trap - EXIT INT TERM
  if [ -n "$SERVE_PID" ] && kill -0 "$SERVE_PID" 2>/dev/null; then
    kill -TERM "$SERVE_PID" 2>/dev/null || true
    for _ in $(seq 1 100); do kill -0 "$SERVE_PID" 2>/dev/null || break; sleep 0.1; done
    kill -KILL "$SERVE_PID" 2>/dev/null || true
    wait "$SERVE_PID" 2>/dev/null || true
  fi
  [ "$STARTED_DB" = 1 ] && scripts/testdb.sh stop >/dev/null 2>&1 || true
  rm -rf -- "$WORK"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Use a database of our own unless the caller supplied DATABASE_URL.
if [ -z "${DATABASE_URL:-}" ]; then
  STARTED_DB=1
  echo "==> starting a temporary Postgres"
  eval "$(scripts/testdb.sh start)"
fi
# Fresh database per run, so re-running never sees old data.
DB="demo_$$_$(date +%s)"
psql_url() { python3 -I -c 'import sys,urllib.parse as u; p=u.urlparse(sys.argv[1]); print(u.urlunparse(p._replace(path="/"+sys.argv[2])))' "$1" "$2"; }
ADMIN_URL="$DATABASE_URL"
DB_URL=$(psql_url "$ADMIN_URL" "$DB")
if command -v psql >/dev/null 2>&1; then
  psql "$ADMIN_URL" -qc "CREATE DATABASE $DB" >/dev/null
else
  echo "demo: psql is required" >&2; exit 1
fi
if [ "$STARTED_DB" = 0 ]; then
  trap 'psql "$ADMIN_URL" -qc "DROP DATABASE IF EXISTS '"$DB"' WITH (FORCE)" >/dev/null 2>&1 || true; cleanup' EXIT
fi
export DATABASE_URL="$DB_URL"
export SOURCES_DIR=

echo "==> building the importer"
go build -o "$WORK/importer" ./cmd/importer

mkdir "$WORK/sources"
sed -E "s|^location:.*$|location: file://$ROOT/testdata/ons/l55o.csv|" docs/examples/ons-l55o.source.yaml >"$WORK/sources/ons-l55o.source.yaml"
cp docs/examples/ons-l55o.mapping.yaml "$WORK/sources/"

echo "==> migrate up"
"$WORK/importer" migrate up
echo "==> sources apply"
"$WORK/importer" sources apply --dir "$WORK/sources" --schema canonical-schema.yaml
echo "==> run (import the fixture)"
"$WORK/importer" run --source ons-l55o

echo "==> serve (plaintext gRPC on a free local port)"
GRPC_ADDR=127.0.0.1:0 TLS_CERT_FILE= TLS_KEY_FILE= "$WORK/importer" serve >"$WORK/serve.log" 2>&1 &
SERVE_PID=$!
ADDR=""
for _ in $(seq 1 300); do
  ADDR=$(sed -nE 's/.*serving (plaintext )?gRPC.* addr=([^ ]+).*/\2/p' "$WORK/serve.log" | head -1)
  [ -n "$ADDR" ] && break
  kill -0 "$SERVE_PID" 2>/dev/null || { cat "$WORK/serve.log" >&2; echo "demo: serve exited early" >&2; exit 1; }
  sleep 0.1
done
[ -n "$ADDR" ] || { cat "$WORK/serve.log" >&2; echo "demo: serve did not start" >&2; exit 1; }
echo "    listening on $ADDR"

call() { # call <title> <grpcurl-json> <demo-client-args...>
  local title=$1 json=$2; shift 2
  echo
  echo "==> $title"
  if command -v grpcurl >/dev/null 2>&1; then
    echo "    grpcurl -plaintext -d '$json' $ADDR dataimporter.v1.DataService/ListRecords"
    grpcurl -plaintext -d "$json" "$ADDR" dataimporter.v1.DataService/ListRecords
  else
    echo "    go run ./scripts/demo-client -addr $ADDR $*"
    go run ./scripts/demo-client -addr "$ADDR" "$@"
  fi
}
call "first 3 records" '{"source_id":"ons-l55o","page_size":3}' -size 3
call "monthly records" '{"source_id":"ons-l55o","page_size":3,"filters":[{"field":"period_type","op":"FILTER_OP_EQ","value":"month"}]}' -type month -size 3
call "monthly records with value >= 3.5" '{"source_id":"ons-l55o","page_size":3,"filters":[{"field":"period_type","op":"FILTER_OP_EQ","value":"month"},{"field":"value","op":"FILTER_OP_GTE","value":3.5}]}' -type month -min 3.5 -size 3

echo
echo "==> done; stopping the server and removing the temporary database"
