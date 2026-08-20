# GitHub Copilot instructions

## Commit messages

When generating a Git commit message, describe only the changes selected for
the commit and use this exact structure:

```text
<type>(<optional-scope>): <concise imperative summary>

Objective: <concrete outcome>
Invariant: <behavior or safety property preserved>
Evidence: <tests, checks or runtime validation>
Limitations: <known boundaries or None.>
```

- Write the message in English.
- Use `feat`, `fix`, `refactor`, `test`, `docs`, `build`, `chore`, `ci` or
  `perf` as the type. Omit the parentheses when there is no useful scope.
- Never use `agent:` or identify who authored the change.
- Replace every placeholder and complete all four body sections in that order.
- Do not claim that a test, check or runtime validation ran unless that evidence
  is available. Write `Evidence: Not run (not provided).` when it is unknown.
- Write `Limitations: None.` when no limitation is evident.
