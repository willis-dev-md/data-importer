# Source: ONS time series L55O (MM23)

CPIH annual rate, 00: all items, 2015=100. Published by the Office for National Statistics.

    https://www.ons.gov.uk/generator?format=csv&uri=/economy/inflationandpriceindices/timeseries/l55o/mm23

A snapshot is kept at `testdata/ons/l55o.csv` (release date 18-11-2026) as the test fixture.

## File structure

    "Title","CPIH ANNUAL RATE 00: ALL ITEMS 2015=100"
    "CDID","L55O"
    "Source dataset ID","MM23"
    "PreUnit",""
    "Unit","%"
    "Release date","18-11-2026"
    "Next release","16 December 2026"
    "Important notes",
    "1989","5.7"          <- annual rows (37)
    "1989 Q1","5.8"       <- quarterly rows (151)
    "1989 JAN","..."      <- monthly rows (454)

Properties that shape the importer:
- 8-line `key,value` preamble, then headerless two-column data (`period`, `value`).
- One column mixes three period granularities.
- The series identifier is in the preamble, so the natural key is `@meta.CDID` + `period`.
- Values can be revised in later releases: upsert plus `content_hash` handles this, and
  history (open question 3) would record revisions.
- Quote style: every field quoted; "Important notes" line has an unquoted empty value.

Verified on the fixture (task 005): 8 preamble keys, 642 records (37 + 151 + 454), 0 row errors, first key
`["L55O","1989"]`, last key `["L55O","2026 OCT"]`, all keys unique.

## Config

See [`../examples/ons-l55o.source.yaml`](../examples/ons-l55o.source.yaml) and
[`../examples/ons-l55o.mapping.yaml`](../examples/ons-l55o.mapping.yaml).

## Scheduling
Cadence is set in the source's `schedule` (the example uses monthly). `Next release` is in the preamble. A later enhancement can read it from the last run's `source_meta` and schedule
the next fetch for that date; v1 just runs on a cron (e.g. daily), skipping unchanged data via `content_hash`.
