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
the ledger holds no request for had already ended there, so it is settled from
the ledger at once and the route answers 200 with how it ended. When the runtime cancels a
delegation because the caller was stopped (`runtime.ErrStopped` as the
context's cause), the run collegium started for it is asked to stop too.

A run parked on a person is stopped the same way: the runtime resolves its
question as stopped and ends it without calling a model, and its inbox message
settles as stopped, with the cause.

A run that has answered and is only held open by what it attached has no turn
left to interrupt, and was not cut short. What is attached to it is stopped
through the runtime, and once they have ended it completes with its answer.

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
| Cost | A child leases budget from its caller and settles when it completes: the lease is released and what the child spent, priced by the runtime, becomes the caller's. Limits apply to the whole tree: every run counts against its root's project and client. See [Usage and spend](#usage-and-spend). |
| Access | A child gets the intersection of its spec's access and its caller's grant. It is never wider. |
| Shape | Depth and fan-out are capped. The zero limits allow no calls. |
| Tracking | Root, parent, depth and trace context travel with the tag, so telemetry shows one tree. |
| Stopping | Stopping a run stops everything attached beneath it, through the runtime. What it delegated carries on. |

## Usage and spend

The runtime reports what every model call used (mandatum's `usage` family,
v0.10.0 and later): tokens, cost from saige's price catalog in integer micros
of a currency, and the model's context window. collegium counts it, holds it
against limits, and stops what goes over.

### Counted once

| Source | When | Key | What it adds |
|--------|------|-----|--------------|
| `usage.call` on a run's stream | As each call completes | The runtime's `accounting_id` | The call, to the usage ledger, under the run's place |
| The run's total (`lifecycle.completed` or `lifecycle.stopped`, else the ledger's output, else `runtime.RecordedUsage` for a failed run) | When the run ends | `run:<id>` | Only what the calls already counted for the run do not cover: a call whose event was missed |

The platform's observer sees every event (a subscription with no kind filter
receives the usage family). A redelivered call, a redelivered terminal event
and a reconciler pass all find their key already counted and add nothing. A
run is counted as the larger, field by field, of the calls seen for it and its
settled total, and both are kept after it settles: a call whose event arrives
after the settlement (the call a stop cut short is reported late, while the
settlement runs from the reconciler or the stop) adds only what goes beyond
the settled total, in either order. Once a run has settled its `spent` no
longer moves, since it has settled into its caller. A run's settled total is
its own calls (`RunUsage.Own`), never the runs it called, which settle their
own: each model call in a tree is counted once, in the run that made it.

Every ledger entry is stamped with the server's clock when it is appended
(the call's own time is kept as `event_at`), and counts in the period its
stamp falls in, whatever order entries arrive in. Counts are kept per scope
and UTC day as entries are applied, so judging a call reads at most 62 days
of totals.

### Roll-up

An attached run (tagged, the caller waits) is rolled up into its caller: once
it has ended and the ledger records it, the Delegator calls
`runtime.ReportChildUsage`, and the caller's `usage.children` and `usage.total`
include it, once. A delegated run (`delegate_task`) is not rolled up: the turn
that handed it over has moved on, so there is no turn to report it to. Its
spend is the same request's work, so it counts against the same budgets: every
run in a tree counts against its root's project and client, its own agent, and
the workspace. The run manifest's `usage.total` sums every run's own calls in
the tree, attached and delegated alike.

### Enforced

| When | What is judged | What happens |
|------|----------------|--------------|
| A run asks to start | Spend and tokens in the period, with runs and concurrency, under one lock | A scope that has reached its spend limit, to the micro-dollar, or its token limit refuses: 402 for spend, 429 for tokens, with the limit, the policy and its version, and `resets_at` |
| A call is counted | Spend and tokens of every scope the run is in (`policy.Exceeded`) | At the warning threshold, one budget alert and one inbox message per scope and limit while it is open. At the limit, the run is stopped through `runtime.Stop`, with a reason naming the limit, the scope and the policy version, a `halted` notice, and one alert. Every other run in the scope is stopped at its own next call |
| A run with a lease spends | Its `spent` plus what it has handed to calls still working (`granted`), against its `lease` | Stopped through the runtime once the two reach the lease |
| A called run ends | Its lease and its spend | The caller's `granted` releases the lease and its `spent` gains what the child spent. If the caller has already settled, the spend climbs on to the nearest ancestor still open, or the root |

An agent's own calls (tagging, `delegate_task`) lease from a metered caller
without naming an amount: each is granted half of what the caller has left.
What is left is read from the calls already counted, and the model call that
asked may not be counted yet, so the other half holds room for it; everything
is trued up when the call settles. A caller with nothing left is refused. A
stop at a limit that could not be requested is asked again at the run's next
counted call. Housekeeping runs are never refused or stopped,
and what they spend is still counted.

### Unpriced calls and other currencies

A limit is in US dollars. A call saige could not price (a model with no rate
card) and money in any other currency are never counted as zero dollars and
never compared with a dollar limit. They are counted apart: `unpriced_calls`
and `other_costs` on the run, on `GET /api/usage`, and in Analytics, where the
spend is shown as a lower bound. The first such call in a scope with a spend
limit raises one alert, open until settled, saying its spend is a lower bound.
Tokens are counted for every call, priced or not, so `max_tokens` on a policy
is the bound that holds whatever the price.

### Where it shows

| Surface | What |
|---------|------|
| `GET /api/runs/{id}` | `usage` (own, children, total, context), `spent`, `granted` |
| The event stream and `listRunEvents` | `usage.call` and `usage.child` |
| `GET /api/usage` | `spend` (used, limit, `unpriced_calls`, `other_costs`) and `tokens` per scope |
| `GET /api/analytics` | `usage` per group: calls, tokens, spend, unpriced calls |
| Console | The run's Activity page (calls, tokens, cost, context), the session gauge's context edge, Analytics, Limits |
| Slack app | Nothing. Usage is not shown on a turn in the console either, so the shared adapter carries none (see [clients](clients.md#usage-on-a-turn)) |

## People in the loop

A run that needs a person parks on a question. mandatum (v0.11.0) owns the
waiting: the question is recorded on the run in the ledger, the run holds no
worker while it waits, and the first answer recorded is the answer. collegium
owns who is asked and where.

| Question | Raised by | Answered with |
|---|---|---|
| Approval | A call to a tool the agent's spec lists in `approvals` | `approve` (optionally with edited `arguments`, checked against the tool's schema) or `deny` (with a note the model is shown) |
| Choice | The agent calls `ask_person` with `options` | `choose` one option |
| Reply | The agent calls `ask_person` without options | `reply` in words |

### Who is asked

Who may answer is decided from the run's pinned revision, so revising an agent
does not change who may answer a question a run already asked:

| Question | Who may answer |
|---|---|
| Approval | `approvers` when the spec names them, else anyone in the workspace. Never the person whose request raised the run, nor the person who started the tree it is in, by any name they go by (a Slack user is also every principal a Slack client of the run's team maps them to, pinned when the run starts), unless the spec sets `self_approval`. mandatum's `AnswerRequest` takes no requester alias, so collegium checks this before it asks the runtime. A GitHub actor is mapped to no principal, so self-approval there is not detected |
| Choice, reply | `answerers` when the spec names them, else anyone in the workspace |

`approvers` and `answerers` name principals by subject and this workspace's
roles as `role:<name>`. The runtime asks collegium for a person's roles
(`runtime.WithRoles`): the roles they signed in with, or for a person a surface
answers for, what the authorization policy grants their subject. Anyone else
holds no role, so a role admits nobody it was not asked about.

Each question becomes one inbox message (kind `approval` or `clarification`,
with an `approval`, `choice` or `reply` part), addressed to exactly those
people: a reader sees it only when the spec names them or a role they hold,
and the requester never sees an approval of their own call. The agent's
`asked_of` hint narrows that to whom it asked, when that is one of them; it
admits nobody. A question whose rules cannot be read is addressed to nobody.
Answering needs `runs:write` as well (for a person a surface answers for,
their own grant must include it, not only the surface's credential's), and the
runtime checks who may answer again before it records anything.

Addressing routes a question to its inbox; it is not confidential. The
question and an approval's arguments are also on the run, its event stream
and its manifest, for anyone with `runs:read`. See
[security](security.md#the-inbox).

### Answering

From the inbox (`resolveInboxPart`), on the run (`answerQuestion`), or in Slack,
an answer is one call: `runtime.Answer`, with the person as `By` and the
credential's own subject as `Via`. A refusal is 403 and changes nothing. A
second answer, or one after the question expired or its run was stopped, is
answered with the message showing what was recorded. The decision is recorded
in the inbox's record of changes, with who answered and, for a surface, which
surface user.

The message settles in place when the run resumes (`approved by olu`,
`chose eu-west`), when nobody answered in time (`expired`, with the default
the run carried on with: a denial for an approval), when the run is stopped
while it waits (`stopped`, with the cause), and when the run ends without an
answer (`closed`). A run is woken through the queue as soon as it is answered
or stopped (`runtime.WithWaker`), and at its question's deadline, so a default
applies when it is due. Questions wait a day by default (`QuestionTTL`).

### Called runs

An attached run (tagged) that parks shows its question on its caller's stream
as `lifecycle.child.parked` (`runtime.WatchChildQuestions`), and its message is
addressed by the called agent's own spec. The caller keeps waiting in the
Delegator, holding its place, while the called run holds none. mandatum's
`WithChildRuns`, which frees the caller too, is not used: it starts the child
inside the runtime, so the call would bypass collegium's call path (the spawn
allowlist, depth and fan-out, the lease, admission and spend, the call tree,
and the conversation and sandbox the child shares), and it has no hook for a
host to make or refuse that call. Usage is rolled up as before, by the
Delegator's `ReportChildUsage`, once per child. Since one place is always kept
for work that waits on nobody, parents waiting on parked children can refuse
new attached calls (`ErrBusy`), never hang.

### Not built

A budget request is not an approval yet: a run at a limit is stopped or
refused, not parked. See the status table.

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
| Usage is kept as counters per scope and UTC day in memory, rebuilt from the entries, and spend has an entry per model call | The same counters in the shared store, checked against a rebuild |
| The live spend tally is per process, so two servers each see half a scope's calls between reads | The spend counter in the same shared store as the admission counter |
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
| Client limits: `max_clients` and `max_clients_per_surface` on the tenant's policy, and a `client` policy scope for runs, concurrency, spend and tokens | Built on `pkg/policy` and the admission lock. Storage per client is not |
| A default client per surface from the `COLLEGIUM_SLACK_*` and `COLLEGIUM_GITHUB_*` variables | Built. Managed by the environment and read-only in the API |
| Client API: `/api/clients` create, read, replace, delete, behind `clients:read` and `clients:write` | Built, tested across tenants |
| Console for clients: Settings, Connections lists them, and an admin can disable or remove one | Built. Creating one, and setting its limits, are API calls. Not checked in a browser |
| Minting GitHub installation tokens, Slack OAuth installation | Not built. A client is given a token |
| GitHub auto-label and per-commit reviews, PR guides, explicit writer-authorized fix commits and workflow reruns | Built in `pkg/integrations/github`, on mandatum API capabilities. Rendering in vultus `go/github` |
| Slack agent-session status and code-channel command policy | Built in `pkg/integrations/slack`. Signed command decoding and optional `AgentAPI` in mandatum. Not live-validated |
| Slack stop button: `agent_session_stopped` asks the runtime to stop the runs answering in that session, confirms in the thread and settles the status. The run's message keeps its partial answer, marked stopped | Built, tested end to end against the fake with a scripted model whose call is shown to be cancelled. The session-to-run map is in memory and per client: one app's button cannot stop a run another app raised. Not live-validated |
| Slack app (`apps/slack`): Socket Mode, a client of the HTTP API as a machine. DMs, mentions, `/agent`, shortcuts, Home, native streaming, session status, stop, inbox items in the run's thread. A thread's conversation is derived from the thread, so a restart loses neither it nor deduplication | Built, tested with fakes for Slack and collegium and against a real server with a fake Slack. A run is recorded as the Slack person's when the app's client maps them (`act_as`), else as the app's. Its runs are not yet recorded as a client's. Not live-validated |
| Questions in Slack: a run's question shows in its thread with vultus's approval and choice cards and live buttons, answered through collegium as the Slack person the app's client maps (`slack.machine`, `slack.people`), refused for anyone unmapped without asking, and settled in place | Built, tested with fakes for Slack and collegium. A reply cannot be typed in Slack yet: vultus has no reply modal. Not live-validated, and not run against a real server: no model that asks is wired into `test:server` |
| `packages/adapter`: the one collegium-to-vultus adapter, run events and inbox messages to vultus's model and vultus actions back to platform calls, shared by the console and the Slack app | Built. A parity test renders one adapted state with both renderers |
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
| `pkg/policy`: versioned limits per workspace, project, agent and client on runs, concurrency, spend, tokens and lease; a narrower scope can only tighten | Built, in memory. Two pure functions, `Decide` at admission and `Exceeded` mid-run, so a refusal or a stop can be replayed from the policy version it names |
| `pkg/usage`: append-only ledger of admitted, refused, warned, halted and ended runs, and of spend (one entry per model call, keyed by the runtime's `accounting_id`, and one settlement per run), with counters per scope and day and `Rebuild` from its entries | Built, in memory. Admissions and spend are kept 62 days, whole days. Tested: a call or a settlement delivered twice counts once, a call after its run settled counts only beyond the settled total, entries out of time order count in their own period, and a rebuild agrees |
| Admission: every run is checked under one lock before it is accepted; a refusal answers 429 or 402 with the limit, the policy version and the reset time, and raises a budget alert | Built, tested with 40 racers against a limit of 7, and at a spend limit reached to the micro-dollar |
| Console for limits: Settings, each project page, and usage against limits in Analytics, with token limits and a lower-bound note on spend | Built. Not checked in a browser |
| Spend and token limits: enforced at admission and mid-run, with the warning threshold and a stop through the runtime. See [Usage and spend](#usage-and-spend) | Built on mandatum v0.10.0's usage reports, tested with scripted priced models under the race detector. Not run against a real provider here: no model key was available. Cost is a list price from saige's catalog, not a provider's invoice. A call in flight when the limit is crossed finishes and is counted, so a scope can end a little over its limit |
| Unpriced calls and other currencies: counted apart, never as dollars, one lower-bound alert per scope, bounded by `max_tokens` | Built, tested |
| Asking a person to approve a run at a limit | Not built. A run parks only at a tool call (`approvals`, `ask_person`): mandatum has no way for a host to park a run on a question of its own, so a mid-run limit can only stop it, and admission refuses before there is a run to park. It needs a runtime call that raises a question on a run from outside its turn (or parks it at its next model call), with a `budget` question kind |
| Limits set by a project's owner | Not built. Only an admin sets limits, because projects have no owners |
| First-run setup: the first admin names themselves and the workspace | Built. `first_admin`, `onboarded_at` and the workspace's setup fields |
| Recents: conversations paged by an opaque keyset cursor, 200 a page | Built |
| Project membership and per-project access | Not built. Everyone in the workspace sees every project |
| Web console on vultus: chat, inbox, agent editor with revisions, evals, knowledge, schedules, analytics, alerts, settings | Built |
| Console home: vultus `Home` as the default view, its model built by `apps/web/src/home.ts` from what the console fetches, its actions routed in place | Built, unit-tested, checked in a browser as an operator, a viewer and someone with no role. Personal accounts and Jira are not offered: the platform has neither |
| Home notifications: open alerts (with `alerts:read`) and open inbox items in one list, critical alerts first, then the inbox, then other alerts, newest first, eight shown and the rest counted. Each links to its own page: `?view=inbox&message=<id>` or `?view=alerts&alert=<id>`, absolute under a public URL for Slack. An inbox item about an alert already listed is left out | Built, unit-tested. A linked id that is not the tenant's own shows not found. Collegium does not publish a Slack Home tab yet |
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
| Publishing that waits for approval | Built: list `publish_artifact` in an agent's `approvals`. Nothing is stored until a person approves, and a denial publishes nothing. Tested |
| Parking a run on a person: approvals of tool calls, choices and replies (`ask_person`), on mandatum v0.11.0 with the run queue as waker, collegium's roles, and a question deadline. See [People in the loop](#people-in-the-loop) | Built, tested under the race detector with scripted models: each kind, who may answer, a requester refused, two answers at once, expiry, stop while parked, a called run's question, usage counted once, tenant isolation. Not run against a real model here: no model key was available |
| Agent editor: may ask a person, approve a tool first, who may approve, self-approval, who may answer | Built. Not checked in a browser |
| Run manifest: one JSON document of a run, every run under it, each pinned definition, every event with its body, and the tree's usage | Built, from the journal. A run older than the journal's ring comes back marked incomplete |
| Token counts, context window and cost per run: `usage` on the run (own, rolled-up attached runs, total, context), live while it works and the runtime's record once it ends, recorded once whatever is redelivered | Built, tested with redelivered events, a reconciler pass, a stopped run and a three-run tree. Context fill is the latest call's prompt against the window saige's catalog declares; a model with no declared window shows tokens only |
| Leases on real cost: a called run is held to its lease by what it spends and what it has handed on, and settles with its caller when it ends, climbing past a caller that settled first | Built, tested, with a grant made before the caller's own call was counted and a call that ends after its caller. An API caller still names a lease; an agent's own calls lease half of what is left |
| Usage in the Slack app | Not shown, on purpose: the console shows none on a turn either. A vultus usage summary would let both show it |
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
| Approve, deny, choose and reply parts for a run waiting on a person | Built. Raised from `lifecycle.parked`, answered through `runtime.Answer` from the inbox, the run, or Slack, each action needing `runs:write`, and settled in place when the run resumes, expires, stops or ends. Shown by the shared adapter as vultus's cards on the run and in the inbox alike, with a parity test per kind |
| Messages addressed to one person or one role | Built for questions: addressed to the principals and `role:<name>` entries that may answer, never to the requester of an approval. Every other kind still goes to everyone who may read it |
| Alert delivery outside the console | Not built |
| OTel export | Not built. Traces are rebuilt from the journal |
| Evals that run on their own, on a schedule or on each revision | Not built. Evals run when someone starts them |
| Model-graded evals | Not built, on purpose for now: every check is exact |
| Email connector, MCP server and Jira wiring | Not built |
| Remote execution nodes, peer connector | Not built |
| Postgres stores | Not built. Everything above is lost on restart |
