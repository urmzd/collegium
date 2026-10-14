import assert from "node:assert/strict";
import { type ChildProcess, execFileSync, spawn } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, test } from "node:test";
import { follow } from "@collegium/adapter";
import { App } from "./app.tsx";
import { type Collegium, httpCollegium, staticToken } from "./collegium.ts";
import { CHANNEL, FakeSlack, mention, settle, stopPressed, TEAM, USER } from "./harness.ts";
import { runId, threadKey } from "./keys.ts";

/**
 * The stand-in client and the app against the real collegium server, built
 * from this checkout and started with dev auth on loopback and its offline
 * example agents. No model is called: the server is started without any
 * provider key in its environment. Slack is the fake.
 *
 * Run with `pnpm test:server`. It needs Go.
 */
const enabled = process.env.COLLEGIUM_SERVER_TEST === "1";
const root = new URL("../../..", import.meta.url).pathname;
let server: ChildProcess | undefined;
let dir = "";
let base = "";

const freePort = () =>
  new Promise<number>((resolve, reject) => {
    const s = createServer();
    s.once("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const port = (s.address() as { port: number }).port;
      s.close(() => resolve(port));
    });
  });

before(async () => {
  if (!enabled) return;
  dir = mkdtempSync(join(tmpdir(), "collegium-slack-"));
  const bin = join(dir, "collegium");
  execFileSync("go", ["build", "-o", bin, "./cmd/collegium"], { cwd: root, stdio: "inherit" });
  const port = await freePort();
  base = `http://127.0.0.1:${port}`;
  // Only what the server needs to start. No provider key is passed on.
  const env = Object.fromEntries(
    ["PATH", "HOME", "TMPDIR"].flatMap((k) => (process.env[k] ? [[k, process.env[k]]] : [])),
  );
  server = spawn(bin, ["serve", "-auth=dev", "-seed", "-addr", `127.0.0.1:${port}`], {
    cwd: dir,
    env,
    stdio: ["ignore", "ignore", "pipe"],
  });
  for (let i = 0; i < 100; i++) {
    const up = await fetch(`${base}/healthz`).then(
      (r) => r.ok,
      () => false,
    );
    if (up) return;
    await new Promise((r) => setTimeout(r, 100));
  }
  throw new Error("collegium did not start");
});

after(() => {
  server?.kill();
  if (dir) rmSync(dir, { recursive: true, force: true });
});

const client = (): Collegium => httpCollegium({ baseUrl: base, token: staticToken() });

/** Every event of a run until it ends, folded into its turn. */
async function finish(c: Collegium, run: { id: string; agent: string }, patience = 10_000) {
  const stop = new AbortController();
  const timer = setTimeout(() => stop.abort(), patience);
  const f = follow(run);
  const kinds: string[] = [];
  for await (const item of c.runEvents(run, { signal: stop.signal })) {
    if (item.type !== "event" || item.event.run_id !== run.id) continue;
    kinds.push(item.event.kind);
    f.apply(item.event);
    if (["done", "failed", "stopped"].includes(f.turn.status)) break;
  }
  clearTimeout(timer);
  stop.abort();
  return { turn: f.turn, kinds };
}

test("the stand-in client against the real routes", { skip: !enabled }, async () => {
  const c = client();
  const me = (await c.getMe()).principal;
  assert.equal(me.subject, "dev");
  assert.equal(me.tenant, "default");
  const { permissions } = await c.getMe();
  for (const p of ["runs:read", "runs:write", "agents:read"]) assert.ok(permissions.includes(p));

  const { agents } = await c.listAgents();
  assert.ok(agents.some((a) => a.spec.name === "lead"));

  // The run id is the caller and the key, and the first run names its conversation.
  const key = threadKey({ team: TEAM, channel: CHANNEL, threadTs: "1700000000.000100" });
  const run = await c.createRun({ agent: "lead", text: "hello" }, key);
  assert.equal(run.id, runId(me.tenant, me.subject, key));
  assert.equal(run.thread, run.id);
  const again = await c.createRun({ agent: "lead", text: "hello" }, key);
  assert.equal(again.id, run.id, "the same key joins the run it started");

  const { turn, kinds } = await finish(c, run);
  assert.equal(kinds[0], "lifecycle.accepted");
  assert.equal(kinds.at(-1), "lifecycle.completed");
  assert.equal(turn.status, "done");
  assert.match(turn.text, /offline provider/);

  const next = await c.createRun({ agent: "", text: "and then?", after: run.id }, `${key}-2`);
  assert.equal(next.thread, run.id);
  assert.equal(next.prev, run.id);
  assert.ok(next.picked, "an empty agent is the platform's picker");
  await finish(c, next);
  const turns = (await c.getThread(run.id))?.turns;
  assert.deepEqual(turns?.map((t) => t.id).sort(), [run.id, next.id].sort());

  assert.equal(await c.getRun("api-dev-no-such-run"), null);
  assert.equal(await c.getThread("api-dev-no-such-run"), null);
  assert.equal(await c.getClient("no-such-client"), null);
  // Stopping a run that has ended changes nothing.
  assert.equal((await c.stopRun(run.id)).status, "completed");

  const page = await c.inbox.list({ limit: 5, state: "open" });
  assert.ok(Array.isArray(page.messages));

  // The inbox-only stream opens and closes cleanly.
  const quiet = new AbortController();
  setTimeout(() => quiet.abort(), 300);
  for await (const _ of c.notices({ signal: quiet.signal })) {
    // Nothing is raised here.
  }
});

test(
  "the app against the real server: a mention, a redelivery, a reply and a stop",
  { skip: !enabled },
  async () => {
    const c = client();
    const me = (await c.getMe()).principal;
    const slack = new FakeSlack();
    const app = new App(slack, c, {
      team: TEAM,
      botUser: "U00BOTBOT",
      subject: me.subject,
      tenant: me.tenant,
      defaultAgent: "lead",
      flushMs: 0,
      pollMs: 200,
    });
    const ts = "1700000999.000100";
    const waitFor = async (check: () => boolean) => {
      for (let i = 0; i < 200 && !check(); i++) await new Promise((r) => setTimeout(r, 25));
      assert.ok(check());
    };

    await app.handle(mention("what can you do?", ts));
    await waitFor(() => slack.methods.includes("chat.stopStream"));
    await app.idle();
    const id = runId(
      me.tenant,
      me.subject,
      threadKey({ team: TEAM, channel: CHANNEL, threadTs: ts }),
    );
    assert.equal((await c.getRun(id))?.status, "completed");
    assert.match(slack.streamed, /offline provider/);
    assert.deepEqual(slack.statuses, ["processing", "active"]);
    assert.equal(slack.of("reactions.add").at(-1)?.name, "white_check_mark");

    // A new process, the same Slack event: collegium has it, so nothing starts.
    const fresh = new App(slack, c, {
      team: TEAM,
      botUser: "U00BOTBOT",
      subject: me.subject,
      tenant: me.tenant,
      defaultAgent: "lead",
      flushMs: 0,
      pollMs: 200,
    });
    const calls = slack.calls.length;
    await fresh.handle(mention("what can you do?", ts));
    await settle();
    await fresh.idle();
    assert.equal(slack.calls.length, calls);

    await fresh.handle(mention("@helper and you?", "1700001000.000100", { thread_ts: ts }));
    await waitFor(() => slack.of("chat.stopStream").length === 2);
    await fresh.idle();
    const turns = (await c.getThread(id))?.turns ?? [];
    const reply = turns.find((t) => t.id !== id);
    assert.equal(reply?.agent, "helper");
    assert.equal(reply?.prev, id);
    assert.equal(reply?.text, "and you?");

    // Nothing is running, so stop only settles the session.
    await fresh.handle(stopPressed(ts));
    await fresh.idle();
    assert.equal(slack.calls.at(-1)?.method, "agents.sessions.setStatus");
    assert.equal(slack.calls.at(-1)?.args.status, "active");
    assert.equal(USER, slack.of("chat.startStream")[0]?.recipient_user_id);
  },
);

test(
  "a failed run is ended by the ledger, reaches the inbox, and is settled through the client",
  { skip: !enabled, timeout: 120_000 },
  async () => {
    const c = client();
    // An agent on a local model server that has no such model fails every attempt without leaving the machine.
    const made = await fetch(`${base}/api/agents`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        name: "nomodel",
        description: "fails on every attempt",
        model: "no-such-model",
        provider: "ollama",
        system_prompt: "x",
      }),
    });
    assert.equal(made.status, 201);
    const run = await c.createRun(
      {
        agent: "nomodel",
        text: "hello",
      },
      "server-test-fail",
    );
    // No event says the last attempt was the last: the ledger ends the run, and its record says so.
    let record = await c.getRun(run.id);
    for (let i = 0; i < 300 && record?.status !== "failed"; i++) {
      await new Promise((r) => setTimeout(r, 100));
      record = await c.getRun(run.id);
    }
    assert.equal(record?.status, "failed");
    const { turn, kinds } = await finish(c, run, 1_000);
    assert.ok(kinds.includes("lifecycle.failed"));
    assert.equal(turn.status, "working", "the events alone never end the turn");
    assert.equal(
      follow(run).settle(record as NonNullable<typeof record>).status,
      "failed",
      "the record does",
    );

    // The workspace's default rule raises it as an alert, with the failed run as a part.
    let message = null;
    for (let i = 0; i < 100 && !message; i++) {
      const page = await c.inbox.list({ limit: 30, state: "open" });
      message =
        page.messages.find((m) => m.about.some((s) => s.kind === "run" && s.id === run.id)) ?? null;
      if (!message) await new Promise((r) => setTimeout(r, 50));
    }
    assert.ok(message, "the failure is in the inbox");
    const part = message.parts.find((p) => p.type === "failure");
    assert.ok(part?.allowed?.length, "this caller may act on it");
    const settled = await c.inbox.send(message, {
      call: "resolve",
      partId: part.id,
      action: "acknowledge",
    });
    assert.equal(settled.parts.find((p) => p.id === part.id && "resolution" in p)?.type, "failure");
    const again = await c.inbox.get(message.id);
    assert.equal(
      (again.parts.find((p) => p.id === part.id) as { resolution?: { outcome: string } }).resolution
        ?.outcome,
      "acknowledge",
    );
  },
);
