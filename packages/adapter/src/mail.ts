/**
 * The one seam between the platform's inbox and vultus's mailbox, for every
 * surface.
 *
 * The model is vultus's (`InboxMessage`, its parts, its actions). The routes
 * and the wire shape are the platform's. `fromServer` turns one into the
 * other, `toRequest` turns a click back into a call, and `mailTransport` makes
 * the calls through whatever a surface authenticates with: the console's
 * session cookie, or a machine's bearer token. When either side changes, this
 * file is what changes.
 */
import { ActionId, type InboxMessage, type MessagePart, partMatches, partTarget, type Resolution, type Tone, type UiAction } from "@vultus/core";
import { interruptOf, settledFromOutcome, type WireQuestion } from "./question.ts";

/** Something a message is about, which the console can open. */
export type Source = { kind: "run" | "thread" | "rule" | "agent" | "project"; id: string; label: string };

/** A mailbox message, plus what only the platform knows about it. */
export type PlatformMessage = InboxMessage & {
  /** What the message is, for the server's kind filter: alert, failure, memory, budget. */
  kind: string;
  /** Everything it is about, as things the console can open by kind and id. */
  about: Source[];
  /** For each part that is a limit that was reached: which limit, on what, by part id. */
  budgets: Record<string, BudgetData>;
};

export const kinds: { id: string; label: string }[] = [
  { id: "alert", label: "Alerts" },
  { id: "failure", label: "Failed runs" },
  { id: "memory", label: "Memory to review" },
  { id: "budget", label: "Limits" },
  { id: "approval", label: "Approvals" },
  { id: "clarification", label: "Questions" },
];

export type LimitKind = "runs" | "concurrent" | "spend" | "lease" | "tokens";
export type Limits = { max_runs?: number; max_concurrent?: number; spend_limit?: number; max_tokens?: number };
export type BudgetData = {
  scope: { kind: string; name?: string };
  which: LimitKind;
  period: "day" | "month";
  /** The limits in force when the message was raised. Spend is micro-USD. */
  current: Limits;
  /** Where the scope stood against the limit then, when the platform says. */
  used?: number;
  /** True when a run was turned away, false when this is only a warning. */
  refused: boolean;
};

/** Which of a refusal's limits is the one that was reached, and its value. */
export function reached(data: BudgetData): { field: keyof Limits; value: number } {
  const field =
    data.which === "spend" ? "spend_limit" : data.which === "concurrent" ? "max_concurrent" : data.which === "tokens" ? "max_tokens" : "max_runs";
  return { field, value: data.current[field] ?? 0 };
}

/** A scope in words: "Project Launch", "Agent oncall", "The workspace". */
export function scopeName(scope: { kind: string; name?: string }): string {
  if (scope.kind === "tenant" || !scope.name) return scope.kind === "tenant" ? "The workspace" : scope.kind;
  return `${scope.kind[0]?.toUpperCase()}${scope.kind.slice(1)} ${scope.name}`;
}

export const limitWord: Record<LimitKind, string> = {
  runs: "runs",
  concurrent: "runs at once",
  spend: "spend",
  lease: "time per run",
  tokens: "tokens",
};

type Wire = Record<string, unknown>;
const str = (v: unknown) => (typeof v === "string" ? v : "");
const obj = (v: unknown): Wire => (v && typeof v === "object" ? (v as Wire) : {});
const list = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);

const sourceKinds = ["run", "thread", "rule", "agent", "project"] as const;
const sourceWord: Record<Source["kind"], string> = { run: "Run", thread: "Conversation", rule: "Rule", agent: "Agent", project: "Project" };
const tone: Record<string, Tone> = { critical: "bad", warning: "warn", info: "info" };

/** The platform's name for an action as the mailbox's. What this reader may do to a part is a list of these. */
const actionIds: Record<string, ActionId> = {
  acknowledge: ActionId.acknowledge,
  retry: ActionId.retry,
  keep: ActionId.keepMemories,
  discard: ActionId.discardMemories,
  raise_limit: ActionId.raiseBudget,
  reply: ActionId.reply,
  approve: ActionId.approve,
  deny: ActionId.deny,
  choose: ActionId.clarify,
};

/** A reached limit as it arrives: flat scope fields, and the limits in force. */
function budgetOf(raw: unknown): BudgetData {
  const b = obj(obj(raw).budget);
  return {
    scope: { kind: str(b.scope_kind), name: str(b.scope_name) || undefined },
    which: (str(b.limit) || "runs") as LimitKind,
    period: str(b.period) === "day" ? "day" : "month",
    current: obj(b.current) as Limits,
    ...(typeof b.used === "number" ? { used: b.used } : {}),
    // Older messages do not say. They were only ever raised on a refusal.
    refused: b.refused !== false,
  };
}

/** The unit a limit is counted in. Spend is shown as dollars, so its micro-USD are divided down. */
export const unitOf = (which: LimitKind) =>
  which === "spend" ? "usd" : which === "concurrent" ? "runs at once" : which === "tokens" ? "tokens" : "runs";
const inUnit = (which: LimitKind, value: number) => (which === "spend" ? value / 1_000_000 : value);
const fromUnit = (which: LimitKind, value: number) => (which === "spend" ? Math.round(value * 1_000_000) : Math.floor(value));

function partFrom(raw: unknown, at: string): MessagePart | null {
  const p = obj(raw);
  const id = str(p.id);
  const out = obj(p.outcome);
  // What this reader may do. An empty list is read only, which is also what a settled part returns.
  const allowed = list(p.actions)
    .map((a) => actionIds[str(a)])
    .filter((a): a is ActionId => a !== undefined);
  const resolution: { resolution?: Resolution } = out.action
    ? { resolution: { outcome: str(out.action), by: str(out.by), at: str(out.at), ...(str(out.note) ? { detail: str(out.note) } : {}) } }
    : {};
  switch (p.type) {
    case "text":
      return { id, type: "text", text: str(p.text) };
    case "alert": {
      const a = obj(p.alert);
      return {
        id,
        type: "alert",
        alert: {
          id: str(a.id) || id,
          service: str(a.rule),
          status: out.action ? "resolved" : str(a.severity) === "critical" ? "down" : "degraded",
          title: str(a.title),
          ...(str(a.body) ? { body: str(a.body) } : {}),
          startedAt: at,
          updatedAt: str(out.at) || at,
        },
        allowed,
        ...resolution,
      };
    }
    case "failure": {
      const f = obj(p.failure);
      return {
        id,
        type: "failure",
        failure: { kind: "internal", message: str(f.error) || "The run failed without saying why.", reference: str(f.run) || undefined, retryable: f.retryable === true },
        allowed,
        ...resolution,
      };
    }
    case "memory": {
      const m = obj(p.memory);
      return {
        id,
        type: "memory",
        proposals: list(m.notes).map((n) => ({ id: str(obj(n).id), text: str(obj(n).text) })),
        allowed,
        ...resolution,
      };
    }
    case "budget": {
      const data = budgetOf(p);
      const limit = reached(data).value;
      return {
        id,
        type: "budget",
        // When the platform does not say how much is used, a refusal means the limit was reached.
        meter: {
          used: inUnit(data.which, data.used ?? limit),
          limit: inUnit(data.which, limit),
          unit: unitOf(data.which),
          ...(data.which === "concurrent" ? {} : { period: data.period }),
        },
        allowed,
        ...resolution,
      };
    }
    case "approval":
    case "choice":
    case "reply": {
      // A question a run is parked on: the same card the run shows, built by
      // the same function, settled with what was recorded.
      const q = obj(p.question);
      const question: WireQuestion = {
        id: str(q.id),
        kind: str(q.kind) || str(p.type),
        agent: str(q.agent),
        prompt: str(q.prompt),
        options: list(q.options).map(str),
        ...(q.tool ? { tool: { name: str(obj(q.tool).name), arguments: obj(q.tool).arguments } } : {}),
        ...(str(q.expires_at) ? { expires_at: str(q.expires_at) } : {}),
      };
      const settled = settledFromOutcome(out);
      if (p.type === "reply") {
        return {
          id,
          type: "reply",
          prompt: question.prompt,
          allowed,
          ...(settled
            ? { resolution: { outcome: settled.action === "reply" ? "replied" : settled.action, by: settled.by && settled.by !== "system" ? settled.by : "", at: settled.at, ...(settled.note ? { detail: settled.note } : {}) } }
            : {}),
        };
      }
      const card = interruptOf(question, settled);
      if (!card) return null;
      return card.type === "approval" ? { id, type: "approval", approval: card, allowed } : { id, type: "choice", clarification: card, allowed };
    }
    default:
      // A part this console does not know is left out rather than guessed at.
      return null;
  }
}

/** The platform's message as a mailbox message. */
export function fromServer(raw: unknown): PlatformMessage {
  const m = obj(raw);
  const from = obj(m.from);
  const source = obj(m.source);
  const at = str(m.updated_at) || str(m.at);
  const sources: Source[] = sourceKinds
    .filter((k) => str(source[k]))
    .map((k) => ({ kind: k, id: str(source[k]), label: `${sourceWord[k]} ${str(source[k])}` }));
  const first = sources.find((s) => s.kind === "run" || s.kind === "rule" || s.kind === "project");
  const state = str(m.state);
  return {
    id: str(m.id),
    subject: str(m.subject),
    from: { kind: (str(from.kind) || "system") as InboxMessage["from"]["kind"], name: str(from.name) },
    at,
    unread: m.unread === true,
    severity: tone[str(m.severity)] ?? "neutral",
    ...(first ? { source: { kind: first.kind as "run" | "rule" | "project", id: first.id, label: first.label } } : {}),
    parts: list(m.parts)
      .map((p) => partFrom(p, at))
      .filter((p): p is MessagePart => p !== null),
    ...(state === "dismissed" ? { dismissedAt: at } : {}),
    ...(state === "snoozed" ? { snoozedUntil: str(m.snoozed_until) || at } : {}),
    // A message about a limit leads with an alert part, so it is tagged by what it is about.
    ...(str(m.kind) === "budget" ? { tag: "Limit" } : {}),
    kind: str(m.kind),
    about: sources,
    budgets: Object.fromEntries(
      list(m.parts)
        .filter((p) => obj(p).type === "budget")
        .map((p) => [str(obj(p).id), budgetOf(p)]),
    ),
  };
}

/** What a click asks of the platform. */
export type Request =
  | { call: "read" }
  | { call: "dismiss" }
  | { call: "snooze"; until: string }
  | { call: "resolve"; partId: string; action: string; values?: Record<string, unknown> };

const HOUR = 3_600_000;

/**
 * A mailbox action as a platform call, or null when the platform has nothing
 * for it.
 */
export function toRequest(message: PlatformMessage, action: UiAction): Request | null {
  const { id, payload } = action;
  if (payload.turnId !== message.id) return null;
  if (id === ActionId.read) return { call: "read" };
  if (id === ActionId.dismiss) return { call: "dismiss" };
  // The shared failure card names its part by the failure's kind, which partMatches knows.
  const part = message.parts.find((p) => partTarget(p) === payload.target) ?? message.parts.find((p) => partMatches(p, payload.target));
  // Snoozing puts the whole message away until a time. It is never a part's own action.
  if (id === ActionId.snooze) return { call: "snooze", until: payload.option || new Date(Date.now() + 24 * HOUR).toISOString() };
  if (!part) return null;
  const resolve = (name: string, values?: Record<string, unknown>): Request => ({ call: "resolve", partId: part.id, action: name, values });
  if (part.type === "budget" && id === ActionId.raiseBudget) {
    const data = message.budgets[part.id];
    if (!data || !payload.option) return null;
    // The new limit arrives in the meter's unit, and goes to the platform in its own.
    return resolve("raise_limit", { limits: { [reached(data).field]: fromUnit(data.which, Number(payload.option)) } });
  }
  switch (id) {
    case ActionId.acknowledge:
      return resolve("acknowledge");
    case ActionId.retry:
      return resolve("retry");
    case ActionId.keepMemories:
      return resolve("keep", { ids: payload.selection ?? [] });
    case ActionId.discardMemories:
      return resolve("discard");
    case ActionId.reply:
      return payload.text?.trim() ? resolve("reply", { text: payload.text.trim() }) : null;
    case ActionId.approve:
      return part.type === "approval" ? resolve("approve") : null;
    case ActionId.deny:
      return part.type === "approval" ? resolve("deny") : null;
    case ActionId.clarify:
      return part.type === "choice" && payload.option ? resolve("choose", { choice: payload.option }) : null;
    default:
      return null;
  }
}

export interface Page {
  messages: PlatformMessage[];
  /** The cursor for the next page, null at the end. */
  next: string | null;
  /** Open and unread messages across the whole inbox, not only this page. */
  open: number;
  unread: number;
}

export interface Query {
  limit: number;
  before?: string;
  state?: "open" | "resolved" | "all";
  kind?: string;
}

/** Everything the inbox asks of the platform. Tests and the gallery pass their own. */
export interface Transport {
  list(q: Query): Promise<Page>;
  get(id: string): Promise<PlatformMessage>;
  send(message: PlatformMessage, request: Request): Promise<PlatformMessage>;
}

export class MailError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

/** One authenticated JSON call to the platform. A surface supplies it: a cookie in the console, a bearer token for a machine. */
export type Http = <T>(method: string, path: string, json?: unknown) => Promise<T>;

const at = (id: string) => `/api/inbox/${encodeURIComponent(id)}`;

/** The inbox's routes, over a surface's own way of calling the platform. */
export function mailTransport(http: Http): Transport {
  return {
    list: async (q) => {
      const params = new URLSearchParams({ limit: String(q.limit), state: q.state ?? "open" });
      if (q.before) params.set("before", q.before);
      if (q.kind) params.set("kind", q.kind);
      const page = await http<{ messages: unknown[]; next: string | null; open: number; unread: number }>("GET", `/api/inbox?${params}`);
      return { messages: (page.messages ?? []).map(fromServer), next: page.next ?? null, open: page.open ?? 0, unread: page.unread ?? 0 };
    },
    get: async (id) => fromServer((await http<{ message: unknown }>("GET", at(id))).message),
    send: async (message, r) => {
      const to = at(message.id);
      if (r.call === "read") return fromServer(await http("POST", `${to}/read`, { read: true }));
      if (r.call === "dismiss") return fromServer(await http("POST", `${to}/dismiss`, {}));
      if (r.call === "snooze") return fromServer(await http("POST", `${to}/snooze`, { until: r.until }));
      // What a part needs to be resolved travels beside the action: ids to keep, new limits, a note.
      return fromServer(await http("POST", `${to}/parts/${encodeURIComponent(r.partId)}/resolve`, { action: r.action, ...r.values }));
    },
  };
}
