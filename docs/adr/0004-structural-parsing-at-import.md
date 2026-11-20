# ADR-0004: Structural parsing at import, semantic mapping on read

Status: accepted (refines ADR-0001)

## Context
ADR-0001 stores raw records and maps on read. The Bank of England workbook shows that some source
clean-up depends on row **order and context**: the year is only on the first row of each year, and section headings
decide which series later rows belong to. That context is lost once rows are stored independently.

## Decision
Split the work in two:
- **Structural parsing (import time, in the adapter):** deterministic row-to-row steps that need sequence context:
  skip rows, section tracking, fill-down, unpivot (wide to long), metadata preamble. The output, plus the source
  position (`_source: {sheet,row}`) and section context (`_ctx`), is what is stored in `raw_records.payload`.
- **Semantic mapping (read time, gRPC):** renames, type coercion, unit and value transforms, validation, canonical
  shape, as in ADR-0001.

To keep ELT's "no re-fetch" benefit, the fetched file itself is kept (`source_files`, keyed by SHA-256), so a
change to the structural pipeline is fixed by **re-parsing the stored file**, not re-downloading it.

## Consequences
+ Raw rows stay self-contained and queryable; mapping stays cheap to change.
+ Pipeline changes are cheap thanks to retained files (`importer reparse --source <id> [--run <id>]`).
- Pipeline changes still need a reparse (unlike mapping changes). Keep the pipeline small and declarative.
- Storage for retained files (mitigate with retention limits per source).
