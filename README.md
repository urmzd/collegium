<p align="center">
  <h1 align="center">open-agent-platform</h1>
  <p align="center">
    Spec-defined agents in sandboxes that call each other over one bus, with governance, human approvals, evals and telemetry built in.
  </p>
</p>

> ## Status
>
> **Early.** One process runs agents through opentag, with sign-in,
> conversations that branch across agents, an agent editor with revisions,
> evals, schedules, alerts, analytics, traces, knowledge search and a web
> console. Every store is in memory. Connections (MCP, OAuth), approvals, local
> accounts and the remaining connectors are not built. See the
> [status table](docs/architecture.md#status).

## What it is

The composition of [opentag](https://github.com/urmzd/opentag) (bus and
connectors), [saige](https://github.com/urmzd/saige) (agent runtime and evals),
[dispatch](https://github.com/urmzd/dispatch) (sandboxes) and
[duraturo](https://github.com/urmzd/duraturo) (durability), with agent-ui as
the component library for web and Slack.

The idea that holds it together: **an agent calls another agent by raising a
tag at it.** A subagent, a handoff and a delegation are the same call with a
different mode. Each call is its own run in its own sandbox, so branches are
detached, compact alone, and can spread across nodes. Budgets, access and depth
limits are checked on the edge between the two runs.

Read [docs/architecture.md](docs/architecture.md) for the full design.

## Preview

<p align="center">
  <img src="showcase/runs.png" alt="The Runs page: a tape of recent runs above a searchable, filterable list" width="49%">
  <img src="showcase/agents.png" alt="The Agents page: one lane per agent with its recent runs and the last day's figures" width="49%">
</p>
<p align="center">
  <img src="showcase/onboarding.png" alt="The first-run page that asks the first admin for their name and the workspace's" width="49%">
  <img src="showcase/analytics.png" alt="The Analytics page: runs, success rate and duration for a time window" width="49%">
</p>

The screenshots are captured with [teasr](https://github.com/urmzd/teasr) from `teasr.toml`.

```sh
make dev   # server on 127.0.0.1:8080, no sign-in, two example agents
make web   # console on http://127.0.0.1:5173
```

`make dev` uses dev auth, which only listens on loopback. The example agents
use opentag's offline provider and call no model. Set an agent's provider to
`anthropic` with `ANTHROPIC_API_KEY` in the environment for real answers.

The console links agent-ui from a sibling checkout at `../agent-ui`.

For real sign-in, roles and machine tokens, see
[docs/security.md](docs/security.md).

## Conversations

`POST /api/runs` takes `agent`, `text`, and optionally `after` and `project`.

- Leave `agent` empty or `auto` and the platform picks: the agent whose name,
  description or prompt best matches the message, or the workspace's baseline.
- `after` names the turn the message follows. Naming a turn that already has a
  reply starts a branch. Any agent can answer any turn, and is sent the branch
  so far.

## API

| Route | Permission |
|-------|------------|
| `GET /api/me` | Signed in |
| `GET`, `POST /api/agents`, `PUT /api/agents/{name}`, `GET /api/agents/{name}/history` | `agents:read`, `agents:write` |
| `GET`, `POST /api/runs`, `GET /api/runs/{id}`, `/tree`, `/trace` | `runs:read`, `runs:write` |
| `GET /api/threads`, `GET /api/threads/{id}` | `runs:read` |
| `GET /api/pick?q=`, `GET`, `PUT /api/settings` | `agents:read`, `agents:write` |
| `GET`, `POST /api/evals`, `PUT`, `DELETE /api/evals/{name}`, `POST .../run`, `GET .../runs` | `evals:read`, `evals:write` |
| `GET`, `PUT /api/preferences`, `GET`, `PUT`, `DELETE /api/preferences/photo` | Signed in, the caller's own |
| `POST /api/runs/{id}/calls` | `runs:write` |
| `GET /api/events?topic=agent[:name[:run]]` | `runs:read`, server-sent events |
| `GET /api/analytics?window=1h` | `runs:read` |
| `GET /api/journal?agent=&run=&kind=&origin=&q=&window=` | `runs:read` |
| `GET`, `POST /api/schedules`, `DELETE /api/schedules/{name}`, `POST .../pause`, `.../resume`, `.../run` | `schedules:read`, `schedules:write` |
| `GET /api/alerts`, `POST /api/alerts/{id}/ack`, `GET`, `POST`, `DELETE /api/alert-rules` | `alerts:read`, `alerts:write` |
| `GET`, `POST /api/knowledge`, `.../{name}/documents`, `GET /api/knowledge/search?q=` | `knowledge:read`, `knowledge:write` |

## Develop

| Task | Command |
|------|---------|
| Set up hooks and modules | `make init` |
| Format, lint and test | `make check` |
| Test only | `make test` |
| Web typecheck and tests | `cd apps/web && pnpm typecheck && pnpm test` |

## License

[Apache-2.0](LICENSE)
