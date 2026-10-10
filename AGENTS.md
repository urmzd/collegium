# AGENTS.md

## What this is

A Go module that composes opentag, saige, dispatch and duraturo into one agent
platform. agent-ui supplies the components. The design is in
[docs/architecture.md](docs/architecture.md).

## Rules

- **Compose, don't reimplement.** If opentag, saige, dispatch or duraturo owns
  a concern, use it or change it upstream.
- **One way to call an agent.** Subagents, handoffs and delegations all go
  through `pkg/call`. Do not add a second path.
- **Default deny.** A zero value grants nothing: no calls, no budget, no access.
- **Fail closed.** A missing or malformed edge on an agent's tag is an error.
- **Nothing presentational here.** Rendering belongs in agent-ui. Anything that
  needs a network call, a database or an identity belongs here.
- **Keep the status table honest.** When a piece lands, update
  `docs/architecture.md#status` in the same change.

## Commands

| Task | Command |
|------|---------|
| Set up | `make init` |
| Format, lint and test | `make check` |
| Test | `make test` |

## Conventions

- Conventional commits. `sr.yaml` derives versions from them.
- Money is an `int64` of micro-USD. No floats.
- Tests run with `-race`.
- No em dashes in docs or comments.
