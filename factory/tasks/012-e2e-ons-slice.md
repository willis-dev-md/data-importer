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
pending
## Log
