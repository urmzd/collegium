import assert from "node:assert/strict";
import { test } from "node:test";
import { fromServer } from "@collegium/adapter";
import { defaultCopy, MessageView } from "@vultus/components";
import { renderToSlack } from "@vultus/render-slack";
import { echoed, forSlack } from "./app.tsx";
import { CollegiumError } from "./collegium.ts";
import {
  alertMail,
  BOT,
  budgetMail,
  CHANNEL,
  click,
  command,
  DM,
  direct,
  type FakeSlack,
  failureMail,
  homeOpened,
  interactive,
  memoryMail,
  mention,
  OTHER_TEAM,
  OTHER_USER,
  SUBJECT,
  settle,
  stopPressed,
  TEAM,
  USER,
  type World,
  world,
} from "./harness.ts";
import { validateBlocks } from "./validate.ts";

const TS = "1700000000.000100";
const KEY = `slack-${TEAM}-${CHANNEL}-1700000000-000100`;

/** Every Block Kit payload these tests hand to Slack, for the validity check at the end. */
const produced: { from: string; blocks?: unknown[]; view?: object }[] = [];
function collect(name: string, slack: FakeSlack) {
  for (const c of slack.calls) {
    if (Array.isArray(c.args.blocks) && c.args.blocks.length > 0) {
      produced.push({ from: `${name}: ${c.method}`, blocks: c.args.blocks });
    }
    if (c.args.view) produced.push({ from: `${name}: ${c.method}`, view: c.args.view as object });
  }
}

/** Deliver one thing from Slack and let the app get as far as it can. */
async function deliver(w: World, envelope: Parameters<World["app"]["handle"]>[0], app = w.app) {
  await app.handle(envelope);
  await settle();
}

/** Run one event of a run through and let Slack catch up. */
async function emit(w: World, kind: string, payload?: unknown, run = w.collegium.last.id) {
  w.collegium.emit(run, kind, payload);
  await settle();
}

const blocksOf = (args: Record<string, unknown> | undefined) => (args?.blocks ?? []) as unknown[];
const textIn = (blocks: unknown[]) => JSON.stringify(blocks);

test("a mention becomes one run, streamed natively, with the session kept in step", async () => {
  const w = world();
  await deliver(w, mention("what changed in the last deploy?", TS));

  // The run: a new conversation named by the thread, the default agent, the Slack person as actor.
  assert.equal(w.collegium.started.length, 1);
  const asked = w.collegium.started[0];
  assert.equal(asked?.idempotencyKey, KEY);
  assert.equal(asked?.agent, "lead");
  assert.equal(asked?.text, "what changed in the last deploy?");
  assert.equal(asked?.after, undefined);
  assert.equal(w.collegium.last.id, `api-${SUBJECT}-${KEY}`);

  // Before any event: seen, processing, and a stream opened in the mention's thread.
  assert.deepEqual(w.slack.methods, [
    "reactions.add",
    "agents.sessions.setStatus",
    "chat.startStream",
  ]);
  assert.equal(w.slack.of("reactions.add")[0]?.name, "eyes");
  assert.equal(w.slack.of("reactions.add")[0]?.timestamp, TS);
  assert.deepEqual(w.slack.of("agents.sessions.setStatus")[0], {
    channel_id: CHANNEL,
    thread_ts: TS,
    status: "processing",
  });
  const opened = w.slack.of("chat.startStream")[0];
  assert.equal(opened?.channel, CHANNEL);
  assert.equal(opened?.thread_ts, TS);
  assert.equal(opened?.recipient_user_id, USER);
  assert.equal(opened?.recipient_team_id, TEAM);

  await emit(w, "lifecycle.accepted", { rev: 1 });
  await emit(w, "lifecycle.started", { attempt: 1 });
  await emit(w, "delta.tool.call", { id: "t1", name: "github.search", phase: "requested" });
  await emit(w, "delta.tool.done", { id: "t1", result: "3 commits" });
  await emit(w, "delta.text", { text: "Three commits " });
  await emit(w, "delta.text", { text: "went out." });
  assert.equal(w.slack.streamed, "Three commits went out.");
  const tasks = w.slack
    .of("chat.appendStream")
    .flatMap((a) => a.chunks as { type: string; title?: string; status?: string }[])
    .filter((c) => c.type === "task_update");
  assert.deepEqual(
    tasks.map((t) => [t.title, t.status]),
    [
      ["Github search", "in_progress"],
      ["Github search", "complete"],
    ],
  );
  assert.ok(
    !w.slack.methods.includes("chat.stopStream"),
    "the stream stays open until the run ends",
  );

  await emit(w, "lifecycle.completed", { text: "Three commits went out." });
  await w.app.idle();

  // The whole conversation with Slack, in order.
  const order = w.slack.methods.filter((m) => m !== "chat.appendStream");
  assert.deepEqual(order, [
    "reactions.add",
    "agents.sessions.setStatus",
    "chat.startStream",
    "chat.stopStream",
    "reactions.remove",
    "reactions.add",
    "agents.sessions.setStatus",
  ]);
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
  assert.equal(w.slack.of("reactions.add").at(-1)?.name, "white_check_mark");
  assert.equal(
    w.slack.streamed,
    "Three commits went out.",
    "the answer is not repeated on completion",
  );
  const final = blocksOf(w.slack.of("chat.stopStream")[0]);
  assert.ok(final.length > 0, "the stream stops with the final blocks");
  assert.match(textIn(final), new RegExp(defaultCopy.helpful));
  assert.match(textIn(final), /lead is AI/);
  collect("streamed answer", w.slack);
});

test("a redelivery of the same Slack event starts no second run, before or after a restart", async () => {
  const w = world();
  await deliver(w, mention("ping", TS));
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "pong" });
  await w.app.idle();
  const calls = w.slack.calls.length;

  await deliver(w, mention("ping", TS, { retry: 1 }));
  await w.app.idle();
  assert.equal(w.collegium.asked.length, 1, "the same process remembers the event");

  // A new process remembers nothing, so it asks collegium, which has the run.
  const again = w.restart();
  await deliver(w, mention("ping", TS), again);
  await again.idle();
  assert.equal(w.collegium.asked.length, 1, "collegium is not asked to start it again");
  assert.equal(w.collegium.runs.size, 1);
  assert.equal(w.slack.calls.length, calls, "and nothing more is written to Slack");
});

test("a reply in the thread continues the conversation, also after a restart", async () => {
  const w = world();
  await deliver(w, mention("first", TS));
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "one" });
  await w.app.idle();
  const first = w.collegium.last.id;

  const app = w.restart();
  await deliver(w, mention("second", "1700000050.000200", { thread_ts: TS }), app);
  const second = w.collegium.started[1];
  assert.equal(second?.after, first);
  assert.equal(second?.idempotencyKey, `slack-${TEAM}-${CHANNEL}-1700000050-000200`);
  assert.equal(w.collegium.last.thread, first, "one Slack thread is one conversation");
  assert.equal(w.slack.of("chat.startStream").at(-1)?.thread_ts, TS);
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "two" });
  await app.idle();
});

test("a direct message is a conversation per thread", async () => {
  const w = world();
  await deliver(w, direct("hello", "1700000100.000100"));
  assert.equal(w.collegium.started[0]?.idempotencyKey, `slack-${TEAM}-${DM}-1700000100-000100`);
  assert.equal(w.slack.of("chat.startStream")[0]?.thread_ts, "1700000100.000100");
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "hi" });
  await w.app.idle();

  // A new top-level message is a new conversation.
  await deliver(w, direct("something else", "1700000200.000100"));
  assert.equal(w.collegium.started[1]?.after, undefined);
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "ok" });
  await w.app.idle();
});

test("an event from another team is ignored", async () => {
  const w = world();
  await deliver(w, mention("hello", TS, { team_id: OTHER_TEAM }));
  await deliver(w, {
    type: "interactive",
    payload: {
      type: "shortcut",
      callback_id: "aui_ask",
      team: { id: OTHER_TEAM },
      user: { id: USER },
    },
  });
  await deliver(w, command("hello", { team_id: OTHER_TEAM }));
  await w.app.idle();
  assert.equal(w.collegium.asked.length, 0);
  assert.equal(w.slack.calls.length, 0);
  assert.equal(w.logs.filter((l) => /another team/.test(l)).length, 3);
});

test("a bot's message, the app's own and an edit never start a run", async () => {
  const w = world();
  await deliver(w, mention("loop", TS, { bot_id: "B01ABCDEF" }));
  await deliver(w, direct("loop", "1700000001.000100", { bot_id: "B01ABCDEF", user: undefined }));
  await deliver(w, direct("loop", "1700000002.000100", { user: BOT }));
  await deliver(w, direct("loop", "1700000003.000100", { subtype: "message_changed" }));
  await deliver(w, direct("loop", "1700000004.000100", { subtype: "bot_message" }));
  await w.app.idle();
  assert.equal(w.collegium.asked.length, 0);
  assert.equal(w.slack.calls.length, 0);
});

test("stop mid-run stops the run, confirms in the thread, settles the session and renders nothing more", async () => {
  const w = world();
  await deliver(w, mention("write a long report", TS));
  const run = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  await emit(w, "delta.text", { text: "Part one. " });

  await deliver(w, stopPressed(TS));
  await w.app.idle();
  assert.deepEqual(w.collegium.stops, [run]);
  const stopped = w.slack.of("chat.stopStream")[0];
  assert.match(textIn(blocksOf(stopped)), new RegExp(defaultCopy.stopped));
  assert.match(textIn(blocksOf(stopped)), new RegExp(defaultCopy.stopPhase.confirmed));
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
  assert.equal(w.slack.of("reactions.add").at(-1)?.name, "black_square_for_stop");
  // Slack's order: the work is stopped, then confirmed, then the status moves.
  const m = w.slack.methods;
  assert.ok(m.indexOf("chat.stopStream") < m.lastIndexOf("agents.sessions.setStatus"));

  // The model call was not interrupted: what it still says is discarded.
  const before = w.slack.calls.length;
  w.collegium.emit(run, "delta.text", { text: "Part two." });
  w.collegium.emit(run, "lifecycle.completed", { text: "Part one. Part two." });
  await settle();
  assert.equal(w.slack.calls.length, before);
  assert.equal(w.slack.streamed, "Part one.");
  collect("stopped", w.slack);
});

test("stop after a restart still finds the run, through collegium", async () => {
  const w = world();
  // A run an earlier process started and is no longer there to follow.
  const run = (await w.collegium.createRun({ agent: "lead", text: "slow question" }, KEY)).id;
  w.collegium.emit(run, "lifecycle.started");

  // This process follows nothing. The session still names the conversation.
  const app = w.app;
  const before = w.slack.calls.length;
  await deliver(w, stopPressed(TS), app);
  await app.idle();
  assert.deepEqual(w.collegium.stops, [run]);
  const after = w.slack.calls.slice(before);
  assert.deepEqual(
    after.map((c) => c.method),
    ["chat.postMessage", "agents.sessions.setStatus"],
  );
  assert.equal(after[0]?.args.thread_ts, TS);
  assert.match(textIn(blocksOf(after[0]?.args)), new RegExp(defaultCopy.stopped));
  assert.equal(after[1]?.args.status, "active");
  collect("stopped after restart", w.slack);

  // With nothing left to stop, the button still settles the session.
  const idle = w.restart();
  await deliver(w, stopPressed(TS), idle);
  await idle.idle();
  assert.deepEqual(w.collegium.stops, [run], "an ended run is not stopped again");
  assert.equal(w.slack.calls.at(-1)?.method, "agents.sessions.setStatus");
  await w.app.idle();
});

test("a stop collegium does not confirm says so, and still renders nothing more", async () => {
  const w = world();
  await deliver(w, mention("question", TS));
  await emit(w, "lifecycle.accepted");
  w.collegium.stopFails = true;
  await deliver(w, stopPressed(TS));
  await w.app.idle();
  assert.match(
    textIn(blocksOf(w.slack.of("chat.stopStream")[0])),
    new RegExp(defaultCopy.stopPhase.failed),
  );
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
});

test("a run stopped from the console ends in Slack, though it publishes no final event", async () => {
  const w = world();
  await deliver(w, mention("question", TS));
  const run = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  w.collegium.set(run, { status: "stopped", cause: "Stopped by dana" });
  await w.app.idle();
  assert.match(textIn(blocksOf(w.slack.of("chat.stopStream")[0])), new RegExp(defaultCopy.stopped));
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
});

test("a stopped event on the stream ends the turn, once the platform publishes one", async () => {
  const w = world();
  await deliver(w, mention("question", TS));
  await emit(w, "lifecycle.accepted");
  await emit(w, "delta.text", { text: "Half an answer." });
  await emit(w, "lifecycle.stopped", { cause: "Stopped by dana" });
  await w.app.idle();
  const final = textIn(blocksOf(w.slack.of("chat.stopStream")[0]));
  assert.match(final, new RegExp(defaultCopy.stopped));
  assert.match(final, new RegExp(defaultCopy.stopPhase.confirmed));
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
});

test("a failed run shows why, and Retry asks again in the same conversation", async () => {
  const w = world();
  await deliver(w, mention("deploy it", TS));
  const failedRun = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  // An attempt the engine will retry is not the end of the turn.
  await emit(w, "lifecycle.failed", { error: "timeout", terminal: false });
  assert.ok(!w.slack.methods.includes("chat.stopStream"));
  await emit(w, "lifecycle.failed", {
    error: "agent: step 2\nprovider: 529 overloaded",
    terminal: true,
  });
  await w.app.idle();
  const final = w.slack.of("chat.stopStream")[0];
  assert.match(textIn(blocksOf(final)), /provider: 529 overloaded/);
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
  assert.equal(w.slack.of("reactions.add").at(-1)?.name, "x");
  collect("failed", w.slack);

  const ts = String(w.slack.calls.find((c) => c.method === "chat.stopStream")?.args.ts);
  await deliver(w, click(blocksOf(final), defaultCopy.retry, { ts, threadTs: TS }));
  assert.equal(w.collegium.started.length, 2);
  const retry = w.collegium.started[1];
  assert.equal(retry?.text, "deploy it");
  assert.equal(retry?.agent, "lead");
  assert.equal(retry?.after, failedRun, "the retry stays in the thread's conversation");
  assert.equal(w.collegium.last.thread, failedRun);
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Deployed." });
  await w.app.idle();

  // A second click joins the first retry.
  await deliver(w, click(blocksOf(final), defaultCopy.retry, { ts, threadTs: TS }));
  await w.app.idle();
  assert.equal(w.collegium.started.length, 2);
});

test("a run whose retries ran out is ended by its record, as the ledger decides", async () => {
  const w = world();
  await deliver(w, mention("deploy it", TS));
  const run = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  // Each attempt reports a failure, and none says it was the last.
  await emit(w, "lifecycle.failed", { error: "connection refused", attempt: 1 });
  await emit(w, "lifecycle.resumed", { attempt: 2 });
  await emit(w, "lifecycle.failed", { error: "connection refused", attempt: 2 });
  assert.ok(!w.slack.methods.includes("chat.stopStream"));
  w.collegium.set(run, {
    status: "failed",
    error: "duraturo: max attempts exhausted\nconnection refused",
  });
  await w.app.idle();
  const final = w.slack.of("chat.stopStream")[0];
  assert.match(textIn(blocksOf(final)), /connection refused/);
  assert.match(textIn(blocksOf(final)), new RegExp(defaultCopy.retry));
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
});

/** A failed turn in the thread, and the message holding its Retry button. */
async function failedTurn(w: World) {
  await deliver(w, mention("deploy it", TS));
  const run = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.failed", { error: "provider: 529 overloaded", terminal: true });
  await w.app.idle();
  const final = w.slack.of("chat.stopStream").at(-1);
  return { run, blocks: blocksOf(final), ts: String(final?.ts) };
}

test("only the person who asked, or an operator, may ask a turn again", async () => {
  const w = world({ operators: ["U03ABCDEF"] });
  const { run, blocks, ts } = await failedTurn(w);

  // Someone else in the channel is told no, and nothing starts.
  await deliver(w, click(blocks, defaultCopy.retry, { ts, threadTs: TS, user: OTHER_USER }));
  await w.app.idle();
  assert.equal(w.collegium.started.length, 1);
  assert.equal(
    w.slack.of("chat.postEphemeral").at(-1)?.text,
    "Only the person who asked can retry this.",
  );
  assert.equal(w.slack.of("chat.postEphemeral").at(-1)?.user, OTHER_USER);

  // An operator may. The new run stays the first asker's.
  await deliver(w, click(blocks, defaultCopy.retry, { ts, threadTs: TS, user: "U03ABCDEF" }));
  assert.equal(w.collegium.started.length, 2);
  const retried = w.collegium.last.id;
  assert.equal(w.collegium.last.prev ?? w.collegium.last.thread, run);
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Deployed." });
  await w.app.idle();

  // So the person who asked can settle what the retried run raises, and the clicker who is not an operator cannot.
  const watching = new AbortController();
  const watch = w.app.watchInbox(watching.signal);
  w.collegium.raise(memoryMail(retried));
  await settle();
  await w.app.idle();
  const posted = w.slack.of("chat.postMessage").at(-1);
  await deliver(
    w,
    click(blocksOf(posted), defaultCopy.discardAll, {
      ts: "1800000009.000100",
      threadTs: TS,
      user: OTHER_USER,
    }),
  );
  assert.equal(w.collegium.resolved.length, 0);
  await deliver(
    w,
    click(blocksOf(posted), defaultCopy.discardAll, { ts: "1800000009.000100", threadTs: TS }),
  );
  await w.app.idle();
  assert.equal(w.collegium.resolved.length, 1);
  watching.abort();
  await watch;
});

test("after a restart, who asked is not known, so only an operator may ask again", async () => {
  const w = world({ operators: ["U03ABCDEF"] });
  const { blocks, ts } = await failedTurn(w);
  const app = w.restart();
  await deliver(w, click(blocks, defaultCopy.retry, { ts, threadTs: TS }), app);
  await app.idle();
  assert.equal(w.collegium.started.length, 1, "fails closed for the person who asked, too");
  assert.equal(
    w.slack.of("chat.postEphemeral").at(-1)?.text,
    "Only the person who asked can retry this.",
  );
  await deliver(w, click(blocks, defaultCopy.retry, { ts, threadTs: TS, user: "U03ABCDEF" }), app);
  assert.equal(w.collegium.started.length, 2);
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "ok" });
  await app.idle();
});

test("asking again is refused once the agent may no longer be reached", async () => {
  const w = world({ clientId: "docs-bot", defaultAgent: undefined });
  const client = {
    id: "docs-bot",
    surface: "slack",
    enabled: true,
    default_agent: "lead",
    identity: { team: TEAM },
  };
  w.collegium.clients.set("docs-bot", client);
  const { blocks, ts } = await failedTurn(w);
  // The client is changed: lead is no longer its agent. Its record is read again after 30s.
  w.collegium.clients.set("docs-bot", { ...client, default_agent: "helper" });
  const app = new (w.app.constructor as typeof import("./app.tsx").App)(w.slack, w.collegium, {
    team: TEAM,
    botUser: BOT,
    subject: SUBJECT,
    clientId: "docs-bot",
    operators: [USER],
    flushMs: 0,
    pollMs: 20,
  });
  await deliver(w, click(blocks, defaultCopy.retry, { ts, threadTs: TS }), app);
  await app.idle();
  assert.equal(w.collegium.started.length, 1);
  assert.equal(w.slack.of("chat.postEphemeral").at(-1)?.text, defaultCopy.cantRetry);
});

/** A failure card as Slack shows it, from Home (a view) or in a thread (a message). */
function failureCard(run: string, where: "home" | "thread", user: string) {
  const blocks = renderToSlack(
    <MessageView message={forSlack(fromServer(failureMail(run)))} />,
  ).blocks;
  const env = click(blocks, defaultCopy.retry, { ts: "1800000005.000100", threadTs: TS, user });
  if (where === "home") {
    const p = env.payload as Record<string, unknown>;
    p.container = { type: "view" };
    p.view = { type: "home" };
    delete p.message;
  }
  return env;
}

for (const where of ["home", "thread"] as const) {
  test(`Retry on an inbox failure card from ${where} never asks collegium to retry on its own`, async () => {
    const w = world({ operators: ["U03ABCDEF"] });
    const { run } = await failedTurn(w);
    w.collegium.mailbox.set("m-failure", failureMail(run));

    // Someone who did not ask: told no, nothing sent.
    await deliver(w, failureCard(run, where, OTHER_USER));
    await w.app.idle();
    assert.equal(w.collegium.started.length, 1);
    assert.deepEqual(w.collegium.resolved, []);
    assert.ok(w.slack.of("chat.postEphemeral").at(-1));

    // A run that is not in one of this app's threads: an operator is told it cannot be retried here.
    w.collegium.mailbox.set("m-console", {
      ...failureMail("api-dana-console-run"),
      id: "m-console",
    });
    w.collegium.runs.set("api-dana-console-run", {
      ...w.collegium.last,
      id: "api-dana-console-run",
      thread: "api-dana-console-run",
    });
    const elsewhere = failureCard("api-dana-console-run", where, "U03ABCDEF");
    const v = JSON.parse(
      String((elsewhere.payload as { actions: { value: string }[] }).actions[0]?.value),
    );
    (elsewhere.payload as { actions: { value: string }[] }).actions[0] = {
      ...(elsewhere.payload as { actions: { action_id: string; value: string }[] }).actions[0],
      value: JSON.stringify({ ...v, turnId: "m-console" }),
    } as { action_id: string; value: string };
    await deliver(w, elsewhere);
    await w.app.idle();
    assert.deepEqual(w.collegium.resolved, []);
    assert.equal(w.slack.of("chat.postEphemeral").at(-1)?.text, defaultCopy.cantRetry);

    // The agent is no longer one the app may reach: told no, nothing sent.
    const gated = world(
      { operators: ["U03ABCDEF"], clientId: "docs-bot", defaultAgent: undefined },
      { slack: w.slack, collegium: w.collegium },
    );
    w.collegium.clients.set("docs-bot", {
      id: "docs-bot",
      surface: "slack",
      enabled: true,
      default_agent: "helper",
      identity: { team: TEAM },
    });
    await deliver(w, failureCard(run, where, "U03ABCDEF"), gated.app);
    await gated.app.idle();
    assert.equal(w.collegium.started.length, 1);
    assert.deepEqual(w.collegium.resolved, []);
    assert.equal(w.slack.of("chat.postEphemeral").at(-1)?.text, defaultCopy.cantRetry);

    // The person who asked: the app asks again in the thread and only acknowledges the card.
    await deliver(w, failureCard(run, where, USER));
    assert.equal(w.collegium.started.length, 2);
    assert.deepEqual((w.collegium.resolved as { body: unknown }[]).at(-1)?.body, {
      action: "acknowledge",
    });
    await emit(w, "lifecycle.accepted");
    await emit(w, "lifecycle.completed", { text: "ok" });
    await w.app.idle();
  });
}

test("a forged Retry on an inbox part that is not a failure is refused, and nothing is sent", async () => {
  const w = world({ operators: [USER] });
  w.collegium.mailbox.set("m-memory", memoryMail("api-svc-slack-any"));
  w.collegium.mailbox.set("m-alert", alertMail());
  for (const [id, target] of [
    ["m-memory", "p1"],
    ["m-alert", "p1"],
    ["m-alert", "no-such-part"],
  ]) {
    const env = interactive({
      type: "block_actions",
      channel: { id: CHANNEL },
      container: { type: "message", channel_id: CHANNEL },
      message: { ts: "1800000007.000100", thread_ts: TS },
      actions: [{ action_id: "aui.retry#0", value: JSON.stringify({ turnId: id, target }) }],
    });
    await deliver(w, env);
    await w.app.idle();
    assert.deepEqual(w.collegium.resolved, [], `${id}/${target}`);
    assert.equal(w.slack.of("chat.postEphemeral").at(-1)?.text, defaultCopy.cantRetry);
  }
  assert.equal(w.collegium.started.length, 0);
});

test("a Slack write that fails mid-turn still ends the turn: whole, and off processing", async () => {
  const w = world();
  await deliver(w, mention("long answer", TS));
  await emit(w, "lifecycle.accepted");
  await emit(w, "delta.text", { text: "First part. " });
  // Slack starts refusing appends, as it does after its retries for a rate limit run out.
  w.slack.failing["chat.appendStream"] = "ratelimited";
  await emit(w, "delta.text", { text: "Second part. " });
  await emit(w, "delta.text", { text: "Third part." });
  await emit(w, "lifecycle.completed", { text: "First part. Second part. Third part." });
  await w.app.idle();
  const m = w.slack.methods;
  assert.equal(
    m.filter((x) => x === "chat.appendStream").length,
    2,
    "nothing more is tried once a write fails",
  );
  // The streamed message is closed with the whole turn, so nothing is posted twice.
  const closed = w.slack.of("chat.stopStream").at(-1);
  assert.match(textIn(blocksOf(closed)), /First part\. Second part\. Third part\./);
  assert.match(textIn(blocksOf(closed)), new RegExp(defaultCopy.helpful));
  assert.equal(w.slack.of("chat.postMessage").length, 0);
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
  assert.equal(w.slack.of("reactions.remove").at(-1)?.name, "eyes");
  assert.equal(w.slack.of("reactions.add").at(-1)?.name, "white_check_mark");
  collect("write failed", w.slack);
});

test("when the streamed message cannot take the turn at all, the turn is posted as a new one", async () => {
  const w = world();
  await deliver(w, mention("long answer", TS));
  await emit(w, "lifecycle.accepted");
  await emit(w, "delta.text", { text: "First part. " });
  w.slack.failing["chat.appendStream"] = "ratelimited";
  w.slack.failing["chat.stopStream"] = (args) => (args.blocks ? "invalid_blocks" : undefined);
  await emit(w, "delta.text", { text: "Second part." });
  await emit(w, "lifecycle.completed", { text: "First part. Second part." });
  await w.app.idle();
  const whole = w.slack.of("chat.postMessage");
  assert.equal(whole.length, 1);
  assert.match(textIn(blocksOf(whole[0])), /First part\. Second part\./);
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
});

test("when editing in place fails, the turn is still posted and the session settled", async () => {
  const w = world();
  w.slack.failing["chat.startStream"] = "unknown_method";
  w.slack.failing["chat.update"] = "ratelimited";
  await deliver(w, mention("hello", TS));
  await emit(w, "lifecycle.accepted");
  await emit(w, "delta.text", { text: "Hello " });
  await emit(w, "delta.text", { text: "there." });
  await emit(w, "lifecycle.completed", { text: "Hello there." });
  await w.app.idle();
  const posts = w.slack.of("chat.postMessage");
  assert.match(textIn(blocksOf(posts.at(-1))), /Hello there\./);
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
});

const pause = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** The answers posted as new messages after a quiet run's stream was closed. */
const finals = (w: World, text: string) =>
  w.slack.of("chat.postMessage").filter((a) => textIn(blocksOf(a)).includes(text));

test("a long, silent tool call is not a failure: the stream closes and the answer follows once", async () => {
  const w = world({ stallMs: 30, slowPollMs: 40 });
  await deliver(w, mention("rebuild the index", TS));
  const run = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  await emit(w, "delta.tool.call", { id: "t1", name: "index.rebuild", phase: "requested" });
  await pause(120);
  // The stream is closed as it stands, still working: no failure, still processing.
  const closed = w.slack.of("chat.stopStream")[0];
  assert.ok(closed, "the stream is not held open");
  assert.ok(!textIn(blocksOf(closed)).includes(defaultCopy.failedTitle));
  assert.ok(!textIn(blocksOf(closed)).includes(defaultCopy.failure.timeout()));
  assert.deepEqual(w.slack.statuses, ["processing"]);
  assert.deepEqual(w.collegium.stops, []);

  w.collegium.set(run, { status: "completed", answer: "The index is rebuilt." });
  await w.app.idle();
  await pause(100);
  assert.equal(finals(w, "The index is rebuilt.").length, 1, "the answer is posted once");
  assert.equal(finals(w, "The index is rebuilt.")[0]?.thread_ts, TS);
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
  assert.equal(w.slack.of("reactions.add").at(-1)?.name, "white_check_mark");
  collect("quiet then done", w.slack);
});

test("a parked run is only waiting, and its end is posted when it comes", async () => {
  const w = world({ stallMs: 30, slowPollMs: 40 });
  await deliver(w, mention("ask the helper", TS));
  const run = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.parked", { reason: "waiting on helper" });
  await pause(120);
  assert.equal(w.slack.of("chat.stopStream").length, 1);
  assert.deepEqual(w.slack.statuses, ["processing"]);
  w.collegium.set(run, { status: "completed", answer: "The helper found it." });
  await w.app.idle();
  assert.equal(finals(w, "The helper found it.").length, 1);
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
});

test("a quiet run does not hold the thread, and the next message follows it as collegium orders turns", async () => {
  const w = world({ stallMs: 30, slowPollMs: 40 });
  await deliver(w, mention("long job", TS));
  const first = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  await pause(120);

  await deliver(w, mention("meanwhile, a quick one", "1700000070.000100", { thread_ts: TS }));
  assert.equal(w.collegium.started.length, 2, "the queue was released");
  // Collegium takes a turn after one that is still running; its transcript says so.
  assert.equal(w.collegium.started[1]?.after, first);
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Quick answer." });

  w.collegium.set(first, { status: "completed", answer: "Long job done." });
  await w.app.idle();
  assert.equal(finals(w, "Long job done.").length, 1);
});

test("stop on a quiet run confirms it once, and its end is not posted again", async () => {
  const w = world({ stallMs: 30, slowPollMs: 40 });
  await deliver(w, mention("long job", TS));
  const run = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  await pause(120);
  await deliver(w, stopPressed(TS));
  await w.app.idle();
  assert.deepEqual(w.collegium.stops, [run]);
  const confirmations = w.slack
    .of("chat.postMessage")
    .filter((a) => textIn(blocksOf(a)).includes(defaultCopy.stopped));
  assert.equal(confirmations.length, 1);
  assert.equal(w.slack.statuses.at(-1), "active");
});

test("on shutdown a quiet run stops being followed", async () => {
  const w = world({ stallMs: 30, slowPollMs: 40 });
  await deliver(w, mention("long job", TS));
  await emit(w, "lifecycle.accepted");
  await pause(120);
  w.app.close();
  await w.app.idle();
  assert.deepEqual(w.collegium.stops, [], "the run is left running");
});

test("an inbox decision collegium refuses is explained in vultus's words, not the platform's", async () => {
  const w = world();
  const watching = new AbortController();
  const watch = w.app.watchInbox(watching.signal);
  await deliver(w, mention("remember this", TS));
  const run = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Noted." });
  await w.app.idle();
  w.collegium.raise(memoryMail(run));
  await settle();
  await w.app.idle();
  const posted = w.slack.of("chat.postMessage").at(-1);
  w.collegium.inbox.send = async () => {
    throw new CollegiumError(403, "platform: forbidden: keep needs agents:write");
  };
  await deliver(
    w,
    click(blocksOf(posted), defaultCopy.discardAll, { ts: "1800000002.000100", threadTs: TS }),
  );
  await w.app.idle();
  assert.equal(w.slack.of("chat.postEphemeral").at(-1)?.text, defaultCopy.noPermission);
  assert.ok(w.logs.some((l) => l.includes("agents:write")));
  watching.abort();
  await watch;
});

test("an answer that never streamed arrives whole when the run completes", async () => {
  const w = world();
  await deliver(w, mention("quick", TS));
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Done." });
  await w.app.idle();
  assert.equal(w.slack.streamed, "Done.");
});

test("where native streaming is refused, the turn is posted once and edited", async () => {
  const w = world();
  w.slack.failing["chat.startStream"] = "unknown_method";
  await deliver(w, mention("hello", TS));
  await emit(w, "lifecycle.accepted");
  await emit(w, "delta.text", { text: "Hello " });
  await emit(w, "delta.text", { text: "there." });
  await emit(w, "lifecycle.completed", { text: "Hello there." });
  await w.app.idle();
  const m = w.slack.methods;
  assert.equal(m.filter((x) => x === "chat.startStream").length, 1, "streaming is tried once");
  assert.equal(m.filter((x) => x === "chat.postMessage").length, 1);
  assert.ok(m.filter((x) => x === "chat.update").length >= 1);
  assert.ok(!m.includes("chat.appendStream") && !m.includes("chat.stopStream"));
  assert.equal(w.slack.of("chat.postMessage")[0]?.thread_ts, TS);
  const last = w.slack.of("chat.update").at(-1);
  assert.match(textIn(blocksOf(last)), /Hello there\./);
  assert.match(textIn(blocksOf(last)), new RegExp(defaultCopy.helpful));
  assert.deepEqual(w.slack.statuses, ["processing", "active"]);
  collect("post and update", w.slack);

  // The refusal is remembered: the next turn does not try to stream.
  await deliver(w, mention("again", "1700000300.000100"));
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Yes." });
  await w.app.idle();
  assert.equal(w.slack.methods.filter((x) => x === "chat.startStream").length, 1);
});

test("an agent named in the message answers, when it exists and may be reached", async () => {
  const w = world();
  await deliver(w, mention("@helper summarize this", TS));
  assert.equal(w.collegium.started[0]?.agent, "helper");
  assert.equal(w.collegium.started[0]?.text, "summarize this");
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "ok" });
  await w.app.idle();

  // A name that is not an agent is only a word.
  await deliver(w, mention("@nobody summarize this", "1700000400.000100"));
  assert.equal(w.collegium.started[1]?.agent, "lead");
  assert.equal(w.collegium.started[1]?.text, "@nobody summarize this");
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "ok" });
  await w.app.idle();
});

test("as a client, the app routes only to its default agent and its list", async () => {
  const w = world({ clientId: "docs-bot", defaultAgent: undefined });
  w.collegium.clients.set("docs-bot", {
    id: "docs-bot",
    surface: "slack",
    enabled: true,
    default_agent: "helper",
    agents: [],
    identity: { team: TEAM },
  });
  await deliver(w, mention("@lead take this", TS));
  assert.equal(
    w.collegium.started[0]?.agent,
    "helper",
    "a name outside the list is not read as an agent",
  );
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "ok" });
  await w.app.idle();
});

test("a client for another team, or one that is gone, routes to nothing", async () => {
  const w = world({ clientId: "docs-bot", defaultAgent: undefined });
  w.collegium.clients.set("docs-bot", {
    id: "docs-bot",
    surface: "slack",
    enabled: true,
    default_agent: "helper",
    identity: { team: OTHER_TEAM },
  });
  await deliver(w, mention("hello", TS));
  await w.app.idle();
  assert.equal(w.collegium.asked.length, 0);
  const said = w.slack.of("chat.postMessage")[0];
  assert.match(textIn(blocksOf(said)), /No agent answers here yet/);
  assert.deepEqual(w.slack.statuses, ["active"]);
  collect("no agent", w.slack);

  // A client that does not say which team it serves is not taken for this one.
  const v = world({ clientId: "docs-bot", defaultAgent: undefined });
  v.collegium.clients.set("docs-bot", {
    id: "docs-bot",
    surface: "slack",
    enabled: true,
    default_agent: "helper",
  });
  await deliver(v, mention("hello", TS));
  await v.app.idle();
  assert.equal(v.collegium.asked.length, 0);
});

test("a run a limit refuses is shown in vultus's words, and collegium's are kept in the log", async () => {
  const w = world();
  w.collegium.refuse = new CollegiumError(429, "the workspace reached 200 runs today", {
    limit: "runs",
    resets_at: new Date(Date.now() + 3_600_000).toISOString(),
  });
  await deliver(w, mention("one more", TS));
  await w.app.idle();
  assert.equal(w.collegium.runs.size, 0);
  const said = w.slack.of("chat.postMessage")[0];
  assert.equal(said?.thread_ts, TS);
  assert.match(textIn(blocksOf(said)), new RegExp(defaultCopy.budgetTitle));
  assert.ok(
    !textIn(blocksOf(said)).includes("200 runs today"),
    "the platform's words are not shown",
  );
  assert.ok(w.logs.some((l) => l.includes("200 runs today")));
  assert.ok(!/"Retry"/.test(textIn(blocksOf(said))), "there is no run to retry");
  assert.equal(w.slack.of("reactions.add").at(-1)?.name, "x");
  collect("refused", w.slack);
});

test("a message sent while a turn is running waits its turn", async () => {
  const w = world();
  await deliver(w, mention("first", TS));
  const first = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  await deliver(w, mention("second", "1700000060.000100", { thread_ts: TS }));
  assert.equal(w.collegium.started.length, 1, "one turn at a time per thread");
  assert.equal(w.slack.of("reactions.add").at(-1)?.timestamp, "1700000060.000100");

  await emit(w, "lifecycle.completed", { text: "one" }, first);
  await settle();
  assert.equal(w.collegium.started.length, 2);
  assert.equal(w.collegium.started[1]?.after, first);
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "two" });
  await w.app.idle();
});

test("stop also drops what was waiting behind the stopped turn", async () => {
  const w = world();
  await deliver(w, mention("first", TS));
  await emit(w, "lifecycle.accepted");
  await deliver(w, mention("second", "1700000060.000100", { thread_ts: TS }));
  await deliver(w, stopPressed(TS));
  await w.app.idle();
  assert.equal(w.collegium.started.length, 1);
  const marks = w.slack.of("reactions.add").filter((a) => a.timestamp === "1700000060.000100");
  assert.deepEqual(
    marks.map((a) => a.name),
    ["eyes", "black_square_for_stop"],
  );
});

test("/agent asks in a new thread, and /agent help lists who can be asked", async () => {
  const w = world();
  await deliver(w, command("what is on call this week?"));
  const root = w.slack.of("chat.postMessage")[0];
  assert.equal(root?.channel, CHANNEL);
  assert.equal(root?.text, `<@${USER}>: what is on call this week?`);
  const rootTs = "1800000001.000100";
  assert.equal(
    w.collegium.started[0]?.idempotencyKey,
    `slack-${TEAM}-${CHANNEL}-1800000001-000100`,
  );
  assert.equal(w.slack.of("chat.startStream")[0]?.thread_ts, rootTs);
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Dana." });
  await w.app.idle();

  await deliver(w, command("help"));
  assert.equal(w.collegium.started.length, 1);
  assert.match(String(w.slack.responses[0]?.body.text), /`@lead`: Takes a request/);
  assert.equal(w.slack.responses[0]?.body.response_type, "ephemeral");

  // Where the app is not a member, it says so to the person alone.
  w.slack.failing["chat.postMessage"] = "not_in_channel";
  await deliver(w, command("hello"));
  await w.app.idle();
  assert.equal(w.collegium.started.length, 1);
  assert.match(String(w.slack.responses[1]?.body.text), /not in this channel/);
});

test("the shortcuts: a form for asking from anywhere, and a message as the question", async () => {
  const w = world();
  await deliver(w, interactive({ type: "shortcut", callback_id: "aui_ask" }));
  const opened = w.slack.of("views.open")[0];
  assert.equal((opened?.view as { callback_id: string }).callback_id, "aui_form");
  collect("ask form", w.slack);

  await deliver(
    w,
    interactive({
      type: "view_submission",
      view: {
        callback_id: "aui_form",
        private_metadata: '{"kind":"ask"}',
        state: { values: { text: { value: { value: "summarize my week" } } } },
      },
    }),
  );
  // No channel: the answer goes to the app's messages with this person.
  assert.equal(w.slack.of("chat.postMessage")[0]?.channel, USER);
  assert.equal(w.collegium.started[0]?.text, "summarize my week");
  assert.match(String(w.collegium.started[0]?.idempotencyKey), new RegExp(`^slack-${TEAM}-${DM}-`));
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Busy." });
  await w.app.idle();

  await deliver(
    w,
    interactive({
      type: "message_action",
      callback_id: "aui_ask_about",
      channel: { id: CHANNEL },
      message: { ts: "1700000500.000100", text: "is the deploy safe to ship?" },
    }),
  );
  assert.equal(w.collegium.started[1]?.text, "is the deploy safe to ship?");
  assert.equal(w.slack.of("chat.startStream").at(-1)?.thread_ts, "1700000500.000100");
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Yes." });
  await w.app.idle();
});

test("an inbox item about a run lands in its thread, and the button resolves it in collegium", async () => {
  const w = world();
  const watching = new AbortController();
  const watch = w.app.watchInbox(watching.signal);
  await deliver(w, mention("remember how we deploy", TS));
  const run = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Noted." });
  await w.app.idle();

  w.collegium.raise(memoryMail(run));
  await settle();
  await w.app.idle();
  const posted = w.slack.of("chat.postMessage").at(-1);
  assert.equal(posted?.thread_ts, TS, "it is shown where the run was asked for");
  assert.match(textIn(blocksOf(posted)), /2 proposed memories for lead/);
  assert.match(textIn(blocksOf(posted)), /Deploys are frozen on Fridays/);
  assert.match(textIn(blocksOf(posted)), new RegExp(defaultCopy.keepSelected));
  const ts = "1800000002.000100";

  // Someone who did not ask for the run cannot settle it.
  const picked = {
    values: { "checkboxes:p1": { any: { selected_options: [{ value: "n1" }] } } },
  };
  await deliver(
    w,
    click(blocksOf(posted), defaultCopy.keepSelected, {
      ts,
      threadTs: TS,
      user: OTHER_USER,
      state: picked,
    }),
  );
  assert.equal(w.collegium.resolved.length, 0);
  assert.equal(w.slack.of("chat.postEphemeral")[0]?.text, defaultCopy.notYours);

  // The person who asked can: the ticked note is kept, the rest are not.
  await deliver(
    w,
    click(blocksOf(posted), defaultCopy.keepSelected, { ts, threadTs: TS, state: picked }),
  );
  await w.app.idle();
  assert.deepEqual(w.collegium.resolved, [
    { id: "m-memory", path: "parts/p1/resolve", body: { action: "keep", ids: ["n1"] } },
  ]);
  const updated = w.slack.of("chat.update").at(-1);
  assert.equal(updated?.ts, ts);
  assert.ok(
    !textIn(blocksOf(updated)).includes(defaultCopy.keepSelected),
    "the buttons become a record",
  );
  assert.match(textIn(blocksOf(updated)), /Kept 1 of 2/);
  collect("inbox in thread", w.slack);

  watching.abort();
  await watch;
});

test("the Home tab shows a person what waits on them, and an operator the whole inbox", async () => {
  const w = world({ operators: [OTHER_USER] });
  await deliver(w, mention("deploy it", TS));
  const run = w.collegium.last.id;
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.failed", { error: "provider: 529 overloaded", terminal: true });
  await w.app.idle();
  w.collegium.mailbox.set("m-failure", failureMail(run));
  w.collegium.mailbox.set("m-other", {
    ...failureMail("api-someone-else"),
    id: "m-other",
    subject: "releaser failed",
  });

  await deliver(w, homeOpened(USER));
  const mine = w.slack.of("views.publish")[0];
  assert.equal(mine?.user_id, USER);
  const view = mine?.view as { type: string; blocks: unknown[] };
  assert.equal(view.type, "home");
  assert.match(textIn(view.blocks), /lead failed/);
  assert.ok(!/releaser failed/.test(textIn(view.blocks)), "another person's run is not shown");

  // An operator sees everything the app may read, and can settle it there.
  w.collegium.mailbox.set("m-alert", alertMail());
  w.collegium.mailbox.set("m-budget", budgetMail());
  await deliver(w, homeOpened(OTHER_USER));
  const all = w.slack.of("views.publish")[1]?.view as { blocks: unknown[] };
  assert.match(textIn(all.blocks), /releaser failed/);
  assert.match(textIn(all.blocks), /6 of 10 runs failed/);
  assert.match(textIn(all.blocks), /Project Launch reached its daily run limit/);

  const home = click(all.blocks, defaultCopy.acknowledge, { ts: "", user: OTHER_USER });
  (home.payload as Record<string, unknown>).container = { type: "view" };
  (home.payload as Record<string, unknown>).view = { type: "home" };
  delete (home.payload as Record<string, unknown>).message;
  await deliver(w, home);
  await w.app.idle();
  assert.deepEqual(w.collegium.resolved.at(-1), {
    id: "m-alert",
    path: "parts/p1/resolve",
    body: { action: "acknowledge" },
  });
  const republished = w.slack.of("views.publish").at(-1);
  assert.equal(republished?.user_id, OTHER_USER);
  assert.equal(
    w.slack.of("views.publish").length,
    3,
    "the tab is published again with the outcome",
  );
  collect("home", w.slack);
});

test("a rating is kept in the log until collegium can store one", async () => {
  const w = world();
  await deliver(w, mention("quick", TS));
  await emit(w, "lifecycle.accepted");
  await emit(w, "lifecycle.completed", { text: "Done." });
  await w.app.idle();
  const final = w.slack.of("chat.stopStream")[0];
  const before = w.slack.calls.length;
  await deliver(
    w,
    click(blocksOf(final), defaultCopy.notHelpful, { ts: String(final?.ts), threadTs: TS }),
  );
  await w.app.idle();
  assert.ok(w.logs.some((l) => l.endsWith(`by ${USER}: down`)));
  assert.equal(w.slack.calls.length, before, "nothing is promised that cannot be kept");
});

test(
  "every Block Kit payload these tests produced is one Slack accepts",
  { skip: !process.env.SLACK_VALIDATE },
  async () => {
    assert.ok(produced.length > 10);
    const failed: string[] = [];
    for (const p of produced) {
      const res = await validateBlocks(p.blocks ?? [], p.view);
      if (!res.ok) failed.push(`${p.from}: ${JSON.stringify(res.errors ?? res.error)}`);
    }
    assert.deepEqual(failed, []);
  },
);

test("text the app repeats as itself can mention a person or channel but never notify everyone", () => {
  for (const ping of [
    "<!channel>",
    "<!here>",
    "<!everyone>",
    "<!subteam^S0123ABC|@oncall>",
    "<!date^1^x|y>",
  ]) {
    const out = echoed(`hi ${ping} there`);
    assert.ok(!out.includes("<!"), `${ping} passed through as ${out}`);
  }
  // A form value arrives raw, command text entity-encoded: both end up the same.
  assert.equal(echoed("<!channel> deploy"), "&lt;!channel&gt; deploy");
  assert.equal(echoed("&lt;!channel&gt; deploy"), "&lt;!channel&gt; deploy");
  assert.equal(echoed("<https://evil.example|bank>"), "&lt;https://evil.example|bank&gt;");
  assert.equal(
    echoed("ask <@U0ABCDEF> in <#C0ABCDEF|ops> & <#C0ABCDEF>"),
    "ask <@U0ABCDEF> in <#C0ABCDEF|ops> &amp; <#C0ABCDEF>",
  );
  assert.equal(echoed("a < b > c"), "a &lt; b &gt; c");
});

test("asking from the shortcut form cannot make the app notify a whole channel", async () => {
  const w = world();
  await deliver(
    w,
    interactive({
      type: "view_submission",
      view: {
        callback_id: "aui_form",
        private_metadata: '{"kind":"ask"}',
        state: { values: { text: { value: { value: "<!channel> what changed?" } } } },
      },
    }),
  );
  const root = w.slack.of("chat.postMessage")[0];
  assert.ok(root, "the question was posted");
  assert.ok(!String(root?.text).includes("<!channel>"), String(root?.text));
});
