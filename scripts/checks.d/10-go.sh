#!/usr/bin/env bash
# Go quality gate: format, vet, build, static analysis, proto lint, tests (with a real Postgres).
cd "$(dirname "$0")/../.." || exit 1
fail=0
[ -f go.mod ] || { echo "FAIL: no go.mod (scaffold task 001 not done yet)"; exit 1; }

unformatted=$(gofmt -l $(git ls-files '*.go' 2>/dev/null; git ls-files --others --exclude-standard '*.go' 2>/dev/null) 2>/dev/null)
[ -z "$unformatted" ] || { echo "FAIL: gofmt needed on:"; echo "$unformatted"; fail=1; }
go vet ./... || fail=1
go build ./... || fail=1
if grep -q 'staticcheck' go.mod; then go tool staticcheck ./... || fail=1; fi
if [ -f buf.yaml ] || [ -f proto/buf.yaml ]; then go tool buf lint || fail=1; fi

# Integration tests need Postgres. Start an ephemeral one unless DATABASE_URL is already provided.
if [ -z "${DATABASE_URL:-}" ] && [ -x scripts/testdb.sh ]; then
  eval "$(scripts/testdb.sh start)" || { echo "FAIL: could not start test database"; exit 1; }
  trap 'scripts/testdb.sh stop >/dev/null 2>&1' EXIT
fi
CI_REQUIRE_DB=1 go test -race -count=1 ./... || fail=1
exit $fail
