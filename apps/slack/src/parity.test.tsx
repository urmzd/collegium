import "./react.ts";
import assert from "node:assert/strict";
import { test } from "node:test";
import {
  type BusEvent,
  follow,
  fromServer,
  type PlatformMessage,
  type Run,
  replay,
} from "@collegium/adapter";
import { defaultCopy, MessageView, TurnView } from "@vultus/components";
import { type MessagePart, partResolution, type Turn } from "@vultus/core";
import { renderToSlack } from "@vultus/render-slack";
import { WebSurface } from "@vultus/render-web";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { forSlack } from "./app.tsx";
import {
  alertMail,
  budgetMail,
  failureMail,
  memoryMail,
  QUESTIONS,
  questionMail,
} from "./harness.ts";

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

/** Text as both surfaces reduce it: no markup, no emphasis, single spaces. */
const plain = (t: string) =>
  t
    .replace(/[*_`>]/g, "")
    .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\s+/g, " ")
    .trim();

/**
 * Everything a turn holds that a person must be able to read, taken from the
 * model and not from either renderer. A renderer that drops a task's output,
 * a citation, a file, the failure or the stop fails the comparison.
 */
function turnContent(turn: Turn): string[] {
  return [
    ...turn.text.split(/\n+/),
    ...turn.tasks.flatMap((t) => [t.title, t.output ?? ""]),
    ...turn.citations.map((c) => c.title),
    ...(turn.attachments ?? []).map((a) => ("title" in a ? (a.title ?? "") : "")),
    ...turn.notices.flatMap((n) => [n.title ?? "", n.body]),
    turn.failure?.message ?? "",
    turn.status === "stopped" ? defaultCopy.stopped : "",
    turn.stop ? defaultCopy.stopPhase[turn.stop] : "",
    defaultCopy.disclaimer(turn.agent.name),
  ]
    .map(plain)
    .filter(Boolean);
}

/** Everything an inbox message holds that a person must be able to read. */
function messageContent(message: PlatformMessage): string[] {
  const part = (p: MessagePart): string[] => {
    const done = partResolution(p);
    const record = done?.detail ? [done.detail] : [];
    switch (p.type) {
      case "text":
        return [p.text];
      case "memory":
        return [...p.proposals.map((n) => n.text), ...record];
      case "alert":
        return [p.alert.title, p.alert.body ?? "", ...record];
      case "failure":
        return [p.failure.message, ...record];
      case "budget":
        return [defaultCopy.budgetTitle, ...record];
      default:
        return record;
    }
  };
  return [message.subject, ...message.parts.flatMap(part)].map(plain).filter(Boolean);
}

/** What each surface is missing of some content. Both lists must be empty. */
function missing(slackText: string, webText: string, content: string[]) {
  const s = plain(slackText);
  const w = plain(webText);
  return {
    slack: content.filter((c) => !s.includes(c)),
    web: content.filter((c) => !w.includes(c)),
  };
}

/** Both surfaces show all of a turn. */
function bothShow(turn: Turn) {
  const el = <TurnView turn={turn} />;
  assert.deepEqual(missing(slack(el), web(el), turnContent(turn)), { slack: [], web: [] });
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
  ev("delta.citation", {
    label: "c1",
    index: 1,
    text: "Deploy 4812 went out at 14:52.",
    source: { document_title: "Deploy log", source_uri: "https://deploys.example.com/4812" },
  }),
  ev("delta.tool.call", { id: "t2", name: "publish_artifact" }),
  ev("action.taken", {
    name: "publish_artifact",
    data: {
      id: "art-1",
      title: "deploy-notes.md",
      kind: "document",
      mime: "text/markdown",
      size: 120,
      version: 1,
    },
  }),
  ev("delta.tool.done", { id: "t2", result: "published" }),
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
    [
      ["Github search", "complete", "3 commits"],
      ["Publish artifact", "complete", "published"],
    ],
  );
  assert.equal(turn.citations.length, 1);
  assert.equal(turn.attachments?.length, 1);
  bothShow(turn);
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
  bothSay(<TurnView turn={turn} />, defaultCopy.retry, RUN);
  bothShow(turn);

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
  bothSay(<TurnView turn={turn} />, defaultCopy.resume);
  bothShow(turn);

  // A stopped run that settled from its record shows the partial answer it carries, in both.
  const fromRecord = live(
    [ev("lifecycle.accepted")],
    run({ status: "stopped", answer: "Half an answer" }),
  );
  assert.equal(fromRecord.status, "stopped");
  assert.equal(fromRecord.text, "Half an answer");
  bothSay(<TurnView turn={fromRecord} />, "Half an answer");
  bothShow(fromRecord);

  // Once stopped, a turn does not move again, whatever a later record says.
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

test("every kind of inbox message holds the same content on both, as each surface is given it", () => {
  for (const wire of [memoryMail(RUN), failureMail(RUN), alertMail(), budgetMail()]) {
    const message = fromServer(wire);
    // The console renders the message as the platform sends it, Slack as the app hands it over.
    const s = slack(<MessageView message={forSlack(message)} />);
    const w = web(<MessageView message={message} />);
    assert.deepEqual(
      missing(s, w, messageContent(message)),
      { slack: [], web: [] },
      String(wire.id),
    );
  }
});

/** The buttons a rendering offers, by label: what a person can do there. */
function slackButtons(element: ReactElement): string[] {
  const pages = renderToSlack(element).pages.flat() as {
    type: string;
    elements?: { type: string; text?: { text: string } }[];
  }[];
  return pages
    .filter((b) => b.type === "actions")
    .flatMap((b) => b.elements ?? [])
    .filter((e) => e.type === "button")
    .map((e) => e.text?.text ?? "")
    .sort();
}
function webButtons(element: ReactElement): string[] {
  const html = renderToStaticMarkup(<WebSurface>{element}</WebSurface>);
  return [...html.matchAll(/<button[^>]*>([\s\S]*?)<\/button>/g)]
    .map((m) => (m[1] ?? "").replace(/<[^>]+>/g, "").trim())
    .filter(Boolean)
    .sort();
}

const asked = (kind: keyof typeof QUESTIONS) => [
  ev("lifecycle.accepted", { rev: 1 }),
  ev("lifecycle.started", { attempt: 1 }),
  ...(kind === "approval"
    ? [ev("delta.tool.call", { id: "call-1", name: "deploy", phase: "requested" })]
    : []),
  ev("lifecycle.parked", { reason: "waiting on a person", question: QUESTIONS[kind] }),
];
const at = "2026-10-09T00:20:00Z";
const answer = {
  approval: {
    run: { question_id: "q-approve", decision: "approve", by: "dana", at },
    inbox: { action: "approve", by: "dana", at },
    says: "Approved by dana",
  },
  choice: {
    run: { question_id: "q-choose", choice: "eu-west", by: "dana", at },
    inbox: { action: "choose", by: "dana", at, note: "eu-west" },
    says: "dana chose eu-west",
  },
  reply: {
    run: { question_id: "q-reply", text: "OPS-42", by: "dana", at },
    inbox: { action: "reply", by: "dana", at, note: "OPS-42" },
    says: "OPS-42",
  },
} as const;

for (const kind of ["approval", "choice", "reply"] as const) {
  test(`a ${kind} a run waits on reads and answers the same on both, on the run and in the inbox`, () => {
    // On the run: waiting, not working, with the question and its buttons.
    const waiting = live(asked(kind));
    assert.equal(waiting.status, "waiting");
    assert.equal(waiting.activity, "waiting");
    assert.deepEqual(replay(run(), asked(kind)), waiting, "the console rebuilds the same state");
    const onRun = <TurnView turn={waiting} />;
    bothSay(onRun, QUESTIONS[kind].prompt);
    if (kind === "approval") bothSay(onRun, defaultCopy.approve, defaultCopy.cancel);
    if (kind === "choice") bothSay(onRun, "us-east", "eu-west");
    if (kind === "reply") bothSay(onRun, defaultCopy.somethingElse);
    assert.deepEqual(
      slackButtons(onRun),
      webButtons(onRun).filter((b) => slackButtons(onRun).includes(b)),
    );

    // In the inbox: the same question, the same choices.
    const mail = fromServer(questionMail(RUN, kind));
    const inMail = <MessageView message={forSlack(mail)} />;
    bothSay(<MessageView message={mail} />, QUESTIONS[kind].prompt);
    if (kind === "approval") bothSay(inMail, defaultCopy.approve, defaultCopy.cancel);
    if (kind === "choice") bothSay(inMail, "us-east", "eu-west");
    if (kind === "reply") bothSay(inMail, defaultCopy.reply);

    // Answered: the record replaces the buttons, in the same words, wherever it is read.
    const settled = live([
      ...asked(kind),
      ev("lifecycle.resumed", { attempt: 2, answer: answer[kind].run }),
    ]);
    assert.notEqual(settled.status, "waiting");
    const decided = fromServer(questionMail(RUN, kind, answer[kind].inbox));
    for (const el of [<TurnView turn={settled} />, <MessageView message={decided} />]) {
      bothSay(el, answer[kind].says);
      assert.ok(!slack(el).includes(`"${defaultCopy.approve}"`), "no button is left to press");
    }
  });
}
