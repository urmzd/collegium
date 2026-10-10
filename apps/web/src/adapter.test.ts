import assert from "node:assert/strict";
import { test } from "node:test";
import { adapter, type BusEvent, body } from "./adapter.ts";

function ev(kind: string, payload?: unknown, seq = 1): BusEvent {
  return {
    seq,
    topic: "agent:lead:r1",
    run_id: "r1",
    tenant: "acme",
    agent: "lead",
    rev: 1,
    origin: "api",
    kind,
    payload: payload === undefined ? undefined : Buffer.from(JSON.stringify(payload)).toString("base64"),
    at: "2026-10-09T00:00:00Z",
  };
}

test("body decodes UTF-8, not Latin-1", () => {
  assert.equal(body<{ text: string }>(ev("delta.text", { text: "café ✓" })).text, "café ✓");
});

test("a malformed body is empty rather than a throw", () => {
  assert.deepEqual(body({ ...ev("delta.text"), payload: "%%%" }), {});
});

test("a streamed answer is not repeated on completion", () => {
  const to = adapter();
  const out = [
    ev("lifecycle.accepted", { rev: 1 }),
    ev("delta.text", { text: "Hello" }),
    ev("lifecycle.completed", { text: "Hello" }),
  ].flatMap(to);
  assert.deepEqual(
    out.map((e) => e.type),
    ["turn.started", "text.delta", "turn.completed"],
  );
});

test("an answer that never streamed arrives with completion", () => {
  const to = adapter();
  const out = [ev("lifecycle.accepted"), ev("lifecycle.completed", { text: "Done" })].flatMap(to);
  assert.deepEqual(out.slice(1), [{ type: "text.delta", text: "Done" }, { type: "turn.completed" }]);
});

test("a retried attempt is not a failed turn", () => {
  const to = adapter();
  assert.deepEqual(to(ev("lifecycle.failed", { error: "timeout", terminal: false })), []);
  const [last] = to(ev("lifecycle.failed", { error: "timeout", terminal: true }));
  assert.equal(last?.type, "turn.failed");
});

test("a tool call keeps its name when the done event omits it", () => {
  const to = adapter();
  to(ev("delta.tool.call", { id: "t1", name: "github.comment", phase: "requested" }));
  const [done] = to(ev("delta.tool.done", { id: "t1", result: "ok" }));
  assert.equal(done?.type, "task.upserted");
  if (done?.type === "task.upserted") {
    assert.equal(done.task.title, "Github comment");
    assert.equal(done.task.status, "complete");
  }
});

test("parked means waiting, which must not animate", () => {
  assert.deepEqual(adapter()(ev("lifecycle.parked", { reason: "approval" })), [
    { type: "activity", activity: "waiting" },
  ]);
});
