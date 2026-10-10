import assert from "node:assert/strict";
import { test } from "node:test";
import { sampleTrees } from "./activity.fixtures.ts";
import type { Run } from "./adapter.ts";
import {
  COPY_LIMIT,
  fileName,
  formats,
  type Item,
  plain,
  redact,
  runManifest,
  runMarkdown,
  statusWord,
  type ToolCall,
  toolCallText,
  toolMarkdown,
  toolOf,
  toolResultText,
  variantOf,
} from "./copyas.ts";

const tree = () => {
  const t = sampleTrees()[1];
  assert.ok(t);
  return t;
};
const runOf = (id: string): Run => {
  const r = tree().runs.find((x) => x.id === id);
  assert.ok(r, id);
  return r;
};
const ids = (item: Item) => formats(item).map((f) => f.id);
const text = (item: Item, id: string) => {
  const f = formats(item).find((x) => x.id === id);
  assert.ok(f, id);
  return f.text();
};

test("statusWord: every state has specific words", () => {
  const r = runOf("r2");
  assert.equal(statusWord({ ...r, status: "accepted" }), "Queued");
  assert.equal(statusWord({ ...r, status: "running", calls: 0 }), "Running");
  assert.equal(statusWord({ ...r, status: "running", calls: 1 }), "Running, calling others");
  assert.equal(statusWord({ ...r, status: "parked" }), "Waiting on you");
  assert.equal(statusWord({ ...r, status: "parked", reason: "a reviewer" }), "Waiting: a reviewer");
  assert.equal(statusWord({ ...r, status: "failed" }), "Failed");
  assert.equal(statusWord({ ...r, status: "completed" }), "Done");
});

test("redact: anything under a secret-looking key is replaced, at any depth", () => {
  const out = redact({ url: "https://x", headers: { Authorization: "Bearer abc", accept: "json" }, items: [{ api_key: "k", name: "n" }], token: null });
  assert.deepEqual(out, { url: "https://x", headers: { Authorization: "[redacted]", accept: "json" }, items: [{ api_key: "[redacted]", name: "n" }], token: null });
});

test("plain: Markdown with its marks taken off", () => {
  assert.equal(plain("# lead\n\n**Asked**\n\n> hello\n\n- `git_tag`, 3s\n"), "lead\n\nAsked\n\nhello\n\n- git_tag, 3s\n");
});

test("run: Markdown, plain text and the JSON manifest", () => {
  const item: Item = { kind: "run", run: runOf("r2"), data: tree() };
  assert.deepEqual(ids(item), ["markdown", "text", "json"]);
  assert.equal(text(item, "markdown"), runMarkdown(runOf("r2"), tree()), "copy and export share one serialiser");
  assert.match(text(item, "markdown"), /^# lead\n/);
  assert.doesNotMatch(text(item, "text"), /^#|\*\*|`/m);
  const manifest = JSON.parse(text(item, "json"));
  assert.equal(manifest.manifest, 1);
  assert.equal(manifest.runs.length, 4);
  assert.deepEqual(manifest.agents.find((a: { name: string }) => a.name === "releaser"), { name: "releaser", revision: 1 });
  assert.equal(runManifest(runOf("r2"), { runs: [], spans: [] }).runs.length, 1, "with no tree, the run itself is still there");
});

test("agent call: what it was asked, what it answered, and its whole conversation", () => {
  const done: Item = { kind: "call", run: runOf("r2b"), data: { runs: [runOf("r2b")], spans: tree().spans } };
  assert.deepEqual(ids(done), ["asked", "answered", "markdown", "text", "json"]);
  assert.equal(text(done, "asked"), "Tell the on-call that 2.4 is delayed\n");
  assert.equal(text(done, "answered"), "Posted in #incidents.\n");
  assert.match(text(done, "markdown"), /^# oncall\n/);
  assert.match(text(done, "markdown"), /- `post_message`, 10s/);
  assert.equal(variantOf(runOf("r2b")), "oncall, revision 1, called as handoff");
});

test("agent call that failed: no answer to copy, but why it failed", () => {
  const failed: Item = { kind: "call", run: runOf("r2a"), data: { runs: [runOf("r2a"), runOf("r2a1")], spans: tree().spans } };
  assert.deepEqual(ids(failed), ["asked", "failure", "markdown", "text", "json"]);
  assert.equal(text(failed, "failure"), "git: tag 'v2.4.0' already exists\n");
  assert.match(text(failed, "markdown"), /\n## helper \(delegate\)\n/, "the agents it called are in its conversation");
});

test("answer: the answer as written, plain, and the run's record", () => {
  const item: Item = { kind: "answer", run: runOf("r2") };
  assert.deepEqual(ids(item), ["markdown", "text", "json"]);
  assert.equal(text(item, "markdown"), "2.4 could not be tagged. The on-call has been told.\n");
  const record = JSON.parse(text(item, "json"));
  assert.equal(record.agent, "lead");
  assert.equal(record.revision, 1);
  assert.equal(record.status, "completed");
  assert.deepEqual(ids({ kind: "answer", run: { ...runOf("r2"), answer: undefined } }), ["json"], "with no answer yet, only the record");
});

test("tool call with arguments and a result: every variation", () => {
  const tool: ToolCall = {
    name: "http_get",
    agent: "lead",
    args: { url: "https://example.com/api", headers: { authorization: "Bearer abc" } },
    result: '{"ok":true}',
    start: "2026-10-09T10:00:00.000Z",
    end: "2026-10-09T10:00:02.500Z",
  };
  const item: Item = { kind: "tool", tool };
  assert.deepEqual(ids(item), ["call", "result", "both", "args", "json"]);
  assert.match(text(item, "call"), /^http_get\(\{\n {2}"url": "https:\/\/example\.com\/api"/);
  assert.equal(text(item, "result"), '{"ok":true}\n');
  assert.deepEqual(JSON.parse(text(item, "args")), { url: "https://example.com/api", headers: { authorization: "[redacted]" } });
  const both = text(item, "both");
  assert.match(both, /\*\*Tool\*\* `http_get`, called by lead/);
  assert.match(both, /- Took: 2\.5s/);
  assert.match(both, /```json\n\{/);
  assert.match(both, /\*\*Result\*\*\n\n\{"ok":true\}/);
  for (const id of ["call", "both", "args", "json"]) assert.doesNotMatch(text(item, id), /Bearer abc/, `${id} keeps the secret out`);
});

test("tool call with no result yet: the call can be copied, and the result says it is still running", () => {
  const tool = toolOf({ run_id: "r3a", agent: "oncall", kind: "tool", name: "search_incidents", start: "2026-10-09T10:00:00Z" });
  const item: Item = { kind: "tool", tool };
  assert.deepEqual(ids(item), ["call", "both", "json"], "no result and no arguments, so neither is offered");
  assert.equal(toolCallText(tool), "search_incidents()");
  assert.equal(toolResultText(tool), "No result yet. The call is still running.");
  assert.match(toolMarkdown(tool), /- Status: Running/);
});

test("tool call that failed: the result is the failure", () => {
  const span = tree().spans.find((s) => s.name === "git_tag");
  assert.ok(span);
  const item: Item = { kind: "tool", tool: toolOf(span) };
  assert.deepEqual(ids(item), ["call", "result", "both", "json"]);
  assert.equal(text(item, "result"), "Failed: git: tag 'v2.4.0' already exists\n");
  assert.match(text(item, "both"), /- Status: Failed/);
  assert.equal(JSON.parse(text(item, "json")).run, "r2a");
});

test("tool call that returned nothing says so", () => {
  assert.equal(toolResultText({ name: "noop", result: "", end: "2026-10-09T10:00:01Z" }), "It returned nothing.");
});

test("fileName: named for the item and the format", () => {
  const run: Item = { kind: "run", run: runOf("r2"), data: tree() };
  const [markdown, , json] = formats(run);
  assert.ok(markdown && json);
  assert.equal(fileName(run, markdown), "run-r2.md");
  assert.equal(fileName(run, json), "run-r2.json");
  const tool: Item = { kind: "tool", tool: { name: "git tag/v2", result: "ok" } };
  const result = formats(tool).find((f) => f.id === "result");
  assert.ok(result);
  assert.equal(fileName(tool, result), "tool-git-tag-v2-result.txt");
  assert.ok(COPY_LIMIT >= 100_000);
});

test("every format of every item ends with one newline", () => {
  const items: Item[] = [
    { kind: "run", run: runOf("r2"), data: tree() },
    { kind: "call", run: runOf("r2a"), data: tree() },
    { kind: "answer", run: runOf("r2") },
    { kind: "tool", tool: { name: "t", args: { a: 1 }, result: "r", end: "2026-10-09T10:00:01Z" } },
  ];
  for (const item of items) for (const f of formats(item)) assert.match(f.text(), /[^\n]\n$/, `${item.kind}:${f.id}`);
});
