#!/usr/bin/env bash
# Ephemeral Postgres 16 for tests.
#   eval "$(scripts/testdb.sh start)"   # exports DATABASE_URL and TESTDB_DIR
#   scripts/testdb.sh stop              # stops it and deletes the directory
# Both commands are idempotent. When run as root, Postgres runs as the `postgres` OS user.
set -u
STATE="${TMPDIR:-/tmp}/data-importer-testdb.$(id -u).state"

die() { echo "testdb: $*" >&2; exit 1; }

find_bindir() {
  local d
  if command -v pg_config >/dev/null 2>&1; then
    d=$(pg_config --bindir 2>/dev/null)
    if [ -x "$d/initdb" ]; then echo "$d"; return 0; fi
  fi
  for d in $(ls -d /usr/lib/postgresql/*/bin 2>/dev/null | sort -rV); do
    if [ -x "$d/initdb" ]; then echo "$d"; return 0; fi
  done
  if command -v initdb >/dev/null 2>&1; then dirname "$(command -v initdb)"; return 0; fi
  return 1
}

# as_pg runs a command as the postgres user when we are root, else directly.
as_pg() {
  if [ "$(id -u)" = 0 ]; then runuser -u postgres -- "$@"; else "$@"; fi
}

free_port() {
  python3 -I -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])'
}

print_exports() {
  # shellcheck disable=SC1090
  . "$STATE"
  echo "export TESTDB_DIR='$TESTDB_DIR'"
  echo "export DATABASE_URL='postgres://postgres@127.0.0.1:$TESTDB_PORT/postgres?sslmode=disable'"
}

running() {
  [ -f "$STATE" ] || return 1
  # shellcheck disable=SC1090
  . "$STATE"
  [ -d "$TESTDB_DIR/data" ] || return 1
  as_pg "$BINDIR/pg_ctl" -D "$TESTDB_DIR/data" status >/dev/null 2>&1
}

remove_dir() {
  # Only ever delete a directory this script created.
  case "$1" in
    */data-importer-testdb.??????) [ -d "$1" ] && rm -r -- "$1" ;;
  esac
  return 0
}

stop_quiet() {
  [ -f "$STATE" ] || return 0
  # shellcheck disable=SC1090
  . "$STATE"
  if [ -d "$TESTDB_DIR/data" ]; then
    as_pg "$BINDIR/pg_ctl" -D "$TESTDB_DIR/data" -m fast -w stop >/dev/null 2>&1 || true
  fi
  remove_dir "$TESTDB_DIR"
  rm -f -- "$STATE"
}

start() {
  if ( running ); then print_exports; return 0; fi
  stop_quiet   # clear any stale state
  local bindir dir port
  bindir=$(find_bindir) || die "cannot find Postgres binaries (pg_config or /usr/lib/postgresql/*/bin)"
  dir=$(mktemp -d "${TMPDIR:-/tmp}/data-importer-testdb.XXXXXX") || die "mktemp failed"
  chmod 755 "$dir"
  if [ "$(id -u)" = 0 ]; then chown postgres "$dir"; fi
  port=$(free_port) || { remove_dir "$dir"; die "no free port"; }
  if ! as_pg "$bindir/initdb" -D "$dir/data" -U postgres -A trust --no-sync >"$dir/initdb.log" 2>&1; then
    cat "$dir/initdb.log" >&2; remove_dir "$dir"; die "initdb failed"
  fi
  if ! as_pg "$bindir/pg_ctl" -D "$dir/data" -l "$dir/postgres.log" -w -t 60 \
    -o "-p $port -k $dir -c listen_addresses=127.0.0.1 -c fsync=off" start >"$dir/pg_ctl.log" 2>&1; then
    cat "$dir/pg_ctl.log" "$dir/postgres.log" >&2
    remove_dir "$dir"
    die "pg_ctl start failed (port $port in use? see docs/runbooks/local-development.md)"
  fi
  { echo "BINDIR='$bindir'"; echo "TESTDB_DIR='$dir'"; echo "TESTDB_PORT='$port'"; } >"$STATE"
  print_exports
}

case "${1:-}" in
  start) start ;;
  stop) stop_quiet; echo "testdb: stopped" >&2 ;;
  *) echo "usage: $0 start|stop" >&2; exit 2 ;;
esac
