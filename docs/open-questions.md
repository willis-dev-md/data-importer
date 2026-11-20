# Open questions

1. **Canonical schema**: which entity and fields? Until decided, a sample schema (`id`, `name`, `email`, `date`,
   `amount`) is used.
2. **Sync mode**: full snapshots, incremental feeds, or both per source? Affects `missing_policy`.
3. **History**: is an append-only version history of raw records needed, or only the latest?
4. **API consumers**: internal services or external clients? Drives auth and rate limiting.
5. **Exotic sources**: OAuth2 client-credentials, GraphQL, XML, SFTP, S3?
6. **Retention and privacy**: does any source contain personal data? Per-source retention/deletion rules?
7. **Scale**: expected records per source and per run, and read QPS?
8. **Deployment target**: where does it run (container platform, managed Postgres)?
