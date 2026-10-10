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
| Ledger that supplies a run's calls and spend | Not built |
| Call tool in the sandbox, and returning a subagent's answer | Not built |
| Cancel propagation down a tree | Not built |
| Access intersection across a call | Not built |
| Envelope adapter for agent-ui | Not built |
| Inbox routing by role | Not built |
| Alert rules over bus events | Not built |
| OTel across the bus | Not built |
| Online evals and analytics | Not built |
| Email connector, MCP server | Not built |
| Remote execution nodes, peer connector | Not built |
| Postgres stores | Not built |
