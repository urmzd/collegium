# Slack app

A Socket Mode Slack app that answers through collegium. It is a client of
collegium's HTTP API and holds no rendering of its own: a run's events go
through [`@collegium/adapter`](../../packages/adapter), the same adapter the
console uses, into one vultus turn, and that turn is rendered by
`@vultus/render-slack`. A run reads the same in Slack and in the console.

It is the experience of vultus's Slack sandbox (`apps/slack-sandbox` in
vultus), driven by real runs instead of fixtures. For how it relates to the Go
webhook connector, see [Slack](../../docs/slack.md#two-ways-to-answer-in-slack).

## Run it

You need the Slack app created from the sandbox manifest
(`SLACK_SANDBOX_SKIP=workflowStep pnpm sandbox manifest` in vultus), installed
to one team, and a collegium server.

```sh
cd apps/slack
pnpm install
cp .envrc.local.example ../../.envrc.local   # fill it in, then direnv allow
pnpm start
```

`vultus` must be checked out beside `collegium`, as for the console. Install
`packages/adapter` once with `pnpm install` in that directory too.

## What it does

| Entry | Where the answer goes |
|-------|-----------------------|
| A direct message | A thread under that message. Each top-level message starts a conversation. |
| A mention in a channel | The mention's thread. |
| `/agent <question>` | A new thread in the channel. `/agent help` lists who can be asked. |
| **Ask the agent** shortcut | The app's messages with you. |
| **Ask agent about this** on a message | That message's thread. |
| Stop button | Stops the run, confirms in the thread, sets the session to active. |
| Home tab | What is waiting on you in collegium's inbox. |

A turn streams with `chat.startStream`, `appendStream` and `stopStream`, and
the session status follows it: processing while it works, active when it ends.
Where native streaming is refused, the turn is posted once and edited. A
message sent while a turn is running waits for it, as in the console.

Bot messages, the app's own messages, edits and joins never start a run.
Deliveries from any team but `SLACK_TEAM_ID` are ignored.

## Who answers

1. An agent named as `@name` in the message, when it exists and the app may
   reach it. The name is taken out of the message.
2. The client's default agent, or `COLLEGIUM_DEFAULT_AGENT`.
3. Collegium's picker, when the app may reach any agent.

With `COLLEGIUM_CLIENT` set, the client record's default agent and `agents`
list apply, read again every 30 seconds. A client that lists agents and names
no default answers nothing, because the picker does not know the list.

## Identity and permissions

The app authenticates as a machine. Least privilege:

| Scope | For |
|-------|-----|
| `collegium:runs:read` | Follow runs, read conversations and the inbox |
| `collegium:runs:write` | Start, continue, retry and stop runs |
| `collegium:agents:read` | Know which agents exist; see proposed memories |
| `collegium:clients:read` | Only with `COLLEGIUM_CLIENT` |

Every button in Slack acts with the app's rights, so each extra scope lets the
people in the loop do more: `agents:write` keeps or discards memories,
`alerts:read` and `alerts:write` show and acknowledge alerts. Only the person
who asked for the run, or someone in `SLACK_OPERATORS`, may settle an inbox
item or ask a turn again (a Retry is always the app's own, never collegium's,
so its answer shows in the thread); a retry stays the first asker's, and is checked
against the client's agents again. When who asked is not known (after a
restart), only an operator may. A reached limit is shown, not raised: raise it
in the console.

A refusal or error from collegium is shown in vultus's failure words; the
platform's own text goes to the log. A Slack write that fails mid-turn stops
the streaming, and the whole turn is posted when it ends, with the session set
off processing. A run that says nothing for 15 minutes (a long tool call, or a
run waiting on other work) is not failed: its stream is closed as it stands,
the session stays processing, the thread is free for the next message, and the
run's record is read less and less often (at most every 3 minutes) until it
ends, when its answer is posted once in the thread. A message sent meanwhile
follows it in the conversation, as collegium orders any turn sent after one
that is still running. The slow follow does not survive a restart; the stop
button still finds the run.

## What survives a restart

Nothing that matters is kept here. A Slack thread's conversation is
`api-<subject>-slack-<team>-<channel>-<thread ts>`, so it is asked of
collegium, not remembered. A message's run is keyed by its own timestamp, so a
Slack redelivery joins the run it started, and the app asks collegium before
starting it. The stop button asks collegium which runs in the thread are still
going.

Kept in memory, and lost on restart:

- Who asked for each run. After a restart only operators can settle that run's
  inbox items or retry it, and Home shows a person nothing until they ask again.
- Which inbox messages were posted to a thread. A change to one after a
  restart is not reflected in the thread.
- The queue of messages waiting behind a turn.
- Whether native streaming was refused.

One gap remains: the first message the app answers in a thread it did not
start (a mention deep in a human thread) is keyed by the thread. If Slack
redelivers it after a restart and after a later message in that thread was
answered, it is answered twice.

## Checks

| Task | Command |
|------|---------|
| Format and lint | `pnpm exec biome check` |
| Typecheck | `pnpm typecheck` |
| Tests, with fakes for Slack and collegium | `pnpm test` |
| Every Block Kit payload the tests produce, through Slack's `blocks.validate` (network, no token) | `pnpm validate` |
| Against a real collegium server built from this checkout (needs Go, no model) | `pnpm test:server` |
