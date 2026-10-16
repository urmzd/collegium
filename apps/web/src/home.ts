/**
 * The home page's model, from what the console already fetched, and where
 * each of its actions goes. Pure: `Home` in vultus draws the model, and the
 * console carries out the intent.
 */
import {
  ActionId,
  ANY_AGENT,
  type HomeConnection,
  type HomeModel,
  type HomeNotification,
  type HomeNotifications,
  type HomeStart,
  type HomeStartId,
  type Tone,
  type UiAction,
} from "@vultus/core";
import { accessOf } from "./access.ts";
import type { AccessDecision, Alert, Client, Me, Revision } from "./api.ts";
import { alertUrl, consoleUrl, inboxItemUrl } from "./links.ts";
import type { PlatformMessage } from "./mail.ts";

export const ASSISTANT = "Collegium";
const DOCS = "https://github.com/urmzd/collegium/blob/main/docs";

export interface HomeInput {
  me: Me;
  /** What the person calls themselves here, or their name from sign-in. */
  name: string;
  agents: Revision[];
  /** Open inbox messages, newest first, as loaded (a bounded first page). */
  waiting: PlatformMessage[];
  /** Open messages in the whole inbox, not only those loaded. */
  waitingCount: number;
  /** The tenant's alerts as the console holds them, or null when the person may not read alerts. */
  alerts: Alert[] | null;
  /**
   * Where this console is reached from outside, such as the server's
   * `-public-url`. Given, every link is absolute so it works from Slack;
   * left out, links are relative to the console that draws the page.
   */
  publicUrl?: string;
  /** Lines under the ask box: what went wrong with the last question, what goes with the next. */
  notices?: { tone: Tone; text: string }[];
  /** Runs that have not ended. */
  running: number;
  /** The workspace's clients, or null when the person may not see them. */
  clients: Client[] | null;
  /** Why the person may do the thing "Why can I do this?" explains. */
  decision: AccessDecision | null;
  /** The agent picked to answer, or `ANY_AGENT`. */
  answeredBy: string;
}

// Each way to start, and what a person needs to take it.
const STARTS: { id: HomeStartId; needs: string }[] = [
  { id: "talk", needs: "runs:write" },
  { id: "build", needs: "agents:read" },
  { id: "search", needs: "knowledge:read" },
  { id: "review", needs: "runs:read" },
  { id: "runs", needs: "runs:read" },
  { id: "schedule", needs: "schedules:read" },
];

const SURFACES: { id: Client["surface"]; name: string; description: string }[] = [
  { id: "slack", name: "Slack", description: "Mention an agent in any channel it has been added to." },
  { id: "github", name: "GitHub", description: "Agents answer on issues and review pull requests." },
];

// What each kind of message wants, starting with a verb, and what happens next.
const WANTS: Record<string, { verb: string; detail: string }> = {
  alert: { verb: "raised an alert", detail: "Waiting for you to acknowledge it." },
  failure: { verb: "reports a failed run", detail: "Waiting for you to retry or dismiss it." },
  memory: { verb: "wants notes reviewed", detail: "Waiting for your approval." },
  budget: { verb: "hit a limit", detail: "Waiting for you to raise it or leave it." },
};

/** One inbox message as something waiting on the person. */
export function waitingOf(m: PlatformMessage, base?: string): HomeNotification {
  const wants = WANTS[m.kind];
  const subject = m.subject.trim().replace(/\.$/, "");
  return {
    kind: "inbox",
    id: m.id,
    messageId: m.id,
    from: m.from.name || ASSISTANT,
    summary: `${wants?.verb ?? "needs you"}: ${subject}.`,
    ...(wants ? { detail: wants.detail } : {}),
    at: m.at,
    url: inboxItemUrl(m.id, base),
  };
}

/** The first line of an alert's body, short enough for one row. The alert's page has all of it. */
export function oneLine(text: string, max = 140): string {
  const first = text.trim().split("\n")[0]?.trim() ?? "";
  return first.length > max ? `${first.slice(0, max - 1).trimEnd()}…` : first;
}

/** One open alert, as a notification that names its rule, severity and when it fired. */
export function alertOf(a: Alert, base?: string): HomeNotification {
  const title = a.title.trim().replace(/\.$/, "");
  return {
    kind: "alert",
    id: a.id,
    // Limits raise alerts with no rule behind them.
    from: a.rule || ((a.kind as string) === "budget" ? "Limits" : ASSISTANT),
    summary: `fired: ${title}.`,
    ...(a.body ? { detail: oneLine(a.body) } : {}),
    at: a.at,
    severity: a.severity === "critical" ? "critical" : "warning",
    url: alertUrl(a.id, base),
  };
}

/** The alerts an inbox message is about, by the alert part it carries. */
function alertsIn(m: PlatformMessage): string[] {
  return m.parts.flatMap((p) => (p.type === "alert" ? [p.alert.id] : []));
}

/** How many open alerts and inbox items Home is handed at most. It shows fewer; the rest are counted. */
export const NOTIFICATION_WINDOW = 30;

/**
 * Inbox items and open alerts, each linking to its own page. Every alert
 * raises an inbox message too; when the person sees the alert, that message
 * is left out so nothing is listed twice, and the inbox count drops by the
 * open alerts it already holds.
 */
export function notificationsOf(input: Pick<HomeInput, "me" | "waiting" | "waitingCount" | "alerts" | "publicUrl">): HomeNotifications {
  const can = (p: string) => (input.me.permissions ?? []).includes(p);
  const base = input.publicUrl;
  const readsInbox = can("runs:read");
  const open = can("alerts:read") && input.alerts ? input.alerts.filter((a) => !a.acked_by) : null;
  const shownAlerts = new Set((open ?? []).map((a) => a.id));
  const inbox = readsInbox
    ? input.waiting.filter((m) => !alertsIn(m).some((id) => shownAlerts.has(id))).slice(0, NOTIFICATION_WINDOW)
    : [];
  // Times arrive with their own offsets, so they are compared as instants.
  const alerts = [...(open ?? [])].sort((x, y) => Date.parse(y.at) - Date.parse(x.at)).slice(0, NOTIFICATION_WINDOW);
  return {
    items: [...alerts.map((a) => alertOf(a, base)), ...inbox.map((m) => waitingOf(m, base))],
    ...(readsInbox
      ? { inbox: { total: Math.max(inbox.length, input.waitingCount - (open?.length ?? 0)), url: consoleUrl("inbox", {}, base) } }
      : {}),
    ...(open ? { alerts: { total: open.length, url: consoleUrl("alerts", {}, base) } } : {}),
  };
}

/** Slack and GitHub, each connected when an enabled client answers on it. */
export function connectionsOf(clients: Client[], canConnect: boolean): HomeConnection[] {
  return SURFACES.map((s) => {
    const on = clients.find((c) => c.surface === s.id && c.enabled);
    const named = on && (s.id === "slack" ? on.identity.team : on.identity.owners?.join(", "));
    const account = named || on?.name;
    return {
      id: s.id,
      name: s.name,
      description: s.description,
      connected: on !== undefined,
      ...(account ? { account } : {}),
      canConnect,
    };
  });
}

export function buildHome(input: HomeInput): HomeModel {
  const { me } = input;
  const can = (p: string) => (me.permissions ?? []).includes(p);
  const counts: Partial<Record<HomeStartId, number>> = { review: input.waitingCount, runs: input.running };
  const starts: HomeStart[] = STARTS.filter((s) => can(s.needs)).map((s) => {
    const count = counts[s.id];
    return { id: s.id, ...(count ? { count } : {}) };
  });
  const canAsk = can("runs:write") && input.agents.length > 0;
  const base = input.publicUrl;
  return {
    person: { id: me.principal.subject, name: input.name || me.principal.email || me.principal.subject },
    assistant: { name: ASSISTANT },
    intro: "I run this workspace's agents.",
    // Only someone who may run agents is offered the ask box.
    ...(can("runs:write") ? { ask: { attach: canAsk, ...(input.notices?.length ? { notices: input.notices } : {}) } } : {}),
    starts,
    notifications: notificationsOf(input),
    ...(canAsk
      ? {
          answeredBy: {
            agents: input.agents.map((a) => ({ id: a.spec.name, name: a.spec.name })),
            selected: input.agents.some((a) => a.spec.name === input.answeredBy) ? input.answeredBy : ANY_AGENT,
          },
        }
      : {}),
    connections: input.clients ? connectionsOf(input.clients, can("clients:write")) : [],
    ...(can("clients:read") ? { connectionsUrl: consoleUrl("connections", {}, base) } : {}),
    access: accessOf(me, input.decision, `${DOCS}/authorization.md`),
    ...(base ? { consoleUrl: base } : {}),
    links: [
      { label: "Documentation", url: DOCS },
      { label: "API reference", url: `${DOCS}/api.md` },
    ],
  };
}

/** What the console does for one Home action. */
export type HomeIntent =
  | { kind: "ask"; text: string }
  | { kind: "attach" }
  | { kind: "answered_by"; agent: string }
  | { kind: "open"; view: "chat" | "agents" | "knowledge" | "inbox" | "runs" | "schedules" | "connections" }
  | { kind: "review"; message: string }
  | null;

const START_VIEW: Record<HomeStartId, Extract<HomeIntent, { kind: "open" }>["view"]> = {
  talk: "chat",
  build: "agents",
  search: "knowledge",
  review: "inbox",
  runs: "runs",
  schedule: "schedules",
};

export function homeIntent(a: UiAction): HomeIntent {
  const p = a.payload;
  switch (a.id) {
    case ActionId.homeAsk:
      // Nothing typed is a request to start a conversation, not to send one.
      return p.text?.trim() ? { kind: "ask", text: p.text.trim() } : { kind: "open", view: "chat" };
    case ActionId.homeStart: {
      const view = START_VIEW[p.target as HomeStartId];
      return view ? { kind: "open", view } : null;
    }
    case ActionId.homeAttach:
      return { kind: "attach" };
    case ActionId.homeAnsweredBy:
      return { kind: "answered_by", agent: p.option || ANY_AGENT };
    case ActionId.homeConnect:
      // Personal accounts are not a thing this platform has; workspace ones are set up under Connections.
      return { kind: "open", view: "connections" };
    case ActionId.open:
      return p.target ? { kind: "review", message: p.target } : null;
    default:
      return null;
  }
}
