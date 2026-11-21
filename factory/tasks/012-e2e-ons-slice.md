# 012 – End-to-end vertical slice: ONS CSV to gRPC

## Goal
Prove the first demo: the real ONS fixture goes CSV to Postgres to gRPC through the actual binary, and document how to run it.

## Depends on
011

## Design refs
`docs/roadmap.md` (M1-M5 demo), `docs/sources/ons-l55o.md`, `docs/operations.md`

## Scope
`internal/e2e/` (or `test/e2e/`), `scripts/demo.sh`, `README.md` ("Quick start"), `docs/roadmap.md` (mark M0-M5 done), tests

## Out of scope
New features. Only fix bugs found, in the owning package, noting them in the log.

## Acceptance criteria (each must be checkable)
- [ ] An e2e test builds the real binary and, against an ephemeral Postgres: runs `migrate up`; `sources apply` with a temp copy of the ONS example source+mapping whose `location` is `file://` + the fixture path; `run` (assert 642 inserted); starts `serve` on a random port; calls `ListRecords` with a real gRPC client and asserts the exact counts from task 011 (642 / 454 / 37 / 108 for the four filters) and the three spot-check records from task 007; runs the import again (642 unchanged); stops `serve` gracefully.
- [ ] The e2e test runs inside `scripts/validate.sh` with no network access (assert no outbound dial: the source location is `file://`).
- [ ] `scripts/demo.sh` performs the same flow for a human (starts a temp DB, imports, serves, prints three `grpcurl`-style example calls using reflection; cleans up on exit).
- [ ] `README.md` "Quick start" documents prerequisites, `make` targets and the demo, and its commands are the ones actually exercised by the e2e test (no untested instructions).
- [ ] `docs/roadmap.md` marks M0 to M5 as done and notes remaining milestones; `scripts/checks.d/20-docs-links.sh` still passes.
- [ ] `scripts/validate.sh` passes.

## Docs impact
README.md (Quick start, exercised by the e2e test); docs/roadmap.md (M0-M5 done); CHANGELOG.md

## Runbook impact
docs/runbooks/demo-and-smoke-test.md (run scripts/demo.sh, expected output, what a failure means)

## Status
validated
## Log
- Round 1 (worker): added `internal/e2e/e2e_test.go` (real binary; file:// source copy; HTTP(S)_PROXY set to closed port 127.0.0.1:1 for every child so any outbound dial fails the run; counts 642/454/37/151/108; spot records matched by period_type+period_start since the label is not exposed; second run `unchanged=642`; SIGTERM exit 0), `scripts/demo.sh` and `scripts/demo-client/main.go` (tiny client used only when grpcurl is absent). demo.sh creates its own database, needs `psql` and `python3`.
- 2026-11-21 validator r1: PASS (counts re-derived with Python; four mutations each made the e2e test fail; demo.sh: normal, twice in a row, SIGINT 130, SIGTERM 143, forced failure all clean up). documentor then runbook-refiner (sequential, shared database) ran; deterministic docs check PASS. Orchestrator corrected one stale line in docs/testing.md (Ctrl+C cleanup was reported UNVERIFIED but both validator and runbook-refiner verified it).
- Honest limits: "no network" is established by code reading (file:// never uses net/http; the closed-port proxy guard covers http(s) only and Go skips proxies for loopback); `unshare -n` was not usable here; the grpcurl branch of demo.sh was not run (grpcurl not installed); demo.sh needs psql and python3.
- Milestones M0-M5 (tasks 001-012) are complete.
