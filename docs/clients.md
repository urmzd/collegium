# Clients

A client is one installation of the platform on a surface: a Slack app in a
team, or a GitHub App installation on some accounts. A workspace has as many as
it needs. One client for everything, several that route to different agents, or
one per agent are all the same record with different values.

## The record

| Field | Meaning |
|-------|---------|
| `id`, `name` | The id names the client in its workspace and is what a run records. Left out, it is made from the name. |
| `surface` | `slack` or `github`. It cannot change after the client is created. |
| `identity` | The external identity the client answers for. Slack: `team` (required) and `app_id`. GitHub: `owners` (required), `installation` and `app_id`. |
| `credentials` | `token` and `signing_secret`. Written, never read back. See [Secrets](#secrets). |
| `default_agent` | Answers a request that named no agent. |
| `agents` | The agents the client may route to, besides its default. |
| `bots` | A Slack bot user id, or a GitHub login, to the agent it stands for. |
| `slack` | `sessions`, `commands`, and `machine` and `people` (see [People a Slack app answers for](#people-a-slack-app-answers-for)). |
| `github` | `auto_labels`, `checks`, `guides`, `mutations`, `read_repositories`. |
| `enabled` | A disabled client answers nothing and carries nothing. |

The options under `slack` and `github` are the ones [Slack](slack.md) and
[GitHub](github.md) describe.

### Which agents a client reaches

Default deny. A client routes to its default agent and to the agents listed in
`agents`. With neither, it routes to nothing. `"agents": ["*"]` means any agent
in the workspace, and has to be written.

The list is applied three times: a bot or an auto-label that names an agent
outside it is refused when the client is saved, a name in a message that is
outside it is not read as an agent, and a tag for an agent outside it is
dropped before it reaches the platform.

A client lends its actions (posting a message, reading a pull request) under the
same rule. A run gets the actions of the client that raised it. On another
surface it gets the oldest client in its workspace that may route to its agent,
so a Slack run reads GitHub only through a GitHub client that lists its agent.

## People a Slack app answers for

A Slack app built on the API (`apps/slack`) signs in as a machine. To ask for
a run or answer a question as the Slack person who clicked, it sends
`act_as: {"client": "<id>", "user": "<Slack user id>"}`. That is honoured only
when the client:

| Field | Meaning |
|-------|---------|
| `slack.machine` | The subject of the credential the app signs in with. No other credential may act for the client's people. |
| `slack.people` | Slack user ids in the client's team, each mapped to the principal it is in this workspace, by subject. At most 500. |

Anyone the map does not name is refused with 403, and the app does not ask on
their behalf. A disabled client maps nobody. The mapped principal's roles, for
`role:<name>` approvers, are what the workspace's authorization policy grants
their subject (`grant ... to subject "..."` or `anyone`), since a Slack click
carries no sign-in. Who may answer is then the run's spec's to say, as for
anyone. A run asked for this way is the person's, so they do not approve its
calls.

## Routing a request to a client

Every Slack request arrives at `/connectors/slack` or
`/connectors/slack/commands`, and every GitHub delivery at `/connectors/github`,
whichever client it is for. A request has no session, so nothing in it is
believed until a secret has verified it.

1. The request states an identity in bytes nobody has verified yet: Slack's
   `api_app_id` and `team_id`, or GitHub's hook target header, `installation.id`
   and repository owner.
2. It is offered, oldest first, to every enabled client that statement does not
   disagree with. A field the request leaves out disagrees with nothing, which
   is how Slack's URL verification and GitHub's ping reach a client.
3. The first client whose own signing secret verifies the exact bytes takes it.
   The tenant, the client and the agents it may reach come from that client's
   record. A `tenant` in the payload is ignored.

| Outcome | Status |
|---------|--------|
| No client is offered the request: unknown, disabled, or no clients at all | 404 |
| Clients were offered it and none could verify it | 401 |

So a request signed with one client's secret and claiming another's identity is
offered to the other and fails its secret. Guessing an identity gains nothing.

Within a workspace, two clients a request could not tell apart are refused when
the second is saved. Two Slack apps in one team must each state their `app_id`.
Two GitHub clients may not serve the same account unless their `app_id` or
`installation` differ.

Two workspaces may each register the same identity. The request goes to
whichever holds the secret that verifies it, and if both do, to the older.
Refusing the second would tell one workspace what another has.

A run id is the runtime's, derived from the workspace and the event id
together. The same event id through another workspace's client is a run of
that workspace's own, with its own id, and never joins or touches the first.

## Secrets

A record holds no secret. Each of `token` and `signing_secret` is written in one
of two ways:

| Written as | Where the secret lives | What a read shows |
|------------|------------------------|-------------------|
| `{"value": "xoxb-..."}` | In the server's secret store, in memory | `{"set": true}` |
| `{"ref": "env:COLLEGIUM_SLACK_TOKEN_DOCS"}` | In the server's environment | `{"set": true, "ref": "env:..."}` |

### Which variables a workspace may reference

The environment belongs to the whole process, and a reference uses a secret
without knowing its value. So a reference is default deny: a variable is read
for a workspace only if the operator gave that exact name to that workspace.

```sh
collegium serve -secret-refs 'acme=COLLEGIUM_SLACK_TOKEN_DOCS,COLLEGIUM_SLACK_SECRET_DOCS;globex=COLLEGIUM_GH_GLOBEX'
```

| Rule | Detail |
|------|--------|
| Who sets it | The operator, with `-secret-refs` at startup. Nothing in the API can read or change it. |
| A workspace not listed | May reference nothing. It can still write its secrets as values. |
| Names | Must start with `COLLEGIUM_`, and not `COLLEGIUM_OIDC_`. Anything else fails startup. |
| The default clients | Their variables are given to the server's `-tenant` and to no other workspace. |
| Where it is checked | Where the secret is read, which every create, every update and every restart goes through. |

The check sits at the read, not only at the write, so a record that carries a
reference its workspace was never given does not start, however it got into the
store. It verifies nothing and signs nothing.

A refused reference is one 400 with one message, whether the variable is unset,
set, given to another workspace, or not a reference at all. The response does
not repeat the name. A caller cannot use it to learn what the server's
environment holds or what another workspace was given.

The variable is read when the client is created or changed. A listed variable
that is unset is refused the same way, so check the server's environment first
when a reference you listed is refused.

No response and no log line carries a secret. The stored type has no field for
one, and the type a secret is written through hides its value from formatting.
A secret left out of an update is kept.

The secret store is in memory like every other store, so a value written
through the API is lost on restart and has to be written again. A reference
survives, because the environment does. A secret manager goes behind the same
three-method `clients.Secrets` interface.

For GitHub, the token is an installation access token. The server does not mint
or refresh one. A fix grant is signed with the client's own webhook secret, so
one client's grant is worth nothing to another.

## Defaults

With nothing configured, no client exists and the webhook routes answer 404.

The `COLLEGIUM_SLACK_*` and `COLLEGIUM_GITHUB_*` variables that used to
configure the one installation per surface now create one default client per
surface, `default-slack` and `default-github`, in the server's `-tenant`. It
behaves as that installation did: any agent in the workspace (`"*"`), the same
options, its secrets left in the environment. It is marked `managed: env` and
is changed in the environment, not through the API. `COLLEGIUM_SLACK_APP`,
`COLLEGIUM_GITHUB_APP` and `COLLEGIUM_GITHUB_INSTALLATION` state its identity
more exactly and are optional.

## Limits

Limits on clients are policies, checked by the same admission as every other
limit. See the status table for what a policy can set.

| Limit | Where it is set | Default |
|-------|-----------------|---------|
| Clients in the workspace | `max_clients` on the tenant's policy | 10 |
| Clients on one surface | `max_clients_per_surface` on the tenant's policy | 5 |
| Runs a client raises in a period | `max_runs` on a policy with scope `{"kind":"client","name":"<id>"}` | None |
| Runs a client has in flight | `max_concurrent` on the same policy | None |
| What a client's runs spend in a period | `spend_limit` (micro-USD) on the same policy | None |
| Tokens a client's runs use in a period | `max_tokens` on the same policy | None |

A policy may set the client caps as high as 100. The count and the insert are
one step, so racing creates cannot pass a cap. Lowering a cap below what exists
removes nothing and refuses the next create.

A client's policy can only tighten what the workspace allows, like a project's
or an agent's. A run called by an agent counts against the client that raised
the run at the root of its tree. A refusal is recorded in the usage ledger with
the client, the policy version and the limit, and raises a budget alert.

Slack and GitHub are still answered 200 for a request whose run a limit
refused: the request was genuine and retrying it would not help.

Spend and tokens are enforced like any scope's: see
[usage and spend](architecture.md#usage-and-spend). Not built: a storage limit
per client. Published files are not counted by client.

## Usage on a turn

The console shows what a run used on its Activity page, the context gauge and
Analytics, and not on the turn in the conversation. The Slack app shows none.
The rule is that a surface shows usage on a turn only if every surface the
shared adapter feeds can, and vultus has no usage summary for Slack:
`@vultus/web` renders `Turn.usage` as a chip, `@vultus/components` renders
nothing. So the adapter leaves `Turn.usage` unset and the two agree. The run's
`usage` is on the API for any client that wants it.

What vultus should have for this:

- A `RunUsage` summary in `@vultus/components` (Slack context line and web)
  taking integer micros, a currency and an unpriced count, so neither surface
  formats money itself. `Turn.usage.costUsd` is a float of dollars today.
- A context gauge (`ContextWindow` exists in `@vultus/core` session state,
  with no component that renders it from a run).
- A limit meter with a lower-bound state, for spend that left calls out.

## API

| Route | Permission |
|-------|------------|
| `GET /api/clients` | `clients:read` |
| `POST /api/clients` | `clients:write` |
| `GET /api/clients/{id}` | `clients:read` |
| `PUT /api/clients/{id}` | `clients:write` |
| `DELETE /api/clients/{id}` | `clients:write` |

Every role can read. Only an admin can write, because a client hands the
workspace's agents a credential. The tenant comes from the caller. Another
workspace's client is a 404 on every route. A list holds one workspace's
clients and never more than 100, with the caps beside it.

```sh
# The server was started with
#   -secret-refs '<tenant>=COLLEGIUM_SLACK_TOKEN_DOCS,COLLEGIUM_SLACK_SECRET_DOCS'
curl -X POST "$COLLEGIUM/api/clients" -H "Authorization: Bearer $TOKEN" -d '{
  "name": "Docs bot",
  "surface": "slack",
  "identity": {"team": "T01ABCDEF", "app_id": "A0DOCS"},
  "default_agent": "docs-bot",
  "slack": {"sessions": true},
  "credentials": {
    "token": {"ref": "env:COLLEGIUM_SLACK_TOKEN_DOCS"},
    "signing_secret": {"ref": "env:COLLEGIUM_SLACK_SECRET_DOCS"}
  }
}'
```

A second Slack app in the same team, answering as another agent, is the same
call with another `app_id`, agent and secrets. Limit what it may raise:

```sh
curl -X PUT "$COLLEGIUM/api/policies" -H "Authorization: Bearer $TOKEN" -d '{
  "scope": {"kind": "client", "name": "docs-bot"},
  "period": "day", "max_runs": 200, "max_concurrent": 2
}'
```

`PUT` replaces the record. Changing a client rebuilds its connector, so the
runs it was carrying lose their place on the surface: a Slack session's stop
button no longer finds them, and an answer still streaming starts a new message.
Deleting a client removes its secrets and its policy. Runs it raised keep their
record of it.

A body that carries `tenant`, `managed` or `version` is rejected.

## Per client, not per process

Each client has its own connector, so everything a connector remembers is the
client's alone: which Slack sessions hold which runs, where commands were
offered, the message a run is editing, a GitHub guide's lock. A Slack app's stop
button stops only runs that app raised, in its own workspace.

An event goes out through the client that raised its run. A run with a route to
another surface goes out through the one client in its workspace that serves
the address and may route to its agent. With none, or with more than one, the
event is undeliverable.

## In the console

Settings, Connections lists a workspace's clients: what each answers for, the
agents it reaches, where its secrets live and its limits. An admin can disable
or remove one. Creating a client and setting its limits are API calls.

## What is not built

- A durable store. Records and secret values are in memory behind
  `clients.Store` and `clients.Secrets`, and are lost on restart.
- Creating and editing a client in the console, and setting a client's limits
  there.
- Minting or refreshing GitHub installation tokens.
- Slack OAuth installation. A client is given a bot token.
- Checking that a client's agents exist when it is saved. A request for an
  agent that does not exist raises no run.
- Jira and email clients. The two surfaces are Slack and GitHub.
