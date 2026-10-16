// The generated client and the layer over it, run against a small HTTP server
// in this process. It proves how requests are built, how failures surface,
// which credentials are sent, and that the event stream parses.
import assert from "node:assert/strict";
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";
import { after, before, test } from "node:test";

import {
  ApiError,
  createClient,
  failureOf,
  isRunCompletedEvent,
  SSEError,
  StreamEndedError,
  type DecodedRunEvent,
  type Run,
  type RunEvent,
  type StreamItem,
} from "../dist/index.js";

interface Seen {
  method: string;
  url: string;
  headers: IncomingMessage["headers"];
  body: string;
}

type Handler = (req: Seen, res: ServerResponse) => void | Promise<void>;

let handler: Handler = (_req, res) => void res.writeHead(500).end();
const seen: Seen[] = [];
let base = "";

const server = createServer((req, res) => {
  const chunks: Buffer[] = [];
  req.on("data", (c: Buffer) => chunks.push(c));
  req.on("end", () => {
    const got: Seen = { method: req.method ?? "", url: req.url ?? "", headers: req.headers, body: Buffer.concat(chunks).toString("utf8") };
    seen.push(got);
    void handler(got, res);
  });
});

before(async () => {
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});

after(() => {
  server.closeAllConnections();
  server.close();
});

function serve(next: Handler): void {
  seen.length = 0;
  handler = next;
}

function json(res: ServerResponse, status: number, body: unknown): void {
  res.writeHead(status, { "Content-Type": "application/json" }).end(JSON.stringify(body));
}

function last(): Seen {
  const got = seen.at(-1);
  assert.ok(got, "the server saw no request");
  return got;
}

const run: Run = {
  id: "r31d24839ddee5733b221391dfb156ce0",
  tag_id: "api-8-svc-acme-slack-evt-1",
  tenant: "acme",
  agent: "helper",
  rev: 1,
  origin: "api",
  text: "Say hello",
  root: "r31d24839ddee5733b221391dfb156ce0",
  depth: 0,
  thread: "r31d24839ddee5733b221391dfb156ce0",
  lease: 0,
  granted: 0,
  spent: 0,
  calls: 0,
  status: "accepted",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

function wire(seq: number, kind: RunEvent["kind"], body?: unknown): RunEvent {
  return {
    seq,
    topic: "agent:helper:r1",
    run_id: "r1",
    tenant: "acme",
    agent: "helper",
    rev: 1,
    origin: "api",
    at: "2026-01-01T00:00:00Z",
    kind,
    ...(body === undefined ? {} : { payload: Buffer.from(JSON.stringify(body), "utf8").toString("base64") }),
  } as RunEvent;
}

test("a run is started with a JSON body, the idempotency key and the bearer token", async () => {
  serve((_req, res) => json(res, 202, run));
  const client = createClient({ baseUrl: `${base}/`, token: "machine-token", headers: { "X-Surface": "slack" } });

  const started = await client.createRun({ agent: "helper", text: "Say hello", after: "t0" }, "slack-evt-1");

  assert.equal(started.id, run.id);
  const req = last();
  assert.equal(`${req.method} ${req.url}`, "POST /api/runs");
  assert.equal(req.headers.authorization, "Bearer machine-token");
  assert.equal(req.headers["idempotency-key"], "slack-evt-1");
  assert.equal(req.headers["content-type"], "application/json");
  assert.equal(req.headers["x-surface"], "slack");
  assert.deepEqual(JSON.parse(req.body), { agent: "helper", text: "Say hello", after: "t0" });
});

test("path parameters are escaped and query parameters are sent only when given", async () => {
  serve((_req, res) => json(res, 200, { messages: [], next: null, open: 0, unread: 0 }));
  const client = createClient({ baseUrl: base });

  await client.listInbox("open", "failure", undefined, undefined, 20);
  assert.equal(last().url, "/api/inbox?state=open&kind=failure&limit=20");

  await client.resolveInboxPart("m/1", "p 2", { action: "retry" });
  assert.equal(last().url, "/api/inbox/m%2F1/parts/p%202/resolve");
  assert.deepEqual(JSON.parse(last().body), { action: "retry" });

  serve((_req, res) => json(res, 200, run));
  await client.stopRun("r1");
  assert.equal(`${last().method} ${last().url}`, "POST /api/runs/r1/stop");
  assert.equal(last().body, "");
});

test("a response with no content resolves to nothing", async () => {
  serve((_req, res) => void res.writeHead(204).end());
  const client = createClient({ baseUrl: base });
  assert.equal(await client.deleteSchedule("nightly"), undefined);
  assert.equal(`${last().method} ${last().url}`, "DELETE /api/schedules/nightly");
});

test("a failure carries its status and the API's error body", async () => {
  serve((_req, res) => json(res, 404, { error: "not found" }));
  const client = createClient({ baseUrl: base });

  const error = await client.getRun("nope").then(
    () => assert.fail("a 404 resolved"),
    (e: unknown) => e,
  );
  assert.ok(error instanceof ApiError);
  assert.equal(error.statusCode, 404);
  assert.deepEqual(error.body, { error: "not found" });
  const failure = failureOf(error);
  assert.deepEqual(failure, { status: 404, message: "not found", body: { error: "not found" }, refusal: undefined });
  assert.equal(failureOf(new Error("offline")), undefined);
});

test("a limit's refusal is typed, and is not retried", async () => {
  const refusal = {
    error: "the project has used its 1 runs for the day",
    limit: "runs",
    remaining: 0,
    policy: { scope: { kind: "project", name: "Launch" }, version: 1, period: "day" },
    resets_at: "2026-01-02T00:00:00Z",
  };
  serve((_req, res) => json(res, 429, refusal));
  const client = createClient({ baseUrl: base, token: "t" });

  const failure = failureOf(await client.createRun({ text: "go" }).catch((e: unknown) => e));
  assert.equal(failure?.status, 429);
  assert.equal(failure?.refusal?.limit, "runs");
  assert.equal(failure?.refusal?.policy?.scope.name, "Launch");
  assert.equal(failure?.refusal?.resets_at, "2026-01-02T00:00:00Z");
  assert.equal(seen.length, 1, "the client retried a refused run");
});

test("retries are opt-in", async () => {
  let calls = 0;
  serve((_req, res) => (++calls < 2 ? json(res, 503, { error: "busy" }) : json(res, 200, run)));
  const client = createClient({ baseUrl: base, retry: { initialDelayMs: 1, maxRetries: 2 } });
  assert.equal((await client.getRun("r1")).id, run.id);
  assert.equal(calls, 2);
});

test("a token function is asked before every request, and a cookie session sends no token", async () => {
  serve((_req, res) => json(res, 200, run));
  let issued = 0;
  const rotating = createClient({ baseUrl: base, token: async () => `token-${++issued}` });
  await rotating.getRun("r1");
  await rotating.getRun("r1");
  assert.equal(last().headers.authorization, "Bearer token-2");

  const inits: RequestInit[] = [];
  const browser = createClient({
    baseUrl: base,
    credentials: "include",
    fetch: (input, init) => {
      inits.push(init ?? {});
      return fetch(input, init);
    },
  });
  await browser.getRun("r1");
  for await (const _ of browser.events({}, { signal: AbortSignal.timeout(50) })) break;
  assert.equal(last().headers.authorization, undefined);
  assert.deepEqual(inits.map((i) => i.credentials), ["include", "include"]);
});

test("a request is cancelled by its signal", async () => {
  serve(() => {
    // Never answers.
  });
  const client = createClient({ baseUrl: base });
  const controller = new AbortController();
  const pending = client.getRun("r1", { signal: controller.signal });
  setTimeout(() => controller.abort(), 20);
  await assert.rejects(pending, { name: "AbortError" });

  await assert.rejects(client.getRun("r1", { timeout: 20 }), { name: "TimeoutError" });

  // A download honours the client's own timeout too.
  const patient = createClient({ baseUrl: base, timeout: 20 });
  await assert.rejects(patient.getArtifactContent("a1"), { name: "TimeoutError" });
});

test("the event stream yields decoded run events and inbox notices", async () => {
  const notice = { id: "m1", kind: "failure", state: "open", updated_at: "2026-01-01T00:00:00Z" };
  const text = wire(2, "delta.text", { text: "Héllo ✓" });
  const frames =
    ": keep-alive\n\n" +
    `data: ${JSON.stringify(wire(1, "lifecycle.accepted", { rev: 1 }))}\n\n` +
    `data: ${JSON.stringify(text)}\n\n` +
    `event: inbox\ndata: ${JSON.stringify(notice)}\n\n` +
    `data: ${JSON.stringify(wire(3, "delta.tool.call", { id: "c1", name: "publish_artifact", phase: "executing" }))}\n\n` +
    `data: ${JSON.stringify(wire(4, "action.taken", { name: "publish_artifact", summary: "Published notes.txt, version 1", data: { id: "a1" } }))}\n\n` +
    `data: ${JSON.stringify(wire(5, "lifecycle.started"))}\n\n` +
    "data: not json\n\n" +
    `data: ${JSON.stringify(wire(6, "lifecycle.completed", { text: "Héllo ✓" }))}\n\n`;
  serve(async (_req, res) => {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    // Written in pieces that split lines and a multi-byte character, the
    // way a network does.
    const bytes = Buffer.from(frames, "utf8");
    for (let i = 0; i < bytes.length; i += 37) {
      res.write(bytes.subarray(i, i + 37));
      await new Promise((r) => setImmediate(r));
    }
    res.end();
  });
  const client = createClient({ baseUrl: base, token: "machine-token" });

  const items: StreamItem[] = [];
  for await (const item of client.events({ topic: "agent:helper:r1", from: 7 })) items.push(item);

  const req = last();
  assert.equal(req.url, "/api/events?topic=agent%3Ahelper%3Ar1&from=7");
  assert.equal(req.headers.accept, "text/event-stream");
  assert.equal(req.headers.authorization, "Bearer machine-token");
  assert.deepEqual(
    items.map((i) => (i.type === "event" ? i.event.kind : `inbox:${i.notice.id}`)),
    ["lifecycle.accepted", "delta.text", "inbox:m1", "delta.tool.call", "action.taken", "lifecycle.started", "lifecycle.completed"],
  );
  const events = items.flatMap((i) => (i.type === "event" ? [i.event] : []));
  const said = events.find((e) => e.kind === "delta.text");
  assert.equal(said?.kind === "delta.text" && said.body?.text, "Héllo ✓");
  assert.equal("payload" in (said ?? {}), false);
  const acted = events.find((e) => e.kind === "action.taken");
  assert.deepEqual(acted?.kind === "action.taken" && acted.body?.data, { id: "a1" });
  assert.equal(events.find((e) => e.kind === "lifecycle.started")?.body, undefined);
  assert.equal(isRunCompletedEvent(wire(6, "lifecycle.completed")), true);
});

test("a run's events end at the event that completes it", async () => {
  serve((_req, res) => {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write(`data: ${JSON.stringify(wire(1, "lifecycle.failed", { error: "flaky", attempt: 1 }))}\n\n`);
    res.write(`data: ${JSON.stringify({ ...wire(1, "delta.text", { text: "other run" }), run_id: "r2" })}\n\n`);
    res.write(`data: ${JSON.stringify(wire(2, "lifecycle.completed", { text: "done" }))}\n\n`);
    // The server keeps the stream open. The reader is the one who leaves.
  });
  const client = createClient({ baseUrl: base });

  const kinds: DecodedRunEvent["kind"][] = [];
  for await (const event of client.runEvents({ agent: "helper", id: "r1" })) kinds.push(event.kind);

  assert.deepEqual(kinds, ["lifecycle.failed", "lifecycle.completed"]);
  assert.equal(last().url, "/api/events?topic=agent%3Ahelper%3Ar1");
});

test("a run's events end at the event that stops it, with its partial answer", async () => {
  serve((_req, res) => {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write(`data: ${JSON.stringify(wire(1, "delta.text", { text: "Half" }))}\n\n`);
    res.write(`data: ${JSON.stringify(wire(2, "lifecycle.stopped", { text: "Half", reason: "Stopped by ada", by: "ada" }))}\n\n`);
    // The server keeps the stream open, with heartbeats. The reader leaves.
  });
  const client = createClient({ baseUrl: base });

  const seen: DecodedRunEvent[] = [];
  for await (const event of client.runEvents({ agent: "helper", id: "r1" })) seen.push(event);

  assert.deepEqual(seen.map((e) => e.kind), ["delta.text", "lifecycle.stopped"]);
  const stopped = seen[1];
  assert.equal(stopped?.kind === "lifecycle.stopped" && stopped.body?.text, "Half");
});

test("a stream is cancelled by its signal, and a refused stream throws with the body", async () => {
  serve((_req, res) => {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.write(`data: ${JSON.stringify(wire(1, "lifecycle.accepted"))}\n\n`);
  });
  const client = createClient({ baseUrl: base });
  const controller = new AbortController();
  let count = 0;
  await assert.rejects(async () => {
    for await (const _ of client.events({}, { signal: controller.signal })) {
      count++;
      controller.abort();
    }
  }, { name: "AbortError" });
  assert.equal(count, 1);

  serve((_req, res) => json(res, 403, { error: "you need runs:read" }));
  const error = await (async () => {
    for await (const _ of client.events()) assert.fail("a refused stream yielded");
  })().catch((e: unknown) => e);
  assert.ok(error instanceof SSEError);
  assert.deepEqual(failureOf(error), { status: 403, message: "you need runs:read", body: { error: "you need runs:read" }, refusal: undefined });
  assert.equal(seen.length, 1, "the client retried a refused stream");
});

test("a download is the file's own bytes", async () => {
  const bytes = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x00, 0xff, 0xfe, 0x0a]);
  serve((_req, res) => void res.writeHead(200, { "Content-Type": "application/octet-stream", ETag: '"abc"' }).end(bytes));
  const client = createClient({ baseUrl: base, token: "machine-token" });

  const blob = await client.getArtifactContent("a/1", "1");
  assert.deepEqual(Buffer.from(await blob.arrayBuffer()), bytes);
  assert.equal(last().url, "/api/artifacts/a%2F1/content?inline=1");
  assert.equal(last().headers.authorization, "Bearer machine-token");
  assert.equal((await client.getArtifactContentRaw("a1")).headers.get("etag"), '"abc"');
  assert.equal(last().url, "/api/artifacts/a1/content");

  serve((_req, res) => json(res, 404, { error: "not found" }));
  assert.equal(failureOf(await client.getPreferencesPhoto().catch((e: unknown) => e))?.status, 404);
});

// Opens a stream that sends the given frames and then stays open, and
// resolves once the server sees the client close it.
function holdOpen(frames: string): Promise<void> {
  return new Promise((closed) => {
    serve((_req, res) => {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write(frames);
      res.on("close", () => closed());
    });
  });
}

function within(ms: number, p: Promise<void>, what: string): Promise<void> {
  return Promise.race([p, new Promise<void>((_, reject) => setTimeout(() => reject(new Error(`${what}: still open after ${ms} ms`)), ms))]);
}

test("leaving the loop closes the connection, however it is left", async () => {
  const client = createClient({ baseUrl: base });
  const frames = `data: ${JSON.stringify(wire(1, "lifecycle.accepted"))}\n\ndata: ${JSON.stringify(wire(2, "lifecycle.completed", { text: "done" }))}\n\n`;

  let closed = holdOpen(frames);
  for await (const _ of client.events()) break;
  await within(500, closed, "break");

  closed = holdOpen(frames);
  const kinds: string[] = [];
  for await (const event of client.runEvents({ agent: "helper", id: "r1" })) kinds.push(event.kind);
  assert.deepEqual(kinds, ["lifecycle.accepted", "lifecycle.completed"]);
  await within(500, closed, "return after the ending event");

  closed = holdOpen(frames);
  await assert.rejects(async () => {
    for await (const _ of client.events()) throw new Error("the reader failed");
  }, /the reader failed/);
  await within(500, closed, "throw");

  // The server ends it, the normal way.
  serve((_req, res) => {
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.end(frames);
  });
  const items: StreamItem[] = [];
  for await (const item of client.events()) items.push(item);
  assert.equal(items.length, 2);
});

// A fake that serves a run's stream in pieces, one per connection, and its
// record from a list of statuses, one per read.
function flaky(streams: string[], statuses: Run["status"][]): { urls: string[] } {
  const urls: string[] = [];
  serve((req, res) => {
    if (req.url.startsWith("/api/runs/r1")) {
      json(res, 200, { ...run, id: "r1", status: statuses.shift() ?? "running" });
      return;
    }
    urls.push(req.url);
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    res.end(streams.shift() ?? "");
  });
  return { urls };
}

const frame = (e: RunEvent) => `data: ${JSON.stringify(e)}\n\n`;
const fast = { maxRetries: 2, initialDelayMs: 1, maxDelayMs: 5 };

test("a stream that drops mid-run is resumed after the last event", async () => {
  const fake = flaky(
    [frame(wire(1, "lifecycle.accepted")) + frame(wire(2, "delta.text", { text: "Hel" })), frame(wire(2, "delta.text", { text: "Hel" })) + frame(wire(3, "delta.text", { text: "lo" })) + frame(wire(4, "lifecycle.completed", { text: "Hello" }))],
    ["running"],
  );
  const client = createClient({ baseUrl: base });
  const seqs: number[] = [];
  for await (const event of client.runEvents({ agent: "helper", id: "r1" }, { resume: fast })) seqs.push(event.seq);
  assert.deepEqual(seqs, [1, 2, 3, 4], "an event was lost or repeated");
  assert.deepEqual(fake.urls, ["/api/events?topic=agent%3Ahelper%3Ar1", "/api/events?topic=agent%3Ahelper%3Ar1&from=2"]);
});

test("a run that ended while the stream was down is read to its end", async () => {
  flaky([frame(wire(1, "lifecycle.accepted")), frame(wire(1, "lifecycle.accepted")) + frame(wire(2, "lifecycle.completed", { text: "done" }))], ["completed"]);
  const client = createClient({ baseUrl: base });
  const kinds: string[] = [];
  for await (const event of client.runEvents({ agent: "helper", id: "r1" }, { resume: fast })) kinds.push(event.kind);
  assert.deepEqual(kinds, ["lifecycle.accepted", "lifecycle.completed"]);
});

test("a last read of an ended run does not wait forever for an event the server no longer holds", async () => {
  // The first stream drops; the record says stopped; the second stream stays open with nothing on it,
  // as the server's does once the run's events have left its retention.
  let streams = 0;
  serve((req, res) => {
    if (req.url.startsWith("/api/runs/r1")) {
      json(res, 200, { ...run, id: "r1", status: "stopped" });
      return;
    }
    res.writeHead(200, { "Content-Type": "text/event-stream" });
    if (streams++ === 0) res.end(frame(wire(1, "delta.text", { text: "Hel" })));
    else res.write(": keep-alive\n\n");
  });
  const client = createClient({ baseUrl: base });
  const kinds: string[] = [];
  const began = Date.now();
  const error = await (async () => {
    for await (const event of client.runEvents({ agent: "helper", id: "r1" }, { resume: { ...fast, drainMs: 100 } })) {
      kinds.push(event.kind);
    }
  })().then(
    () => assert.fail("returned without an ending event"),
    (e: unknown) => e,
  );
  assert.ok(error instanceof StreamEndedError, String(error));
  assert.equal(error.reason, "stopped");
  assert.deepEqual(kinds, ["delta.text"]);
  assert.ok(Date.now() - began < 5_000, "the drain was bounded");
});

test("a stream that cannot be resumed throws rather than ending quietly", async () => {
  const client = createClient({ baseUrl: base });
  const follow = async (resume = fast) => {
    const kinds: string[] = [];
    const error = await (async () => {
      for await (const event of client.runEvents({ agent: "helper", id: "r1" }, { resume })) kinds.push(event.kind);
    })().then(
      () => assert.fail(`returned without an ending event after ${kinds.join(", ")}`),
      (e: unknown) => e,
    );
    assert.ok(error instanceof StreamEndedError, String(error));
    return { error, kinds };
  };

  flaky([frame(wire(1, "delta.text", { text: "Hel" }))], ["running", "running", "running"]);
  const gaveUp = await follow();
  assert.equal(gaveUp.error.reason, "gave-up");
  assert.deepEqual(gaveUp.kinds, ["delta.text"]);
  assert.equal(seen.filter((r) => r.url.startsWith("/api/events")).length, 3, "it did not try exactly twice more");

  flaky([frame(wire(1, "delta.text", { text: "Hel" }))], ["stopped"]);
  const stopped = await follow();
  assert.equal(stopped.error.reason, "stopped");
  assert.equal(stopped.error.run?.status, "stopped");

  flaky([frame(wire(1, "delta.text"))], ["completed"]);
  assert.equal((await follow()).error.reason, "ended");

  // A refusal is the server's answer, not a dropped stream.
  serve((_req, res) => json(res, 404, { error: "not found" }));
  await assert.rejects(async () => {
    for await (const _ of client.runEvents({ agent: "helper", id: "r1" }, { resume: fast })) assert.fail("yielded");
  }, (e: unknown) => failureOf(e)?.status === 404);
  assert.equal(seen.length, 1);

  // Aborting while waiting to reconnect ends it with the abort.
  flaky([], ["running", "running", "running"]);
  const controller = new AbortController();
  setTimeout(() => controller.abort(), 30);
  await assert.rejects(async () => {
    for await (const _ of client.runEvents({ agent: "helper", id: "r1" }, { resume: { initialDelayMs: 1000 } }, { signal: controller.signal })) assert.fail("yielded");
  }, { name: "AbortError" });
});
