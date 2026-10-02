# oneBeat Backend Project Rules

## Code Comments

- Add comments where they preserve information that the code alone cannot make obvious: business invariants, security boundaries, transaction and concurrency semantics, external-system limitations, cache or fallback behavior, and intentional tradeoffs.
- Comments should explain why a choice exists and what must remain true. Do not narrate straightforward syntax or add comments to every line.
- Give exported types and functions concise documentation comments when their contract or role is not already self-evident.
- Keep comments next to the code they constrain, and update or remove them whenever behavior changes. A stale comment is a defect.
- Never place secrets, redemption plaintext, personal data, or sensitive payload examples in comments.

## SQL Performance

- Design every new or changed SQL query together with its access path. Equality filters should match leading index columns; range filters and `ORDER BY` columns should follow in the order the query consumes them.
- Avoid unbounded scans, broad index-prefix scans, and multi-column `OR` predicates on growing tables. Split lookups when that makes index selection predictable without changing business priority.
- Use partial or covering indexes only when they materially reduce rows or heap reads; account for index size and write amplification instead of adding speculative indexes.
- Check hot-path and background-job queries with PostgreSQL `EXPLAIN` against representative data. A sequential scan is acceptable only for deliberately small or bounded tables and should be documented when the reason is not obvious.
- Add or remove indexes and tables through new reversible migrations. Never rewrite a migration that may already have been applied.
