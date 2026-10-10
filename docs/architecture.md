# Architecture

This is the plan of record. The [status table](#status) says what exists.

## What this repo is

collegium is the composition. It owns the pieces that only exist once
the parts are put together: the call tree, governance, routing work to people,
and the stores behind them. It does not reimplement what it composes.

| Part | Owns | Repo |
|------|------|------|
| Bus, topics, connectors, agent specs, revision pinning | Transport and surfaces | [mandatum](https://github.com/urmzd/mandatum) |
| Agent loop, tools, MCP client, RAG, evals, OTel | Runtime | [saige](https://github.com/urmzd/saige) |
| Sandboxed execution nodes, NGAC | Confinement | [legatus](https://github.com/urmzd/legatus) |
| Durable runs and replay | Crash survival | [duraturo](https://github.com/urmzd/duraturo) |
| Components and helpers for web and Slack | Presentation | vultus |

## The boundary with vultus

vultus is components and helpers only. It takes data in as props and events
and emits actions out.

The test for where something goes: if it needs a network call, a database or an
identity, it belongs here. The platform owns the wire. It translates mandatum
envelopes into vultus events and turns vultus actions back into tags. That
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
| Delegate | Brief | Keeps working | The inbox, later |

### One origin, two lifetimes

Every run belongs to one origin: the run a person, a schedule or a trigger
started. Runs nest under it as deep as the limits allow, and each records its
origin, its caller and how it was called.

| Lifetime | How it is started | If the caller fails or is stopped | When the caller answers first |
|----------|-------------------|-----------------------------------|-------------------------------|
| Attached | Tagging an agent, or a subagent call | It is stopped, and so is all that is attached to it | The caller is held open until it ends |
| Delegated | `delegate_task`, or a delegate or handoff call | Nothing. It carries on | Nothing. The caller is done |

What a delegated run attaches dies with it, not with whoever delegated to it.
A run stopped this way is `stopped`, not `failed`, and says why.

### Detached branches

The child is a durable run of its own, with its own journal, context window and
topic (`agent:<name>:<runID>`). The caller holds a reference and a result, never
the child's work. Each branch compacts alone, and a tree can spread across
nodes.

mandatum's built-in delegate tool is the attached counterpart: a sub-task inside
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
mandatum streams out of the sandbox through an in-process channel, so every
agent runs inside the serving process.

## Storage

Target the Postgres wire protocol. AlloyDB is one deployment of it, not a
requirement.

Known limit: duraturo's Postgres queue does not implement the delta log, so a
Postgres-only deployment runs durably but cannot stream. Redis stays in the
stack until that is closed.

## Limits at scale

Admission is one process holding one lock, reading counts from memory. That is correct for a single server and wrong for more than one.

| Limit today | What it needs |
|-------------|---------------|
| The admission lock is in process, so two servers can both admit the last run | A counter in the shared store, taken in the same transaction as the ledger entry |
| The usage ledger and policies are lost on restart | A durable ledger. `Rebuild` already derives every count from entries |
| Usage is recounted from entries on each read | Counters per scope and period, checked against a rebuild |
| Policy history grows without bound | Paging, as for conversations |
| The inbox record grows without bound, and open counts are a scan | A durable log, with counters per reader |
| The artifact index is in memory while the bytes are on disk, so a restart orphans stored files | The index in the same durable store as everything else |
| A download reads the whole file into memory, which the 2 MB cap makes safe | Streaming, before the cap is raised |
| The journal is a ring of 100,000 events | An event store, or OTel export |

## Status

| Piece | State |
|-------|-------|
| `pkg/call`: edge, modes, depth, fan-out and lease checks | Built, tested |
| `pkg/runs`: run index, call tree, statistics | Built, in memory |
| `internal/platform`: mandatum composed, `Invoke`, `Call` with the spawn allowlist | Built, tested |
| Ledger reconciler: ends runs whose retries ran out with no final event | Built, tested |
| `pkg/auth`: OIDC sign-in, machine tokens, roles and scopes | Built, tested against an in-process issuer |
| HTTP API, sessions, event stream | Built, tested |
| Schedules through mandatum's cron trigger: pause, run now, next fires, history | Built, in memory |
| `pkg/journal`: event log, query, run traces | Built, a bounded ring in memory |
| `pkg/alerts`: rules on run failure, failure rate and duration | Built, in memory |
| `pkg/knowledge`: sources, documents, BM25 search, per-document exclusion, saved variants | Built, in memory, lexical only |
| `pkg/memory`: per-agent notes sent with every turn, written by people or by a memory agent | Built, in memory |
| Housekeeping runs: a title agent names a conversation, a memory agent writes notes after a turn | Built. Off until a workspace picks the agents, and never on the offline provider |
| `pkg/evals`: suites of deterministic checks for agents, retrieval metrics for knowledge, scored by saige | Built, in memory |
| Conversations: threads, branches, any agent on any turn, replayed history with a configurable limit | Built, in memory |
| `pkg/pick`: lexical agent picker over name, description and prompt, with a baseline | Built |
| Projects: conversations filed together, shared memory sent to every agent in the project, schedules whose runs are filed there | Built, in memory |
| Workspace settings, `pkg/prefs`: per-person theme, accent and profile picture | Built, in memory |
| Memory approval: what the memory agent writes waits for a person, following saige's memory policy | Built |
| `pkg/policy`: versioned limits per workspace, project and agent on runs, concurrency, spend and lease; a narrower scope can only tighten | Built, in memory. One pure `Decide` function, so a refusal can be replayed from the policy version it names |
| `pkg/usage`: append-only ledger of admitted, refused, warned and ended runs, with `Rebuild` from its entries | Built, in memory. Admissions are kept 62 days |
| Admission: every run is checked under one lock before it is accepted; a refusal answers 429 or 402 with the limit, the policy version and the reset time, and raises a budget alert | Built, tested with 40 racers against a limit of 7 |
| Console for limits: Settings, each project page, and usage against limits in Analytics | Built |
| Spend limits | Stored and versioned, not enforced. mandatum reports no cost for a run, so `spend.metered` is false |
| Asking a person to approve a run at a limit | Not built. A run cannot be parked on a person, so the only action is to block |
| Limits set by a project's owner | Not built. Only an admin sets limits, because projects have no owners |
| First-run setup: the first admin names themselves and the workspace | Built. `first_admin`, `onboarded_at` and the workspace's setup fields |
| Recents: conversations paged by an opaque keyset cursor, 200 a page | Built |
| Project membership and per-project access | Not built. Everyone in the workspace sees every project |
| Web console on vultus: chat, inbox, agent editor with revisions, evals, knowledge, schedules, analytics, alerts, settings | Built |
| Local accounts with a first-user admin, for running without an identity provider | Not built. Without OIDC the only mode is loopback dev auth |
| Model parameters on an agent (temperature, token and step limits) | Not built. mandatum's agent spec has no fields for them |
| Stopping a run in flight | Not built. The composer's queue waits for the run to end |
| Skill consolidation | Not built |
| Sandbox: each conversation has a scratch directory that agents with the file tools read and write. It is opened as an `os.Root`, bounded in file size, count and total, and removed after 7 days untouched | Built, tested against traversal, absolute paths and planted links |
| `pkg/blob`: put, get, stat, delete, list behind one interface, on disk or in memory, with a conformance suite a new backend must pass | Built. An S3-compatible store is not written |
| `pkg/artifacts`: published files with a sniffed type, sha256, and a version per title per conversation | Built, the index is in memory, the bytes are on disk |
| Publishing: the `publish_artifact` tool copies a sandbox file to storage and reports it on the run's stream, where the console shows it as a card | Built |
| Storage limits per workspace, project and agent, as `max_storage` on a policy | Built. A refusal raises a message in the inbox |
| Downloads: tenant and right checked on every request, always an attachment unless an inert type is asked for inline, never HTML or SVG inline | Built, tested |
| Built-in agents: `collegium-writer` writes a document and publishes it. Provided by the platform, not editable, present only when the server is given `-builtin-provider` and `-builtin-model` | Built. One agent. Not run against a real model here: no provider key was available |
| Called agents are runs: an agent that tags another starts a run with its own id, events and record, in the caller's conversation and sandbox | Built, on mandatum's `WithDelegator` hook. Needs the mandatum branch `feat/delegated-runs`, pinned by a `replace` in `go.mod` until it is tagged |
| Two lifetimes for a called run. Attached (tagged, the caller waits): ends with its caller, and the caller is not done until it is. Delegated (`delegate_task`, the caller carries on): independent, and its answer goes to the inbox | Built, tested three levels deep under the race detector |
| Stopping a run: `POST /api/runs/{id}/stop` stops it and everything attached to it, with the cause recorded. What it delegated keeps running | Built. A stop is taken at the platform's edge: the run can start nothing more and its answer is discarded, but a model call in flight is not interrupted, because the runtime has no cancel |
| A place is always kept for work that waits on nobody, so runs waiting on each other cannot fill every place and hang | Built. With `Concurrency` 4 by default, three runs may wait at once and a fourth is told no |
| Publishing that waits for approval | Not built. A run cannot be parked on a person |
| Run manifest: one JSON document of a run, every run under it, each pinned definition and every event with its body | Built, from the journal. A run older than the journal's ring comes back marked incomplete |
| Token counts, context window and cost per run | Not available. The runtime reports none |
| Uploading binary files (PDF, images) to knowledge or a message | Not built. Files are read as text in the browser |
| Evals against a knowledge variant | Not built. Evals search the current view |
| Summarizing history | Not built. Long conversations are truncated to the limit, oldest turns first |
| Knowledge retrieval inside a run | Not built. mandatum's retriever is not given the tenant or the agent, so wiring it would cross tenants |
| Connections: MCP servers, OAuth to third parties, stored credentials | Not built |
| Schedule delivery to Slack, email or another system | Not built. Needs connections |
| Call tool inside the sandbox | Not built. Calls are made through the API |
| Returning a subagent's answer to its caller | Built for a call made by tagging: the answer is the tool's result. A call made through the API is not returned to the caller's model |
| Cancel propagation down a tree | Built through attached runs, as a stop at the platform's edge. See above |
| Access intersection across a call | Not built |
| `pkg/inbox`: one server inbox of messages with typed parts, per-person read state, paging, and a record of changes that rebuilds it | Built, in memory |
| What lands in the inbox: fired alert rules, failed runs no rule caught, proposed memories, reached limits | Built. An alert and its message are one state: settling either settles both |
| Resolving in the inbox: acknowledge, retry a failed turn, keep or discard proposed memories, raise a limit, dismiss, snooze | Built. Each needs the right for the thing it does, and a second resolve returns the first outcome |
| Approve, deny, choose and reply parts for a run waiting on a person | Defined, never raised. A run cannot be parked on a person |
| Messages addressed to one person or one role | Not built. A message goes to everyone who may read its kind |
| Alert delivery outside the console | Not built |
| OTel export | Not built. Traces are rebuilt from the journal |
| Evals that run on their own, on a schedule or on each revision | Not built. Evals run when someone starts them |
| Model-graded evals | Not built, on purpose for now: every check is exact |
| Email connector, MCP server, Slack and Jira wiring | Not built |
| Remote execution nodes, peer connector | Not built |
| Postgres stores | Not built. Everything above is lost on restart |
