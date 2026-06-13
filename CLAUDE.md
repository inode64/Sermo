# Sermo

Project conventions for all agents live in [AGENTS.md](AGENTS.md) — **read and follow them from the first step**.

In particular: any agent that will modify code **must** create a dedicated `git worktree`
(see "AI / agent workspaces" section) so that multiple agents can run in parallel and every
completed change is merged back into the local `main` branch from the primary checkout.
Never edit directly in the human's primary tree.

User documentation: `docs/`.
