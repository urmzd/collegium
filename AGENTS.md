# AGENTS.md

## What this is

A Go module that composes mandatum, saige, legatus and duraturo into one agent
platform. vultus supplies the components. The design is in
[docs/architecture.md](docs/architecture.md).

## Rules

- **Compose, don't reimplement.** If mandatum, saige, legatus or duraturo owns
  a concern, use it or change it upstream.
- **One way to call an agent.** Subagents, handoffs and delegations all go
  through `pkg/call`. Do not add a second path.
- **Default deny.** A zero value grants nothing: no calls, no budget, no access.
- **Fail closed.** A missing or malformed edge on an agent's tag is an error.
- **Nothing presentational here.** Rendering belongs in vultus. Anything that
  needs a network call, a database or an identity belongs here.
- **Tenant comes from the Principal.** Never read it from a request, and return 404 for another tenant's objects.
- **Every route checks one permission.** Add it in `Server.Handler`, next to the route.
- **The ledger decides whether a run is over.** Events report attempts. Do not infer an ending from silence.
- **Bound every query.** A window, a row cap and a bucket cap, enforced on the server.
- **Evals are deterministic.** A check is a pure function of the output. Do not add a model as a judge without saying so in the suite.
- **One title per screen, on the page.** There is no top bar. What is running lives on the Runs page.
- **Agents are told apart by color, not by letters in boxes.** Use `AgentMark`. Controls under the composer are text, not boxes.
- **The API has one contract.** A route, a handler's answer or a type's JSON
  fields change together with `api/openapi.yaml`, then `make client`. Never edit
  `packages/client/src/generated`.
- **Keep the status table honest.** When a piece lands, update
  `docs/architecture.md#status` in the same change.

## Layout

- `pkg/call`: the call primitive. `pkg/runs`: the run index and statistics. `pkg/auth`: principals, roles, OIDC.
- `pkg/journal`: the event log, queries and traces. `pkg/alerts`: rules and alerts. `pkg/knowledge`: sources and search.
- `pkg/memory`: what an agent carries between conversations. `internal/platform/hooks.go`: housekeeping runs.
- `pkg/clients`: the Slack apps and GitHub installations a workspace answers through. `internal/platform/connectors.go`: one surface per scheme, fanning in and out to them.
- `pkg/evals`: suites and runs, scored by saige. `pkg/pick`: who answers a message. `pkg/prefs`: a person's own settings.
- `internal/platform`: the composition root. `internal/httpapi`: routes, sessions, event stream.
- `cmd/collegium`: the binary. `apps/web`: the console, built on vultus. `apps/slack`: the Slack app, a client of the API.
- `packages/adapter`: the one adapter between the platform's wire shapes and vultus's model. The console and the Slack app both use it; nothing surface-specific goes in it.
- `api/openapi.yaml`: the API contract, checked by `internal/httpapi/openapi_test.go`. `packages/client`: the TypeScript client generated from it by oag.

## Commands

| Task | Command |
|------|---------|
| Set up | `make init` |
| Format, lint and test | `make check` |
| Test | `make test` |
| Preview | `make dev`, then `make web` |
| Web checks | `cd apps/web && pnpm typecheck && pnpm test` |
| Adapter checks | `cd packages/adapter && pnpm typecheck && pnpm test` |
| Slack app checks | `cd apps/slack && pnpm exec biome check && pnpm typecheck && pnpm test` |
| Regenerate the client | `make client` (oag version pinned in the `Makefile`) |
| Client checks | `make client-check` |
| Lint the spec | `make spec-lint` |

## Conventions

- Conventional commits. `sr.yaml` derives versions from them.
- Money is an `int64` of micro-USD. No floats.
- Tests run with `-race`.
- No em dashes in docs or comments.
