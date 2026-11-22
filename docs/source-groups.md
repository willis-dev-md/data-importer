# Source groups (many datasets, one definition)

> **NOT IMPLEMENTED YET (planned, M7b).** Nothing in this document is in the code. Task 017 (ASHE Table 20) found this
> out while planning and imports its two editions as standalone sources instead:
> [ashe-t20-2026p](examples/ashe-t20-2026p.source.yaml) and [ashe-t20-2025r](examples/ashe-t20-2025r.source.yaml).
> The group file [ashe-t20-annual-pay.group.yaml](examples/ashe-t20-annual-pay.group.yaml) is a future design only.
> Until M7b, use one standalone source per dataset or edition.

Many setups import dozens or hundreds of datasets that share one structure and differ only by a parameter
(for ONS: the series ID `CDID` and dataset ID in the URL). Copying a source file per dataset does not scale, so a
**source group** defines the shared parts once and lists the members.

```yaml
group: ons-timeseries
template:
  kind: csv
  location: "https://www.ons.gov.uk/generator?format=csv&uri=/{path}/timeseries/{cdid}/{dataset}"
  csv:
    header: false
    columns: [period, value]
    preamble: { lines: 8, as: metadata }
  natural_key: ["@meta.CDID", period]
  schedule: { every: monthly, day_of_month: 18, at: "09:30", timezone: Europe/London, on_missed: run_once }
  max_error_ratio: 0.01
  mapping: ons-timeseries@1          # shared mapping (name@version), uses @meta so it is series-agnostic
rate_limit: { host: www.ons.gov.uk, requests_per_sec: 1 }
members:
  - { id: ons-l55o, cdid: l55o, dataset: mm23, path: economy/inflationandpriceindices }
  - { id: ons-d7g7, cdid: d7g7, dataset: mm23, path: economy/inflationandpriceindices }
  - { id: ons-lf24, cdid: lf24, dataset: lms,  path: employmentandlabourmarket/peopleinwork/employmentandemployeetypes,
      schedule: { every: monthly, day_of_month: 16, at: "09:30", timezone: Europe/London } }   # override
members_file: members.csv            # optional: bulk list (id,cdid,dataset,path,...) merged with the above
```

## Semantics
- **Expansion:** the group expands into one ordinary source per member (`sources.group_id` set). Each member
  therefore has its own `import_runs`, `next_run_at`, errors and health. One broken dataset never blocks the rest.
- **Substitution:** `{name}` placeholders in any template string are filled from the member's fields. Unknown or
  missing placeholders fail validation at save time.
- **Overrides:** a member can override `schedule`, `mapping`, `enabled` or any template key (deep merge).
- **Shared mapping:** `mapping: name@version` references a mapping not tied to a single source. Changing it
  (new version, then activate) applies to every member that pins `name` without a version, or on a per-member
  basis if pinned. Heterogeneous datasets simply use a different template or mapping.
- **Onboarding:** `importer group apply group.yaml` (idempotent: adds, updates, and disables removed members, never
  deletes data) and `importer group add <group> --member cdid=...`. Also available over gRPC (`PutSourceGroup`).
- **Dry run:** `importer group plan` shows members added/changed/disabled and validates each URL placeholder.
- **Raw data stays per member:** `raw_records` is already keyed by `(source_id, natural_key)`, so there is no
  collision between members.

## Operating many sources
- **Scheduling spread:** members with the same schedule would all fire at once. The scheduler adds deterministic
  per-member jitter (hash of id, within `jitter`, default 10m for groups) so load is smoothed.
- **Concurrency:** a global worker pool (`IMPORT_CONCURRENCY`, default 4) bounds simultaneous runs; due sources
  queue.
- **Politeness:** per-host rate limiting is shared across all members hitting that host
  (`rate_limit.host`), plus honouring `Retry-After`, and a descriptive `User-Agent`.
- **Failure isolation and visibility:** `ListSources` and metrics are labelled by `group`; a group health view shows
  members failing, stale or never run. Alert on the count of stale members, not per member.
- **Mapping governance:** because the canonical entity is a time-series observation, one `series_id` field
  identifies the member in queries; `ListRecords` can filter by `series_id`, `group` or both.

## Discovery (later)
Finding datasets automatically (e.g. from a publisher's catalogue or search) is out of scope for v1. The group file
or `members_file` is the source of truth; a helper to generate `members.csv` can come later.
