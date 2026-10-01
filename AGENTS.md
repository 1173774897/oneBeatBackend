# oneBeat Backend Project Rules

## Code Comments

- Add comments where they preserve information that the code alone cannot make obvious: business invariants, security boundaries, transaction and concurrency semantics, external-system limitations, cache or fallback behavior, and intentional tradeoffs.
- Comments should explain why a choice exists and what must remain true. Do not narrate straightforward syntax or add comments to every line.
- Give exported types and functions concise documentation comments when their contract or role is not already self-evident.
- Keep comments next to the code they constrain, and update or remove them whenever behavior changes. A stale comment is a defect.
- Never place secrets, redemption plaintext, personal data, or sensitive payload examples in comments.
