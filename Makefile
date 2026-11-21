.PHONY: build test lint fmt testdb-up testdb-down

build:
	go build -o bin/importer ./cmd/importer

# Uses DATABASE_URL if set, otherwise starts and stops an ephemeral Postgres.
test:
	@if [ -n "$$DATABASE_URL" ]; then go test -race -count=1 ./...; \
	else eval "$$(scripts/testdb.sh start)" && trap 'scripts/testdb.sh stop' EXIT && go test -race -count=1 ./...; fi

lint:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	go tool staticcheck ./...

fmt:
	gofmt -w .

# Usage: eval "$$(make -s testdb-up)"
testdb-up:
	@scripts/testdb.sh start

testdb-down:
	@scripts/testdb.sh stop
