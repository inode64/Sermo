# Git commit message instructions (JetBrains Copilot)

PhpStorm / IntelliJ Copilot **does not** read `.gitmessage` or `commit.template`
when generating a commit message. This file is the workspace prompt for
**Settings → Tools → GitHub Copilot → Custom Instructions → Git Commit
Instructions** (Workspace). Keep it in lockstep with `.github/copilot-instructions.md`
and the commit contract in AGENTS.md.

When generating a Git commit message, describe only the changes selected for
the commit. Output **only** the commit message, in English, with this exact
structure and no extra commentary:

```text
<type>(<optional-scope>): <concise imperative summary>

Objective: <concrete outcome>
Invariant: <behavior or safety property preserved>
Evidence: <tests, checks or runtime validation>
Limitations: <known boundaries or None.>
```

Rules:

- Use `feat`, `fix`, `refactor`, `test`, `docs`, `build`, `chore`, `ci` or
  `perf` as the type. Omit the parentheses when there is no useful scope.
- Never use an `agent:` prefix or identify who authored the change.
- Replace every placeholder. Complete all four body headings in that order.
- Do not claim that a test, check or runtime validation ran unless that
  evidence is in the diff or known. Write `Evidence: Not run (not provided).`
  when it is unknown.
- Write `Limitations: None.` when no limitation is evident.
- Do not invent Conventional Commits footers (`BREAKING CHANGE`, `Co-authored-by`)
  unless the staged diff requires them.
