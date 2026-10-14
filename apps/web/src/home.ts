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
  type HomeStart,
  type HomeStartId,
  type HomeWaiting,
  type UiAction,
} from "@vultus/core";
import { accessOf } from "./access.ts";
import type { AccessDecision, Client, Me, Revision } from "./api.ts";
import type { PlatformMessage } from "./mail.ts";

export const ASSISTANT = "Collegium";
const DOCS = "https://github.com/urmzd/collegium/blob/main/docs";

export interface HomeInput {
  me: Me;
  /** What the person calls themselves here, or their name from sign-in. */
  name: string;
  agents: Revision[];
  /** Open inbox messages, newest first. */
  waiting: PlatformMessage[];
  /** Open messages in the whole inbox, not only those loaded. */
  waitingCount: number;
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
export function waitingOf(m: PlatformMessage): HomeWaiting {
  const wants = WANTS[m.kind];
  const subject = m.subject.trim().replace(/\.$/, "");
  return {
    id: m.id,
    messageId: m.id,
    from: m.from.name || "Collegium",
    summary: `${wants?.verb ?? "needs you"}: ${subject}.`,
    ...(wants ? { detail: wants.detail } : {}),
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

/** A console link to a view, which the console opens in place. */
export function viewUrl(view: string, extra: Record<string, string> = {}): string {
  return `/?${new URLSearchParams({ view, ...extra })}`;
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
  return {
    person: { id: me.principal.subject, name: input.name || me.principal.email || me.principal.subject },
    assistant: { name: ASSISTANT },
    intro: "I run this workspace's agents.",
    ask: { attach: canAsk },
    starts,
    waiting: can("runs:read") ? input.waiting.map(waitingOf) : [],
    ...(canAsk
      ? {
          answeredBy: {
            agents: input.agents.map((a) => ({ id: a.spec.name, name: a.spec.name })),
            selected: input.agents.some((a) => a.spec.name === input.answeredBy) ? input.answeredBy : ANY_AGENT,
          },
        }
      : {}),
    connections: input.clients ? connectionsOf(input.clients, can("clients:write")) : [],
    ...(can("clients:read") ? { connectionsUrl: viewUrl("connections") } : {}),
    access: accessOf(me, input.decision, `${DOCS}/authorization.md`),
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

/** Where a console link goes, or null for a link that leaves the console. */
export function destinationOf(href: string, origin: string): { view: string; params: URLSearchParams } | null {
  let url: URL;
  try {
    url = new URL(href, origin);
  } catch {
    return null;
  }
  if (url.origin !== origin || url.pathname !== "/") return null;
  const view = url.searchParams.get("view");
  return view ? { view, params: url.searchParams } : null;
}
