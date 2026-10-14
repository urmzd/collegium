import assert from "node:assert/strict";
import { test } from "node:test";
import { adapter, type BusEvent, body } from "./run.ts";

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
    ["turn.started", "activity", "text.delta", "turn.completed"],
  );
});

test("an answer that never streamed arrives with completion", () => {
  const to = adapter();
  const out = [ev("lifecycle.accepted"), ev("lifecycle.completed", { text: "Done" })].flatMap(to);
  assert.deepEqual(out.slice(1), [{ type: "text.delta", text: "Done" }, { type: "turn.completed" }]);
});

test("a stopped run ends the turn as stopped, keeping its partial answer", () => {
  const to = adapter();
  const out = [ev("lifecycle.accepted"), ev("lifecycle.stopped", { text: "Half", reason: "Stopped by ada", by: "ada" })].flatMap(to);
  assert.deepEqual(out.slice(1), [{ type: "text.delta", text: "Half" }, { type: "turn.stopped" }, { type: "stop.updated", stop: "confirmed" }]);
  const streamed = adapter();
  const again = [ev("delta.text", { text: "Half" }), ev("lifecycle.stopped", { text: "Half" })].flatMap(streamed);
  assert.deepEqual(again.map((e) => e.type).slice(-2), ["turn.stopped", "stop.updated"]);
  assert.equal(again.filter((e) => e.type === "text.delta").length, 1);
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
  const done = to(ev("delta.tool.done", { id: "t1", result: "ok" })).find((e) => e.type === "task.upserted");
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

test("the orb is told what kind of work a tool call is", () => {
  const activity = (name: string) => {
    const [first] = adapter()(ev("delta.tool.call", { id: "t", name, phase: "requested" }));
    return first?.type === "activity" ? first.activity : undefined;
  };
  assert.equal(activity("knowledge.search"), "searching");
  assert.equal(activity("delegate_helper"), "delegating");
  assert.equal(activity("github.comment"), "calling");
});

test("a call to another agent is that agent, not a tool", () => {
  const to = adapter();
  const [doing, started] = to(ev("delta.tool.call", { id: "t1", name: "tag_helper", phase: "requested" }));
  assert.deepEqual(doing, { type: "activity", activity: "delegating" });
  assert.equal(started?.type, "task.upserted");
  if (started?.type === "task.upserted") {
    assert.equal(started.task.title, "helper");
    assert.equal(started.task.subagent?.name, "helper");
  }
  const done = to(ev("delta.tool.done", { id: "t1", result: "Three tests fail." })).find((e) => e.type === "task.upserted");
  if (done?.type === "task.upserted") {
    assert.equal(done.task.status, "complete");
    assert.deepEqual(done.task.subagent, { name: "helper", runId: undefined, summary: "Three tests fail.", hue: undefined });
  }
  const [, tinted] = adapter({ hue: () => 210 })(ev("delta.tool.call", { id: "t3", name: "tag_helper" }));
  if (tinted?.type === "task.upserted") {
    assert.equal(tinted.task.subagent?.hue, 210);
  }
  // An ordinary tool is still a tool.
  const plain = to(ev("delta.tool.call", { id: "t2", name: "github.comment" })).find((e) => e.type === "task.upserted");
  assert.equal(plain?.type === "task.upserted" && plain.task.subagent, undefined);
});

test("a published file is one card that goes from generating to ready", () => {
  const to = adapter();
  const opened = to(ev("delta.tool.call", { id: "t1", name: "publish_artifact" })).find((e) => e.type === "attachment.upserted");
  assert.equal(opened?.type === "attachment.upserted" && opened.attachment.type === "artifact" && opened.attachment.phase, "generating");
  const [ready] = to(
    ev("action.taken", {
      name: "publish_artifact",
      data: { id: "art-7", title: "report.md", kind: "document", mime: "text/markdown", size: 19, version: 2, preview: "# Q3" },
    }),
  );
  assert.deepEqual(ready, {
    type: "attachment.upserted",
    attachment: {
      type: "artifact",
      id: "t1",
      title: "report.md",
      kind: "document",
      phase: "ready",
      mimeType: "text/markdown",
      sizeBytes: 19,
      version: 2,
      url: "/api/artifacts/art-7/content?inline=1",
      downloadUrl: "/api/artifacts/art-7/content",
      description: undefined,
      previewText: "# Q3",
    },
  });
});

test("a publish that is refused shows a failed file with the reason", () => {
  const to = adapter();
  to(ev("delta.tool.call", { id: "t1", name: "publish_artifact" }));
  const [failed] = to(ev("action.taken", { name: "publish_artifact", summary: "report.md", error: "platform: storage limit reached: the workspace is full" }));
  assert.equal(failed?.type, "attachment.upserted");
  if (failed?.type === "attachment.upserted" && failed.attachment.type === "artifact") {
    assert.equal(failed.attachment.id, "t1");
    assert.equal(failed.attachment.phase, "failed");
    assert.match(failed.attachment.error ?? "", /storage limit/);
  }
  // Another connector's action is not a file.
  assert.deepEqual(to(ev("action.taken", { name: "jira_transition" })), []);
});

test("a call to another agent learns which run it started", () => {
  const to = adapter();
  to(ev("delta.tool.call", { id: "t1", name: "tag_helper" }));
  const [named] = to(ev("action.taken", { name: "tag_helper", data: { run: "run-9", agent: "helper", lifecycle: "attached" } }));
  assert.equal(named?.type, "task.upserted");
  if (named?.type === "task.upserted") {
    assert.equal(named.task.id, "t1");
    assert.equal(named.task.subagent?.runId, "run-9");
    assert.equal(named.task.status, "in_progress");
  }
  // The run is still known when the answer comes back.
  const done = to(ev("delta.tool.done", { id: "t1", result: "Three tests fail." })).find((e) => e.type === "task.upserted");
  if (done?.type === "task.upserted") {
    assert.equal(done.task.status, "complete");
    assert.equal(done.task.subagent?.runId, "run-9");
    assert.equal(done.task.subagent?.summary, "Three tests fail.");
  }
});

test("work handed on without waiting is drawn as the agent it went to", () => {
  const to = adapter();
  const first = to(ev("delta.tool.call", { id: "t1", name: "delegate_task" })).find((e) => e.type === "task.upserted");
  assert.equal(first?.type === "task.upserted" && first.task.subagent, undefined);
  const [named] = to(ev("action.taken", { name: "delegate_task", data: { run: "run-4", agent: "worker", lifecycle: "delegated" } }));
  if (named?.type === "task.upserted") {
    assert.equal(named.task.title, "worker");
    assert.deepEqual(named.task.subagent, { name: "worker", runId: "run-4", summary: undefined, hue: undefined });
  } else {
    assert.fail("no task for the run that was started");
  }
});

test("a surface that is not the platform's own origin places file links itself", () => {
  const published = ev("action.taken", { name: "publish_artifact", data: { id: "art-7", title: "report.md", kind: "document", mime: "text/markdown", size: 19, version: 1 } });
  const [abs] = adapter({ href: (path) => `https://collegium.example.com${path}` })(published);
  assert.equal(abs?.type === "attachment.upserted" && abs.attachment.type === "artifact" && abs.attachment.downloadUrl, "https://collegium.example.com/api/artifacts/art-7/content");
  const [none] = adapter({ href: () => undefined })(published);
  assert.equal(none?.type === "attachment.upserted" && none.attachment.type === "artifact" && none.attachment.url, undefined);
  assert.equal(none?.type === "attachment.upserted" && none.attachment.type === "artifact" && none.attachment.phase, "ready");
});

test("a stopped event ends the turn as stopped, confirmed", () => {
  const to = adapter();
  assert.deepEqual(to(ev("lifecycle.stopped", { cause: "Stopped by dana" })), [
    { type: "turn.stopped" },
    { type: "stop.updated", stop: "confirmed" },
  ]);
});
