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

### Stopping a run

A stop is the runtime's, not the platform's. `POST /api/runs/{id}/stop` and a
Slack stop button both call mandatum's `runtime.Stop` with a reason and who
asked. The runtime records the request on the run in the duraturo ledger
(`runtime.WithStopLedger`), so a run still in the queue or between attempts
honours it whenever it starts, and publishes it on the bus to the attempt
executing the run, which cancels the model call in flight and any running
tools, and ends the run with `lifecycle.stopped` carrying what the agent had
said. "Stopped" means cut short: the agent loop judges it, and a turn that had
finished its answer before the request took effect ends `completed`.

| State | Status | `stopping` | What it means |
|-------|--------|------------|---------------|
| Asked | unchanged (`accepted`, `running`, `parked`) | `true` | The request is published. The run can start no further calls from this moment. |
| Stopped | `stopped` | absent | The runtime confirmed it. `answer` is the partial answer people saw, `cause` is the reason. |
| Finished first | `completed` or `failed` | absent | The turn ended before the request reached it. It is reported as it ended, never as stopped. |

The route answers 202 with `stopping` while the run has not ended and does not
wait for it. A run that has ended is answered 200 as it is, so asking twice is
safe. The runtime gives a turn `StopGrace` (ten seconds by default) to wrap up
before it cancels it, and the run still ends stopped. A run in the queue stops
before its first model call when a worker picks it up.

After asking, the platform reads the ledger (`runtime.StopRequested`). A run
the ledger holds no request for had already ended there, so it is not shown as
`stopping`: its own terminal event is on its way. When the runtime cancels a
delegation because the caller was stopped (`runtime.ErrStopped` as the
context's cause), the run collegium started for it is asked to stop too.

A run that has answered and is only held open by what it attached has no turn
left to interrupt. It ends stopped at once, keeping its answer, and what is
attached to it is stopped through the runtime.

If the runtime cannot carry a stop (`runtime.ErrStopUnavailable`, a bus that
cannot be subscribed to), the request fails closed: the run is still marked
`stopping`, so it starts nothing more, the route answers 503, and the run ends
as its own events say. It is never reported stopped while it is still working.

A stopped turn is kept in its conversation. The next turn is sent its partial
answer followed by mandatum's `StopMarker` line. Who stopped it and why is not
sent to a model. Housekeeping (titles, memory) does not run after a stopped
turn, an eval case that was stopped is a failed case, and a stopped run counts
as `stopped` in the statistics, not as active.

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
| Stopping | Stopping a run stops everything attached beneath it, through the runtime. What it delegated carries on. |

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
| Clients and the secret values written for them are lost on restart, so a webhook is a 404 until they are created again | A durable `clients.Store`, and a secret manager behind `clients.Secrets`. `New` already starts every enabled client a store holds |
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
| GitHub and Slack integrations: signed webhook ingress, tenant-bound tools and delivery through `Invoke` | Built, tested with simulated APIs |
| `pkg/clients`: a workspace's Slack apps and GitHub installations as records, each with an identity, an allow-list of agents, options and an enabled flag. Several per surface per workspace, and several workspaces per process. See [clients](clients.md) | Built, in memory behind `clients.Store`. Tested with two clients in one workspace and two workspaces in one team |
| Routing a webhook to a client: offered to the clients whose identity it states, taken by the first whose own secret verifies the bytes. 404 for unknown or disabled, 401 for a signature no offered client accepts | Built. One surface per address scheme sits in mandatum's registry and fans in and out to the clients behind it |
| Client secrets: written as a value or an `env:COLLEGIUM_*` reference, never returned or logged | Built. Values are in memory behind `clients.Secrets` and lost on restart. A reference survives one |
| Secret references are default deny per tenant: read only for a variable the operator listed for that tenant with `-secret-refs`, checked where the secret is read, refused the same way whatever the reason | Built, tested across tenants and against a record written straight into the store |
| Client limits: `max_clients` and `max_clients_per_surface` on the tenant's policy, and a `client` policy scope for runs and concurrency | Built on `pkg/policy` and the admission lock. Spend and storage per client are not |
| A default client per surface from the `COLLEGIUM_SLACK_*` and `COLLEGIUM_GITHUB_*` variables | Built. Managed by the environment and read-only in the API |
| Client API: `/api/clients` create, read, replace, delete, behind `clients:read` and `clients:write` | Built, tested across tenants |
| Console for clients: Settings, Connections lists them, and an admin can disable or remove one | Built. Creating one, and setting its limits, are API calls. Not checked in a browser |
| Minting GitHub installation tokens, Slack OAuth installation | Not built. A client is given a token |
| GitHub auto-label and per-commit reviews, PR guides, explicit writer-authorized fix commits and workflow reruns | Built in `pkg/integrations/github`, on mandatum API capabilities. Rendering in vultus `go/github` |
| Slack agent-session status and code-channel command policy | Built in `pkg/integrations/slack`. Signed command decoding and optional `AgentAPI` in mandatum. Not live-validated |
| Slack stop button: `agent_session_stopped` asks the runtime to stop the runs answering in that session, confirms in the thread and settles the status. The run's message keeps its partial answer, marked stopped | Built, tested end to end against the fake with a scripted model whose call is shown to be cancelled. The session-to-run map is in memory and per client: one app's button cannot stop a run another app raised. Not live-validated |
| `pkg/call`: edge, modes, depth, fan-out and lease checks | Built, tested |
| `pkg/runs`: run index, call tree, statistics | Built, in memory |
| `internal/platform`: mandatum composed, `Invoke`, `Call` with the spawn allowlist | Built, tested |
| Ledger reconciler: ends runs whose retries ran out with no final event | Built, tested |
| `pkg/auth`: OIDC sign-in, machine tokens, roles and scopes | Built, tested against an in-process issuer |
| `pkg/authz`: a policy language mapping claims to roles and roles to permissions, with Explain | Built. The built-in policy reproduces the three roles. `COLLEGIUM_AUTHZ_POLICY` loads a file |
| `pkg/auth/mockidp`: a loopback-only development identity provider, `-auth=mock` | Built. Tokens go through the real verifier |
| HTTP API, sessions, event stream | Built, tested |
| API contract: `api/openapi.yaml`, OpenAPI 3.2, every route with its permission and a typed event union. Tests check it against the registered routes, the permissions the server enforces, the Go types' JSON fields, and real responses and the event stream. See [API](api.md) | Built. Webhook ingress under `/connectors` is left out on purpose |
| `packages/client`: `@collegium/client`, generated by oag from the contract, with a hand-written layer for auth, decoded events and downloads | Built, tested against an in-process server and run once against `make dev`. No surface uses it yet: the console still has its own `apps/web/src/api.ts` |
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
| `pkg/policy`: versioned limits per workspace, project, agent and client on runs, concurrency, spend and lease; a narrower scope can only tighten | Built, in memory. One pure `Decide` function, so a refusal can be replayed from the policy version it names |
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
| Console home: vultus `Home` as the default view, its model built by `apps/web/src/home.ts` from what the console fetches, its actions routed in place | Built, unit-tested, checked in a browser as an operator, a viewer and someone with no role. Personal accounts and Jira are not offered: the platform has neither |
| Console menu: Home, New conversation, Inbox, then Work, Build, Watch and Workspace. An entry is hidden from someone who may not read what it opens | Built. Connections, Access and Limits open their Settings sections |
| "Why can I do this?": `GET /api/access/explain` and the Access panel. A sign-in keeps the grant behind each role, not the claims or the token | Built, tested. Sessions are in memory, so a restart signs everyone out |
| Local accounts with a first-user admin, for running without an identity provider | Not built. Without OIDC the modes are loopback dev auth and the loopback mock provider |
| Model parameters on an agent (temperature, token and step limits) | Not built. mandatum's agent spec has no fields for them |
| A stop control in the console's composer | Not built. The API stops a run in flight and the console's event adapter ends a turn on `lifecycle.stopped`, but the composer has no stop button and its queue still waits for the run to end |
| Skill consolidation | Not built |
| Sandbox: each conversation has a scratch directory that agents with the file tools read and write. It is opened as an `os.Root`, bounded in file size, count and total, and removed after 7 days untouched | Built, tested against traversal, absolute paths and planted links |
| `pkg/blob`: put, get, stat, delete, list behind one interface, on disk or in memory, with a conformance suite a new backend must pass | Built. An S3-compatible store is not written |
| `pkg/artifacts`: published files with a sniffed type, sha256, and a version per title per conversation | Built, the index is in memory, the bytes are on disk |
| Publishing: the `publish_artifact` tool copies a sandbox file to storage and reports it on the run's stream, where the console shows it as a card | Built |
| Storage limits per workspace, project and agent, as `max_storage` on a policy | Built. A refusal raises a message in the inbox |
| Downloads: tenant and right checked on every request, always an attachment unless an inert type is asked for inline, never HTML or SVG inline | Built, tested |
| Built-in agents: `collegium-writer` writes a document and publishes it. Provided by the platform, not editable, present only when the server is given `-builtin-provider` and `-builtin-model` | Built. One agent. Not run against a real model here: no provider key was available |
| Called agents are runs: an agent that tags another starts a run with its own id, events and record, in the caller's conversation and sandbox | Built, on mandatum's `WithDelegator` hook |
| Run ids are the runtime's, derived from the workspace and the tag id together, so two workspaces offering one trigger id get two runs. Every index, route, ledger entry and grant uses the id the runtime returns | Built on mandatum v0.7.0 and later, tested with two workspaces reusing one tag id and one Slack event id |
| Two lifetimes for a called run. Attached (tagged, the caller waits): ends with its caller, and the caller is not done until it is. Delegated (`delegate_task`, the caller carries on): independent, and its answer goes to the inbox | Built, tested three levels deep under the race detector |
| Stopping a run: `POST /api/runs/{id}/stop` asks the runtime to stop it and everything attached to it. The model call in flight is cancelled, the run ends `stopped` with its partial answer and cause, and what it delegated keeps running. See [Stopping a run](#stopping-a-run) | Built on mandatum v0.8.0's `runtime.Stop`, recorded in the run ledger, tested with scripted models under the race detector, three levels deep, and against a completion racing the stop |
| A place is always kept for work that waits on nobody, so runs waiting on each other cannot fill every place and hang | Built. With `Concurrency` 4 by default, three runs may wait at once and a fourth is told no |
| Publishing that waits for approval | Not built. A run cannot be parked on a person |
| Run manifest: one JSON document of a run, every run under it, each pinned definition and every event with its body | Built, from the journal. A run older than the journal's ring comes back marked incomplete |
| Token counts, context window and cost per run | Not available. The runtime reports none |
| Uploading binary files (PDF, images) to knowledge or a message | Not built. Files are read as text in the browser |
| Evals against a knowledge variant | Not built. Evals search the current view |
| Summarizing history | Not built. Long conversations are truncated to the limit, oldest turns first |
| Knowledge retrieval inside a run | Not built. mandatum's retriever is not given the tenant or the agent, so wiring it would cross tenants |
| Connections: MCP servers, OAuth to third parties | Not built. Slack and GitHub credentials are held per client, see above |
| Schedule delivery to Slack, email or another system | Not built. Needs connections |
| Call tool inside the sandbox | Not built. Calls are made through the API |
| Returning a subagent's answer to its caller | Built for a call made by tagging: the answer is the tool's result. A subagent that was stopped returns an error saying so, not its partial answer. A call made through the API is not returned to the caller's model |
| Cancel propagation down a tree | Built through attached runs: each is asked to stop through the runtime, at the request and again when the runtime cancels the delegation it was waiting on, and keeps its own partial answer. Delegated runs are not touched |
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
| Email connector, MCP server and Jira wiring | Not built |
| Remote execution nodes, peer connector | Not built |
| Postgres stores | Not built. Everything above is lost on restart |
