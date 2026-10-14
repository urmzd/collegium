import assert from "node:assert/strict";
import { test } from "node:test";
import { choose, mayRoute } from "./agents.ts";
import {
  type ClientRecord,
  type CollegiumError,
  clientCredentials,
  frames,
  httpCollegium,
  staticToken,
} from "./collegium.ts";
import { readConfig } from "./config.ts";
import { eventKey, runId, sessionOf, threadKey } from "./keys.ts";

const session = { team: "T01ABCDEF", channel: "C01ABCDEF", threadTs: "1700000000.000100" };

test("a thread names its conversation, and the name leads back to the thread", () => {
  const key = threadKey(session);
  assert.equal(key, "slack-T01ABCDEF-C01ABCDEF-1700000000-000100");
  assert.match(key, /^[A-Za-z0-9_-]+$/, "a run id is a bus topic segment");
  assert.deepEqual(sessionOf("svc-slack", runId("svc-slack", key)), session);
});

test("a conversation this app did not name is not taken for one of its threads", () => {
  assert.equal(sessionOf("svc-slack", "api-dana-01J8ZK"), null);
  assert.equal(sessionOf("svc-slack", runId("someone-else", threadKey(session))), null);
  assert.equal(sessionOf("svc-slack", "api-svc-slack-slack-T01ABCDEF-C01ABCDEF-17-1-extra"), null);
});

test("an event's key is its message, or its message and the trigger that asked about it", () => {
  assert.equal(
    eventKey(session, "1700000050.000200"),
    "slack-T01ABCDEF-C01ABCDEF-1700000050-000200",
  );
  assert.equal(
    eventKey(session, session.threadTs),
    threadKey(session),
    "a thread's first message is the thread",
  );
  assert.equal(
    eventKey(session, "1700000050.000200", "12.34.abc"),
    "slack-T01ABCDEF-C01ABCDEF-1700000050-000200-12-34-abc",
  );
});

const agents = [{ name: "lead" }, { name: "helper" }, { name: "docs-bot" }];
const client = (over: Partial<ClientRecord>): ClientRecord => ({
  id: "c1",
  surface: "slack",
  enabled: true,
  ...over,
});

test("choose: a named agent, then the default, then the picker", () => {
  assert.deepEqual(choose("@helper look at this", agents, { defaultAgent: "lead" }), {
    agent: "helper",
    text: "look at this",
    how: "named",
  });
  assert.deepEqual(choose("ask @docs-bot, please", agents, {}), {
    agent: "docs-bot",
    text: "ask , please",
    how: "named",
  });
  assert.deepEqual(choose("hello", agents, { defaultAgent: "lead" }), {
    agent: "lead",
    text: "hello",
    how: "default",
  });
  assert.deepEqual(choose("hello", agents, {}), { agent: "", text: "hello", how: "picker" });
  // An address is not a name, and neither is a word that is no agent.
  assert.equal(choose("mail dana@helper.example", agents, {}).how, "picker");
  assert.equal(choose("@someone hello", agents, {}).text, "@someone hello");
  // Naming an agent and saying nothing else still sends the words.
  assert.equal(choose("@helper", agents, {}).text, "@helper");
});

test("choose: a client's list is default deny", () => {
  const listed = { client: client({ default_agent: "docs-bot", agents: ["helper"] }) };
  assert.equal(choose("@helper hi", agents, listed).agent, "helper");
  assert.equal(
    choose("@lead hi", agents, listed).agent,
    "docs-bot",
    "a name outside the list is not an agent",
  );
  assert.equal(
    choose("hi", agents, { client: client({}) }).agent,
    null,
    "no default and no list routes to nothing",
  );
  assert.equal(
    choose("hi", agents, { client: client({ agents: ["helper"] }) }).agent,
    null,
    "the picker does not know the list",
  );
  assert.equal(choose("hi", agents, { client: client({ agents: ["*"] }) }).how, "picker");
  assert.equal(
    choose("@helper hi", agents, { client: client({ enabled: false, agents: ["*"] }) }).agent,
    null,
  );
  assert.equal(mayRoute({ client: client({ agents: ["*"] }) }, "anything"), true);
  assert.equal(mayRoute({}, "anything"), true);
});

test("config: every missing name is reported at once, and no value is echoed", () => {
  assert.throws(
    () => readConfig({ SLACK_BOT_TOKEN: "not-a-real-token" }),
    (e: Error) =>
      /SLACK_APP_TOKEN, SLACK_TEAM_ID, COLLEGIUM_TENANT/.test(e.message) &&
      !e.message.includes("not-a-real-token"),
  );
  const cfg = readConfig({
    SLACK_BOT_TOKEN: "b",
    SLACK_APP_TOKEN: "a",
    SLACK_TEAM_ID: "T01ABCDEF",
    SLACK_OPERATORS: "U01ABCDEF, U02ABCDEF",
    COLLEGIUM_TENANT: "acme",
    COLLEGIUM_PUBLIC_URL: "https://collegium.example.com/",
  });
  assert.equal(cfg.collegium.url, "http://127.0.0.1:8080");
  assert.equal(cfg.collegium.publicUrl, "https://collegium.example.com");
  assert.deepEqual(cfg.slack.operators, ["U01ABCDEF", "U02ABCDEF"]);
});

const stream = (...chunks: string[]) =>
  new ReadableStream<Uint8Array>({
    start(c) {
      for (const chunk of chunks) c.enqueue(new TextEncoder().encode(chunk));
      c.close();
    },
  });

test("frames: split anywhere, named events, comments and multi-line data", async () => {
  const out = [];
  const body = stream(
    ": keep-alive\n\nda",
    'ta: {"seq":1}\n\nevent: inbox\ndata: {"id":"m1"}\n',
    "\ndata: a\ndata: b\r\n\r\n",
    "data: lost",
  );
  for await (const f of frames(body)) out.push(f);
  assert.deepEqual(out, [
    { event: "message", data: '{"seq":1}' },
    { event: "inbox", data: '{"id":"m1"}' },
    { event: "message", data: "a\nb" },
  ]);
});

/** A fetch that answers from a list and remembers what it was asked. */
function fakeFetch(answers: (Response | (() => Response))[]) {
  const asked: { url: string; init: RequestInit }[] = [];
  const fn = (async (url: string | URL | Request, init: RequestInit = {}) => {
    asked.push({ url: String(url), init });
    const next = answers.shift();
    if (!next) throw new Error(`unexpected request to ${url}`);
    return typeof next === "function" ? next() : next;
  }) as typeof fetch;
  return { fn, asked };
}
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

test("createRun sends only the body the route accepts, with the key as a header", async () => {
  const { fn, asked } = fakeFetch([json({ id: "api-svc-k1", agent: "lead" }, 202)]);
  const c = httpCollegium({
    baseUrl: "http://collegium.test/",
    token: staticToken("t0k"),
    fetch: fn,
  });
  await c.createRun({ agent: "lead", text: "hello", after: "api-svc-k0" }, "k1");
  assert.equal(asked[0]?.url, "http://collegium.test/api/runs");
  const headers = asked[0]?.init.headers as Record<string, string>;
  assert.equal(headers["Idempotency-Key"], "k1");
  assert.equal(headers.authorization, "Bearer t0k");
  // The route rejects fields it does not know, so nothing else may be in the body.
  assert.deepEqual(JSON.parse(String(asked[0]?.init.body)), {
    agent: "lead",
    text: "hello",
    after: "api-svc-k0",
  });
});

test("a refusal carries the limit, a missing object is null, and an error keeps its status", async () => {
  const { fn } = fakeFetch([
    json(
      {
        error: "the workspace reached 200 runs today",
        limit: "runs",
        remaining: 0,
        resets_at: "2026-10-10T00:00:00Z",
        policy: { version: 3 },
      },
      429,
    ),
    json({ error: "not found" }, 404),
    json({ error: "not found" }, 404),
    json({ error: "you need runs:write" }, 403),
  ]);
  const c = httpCollegium({ baseUrl: "http://collegium.test", token: staticToken(), fetch: fn });
  await assert.rejects(
    c.createRun({ agent: "", text: "x" }, "k"),
    (e: CollegiumError) =>
      e.status === 429 && e.refusal?.limit === "runs" && e.refusal.policy?.version === 3,
  );
  assert.equal(await c.getRun("nope"), null);
  assert.equal(await c.getThread("nope"), null);
  await assert.rejects(
    c.stopRun("r1"),
    (e: CollegiumError) => e.status === 403 && e.refusal === undefined,
  );
});

test("the event stream hands each event on once across a reconnect, and ends on abort", async () => {
  const frame = (seq: number, kind: string) =>
    `data: ${JSON.stringify({ seq, run_id: "r1", agent: "lead", kind })}\n\n`;
  const sse =
    (...parts: string[]) =>
    () =>
      new Response(stream(...parts), { status: 200 });
  const { fn, asked } = fakeFetch([
    sse(
      frame(1, "lifecycle.accepted"),
      'event: inbox\ndata: {"id":"m1","kind":"memory"}\n\n',
      "data: not json\n\n",
    ),
    // The server replays what the bus holds on every open.
    sse(frame(1, "lifecycle.accepted"), frame(2, "lifecycle.completed")),
  ]);
  const c = httpCollegium({
    baseUrl: "http://collegium.test",
    token: staticToken(),
    fetch: fn,
    reconnectMs: 0,
  });
  const stop = new AbortController();
  const got: string[] = [];
  for await (const item of c.runEvents({ id: "r1", agent: "lead" }, { signal: stop.signal })) {
    got.push(item.type === "event" ? item.event.kind : `inbox:${item.notice.id}`);
    if (item.type === "event" && item.event.kind === "lifecycle.completed") stop.abort();
  }
  assert.deepEqual(got, ["lifecycle.accepted", "inbox:m1", "lifecycle.completed"]);
  assert.equal(asked[0]?.url, "http://collegium.test/api/events?topic=agent%3Alead%3Ar1");
});

test("an event stream the caller may not read is an error, not a retry loop", async () => {
  const { fn } = fakeFetch([json({ error: "you need runs:read" }, 403)]);
  const c = httpCollegium({
    baseUrl: "http://collegium.test",
    token: staticToken(),
    fetch: fn,
    reconnectMs: 0,
  });
  await assert.rejects(async () => {
    for await (const _ of c.runEvents(
      { id: "r1", agent: "lead" },
      { signal: new AbortController().signal },
    )) {
      // nothing arrives
    }
  }, /403/);
});

test("a machine token is asked for once, kept, and asked for again when refused", async () => {
  let clock = 0;
  const issued = fakeFetch([
    json({ access_token: "first", expires_in: 3600 }),
    json({ access_token: "second", expires_in: 3600 }),
  ]);
  const token = clientCredentials({
    tokenUrl: "https://idp.example.com/oauth2/token",
    clientId: "slack-app",
    clientSecret: "not-a-real-secret",
    scopes: ["collegium:runs:read", "collegium:runs:write"],
    fetch: issued.fn,
    now: () => clock,
  });
  const api = fakeFetch([
    json({ error: "sign in to continue" }, 401),
    json({ principal: { subject: "slack-app", tenant: "acme", kind: "machine" }, permissions: [] }),
  ]);
  const c = httpCollegium({ baseUrl: "http://collegium.test", token, fetch: api.fn });
  assert.equal((await c.getMe()).principal.subject, "slack-app");
  assert.equal(issued.asked.length, 2, "the refusal asks the provider again");
  assert.equal(
    (api.asked[1]?.init.headers as Record<string, string>).authorization,
    "Bearer second",
  );
  const form = new URLSearchParams(String(issued.asked[0]?.init.body));
  assert.equal(form.get("grant_type"), "client_credentials");
  assert.equal(form.get("scope"), "collegium:runs:read collegium:runs:write");
  assert.ok(
    !String(issued.asked[0]?.init.body).includes("not-a-real-secret"),
    "the secret travels in the header only",
  );

  clock = 1000;
  assert.equal(await token(), "second", "a held token is reused");
  clock = 3_600_000;
  await assert.rejects(token(), /did not issue a token|unexpected request/);
});
