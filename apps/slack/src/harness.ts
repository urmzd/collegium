/**
 * Test doubles for both sides of the app: a Slack that records every call and
 * a collegium whose runs the test drives one event at a time. Every id here is
 * made up.
 */
import { type BusEvent, type Http, mailTransport, type Run } from "@collegium/adapter";
import { App, type AppOptions } from "./app.tsx";
import {
  type Agent,
  type ClientRecord,
  type Collegium,
  CollegiumError,
  type InboxNotice,
  type RunRequest,
  type StreamItem,
} from "./collegium.ts";
import { runId, tagId } from "./keys.ts";

/** A run asked for: the body and the key, as `createRun` takes them. */
export interface Asked extends RunRequest {
  idempotencyKey: string;
}

import { type Envelope, type SlackApi, SlackError } from "./slack.ts";

export const TEAM = "T01ABCDEF";
export const OTHER_TEAM = "T09ZZZZZZ";
export const CHANNEL = "C01ABCDEF";
export const DM = "D01ABCDEF";
export const USER = "U01ABCDEF";
export const OTHER_USER = "U02ABCDEF";
export const BOT = "U00BOTBOT";
export const SUBJECT = "svc-slack";

export interface Call {
  method: string;
  args: Record<string, unknown>;
}

/** A Slack that answers every call and remembers it. */
export class FakeSlack implements SlackApi {
  readonly calls: Call[] = [];
  readonly responses: { url: string; body: Record<string, unknown> }[] = [];
  /** Methods that fail, by error code. A function decides per call. */
  failing: Record<string, string | ((args: Record<string, unknown>) => string | undefined)> = {};
  private clock = 1_800_000_000;

  async call<T>(method: string, args: Record<string, unknown> = {}): Promise<T> {
    this.calls.push({ method, args });
    const fail = this.failing[method];
    const code = typeof fail === "function" ? fail(args) : fail;
    if (code) throw new SlackError(method, code);
    if (method === "chat.startStream" || method === "chat.postMessage") {
      this.clock += 1;
      const channel = String(args.channel).startsWith("U") ? DM : args.channel;
      return { ok: true, ts: `${this.clock}.000100`, channel } as T;
    }
    return { ok: true } as T;
  }

  async respond(url: string, body: Record<string, unknown>) {
    this.responses.push({ url, body });
  }

  /** The names of the calls made, in order. */
  get methods(): string[] {
    return this.calls.map((c) => c.method);
  }

  of(method: string): Record<string, unknown>[] {
    return this.calls.filter((c) => c.method === method).map((c) => c.args);
  }

  /** Session statuses in the order they were set. */
  get statuses(): string[] {
    return this.of("agents.sessions.setStatus").map((a) => String(a.status));
  }

  /** Everything streamed as answer text, joined. */
  get streamed(): string {
    return this.of("chat.appendStream")
      .flatMap((a) => a.chunks as { type: string; text?: string }[])
      .filter((c) => c.type === "markdown_text")
      .map((c) => c.text ?? "")
      .join("");
  }
}

/** A queue a test can push into and an async reader can drain. */
class Feed<T> {
  private readonly items: T[] = [];
  private waiters: (() => void)[] = [];
  push(item: T) {
    this.items.push(item);
    for (const w of this.waiters.splice(0)) w();
  }
  async *read(signal: AbortSignal): AsyncGenerator<T> {
    for (;;) {
      while (this.items.length > 0) yield this.items.shift() as T;
      if (signal.aborted) return;
      await new Promise<void>((resolve) => {
        this.waiters.push(resolve);
        signal.addEventListener("abort", () => resolve(), { once: true });
      });
      if (signal.aborted && this.items.length === 0) return;
    }
  }
}

const b64 = (v: unknown) => Buffer.from(JSON.stringify(v)).toString("base64");

/** A collegium that keeps runs, conversations and an inbox in memory, shaped like the real API. */
export class FakeCollegium implements Collegium {
  readonly runs = new Map<string, Run>();
  /** Every turn asked for, including ones that joined an existing run. */
  readonly asked: Asked[] = [];
  /** Turns that started a run. */
  readonly started: Asked[] = [];
  readonly stops: string[] = [];
  readonly resolved: { id: string; path: string; body: unknown }[] = [];
  agentList: Agent[] = [
    { name: "lead", description: "Takes a request and may call the helper." },
    { name: "helper", description: "Does one piece of work for the lead." },
  ];
  clients = new Map<string, ClientRecord>();
  /** Inbox messages as the platform sends them. */
  readonly mailbox = new Map<string, Record<string, unknown>>();
  /** Set to refuse the next turn, as a limit would. */
  refuse: CollegiumError | null = null;
  /** Set to make stop fail. */
  stopFails = false;
  private readonly feeds = new Map<string, Feed<StreamItem>>();
  private readonly notes = new Feed<InboxNotice>();
  private seq = new Map<string, number>();
  private clock = Date.parse("2026-10-09T00:00:00Z");

  private at() {
    this.clock += 1000;
    return new Date(this.clock).toISOString();
  }

  private feed(id: string) {
    let f = this.feeds.get(id);
    if (!f) {
      f = new Feed();
      this.feeds.set(id, f);
    }
    return f;
  }

  async getMe() {
    return {
      principal: { subject: SUBJECT, tenant: "acme", kind: "machine" as const },
      permissions: ["runs:read", "runs:write", "agents:read"],
    };
  }
  async listAgents() {
    return { agents: this.agentList.map((spec) => ({ spec, rev: 1 })), builtin: [] };
  }
  async getClient(id: string) {
    return this.clients.get(id) ?? null;
  }

  async createRun(body: RunRequest, key = "random"): Promise<Run> {
    const t: Asked = { ...body, idempotencyKey: key };
    this.asked.push(t);
    if (this.refuse) {
      const e = this.refuse;
      this.refuse = null;
      throw e;
    }
    const id = runId("acme", SUBJECT, t.idempotencyKey);
    const existing = this.runs.get(id);
    if (existing) return existing;
    const prev = t.after ? this.runs.get(t.after) : undefined;
    if (t.after && !prev) throw new CollegiumError(404, "not found");
    const now = this.at();
    const run: Run = {
      id,
      tag_id: tagId(SUBJECT, t.idempotencyKey),
      tenant: "acme",
      // An empty agent is the platform's picker, which here always picks the baseline.
      agent: t.agent || "lead",
      rev: 1,
      origin: "api",
      text: t.text,
      actor: { id: SUBJECT, bot: true },
      root: id,
      depth: 0,
      thread: prev?.thread ?? id,
      ...(t.after ? { prev: t.after } : {}),
      lease: 0,
      granted: 0,
      calls: 0,
      status: "accepted",
      created_at: now,
      updated_at: now,
    };
    this.runs.set(id, run);
    this.started.push(t);
    return run;
  }

  async getRun(id: string) {
    return this.runs.get(id) ?? null;
  }

  async getThread(id: string) {
    const turns = [...this.runs.values()].filter((r) => r.thread === id);
    return turns.length > 0 ? { turns } : null;
  }

  runEvents(run: { id: string }, options: { signal?: AbortSignal }) {
    return this.feed(run.id).read(options.signal ?? new AbortController().signal);
  }

  notices(options: { signal?: AbortSignal }) {
    return this.notes.read(options.signal ?? new AbortController().signal);
  }

  async stopRun(id: string): Promise<Run> {
    this.stops.push(id);
    if (this.stopFails) throw new CollegiumError(500, "something went wrong on our side");
    const run = this.runs.get(id);
    if (!run) throw new CollegiumError(404, "not found");
    if (run.status !== "completed" && run.status !== "failed") {
      this.set(id, { status: "stopped", cause: `Stopped by ${SUBJECT}` });
    }
    return this.runs.get(id) as Run;
  }

  /** The inbox routes, over the real mapping, so the wire shapes are the ones exercised. */
  private http: Http = async <T>(method: string, path: string, json?: unknown): Promise<T> => {
    const url = new URL(path, "http://collegium.test");
    const [, , , id, ...rest] = url.pathname.split("/");
    if (method === "GET" && !id) {
      const messages = [...this.mailbox.values()].filter((m) => m.state === "open");
      return { messages, next: null, open: messages.length, unread: 0 } as T;
    }
    const message = id ? this.mailbox.get(decodeURIComponent(id)) : undefined;
    if (!message) throw new CollegiumError(404, "not found");
    if (method === "GET") return { message, history: [] } as T;
    this.resolved.push({ id: String(message.id), path: rest.join("/"), body: json });
    if (rest[0] === "parts") {
      const body = json as { action: string; ids?: string[] };
      const outcome = {
        action: body.action,
        by: SUBJECT,
        at: this.at(),
        ...(body.ids ? { note: `Kept ${body.ids.length} of 2` } : {}),
      };
      message.parts = (message.parts as Record<string, unknown>[]).map((p) =>
        p.id === rest[1] ? { ...p, state: "resolved", actions: [], outcome } : p,
      );
      message.state = "resolved";
    }
    if (rest[0] === "dismiss") message.state = "dismissed";
    return message as T;
  };
  inbox = mailTransport(this.http);

  // What a test does to the platform.

  /** Change a run's record, as the ledger would. */
  set(id: string, change: Partial<Run>) {
    const run = this.runs.get(id);
    if (run) this.runs.set(id, { ...run, ...change, updated_at: this.at() });
  }

  /** Publish one event on a run's stream. */
  emit(id: string, kind: string, payload?: unknown) {
    const run = this.runs.get(id);
    if (!run) throw new Error(`no run ${id}`);
    const seq = (this.seq.get(id) ?? 0) + 1;
    this.seq.set(id, seq);
    const event: BusEvent = {
      seq,
      topic: `agent:${run.agent}:${id}`,
      run_id: id,
      tenant: "acme",
      agent: run.agent,
      rev: 1,
      origin: "api",
      kind,
      payload: payload === undefined ? undefined : b64(payload),
      at: this.at(),
    };
    if (kind === "lifecycle.started") this.set(id, { status: "running" });
    if (kind === "lifecycle.completed") {
      this.set(id, { status: "completed", answer: (payload as { text?: string })?.text });
    }
    if (kind === "lifecycle.failed" && (payload as { terminal?: boolean })?.terminal) {
      this.set(id, { status: "failed", error: (payload as { error?: string }).error });
    }
    this.feed(id).push({ type: "event", event });
  }

  /** Put a message in the inbox and announce it. */
  raise(message: Record<string, unknown>) {
    this.mailbox.set(String(message.id), message);
    this.notes.push({
      id: String(message.id),
      kind: String(message.kind),
      state: String(message.state),
      updated_at: this.at(),
    });
  }

  /** The run most recently started. */
  get last(): Run {
    const run = [...this.runs.values()].at(-1);
    if (!run) throw new Error("no run was started");
    return run;
  }
}

/** Let everything already scheduled run: promise chains, zero timers, and the turn pump. */
export async function settle(rounds = 8): Promise<void> {
  for (let i = 0; i < rounds; i++) await new Promise((r) => setTimeout(r, 0));
}

export interface World {
  slack: FakeSlack;
  collegium: FakeCollegium;
  app: App;
  logs: string[];
  /** A second app over the same Slack and collegium, as after a restart. */
  restart(): App;
}

export function world(
  opts: Partial<AppOptions> = {},
  from?: Pick<World, "slack" | "collegium">,
): World {
  const slack = from?.slack ?? new FakeSlack();
  const collegium = from?.collegium ?? new FakeCollegium();
  const logs: string[] = [];
  const make = () =>
    new App(slack, collegium, {
      team: TEAM,
      botUser: BOT,
      subject: SUBJECT,
      tenant: "acme",
      defaultAgent: "lead",
      flushMs: 0,
      pollMs: 20,
      log: (line) => logs.push(line),
      ...opts,
    });
  return { slack, collegium, app: make(), logs, restart: make };
}

// Slack deliveries, as Socket Mode hands them over.

let envelopes = 0;
const envelope = (type: string, payload: Record<string, unknown>): Envelope => {
  envelopes += 1;
  return { type, envelope_id: `env-${envelopes}`, payload };
};

export const mention = (text: string, ts: string, over: Record<string, unknown> = {}): Envelope =>
  envelope("events_api", {
    team_id: TEAM,
    event_id: `Ev${ts.replace(".", "")}`,
    event: {
      type: "app_mention",
      channel: CHANNEL,
      user: USER,
      text: `<@${BOT}> ${text}`,
      ts,
      ...over,
    },
    ...(over.team_id ? { team_id: over.team_id } : {}),
  });

export const direct = (text: string, ts: string, over: Record<string, unknown> = {}): Envelope =>
  envelope("events_api", {
    team_id: TEAM,
    event: { type: "message", channel_type: "im", channel: DM, user: USER, text, ts, ...over },
  });

export const stopPressed = (threadTs: string, channel = CHANNEL): Envelope =>
  envelope("events_api", {
    team_id: TEAM,
    event: { type: "agent_session_stopped", channel, thread_ts: threadTs, user: USER },
  });

export const homeOpened = (user = USER): Envelope =>
  envelope("events_api", {
    team_id: TEAM,
    event: { type: "app_home_opened", tab: "home", user },
  });

/** A click on a button the app rendered: the button is found in the blocks and sent back as Slack would. */
export function click(
  blocks: unknown[],
  label: string,
  at: { channel?: string; ts: string; threadTs?: string; user?: string; state?: unknown },
): Envelope {
  type Button = { text?: { text: string }; value?: string };
  type Element = Button & {
    type: string;
    action_id?: string;
    positive_button?: Button;
    negative_button?: Button;
  };
  const elements = (blocks as { type: string; elements?: Element[] }[]).flatMap(
    (b) => (b.type === "actions" || b.type === "context_actions" ? b.elements : undefined) ?? [],
  );
  // A rating control is one element with two buttons, each with its own value.
  const buttons = elements.flatMap((e) =>
    e.type === "feedback_buttons"
      ? [e.positive_button, e.negative_button].map((b) => ({
          action_id: e.action_id,
          value: b?.value,
          label: b?.text?.text,
        }))
      : e.type === "button"
        ? [{ action_id: e.action_id, value: e.value, label: e.text?.text }]
        : [],
  );
  const button = buttons.find((b) => b.label === label);
  if (!button) {
    throw new Error(
      `no button "${label}" among: ${buttons.map((b) => b.label).join(", ") || "none"}`,
    );
  }
  envelopes += 1;
  return envelope("interactive", {
    type: "block_actions",
    team: { id: TEAM },
    user: { id: at.user ?? USER, team_id: TEAM },
    channel: { id: at.channel ?? CHANNEL },
    container: { type: "message", channel_id: at.channel ?? CHANNEL },
    trigger_id: `trigger-${envelopes}`,
    message: { ts: at.ts, thread_ts: at.threadTs },
    actions: [{ action_id: button.action_id, value: button.value }],
    ...(at.state ? { state: at.state } : {}),
  });
}

export const command = (text: string, over: Record<string, unknown> = {}): Envelope => {
  envelopes += 1;
  return envelope("slash_commands", {
    command: "/agent",
    text,
    team_id: TEAM,
    user_id: USER,
    channel_id: CHANNEL,
    trigger_id: `trigger-${envelopes}`,
    response_url: "https://hooks.slack.com/commands/T01ABCDEF/1/fake",
    ...over,
  });
};

export const interactive = (payload: Record<string, unknown>): Envelope => {
  envelopes += 1;
  return envelope("interactive", {
    team: { id: TEAM },
    user: { id: USER, team_id: TEAM },
    trigger_id: `trigger-${envelopes}`,
    ...payload,
  });
};

/** Proposed memories about a run, as the platform's inbox sends them. */
export function memoryMail(run: string): Record<string, unknown> {
  return {
    id: "m-memory",
    kind: "memory",
    subject: "2 proposed memories for lead",
    from: { kind: "agent", name: "lead" },
    at: "2026-10-09T00:10:00Z",
    updated_at: "2026-10-09T00:10:00Z",
    severity: "info",
    state: "open",
    unread: true,
    source: { run, agent: "lead" },
    parts: [
      {
        id: "p1",
        type: "memory",
        state: "open",
        actions: ["keep", "discard"],
        memory: {
          owner: "lead",
          agent: "lead",
          notes: [
            { id: "n1", text: "Deploys are frozen on Fridays." },
            { id: "n2", text: "The staging cluster is rebuilt every Monday." },
          ],
        },
      },
    ],
  };
}

/** A failed run no rule caught, as the platform's inbox sends it. */
export function failureMail(run: string): Record<string, unknown> {
  return {
    id: "m-failure",
    kind: "failure",
    subject: "lead failed",
    from: { kind: "agent", name: "lead" },
    at: "2026-10-09T00:10:00Z",
    updated_at: "2026-10-09T00:10:00Z",
    severity: "warning",
    state: "open",
    unread: true,
    source: { run, agent: "lead" },
    parts: [
      {
        id: "p1",
        type: "failure",
        state: "open",
        actions: ["retry", "acknowledge"],
        failure: { run, agent: "lead", error: "provider: 529 overloaded", retryable: true },
      },
    ],
  };
}

/** A fired alert rule, as the platform's inbox sends it. */
export function alertMail(): Record<string, unknown> {
  return {
    id: "m-alert",
    kind: "alert",
    subject: "oncall is failing more than half its runs",
    from: { kind: "rule", name: "oncall-failure-rate" },
    at: "2026-10-09T00:20:00Z",
    updated_at: "2026-10-09T00:20:00Z",
    severity: "critical",
    state: "open",
    unread: true,
    source: { alert: "al-1", rule: "oncall-failure-rate", agent: "oncall" },
    parts: [
      {
        id: "p1",
        type: "alert",
        state: "open",
        actions: ["acknowledge"],
        alert: {
          id: "al-1",
          rule: "oncall-failure-rate",
          kind: "failure_rate",
          severity: "critical",
          title: "6 of 10 runs failed in the last 15 minutes",
          body: "The provider returned 529 for each failed run.",
        },
      },
    ],
  };
}

/** A reached limit, as the platform's inbox sends it. */
export function budgetMail(): Record<string, unknown> {
  return {
    id: "m-budget",
    kind: "budget",
    subject: "Project Launch reached its daily run limit",
    from: { kind: "system", name: "Limits" },
    at: "2026-10-09T00:30:00Z",
    updated_at: "2026-10-09T00:30:00Z",
    severity: "warning",
    state: "open",
    unread: false,
    source: { project: "Launch" },
    parts: [
      {
        id: "p1",
        type: "budget",
        state: "open",
        actions: ["raise_limit", "acknowledge"],
        budget: {
          scope_kind: "project",
          scope_name: "Launch",
          limit: "runs",
          period: "day",
          version: 2,
          used: 100,
          refused: true,
          current: { max_runs: 100 },
        },
      },
    ],
  };
}
