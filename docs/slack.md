# Slack agent delivery

## Two ways to answer in Slack

| | Webhook connector | Slack app |
|---|---|---|
| Where | Go, in `collegium serve` (`pkg/integrations/slack`) | TypeScript, `apps/slack`, a separate process |
| How Slack reaches it | Signed webhooks to a public URL | Socket Mode, no public URL |
| How it reaches collegium | In process | The HTTP API, as a machine |
| A run looks like | One plain message per run, edited with `chat.update` | vultus components: native streaming, plans, tool steps, failures with Retry, files |
| Agent session status and the stop button | Yes | Yes |
| Code channel slash commands | Yes | No |
| `/agent`, shortcuts, Home tab | No | Yes, from vultus's sandbox manifest |
| Inbox items about a run | No | In the run's thread, and on Home |
| Several apps per workspace, as [clients](clients.md) | Yes: tenant, limits and allow-list come from the client | Reads one client's default agent and allow-list. Its runs are not yet recorded as the client's, see below |
| Who asked | Recorded on the run | Recorded as the person, when the app's client maps them (`act_as`); else as the app |
| Questions a run waits on | No | In the run's thread, answered for the person the client maps. See below |
| Tokens and cost | No | No. See [usage on a turn](clients.md#usage-on-a-turn) |

Use the connector for code channels, for several apps per workspace, and where
a public URL is fine. Use the Slack app for the console's experience in Slack,
or where nothing may listen on a public address. Do not point both at the same
Slack app: both would answer.

The rest of this page describes the connector. The app is described in its
[README](../apps/slack/README.md).

## What the API lacks for the Slack app

The app works around each of these and says where in its README.

| Missing | Effect today | Proposal |
|---------|--------------|----------|
| `actor` on `POST /api/runs` for anyone | Runs are recorded as the Slack person only when the app's client maps them (`act_as`, see [clients](clients.md#people-a-slack-app-answers-for)); anyone else's run is the app's | Built for mapped people. A Slack person nobody mapped stays unnamed on purpose |
| `client` on `POST /api/runs`, or a machine bound to a client | The client's run limits and allow-list are not applied by the platform | Bind a machine credential to one client (a `client` claim, or a `client_id` on the token mapped in the client record) so the platform places its runs as it places webhook runs |
| `metadata` on `POST /api/runs` | The Slack team, channel and thread a run answers are not on the run; the app derives them from the run id instead | Accept a bounded `metadata` map (string keys and values, a few hundred bytes) and return it on the run, so a surface can find its own place without encoding it in the idempotency key |
| A picker that knows a client's allow-list | A client with a list and no default answers nothing | `GET /api/pick?client=` and the same rule in `POST /api/runs` |
| The run a retry started, in the resolve response | Retry from Slack starts its own run and acknowledges the inbox part | Return `{message, run}` from `POST /api/inbox/{id}/parts/{part}/resolve` |
| A stream of inbox notices alone | The app subscribes to a topic no agent can have | `GET /api/inbox/events` |
| A route for feedback | A rating is written to the app's log | `POST /api/runs/{id}/feedback` |
| A modal to type a reply to a question | A reply is answered in the console | vultus: a reply modal in `@vultus/render-slack`, as for feedback |

### Questions a run waits on

The Slack app shows a run's question in the run's thread: the streamed message
is closed with vultus's approval or choice card, so its buttons can be pressed.
A click answers through collegium (`answerQuestion`) as the person the app's
client maps the Slack user to; a Slack user it does not map is told in vultus's
words that they cannot, and collegium is not asked. Collegium decides the rest:
who may answer is the run's spec's to say, and a refusal is shown with vultus's
`noPermission` copy. The card settles in place from the run's own events,
whoever answered, and the answer continues in a new message. A question's inbox
message is not posted in the thread again. Configure the client with the app's
credential and the people it answers for:

```json
"slack": {"machine": "<subject of the app's machine credential>", "people": {"U01ANA": "ana@example.com"}}
```

The webhook connector shows a parked run as a suspended session and does not
take answers.

The Slack connector accepts signed mentions, direct messages and code channel
slash commands. Responses use one message per run, edited as events arrive.

## App definition

[`examples/slack/manifest.json`](../examples/slack/manifest.json) declares
the app: an agent view, a bot user, code channels, and the scopes and events the
connector uses. Replace `YOUR_HOST` with the public address of `collegium serve`,
then create the app from the manifest at <https://api.slack.com/apps>.

| Manifest entry | Why the connector needs it |
|----------------|----------------------------|
| `app_mentions:read`, `app_mention` | A mention in a channel raises a run. |
| `im:history`, `message.im` | A direct message raises a run. |
| `chat:write` | Post and edit the run's message; report session status. |
| `reactions:write` | The `slack_add_reaction` action. |
| `agent_view`, `assistant:write` | Declares the app an agent, which sessions require. |
| `agent_session_stopped` | Shows Slack's stop button on a working session and delivers the press. |
| `code_channels`, `code_channels:manage` | Register and receive slash commands. |

Slack sends events to `/connectors/slack` and command invocations to
`/connectors/slack/commands`. Both are verified with the same signing secret
before anything is parsed.

## Configuration

Each Slack app is a [client](clients.md): its team and app id, its secrets, the
agents it may reach, and the options on this page. A workspace may have several,
and every app posts to the same two URLs. The request goes to the client whose
signing secret verifies it.

For a single app, these variables create the default client, `default-slack`:

| Variable | Meaning |
|----------|---------|
| `COLLEGIUM_SLACK_TOKEN` | Bot token (`xoxb-`). Required with the signing secret. |
| `COLLEGIUM_SLACK_SIGNING_SECRET` | Authenticates every inbound request. |
| `COLLEGIUM_SLACK_TEAM` | Team id the client serves. A request for another team is a 404. |
| `COLLEGIUM_SLACK_APP` | App id. Optional for one app, required of each when a team has several. |
| `COLLEGIUM_SLACK_BOTS` | `U08BOT=docs-bot` pairs: which agent a bot user id stands for. |
| `COLLEGIUM_SLACK_DEFAULT_AGENT` | Answers a mention or command that named no agent. |
| `COLLEGIUM_SLACK_SESSIONS` | `true` reports run lifecycle as session status. |
| `COLLEGIUM_SLACK_COMMANDS` | JSON array of commands to offer in code channels. |

```sh
export COLLEGIUM_SLACK_SESSIONS=true
export COLLEGIUM_SLACK_COMMANDS='[{"name":"create-pr","description":"Open a pull request","argument_hint":"[title]"}]'
```

Malformed values fail startup. So do commands Slack would refuse: more than 10,
a name over 31 characters, a leading slash, a missing description, a duplicate.
The same values on a client created through the API are refused with a 400.

| Variable | Client field |
|----------|--------------|
| `COLLEGIUM_SLACK_TOKEN`, `COLLEGIUM_SLACK_SIGNING_SECRET` | `credentials.token`, `credentials.signing_secret` |
| `COLLEGIUM_SLACK_TEAM`, `COLLEGIUM_SLACK_APP` | `identity.team`, `identity.app_id` |
| `COLLEGIUM_SLACK_BOTS` | `bots` |
| `COLLEGIUM_SLACK_DEFAULT_AGENT` | `default_agent` |
| `COLLEGIUM_SLACK_SESSIONS`, `COLLEGIUM_SLACK_COMMANDS` | `slack.sessions`, `slack.commands` |

The default client may route to any agent. One created through the API routes
to its default agent and its `agents` list, and to nothing else.

## Session status

With sessions on, each lifecycle event calls `agents.sessions.setStatus`:

| Run event | Session status | What Slack shows |
|-----------|----------------|------------------|
| accepted, started, resumed | `processing` | The "Working…" indicator. |
| parked | `suspended` | Waiting on the user. |
| completed, failed, stopped | `active` | Ready for the next prompt. |

A failed or stopped run reports `active` because its error, or its partial
answer marked stopped, is rendered in the run's own message and the thread
remains usable. The connector never reports `closed`.

A session is a thread in an ordinary channel and the whole channel in a code
channel. The trigger cannot tell the two apart, so the sink sends the thread
first and repeats without it when Slack answers `thread_ts_not_allowed`.

## Stopping a run

Slack shows a stop button on a `processing` session because the manifest
subscribes to `agent_session_stopped`. A press is handled in the order Slack
asks for:

1. Every run answering in that session is asked to stop through the runtime,
   with the reason `Stopped by slack:<user id>`. Runs attached to it are asked
   too; runs it delegated carry on. A stop on a code channel covers every run
   in the channel.
2. `Stopped by @user.` is posted in the session.
3. The session is set to `active`.

The model call in flight is cancelled. The run ends with `lifecycle.stopped`,
and mandatum's sink edits the run's message to keep what the agent had said,
marked stopped with the reason underneath. From the press until that event,
nothing the run still publishes sets the session back to working, and the
`active` it reports on ending is where the session stays. A run whose turn
finished before the request reached it ends completed, as usual.

A second press, or Slack redelivering the first, finds no run left to stop and
posts nothing. Anyone who can see the button may press it: the session, not a
console role, is the authority. If the runtime cannot carry the request, the
press is answered with an error so Slack redelivers it, and nothing is posted.

A follow-up mention in the thread is answered by a new run. Slack mentions do
not carry the thread's history in collegium today, so that run is not sent the
stopped turn. A conversation in the console is: see
[Stopping a run](architecture.md#stopping-a-run). Carrying a Slack thread's
history needs mandatum's thread journal (`runtime.WithThreads`), which records a
stopped turn with its partial answer and a fixed marker.

A press reaches only the runs its own client raised. Two apps answering in one
thread each hold their own sessions, and another workspace's app in the same
team cannot stop a run it did not raise.

Which runs a session holds is remembered in memory, per client. After a
restart, or after the client is changed, the button still settles the status
but finds no run to stop.

Check the manifest with the Slack CLI from `examples/slack`:

```sh
slack manifest validate --team <team id>
```

## Slash commands

Commands are registered with `agents.conversations.setCommands` the first time a
run is accepted in a channel. Slack answers `not_agent_channel` for an ordinary
channel; that outcome is remembered, so it costs one call per channel per
process.

An invocation becomes a tag whose text is what a human would have typed:
`/create-pr Fix the flaky login test`. The agent is the one named in the text
with `@name`, otherwise the default agent. The answer is a top-level message in
the code channel. With no agent configured, the invoker gets an ephemeral
message saying so.

## Errors

Slack reports failures as `ok: false` with a code. For status and command
calls collegium logs permanent refusal codes as warnings and still treats the event as
delivered: the answer is already on the surface, and retrying a refusal would
never succeed. Transport failures and transient Slack server/rate-limit errors
are returned, so the router retries them with its bounded backoff.

| Code | Usual cause |
|------|-------------|
| `feature_disabled` | Agent sessions are not enabled for the workspace. |
| `missing_scope` | The installed app predates a scope in the manifest. Reinstall. |
| `thread_ts_required` | A top-level route in an ordinary channel has no session. |
| `no_actions_url` | The manifest has no `slash_command_url`. |
| `colliding_with_builtin` | A command shares a name with a Slack command. |
| `too_many_commands` | Other agents in the channel already use the 10 slots. |
| `not_authorized` | The app is not the channel's agent bot. |

## Remaining work

- **Creating code channels.** The connector answers in code channels that
  already exist. It does not call `agents.conversations.create`, set context bar
  items, publish views or archive.
- **Unmentioned messages in a code channel.** Only mentions and commands raise
  runs there; `message.session` is not handled.
- **Session titles and renames** are left to Slack and its users.
- **Native streaming.** The connector edits answers with `chat.update`. The
  [Slack app](../apps/slack/README.md) streams them.
- Command registration, lifecycle replay cursors and the runs a session holds
  are bounded, in memory and per client, like the sink's message handles. A
  restart forgets them, and so does changing the client.

Code channels and agent sessions are new Slack surfaces. This connector is
tested against the documented contract through the in-memory fake and has not
yet been exercised against a live workspace.
