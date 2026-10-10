import { messageState, openParts } from "@agent-ui/core";
import { fromServer, type PlatformMessage, type Transport } from "./mail.ts";

const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();

/**
 * One message of every kind the platform sends, in the shape it sends them.
 * They go through `fromServer`, so the gallery and the tests exercise the
 * same path the real inbox does.
 */
export function wireMail(): Record<string, unknown>[] {
  return [
    {
      id: "m-alert",
      kind: "alert",
      subject: "oncall is failing more than half its runs",
      from: { kind: "rule", name: "oncall-failure-rate" },
      at: ago(4),
      updated_at: ago(4),
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
    },
    {
      id: "m-failure",
      kind: "failure",
      subject: "A release run failed",
      from: { kind: "agent", name: "releaser" },
      at: ago(22),
      updated_at: ago(22),
      severity: "warning",
      state: "open",
      unread: true,
      source: { run: "api-7f3k", agent: "releaser" },
      parts: [
        { id: "p1", type: "text", state: "info", actions: [], text: "I could not tag 2.4.0. The tag already exists on the remote, so I stopped before pushing anything." },
        { id: "p2", type: "failure", state: "open", actions: ["retry", "acknowledge"], failure: { run: "api-7f3k", agent: "releaser", error: "git: tag 'v2.4.0' already exists", retryable: true } },
      ],
    },
    {
      id: "m-memory",
      kind: "memory",
      subject: "3 things to remember about on-call",
      from: { kind: "agent", name: "oncall" },
      at: ago(70),
      updated_at: ago(70),
      severity: "info",
      state: "open",
      unread: false,
      source: { agent: "oncall" },
      parts: [
        {
          id: "p1",
          type: "memory",
          state: "open",
          actions: ["keep", "discard"],
          memory: {
            owner: "agent:oncall",
            agent: "oncall",
            notes: [
              { id: "n1", text: "Checkout pages go to the payments rota, not the platform rota." },
              { id: "n2", text: "Incidents are summarized in the #incidents channel within an hour." },
              { id: "n3", text: "The staging cluster is rebuilt every Monday morning." },
            ],
          },
        },
      ],
    },
    {
      id: "m-budget",
      kind: "budget",
      subject: "Project Launch reached its daily run limit",
      from: { kind: "system", name: "Limits" },
      at: ago(130),
      updated_at: ago(130),
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
          budget: { scope_kind: "project", scope_name: "Launch", limit: "runs", period: "day", version: 2, current: { max_runs: 100 } },
        },
      ],
    },
    {
      id: "m-done",
      kind: "alert",
      subject: "An eval fell below 80%",
      from: { kind: "rule", name: "evals-below-80" },
      at: ago(1500),
      updated_at: ago(1400),
      severity: "warning",
      state: "resolved",
      unread: false,
      source: { alert: "al-0", rule: "evals-below-80" },
      parts: [
        {
          id: "p1",
          type: "alert",
          state: "resolved",
          actions: [],
          outcome: { action: "acknowledge", by: "Ada", at: ago(1400) },
          alert: { id: "al-0", rule: "evals-below-80", kind: "eval_below", severity: "warning", title: "triage-basics passed 7 of 10 cases" },
        },
      ],
    },
  ];
}

export const sampleMail = (): PlatformMessage[] => wireMail().map(fromServer);

const isOpen = (m: PlatformMessage) => messageState(m) === "open" && openParts(m).length > 0;

/**
 * An inbox held in memory, for the gallery and for tests. It pages, filters
 * and resolves the way the platform does, and `fail` makes the next call of
 * that name reject, to see what the screen does when the platform says no.
 */
export function memoryTransport(seed: PlatformMessage[] = sampleMail(), size = 30): Transport & { fail: Set<string>; all: () => PlatformMessage[] } {
  let all = [...seed].sort((a, b) => b.at.localeCompare(a.at));
  const fail = new Set<string>();
  const guard = (name: string) => {
    if (fail.delete(name)) throw new Error("The platform refused.");
  };
  return {
    fail,
    all: () => all,
    list: async (q) => {
      guard("list");
      const state = q.state ?? "open";
      const kept = all.filter((m) => (state === "all" || (state === "open") === isOpen(m)) && (!q.kind || m.kind === q.kind));
      const from = q.before ? kept.findIndex((m) => m.id === q.before) + 1 : 0;
      const messages = kept.slice(from, from + Math.min(q.limit, size));
      const last = messages[messages.length - 1];
      return {
        messages,
        next: last && from + messages.length < kept.length ? last.id : null,
        open: all.filter(isOpen).length,
        unread: all.filter((m) => m.unread).length,
      };
    },
    get: async (id) => {
      guard("get");
      const found = all.find((m) => m.id === id);
      if (!found) throw new Error("No such message.");
      return found;
    },
    send: async (message, r) => {
      guard(r.call);
      const now = new Date().toISOString();
      const was = all.find((m) => m.id === message.id) ?? message;
      const next: PlatformMessage =
        r.call === "read"
          ? { ...was, unread: false }
          : r.call === "dismiss"
            ? { ...was, unread: false, dismissedAt: now }
            : r.call === "snooze"
              ? { ...was, unread: false, snoozedUntil: r.until }
              : {
                  ...was,
                  unread: false,
                  parts: was.parts.map((p) => (p.id === r.partId ? { ...p, resolution: { outcome: r.action, by: "You", at: now } } : p)),
                };
      all = all.map((m) => (m.id === next.id ? next : m));
      return next;
    },
  };
}
