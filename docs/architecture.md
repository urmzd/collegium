# Architecture

This is the plan of record. The [status table](#status) says what exists.

## What this repo is

open-agent-platform is the composition. It owns the pieces that only exist once
the parts are put together: the call tree, governance, routing work to people,
and the stores behind them. It does not reimplement what it composes.

| Part | Owns | Repo |
|------|------|------|
| Bus, topics, connectors, agent specs, revision pinning | Transport and surfaces | [opentag](https://github.com/urmzd/opentag) |
| Agent loop, tools, MCP client, RAG, evals, OTel | Runtime | [saige](https://github.com/urmzd/saige) |
| Sandboxed execution nodes, NGAC | Confinement | [dispatch](https://github.com/urmzd/dispatch) |
| Durable runs and replay | Crash survival | [duraturo](https://github.com/urmzd/duraturo) |
| Components and helpers for web and Slack | Presentation | agent-ui |

## The boundary with agent-ui

agent-ui is components and helpers only. It takes data in as props and events
and emits actions out.

The test for where something goes: if it needs a network call, a database or an
identity, it belongs here. The platform owns the wire. It translates opentag
envelopes into agent-ui events and turns agent-ui actions back into tags. That
adapter is the only place the two meet.

## One primitive: the call

An agent is defined by a spec and runs in a sandbox. When one agent needs
another, it makes a call: a new run of the other agent, in its own sandbox,
raised as a tag on the bus and linked to its caller by an edge.

A subagent, a handoff and a delegation are the same call with a different mode.

| Mode | Context passed | Caller after the call | Answer goes to |
|------|----------------|-----------------------|----------------|
| Subagent | Brief | Parks, then resumes | The caller |
| Handoff | Compacted history | Ends | The original surface |
| Delegate | Brief | Keeps working | The caller's inbox, later |

### Detached branches

The child is a durable run of its own, with its own journal, context window and
topic (`agent:<name>:<runID>`). The caller holds a reference and a result, never
the child's work. Each branch compacts alone, and a tree can spread across
nodes.

opentag's built-in delegate tool is the attached counterpart: a sub-task inside
the caller's turn with no run id and no stream. Both shapes stay available.

## Governance rides the edge

| Concern | Rule |
|---------|------|
| Cost | A child leases budget from its caller and settles when it completes. Limits apply to the whole tree, keyed by the root run. |
| Access | A child gets the intersection of its spec's access and its caller's grant. It is never wider. |
| Shape | Depth and fan-out are capped. The zero limits allow no calls. |
| Tracking | Root, parent, depth and trace context travel with the tag, so telemetry shows one tree. |
| Stopping | Cancelling a run cancels everything beneath it. |

## People in the loop

A run that needs a person parks. A policy names who may resolve it, by role
rather than by person. The item lands in every matching inbox, the first valid
decision resumes the run, and the decision is recorded for audit.

A budget request is an approval. A child that exhausts its lease parks and asks
its caller's owner for more, so one flow covers cost limits and approvals.

An approver cannot grant more than they hold.

## Decentralized

A peer node is a connector. One node addresses an agent on another the same way
it addresses a Slack thread, so a cross-node call is a tag with a route.

This depends on execution nodes that publish to the bus themselves. Today
opentag streams out of the sandbox through an in-process channel, so every
agent runs inside the serving process.

## Storage

Target the Postgres wire protocol. AlloyDB is one deployment of it, not a
requirement.

Known limit: duraturo's Postgres queue does not implement the delta log, so a
Postgres-only deployment runs durably but cannot stream. Redis stays in the
stack until that is closed.

## Status

| Piece | State |
|-------|-------|
| `pkg/call`: edge, modes, depth, fan-out and lease checks | Built, tested |
| `pkg/runs`: run index, call tree, statistics | Built, in memory |
| `internal/platform`: opentag composed, `Invoke`, `Call` with the spawn allowlist | Built, tested |
| Ledger reconciler: ends runs whose retries ran out with no final event | Built, tested |
| `pkg/auth`: OIDC sign-in, machine tokens, roles and scopes | Built, tested against an in-process issuer |
| HTTP API, sessions, event stream | Built, tested |
| Schedules through opentag's cron trigger: pause, run now, next fires, history | Built, in memory |
| `pkg/journal`: event log, query, run traces | Built, a bounded ring in memory |
| `pkg/alerts`: rules on run failure, failure rate and duration | Built, in memory |
| `pkg/knowledge`: sources, documents, BM25 search, per-document exclusion, saved variants | Built, in memory, lexical only |
| `pkg/memory`: per-agent notes sent with every turn, written by people or by a memory agent | Built, in memory |
| Housekeeping runs: a title agent names a conversation, a memory agent writes notes after a turn | Built. Off until a workspace picks the agents, and never on the offline provider |
| `pkg/evals`: suites of deterministic checks for agents, retrieval metrics for knowledge, scored by saige | Built, in memory |
| Conversations: threads, branches, any agent on any turn, replayed history with a configurable limit | Built, in memory |
| `pkg/pick`: lexical agent picker over name, description and prompt, with a baseline | Built |
| Projects, workspace settings, `pkg/prefs`: per-person theme, accent and profile picture | Built, in memory |
| Web console on agent-ui: chat, inbox, agent editor with revisions, evals, knowledge, schedules, analytics, alerts, settings | Built |
| Local accounts with a first-user admin, for running without an identity provider | Not built. Without OIDC the only mode is loopback dev auth |
| Model parameters on an agent (temperature, token and step limits) | Not built. opentag's agent spec has no fields for them |
| Stopping a run in flight | Not built. The composer's queue waits for the run to end |
| Skill consolidation | Not built |
| Uploading binary files (PDF, images) to knowledge or a message | Not built. Files are read as text in the browser |
| Evals against a knowledge variant | Not built. Evals search the current view |
| Summarizing history | Not built. Long conversations are truncated to the limit, oldest turns first |
| Knowledge retrieval inside a run | Not built. opentag's retriever is not given the tenant or the agent, so wiring it would cross tenants |
| Connections: MCP servers, OAuth to third parties, stored credentials | Not built |
| Schedule delivery to Slack, email or another system | Not built. Needs connections |
| Call tool inside the sandbox | Not built. Calls are made through the API |
| Returning a subagent's answer to a parked caller | Not built |
| Spend tracking | Not built. Only granted leases are counted |
| Cancel propagation down a tree | Not built |
| Access intersection across a call | Not built |
| Approvals and budget requests in the inbox, routed by role | Not built. The inbox shows alerts and failed runs |
| Alert delivery outside the console | Not built |
| OTel export | Not built. Traces are rebuilt from the journal |
| Evals that run on their own, on a schedule or on each revision | Not built. Evals run when someone starts them |
| Model-graded evals | Not built, on purpose for now: every check is exact |
| Email connector, MCP server, Slack and Jira wiring | Not built |
| Remote execution nodes, peer connector | Not built |
| Postgres stores | Not built. Everything above is lost on restart |
