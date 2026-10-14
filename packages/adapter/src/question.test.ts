import assert from "node:assert/strict";
import { test } from "node:test";
import { ActionId, type UiAction } from "@vultus/core";
import { follow } from "./fold.ts";
import { fromServer, toRequest } from "./mail.ts";
import { answerOfAction, interruptOf } from "./question.ts";
import type { BusEvent } from "./run.ts";

let seq = 0;
const ev = (kind: string, payload?: unknown): BusEvent => ({
  seq: ++seq,
  topic: "agent:lead:r1",
  run_id: "r1",
  tenant: "acme",
  agent: "lead",
  rev: 1,
  origin: "api",
  kind,
  payload: payload === undefined ? undefined : Buffer.from(JSON.stringify(payload)).toString("base64"),
  at: "2026-10-14T00:00:00Z",
});

const at = "2026-10-14T00:01:00Z";
const questions = {
  approval: {
    id: "q-a",
    kind: "approval",
    agent: "lead",
    prompt: "Allow lead to call file_write?",
    tool: { name: "file_write", call_id: "call-1", arguments: { path: "plan.md" } },
    expires_at: "2026-10-15T00:00:00Z",
  },
  choice: { id: "q-c", kind: "choice", agent: "lead", prompt: "Which colour?", options: ["red", "blue"] },
  reply: { id: "q-r", kind: "reply", agent: "lead", prompt: "What is the ticket?" },
} as const;

// What the platform records for each kind, and what the run resumes with.
const answers = {
  approval: { answer: { question_id: "q-a", decision: "approve", by: "olu", at }, outcome: { action: "approve", by: "olu", at } },
  choice: { answer: { question_id: "q-c", choice: "blue", by: "olu", at }, outcome: { action: "choose", by: "olu", at, note: "blue" } },
  reply: { answer: { question_id: "q-r", text: "OPS-42", by: "olu", at }, outcome: { action: "reply", by: "olu", at, note: "OPS-42" } },
} as const;

const part = (kind: keyof typeof questions, outcome?: unknown) =>
  fromServer({
    id: "m-1",
    kind: kind === "approval" ? "approval" : "clarification",
    subject: "lead asks",
    from: { kind: "agent", name: "lead" },
    at: "2026-10-14T00:00:00Z",
    updated_at: "2026-10-14T00:00:00Z",
    severity: "info",
    state: outcome ? "resolved" : "open",
    unread: true,
    source: { run: "r1" },
    parts: [
      {
        id: "p1",
        type: kind,
        state: outcome ? "resolved" : "open",
        actions: outcome ? [] : kind === "approval" ? ["approve", "deny"] : kind === "choice" ? ["choose"] : ["reply"],
        question: { ...questions[kind], run: "r1" },
        ...(outcome ? { outcome } : {}),
      },
    ],
  });

for (const kind of ["approval", "choice", "reply"] as const) {
  test(`a ${kind} question waits on the run, and its answer settles it in place`, () => {
    const f = follow({ id: "r1", agent: "lead" });
    for (const e of [ev("lifecycle.accepted", { rev: 1 }), ev("lifecycle.started", { attempt: 1 })]) f.apply(e);
    if (kind === "approval") f.apply(ev("delta.tool.call", { id: "call-1", name: "file_write", phase: "requested" }));
    let turn = f.apply(ev("lifecycle.parked", { reason: "waiting on a person", question: questions[kind] }));
    assert.equal(turn.status, "waiting");
    assert.equal(turn.activity, "waiting");
    assert.equal(turn.interrupts.length, 1);
    assert.deepEqual(turn.interrupts[0], interruptOf(questions[kind]));
    if (kind === "approval") {
      const call = turn.tasks.find((t) => t.id === "call-1");
      assert.equal(call?.status, "waiting", "the held call reads waiting, not running");
    }

    turn = f.apply(ev("lifecycle.resumed", { attempt: 2, answer: answers[kind].answer }));
    assert.notEqual(turn.status, "waiting");
    const settled = turn.interrupts[0];
    if (settled?.type === "approval") assert.deepEqual(settled.decision, { kind: "approved", by: "olu", at });
    else if (kind === "choice") assert.deepEqual(settled?.answer, { kind: "answered", by: "olu", at, optionIds: ["blue"] });
    else assert.deepEqual(settled?.answer, { kind: "answered", by: "olu", at, optionIds: [], other: "OPS-42" });
  });

  test(`a ${kind} question reads the same on the run and in the inbox`, () => {
    // Open: the inbox part carries the run's own card.
    const open = part(kind).parts[0];
    const card = interruptOf(questions[kind]);
    if (open?.type === "approval") assert.deepEqual(open.approval, card);
    else if (open?.type === "choice") assert.deepEqual(open.clarification, card);
    else assert.equal(open?.type === "reply" && open.prompt, questions.reply.prompt);
    // Settled: the run's resumed event and the inbox's outcome say the same.
    const f = follow({ id: "r1", agent: "lead" });
    f.apply(ev("lifecycle.parked", { question: questions[kind] }));
    const onRun = f.apply(ev("lifecycle.resumed", { answer: answers[kind].answer })).interrupts[0];
    const inMail = part(kind, answers[kind].outcome).parts[0];
    if (inMail?.type === "approval") assert.deepEqual(inMail.approval, onRun);
    else if (inMail?.type === "choice") assert.deepEqual(inMail.clarification, onRun);
    else assert.deepEqual(inMail?.type === "reply" && inMail.resolution, { outcome: "replied", by: "olu", at, detail: "OPS-42" });
  });
}

test("a click on a question asks for the answer it means, on the run and in the inbox", () => {
  const click = (id: UiAction["id"], turnId: string, target: string, extra: Partial<UiAction["payload"]> = {}): UiAction => ({
    id,
    payload: { turnId, target, ...extra },
  });
  assert.deepEqual(answerOfAction(click(ActionId.approve, "r1", "q-a")), { run: "r1", question: "q-a", body: { action: "approve" } });
  assert.deepEqual(answerOfAction(click(ActionId.deny, "r1", "q-a")), { run: "r1", question: "q-a", body: { action: "deny" } });
  assert.deepEqual(answerOfAction(click(ActionId.clarify, "r1", "q-c", { option: "red" })), {
    run: "r1",
    question: "q-c",
    body: { action: "choose", choice: "red" },
  });
  assert.deepEqual(answerOfAction(click(ActionId.clarifyOther, "r1", "q-r", { text: " OPS-42 " })), {
    run: "r1",
    question: "q-r",
    body: { action: "reply", text: "OPS-42" },
  });
  assert.equal(answerOfAction(click(ActionId.clarifyOther, "r1", "q-r")), null, "a reply with no words is not an answer");
  assert.equal(answerOfAction(click(ActionId.ask, "r1", "q-a", { text: "why?" })), null);

  const approval = part("approval");
  assert.deepEqual(toRequest(approval, click(ActionId.approve, "m-1", "q-a")), { call: "resolve", partId: "p1", action: "approve", values: undefined });
  assert.deepEqual(toRequest(approval, click(ActionId.deny, "m-1", "q-a")), { call: "resolve", partId: "p1", action: "deny", values: undefined });
  assert.deepEqual(toRequest(part("choice"), click(ActionId.clarify, "m-1", "q-c", { option: "blue" })), {
    call: "resolve",
    partId: "p1",
    action: "choose",
    values: { choice: "blue" },
  });
  assert.deepEqual(toRequest(part("reply"), click(ActionId.reply, "m-1", "p1", { text: "OPS-42" })), {
    call: "resolve",
    partId: "p1",
    action: "reply",
    values: { text: "OPS-42" },
  });
  // What the reader may do comes from the platform, in vultus's ids.
  assert.deepEqual(approval.parts[0]?.allowed, [ActionId.approve, ActionId.deny]);
  assert.deepEqual(part("choice").parts[0]?.allowed, [ActionId.clarify]);
});

test("a question nobody answered, or whose run was stopped, says so", () => {
  const expired = part("approval", { action: "expired", by: "system", at, note: "Nobody answered in time, so it was denied" }).parts[0];
  assert.deepEqual(expired?.type === "approval" && expired.approval.decision, { kind: "expired", at });
  const f = follow({ id: "r1", agent: "lead" });
  f.apply(ev("lifecycle.parked", { question: questions.choice }));
  const stopped = f.apply(ev("lifecycle.stopped", { reason: "Stopped by ada", by: "ada" }));
  const i = stopped.interrupts[0];
  assert.equal(i?.type === "clarification" && i.answer.kind, "declined");
  const g = follow({ id: "r1", agent: "lead" });
  g.apply(ev("lifecycle.parked", { question: questions.choice }));
  const late = g.apply(ev("lifecycle.resumed", { answer: { question_id: "q-c", choice: "blue", expired: true, at } })).interrupts[0];
  assert.deepEqual(late?.type === "clarification" && late.answer, {
    kind: "declined",
    by: "Nobody",
    at,
    reason: "Nobody answered in time, so it went with blue",
  });
});
