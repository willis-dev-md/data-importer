# ADR-0003: Declarative, closed-set mappings (no scripting)

Status: accepted

Mappings are YAML using a fixed set of pure transforms, validated by JSON Schema and dry-runnable. User scripting
(Lua/JS/expr) was rejected for v1: security surface, harder validation and non-deterministic behaviour. Revisit if
the transform set proves too limiting.
