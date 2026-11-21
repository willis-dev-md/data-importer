#!/usr/bin/env bash
# Fails if the committed generated code under gen/ differs from `buf generate` (works offline).
cd "$(dirname "$0")/../.." || exit 1
[ -f buf.gen.yaml ] || exit 0
tmp=$(mktemp -d) || exit 1
trap 'rm -rf "$tmp"' EXIT
[ -d gen ] && cp -r gen "$tmp/before" || mkdir "$tmp/before"
go tool buf generate || { echo "FAIL: buf generate"; exit 1; }
if ! diff -r "$tmp/before" gen >"$tmp/diff"; then
  head -40 "$tmp/diff"
  echo "FAIL: gen/ is stale; run 'go tool buf generate' and commit the result"
  exit 1
fi
