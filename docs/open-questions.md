# Open questions

1. **Canonical schema**: now provisionally a **time-series observation** (series_id, period, value, unit), driven by
   the first source (ONS). Confirm whether other setups are also time series or a different entity.
2. **Sync mode**: full snapshots, incremental feeds, or both per source? Affects `missing_policy`.
3. **History**: is an append-only version history of raw records needed, or only the latest?
3a. **Schedule defaults**: sensible default timezone and `on_missed`; should failed runs retry before the next slot?
4. **API consumers**: internal services or external clients? Drives auth and rate limiting.
5. **Exotic sources**: OAuth2 client-credentials, GraphQL, XML, SFTP, S3?
6. **Retention and privacy**: does any source contain personal data? Per-source retention/deletion rules?
7. **Scale**: expected records per source and per run, and read QPS?
8. **Deployment target**: where does it run (container platform, managed Postgres)?
9. **ONS scope**: answered, many datasets. Open: how many (tens vs thousands), where the member list comes from
   (hand-maintained file vs generated from a catalogue), and handling of revisions to past values.
9a. **Cross-publisher groups**: answered, no: BoE is an XLSX with a different structure, so each publisher/structure
   gets its own template or standalone source, all mapped to the same canonical observation.
9b. **Which sheet(s) of the BoE workbook**: I recommend only `HISTORICAL SINCE 1694` (events) and deriving daily
    values on read; confirm you do not need the hidden daily `Raw Data` sheet (it contains future-dated rows).
9c. **File retention**: how long to keep fetched files (`source_files`) per source, and where (DB large object vs
    object storage)?
9d. **Licence/attribution**: check each publisher's reuse terms and add attribution metadata per source.
9e. **MCP sources**: which servers, which transport (remote HTTP vs local stdio), how are they authenticated, and
    do their tools return structured output or text? Where will stdio servers run?
10. **Granularity**: keep annual, quarterly and monthly rows together (as published) or split into separate
    canonical series?
