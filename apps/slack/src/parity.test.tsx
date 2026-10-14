import "./react.ts";
import assert from "node:assert/strict";
import { test } from "node:test";
import { type BusEvent, follow, fromServer, type Run, replay } from "@collegium/adapter";
import { defaultCopy, MessageView, TurnView } from "@vultus/components";
import type { Turn } from "@vultus/core";
import { renderToSlack } from "@vultus/render-slack";
import { WebSurface } from "@vultus/render-web";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { failureMail, memoryMail } from "./harness.ts";

/**
 * One state, two renderers. A recorded run goes through the shared adapter
 * once. The console and the Slack app both render that one turn with the same
 * vultus components, so what a person reads is the same in both places.
 */

const RUN = "api-svc-slack-slack-T01ABCDEF-C01ABCDEF-1700000000-000100";
const run = (over: Partial<Run> = {}): Run => ({
  id: RUN,
  tenant: "acme",
  agent: "lead",
  rev: 1,
  origin: "api",
  text: "what changed in the last deploy?",
  root: RUN,
  depth: 0,
  thread: RUN,
  lease: 0,
  granted: 0,
  calls: 0,
  status: "running",
  created_at: "2026-10-09T00:00:00Z",
  updated_at: "2026-10-09T00:00:00Z",
  ...over,
});

let seq = 0;
const ev = (kind: string, payload?: unknown): BusEvent => ({
  seq: ++seq,
  topic: `agent:lead:${RUN}`,
  run_id: RUN,
  tenant: "acme",
  agent: "lead",
  rev: 1,
  origin: "api",
  kind,
  payload:
    payload === undefined ? undefined : Buffer.from(JSON.stringify(payload)).toString("base64"),
  at: "2026-10-09T00:00:01Z",
});

/** The turn the Slack app holds: events folded in one at a time, as they arrive. */
function live(events: BusEvent[], record?: Run): Turn {
  const f = follow(run());
  for (const e of events) f.apply(e);
  if (record) f.settle(record);
  return f.turn;
}

/** What Slack shows: every string in every block. */
function slack(element: ReactElement): string {
  const out = renderToSlack(element);
  return JSON.stringify(out.pages).replace(/\\n/g, " ");
}

/** What the console shows: the markup's text. */
function web(element: ReactElement): string {
  return renderToStaticMarkup(<WebSurface>{element}</WebSurface>)
    .replace(/<[^>]+>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/\s+/g, " ");
}

/** Both surfaces say each of these. */
function bothSay(element: ReactElement, ...phrases: string[]) {
  const s = slack(element);
  const w = web(element);
  for (const phrase of phrases) {
    assert.ok(s.includes(phrase), `Slack does not say "${phrase}"`);
    assert.ok(w.includes(phrase), `the console does not say "${phrase}"`);
  }
}

const answered = [
  ev("lifecycle.accepted", { rev: 1 }),
  ev("lifecycle.started", { attempt: 1 }),
  ev("delta.tool.call", { id: "t1", name: "github.search", phase: "requested" }),
  ev("delta.tool.done", { id: "t1", result: "3 commits" }),
  ev("delta.text", { text: "Three commits " }),
  ev("delta.text", { text: "went out." }),
  ev("lifecycle.completed", { text: "Three commits went out." }),
];

test("a streamed answer with a tool call is one turn, rendered by both surfaces", () => {
  const turn = live(answered);
  // The console rebuilds a run with `replay`. It is the same state the Slack app streamed.
  assert.deepEqual(replay(run(), answered), turn);
  assert.equal(turn.status, "done");
  assert.equal(turn.text, "Three commits went out.");
  assert.deepEqual(
    turn.tasks.map((t) => [t.title, t.status, t.output]),
    [["Github search", "complete", "3 commits"]],
  );
  bothSay(
    <TurnView turn={turn} />,
    "Three commits went out.",
    "Github search",
    defaultCopy.disclaimer("lead"),
  );
});

test("a failed run is the same failure in both", () => {
  const events = [
    ev("lifecycle.accepted"),
    ev("lifecycle.failed", { error: "timeout", terminal: false }),
    ev("lifecycle.failed", { error: "agent: step 2\nprovider: 529 overloaded", terminal: true }),
  ];
  const turn = live(events);
  assert.deepEqual(replay(run(), events), turn);
  assert.equal(turn.status, "failed");
  assert.deepEqual(turn.failure, {
    kind: "internal",
    message: "provider: 529 overloaded",
    reference: RUN,
  });
  // The reference is a copy chip on the web and a line of text on Slack: the same id either way.
  bothSay(<TurnView turn={turn} />, "provider: 529 overloaded", defaultCopy.retry, RUN);

  // A run whose final event is gone is ended from its record, to the same state.
  const fromRecord = live(
    [ev("lifecycle.accepted")],
    run({ status: "failed", error: "agent: step 2\nprovider: 529 overloaded" }),
  );
  assert.deepEqual(fromRecord.failure, turn.failure);
  assert.equal(fromRecord.status, "failed");
});

test("a stopped run is stopped in both, with nothing after the stop", () => {
  const events = [ev("lifecycle.accepted"), ev("delta.text", { text: "Part one. " })];
  const turn = live(events, run({ status: "stopped", cause: "Stopped by svc-slack" }));
  assert.equal(turn.status, "stopped");
  assert.equal(turn.stop, "confirmed");
  assert.equal(turn.text, "Part one. ");
  bothSay(
    <TurnView turn={turn} />,
    "Part one.",
    defaultCopy.stopped,
    defaultCopy.stopPhase.confirmed,
    defaultCopy.resume,
  );

  // The model call was not interrupted, so the record may still gain an answer. A stopped turn does not.
  const f = follow(run());
  for (const e of events) f.apply(e);
  f.settle(run({ status: "stopped" }));
  assert.equal(
    f.settle(run({ status: "completed", answer: "Part one. Part two." })).text,
    "Part one. ",
  );
});

test("an inbox item is one message, with the same choices in both", () => {
  const memory = fromServer(memoryMail(RUN));
  bothSay(
    <MessageView message={memory} />,
    "2 proposed memories for lead",
    "Deploys are frozen on Fridays.",
    "The staging cluster is rebuilt every Monday.",
    defaultCopy.discardAll,
  );
  // The keep button counts the ticked notes on the web ("Keep 2 of 2"); Slack cannot update a label as boxes are ticked.
  assert.ok(slack(<MessageView message={memory} />).includes(defaultCopy.keepSelected));
  assert.match(web(<MessageView message={memory} />), /Keep 2 of 2/);
  const failure = fromServer(failureMail(RUN));
  bothSay(
    <MessageView message={failure} />,
    "lead failed",
    "provider: 529 overloaded",
    defaultCopy.retry,
  );

  // Once decided, both show the record instead of the buttons.
  const wire = memoryMail(RUN);
  const parts = wire.parts as Record<string, unknown>[];
  const decided = fromServer({
    ...wire,
    state: "resolved",
    parts: [
      {
        ...parts[0],
        state: "resolved",
        actions: [],
        outcome: { action: "keep", by: "dana", at: "2026-10-09T00:20:00Z", note: "Kept 1 of 2" },
      },
    ],
  });
  bothSay(<MessageView message={decided} />, "Kept 1 of 2");
  assert.ok(!slack(<MessageView message={decided} />).includes(defaultCopy.keepSelected));
  assert.ok(!/Keep \d/.test(web(<MessageView message={decided} />)));
});

test("an approval for a run that waits on a person is not something the platform sends yet", () => {
  // The inbox defines approval and choice parts and never raises them, so neither surface is given one to show.
  const wire = memoryMail(RUN);
  const message = fromServer({
    ...wire,
    parts: [{ id: "p9", type: "approval", state: "open", actions: [] }],
  });
  assert.deepEqual(message.parts, []);
});
