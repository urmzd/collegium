import assert from "node:assert/strict";
import { test } from "node:test";
import { ActionId, ANY_AGENT, homeAction, type UiAction } from "@vultus/core";
import { Home } from "@vultus/components";
import { WebSurface } from "@vultus/render-web";
import { renderToStaticMarkup } from "react-dom/server";
import { PERMISSIONS } from "./access.ts";
import type { AccessDecision, Alert, Client, Me, Revision } from "./api.ts";
import { ASSISTANT, alertOf, buildHome, connectionsOf, type HomeInput, homeIntent, notificationsOf, waitingOf } from "./home.ts";
import type { PlatformMessage } from "./mail.ts";

const reads = PERMISSIONS.filter((p) => p.endsWith(":read"));
const person = (roles: string[], permissions: string[]): Me => ({
  principal: { subject: "00u-ada", tenant: "acme", kind: "user", name: "Ada Lovelace", roles },
  permissions,
});
const operator = person(["viewer", "operator"], [...reads, "runs:write", "schedules:write", "alerts:write", "evals:write", "artifacts:write"]);
const viewer = person(["viewer"], [...reads]);

const agent = (name: string) => ({ spec: { name } }) as Revision;
const message = (id: string, kind: string, subject: string, alert?: string): PlatformMessage =>
  ({
    id,
    kind,
    subject,
    from: { kind: "agent", name: "oncall" },
    at: "2026-10-13T00:00:00Z",
    parts: alert ? [{ id: "p1", type: "alert", alert: { id: alert, service: "deploys", status: "down", title: subject, startedAt: "2026-10-13T00:00:00Z", updatedAt: "2026-10-13T00:00:00Z" } }] : [],
    about: [],
    budgets: {},
  }) as PlatformMessage;
const alert = (id: string, over: Partial<Alert> = {}): Alert => ({
  id,
  rule: "deploys",
  kind: "run_failed",
  severity: "critical",
  title: `Run of deployer failed (${id})`,
  at: "2026-10-13T01:00:00Z",
  ...over,
});
const slack = {
  id: "support",
  name: "Support",
  surface: "slack",
  identity: { team: "T01ACME" },
  enabled: true,
} as Client;
const decision: AccessDecision = {
  permission: "runs:write",
  allowed: true,
  outcome: "allowed",
  role: "operator",
  held: [{ role: "operator", grant: 'grant operator to group "eng-oncall"', group: "eng-oncall" }],
};

const input = (over: Partial<HomeInput> = {}): HomeInput => ({
  me: operator,
  name: "Ada Lovelace",
  agents: [agent("helper"), agent("oncall")],
  waiting: [message("m1", "memory", "3 things to remember about on-call.")],
  waitingCount: 2,
  alerts: [],
  running: 1,
  clients: [slack],
  decision,
  answeredBy: ANY_AGENT,
  ...over,
});

test("buildHome: the operator's home, every field from what the console holds", () => {
  const h = buildHome(input());
  assert.deepEqual(h.person, { id: "00u-ada", name: "Ada Lovelace" });
  assert.equal(h.assistant.name, ASSISTANT);
  assert.deepEqual(
    h.starts.map((s) => [s.id, s.count]),
    [
      ["talk", undefined],
      ["build", undefined],
      ["search", undefined],
      ["review", 2],
      ["runs", 1],
      ["schedule", undefined],
    ],
  );
  assert.deepEqual(h.notifications.items, [
    {
      kind: "inbox",
      id: "m1",
      messageId: "m1",
      from: "oncall",
      summary: "wants notes reviewed: 3 things to remember about on-call.",
      detail: "Waiting for your approval.",
      at: "2026-10-13T00:00:00Z",
      url: "/?view=inbox&message=m1",
    },
  ]);
  assert.deepEqual(h.answeredBy, { agents: [{ id: "helper", name: "helper" }, { id: "oncall", name: "oncall" }], selected: ANY_AGENT });
  assert.equal(h.ask?.attach, true);
  assert.deepEqual(
    h.connections.map((c) => [c.id, c.connected, c.account, c.canConnect]),
    [
      ["slack", true, "T01ACME", false],
      ["github", false, undefined, false],
    ],
  );
  assert.equal(h.connectionsUrl, "/?view=connections");
  assert.equal(h.access.role, "operator");
  assert.equal(h.access.group, "eng-oncall");
});

test("buildHome: a viewer is offered nothing that needs a change it cannot make", () => {
  const h = buildHome(input({ me: viewer, decision: null }));
  assert.ok(!h.starts.some((s) => s.id === "talk"), "a viewer cannot start a conversation");
  assert.equal(h.answeredBy, undefined);
  assert.equal(h.ask, undefined, "no ask box for someone who cannot run agents");
  assert.equal(h.access.role, "viewer");
  assert.equal(h.access.denies, "run agents");
});

test("buildHome: someone who reads nothing gets no starts, no waiting and no connections", () => {
  const h = buildHome(input({ me: person([], []), clients: null, decision: null }));
  assert.deepEqual(h.starts, []);
  assert.deepEqual(h.notifications, { items: [] });
  assert.deepEqual(h.connections, []);
  assert.equal(h.connectionsUrl, undefined);
  assert.equal(h.access.role, undefined);
});

test("buildHome: a chosen agent that is gone falls back to whoever fits best", () => {
  assert.equal(buildHome(input({ answeredBy: "ghost" })).answeredBy?.selected, ANY_AGENT);
  assert.equal(buildHome(input({ answeredBy: "oncall" })).answeredBy?.selected, "oncall");
});

test("waitingOf: an unknown kind still reads as a sentence", () => {
  assert.equal(waitingOf(message("m2", "other", "Look at this")).summary, "needs you: Look at this.");
});

test("connectionsOf: a disabled client is not a connection; an admin may connect", () => {
  const c = connectionsOf([{ ...slack, enabled: false }], true);
  assert.equal(c[0]?.connected, false);
  assert.equal(c[0]?.canConnect, true);
  const gh = connectionsOf([{ ...slack, surface: "github", identity: { owners: ["acme", "acme-labs"] } } as Client], false);
  assert.equal(gh[1]?.account, "acme, acme-labs");
});

test("homeIntent: every Home action goes somewhere", () => {
  const typed: UiAction = { ...homeAction.ask(), payload: { ...homeAction.ask().payload, text: " why? " } };
  assert.deepEqual(homeIntent(typed), { kind: "ask", text: "why?" });
  assert.deepEqual(homeIntent(homeAction.ask()), { kind: "open", view: "chat" });
  assert.deepEqual(homeIntent(homeAction.start("build")), { kind: "open", view: "agents" });
  assert.deepEqual(homeIntent(homeAction.start("search")), { kind: "open", view: "knowledge" });
  assert.deepEqual(homeIntent(homeAction.start("review")), { kind: "open", view: "inbox" });
  assert.deepEqual(homeIntent(homeAction.start("runs")), { kind: "open", view: "runs" });
  assert.deepEqual(homeIntent(homeAction.start("schedule")), { kind: "open", view: "schedules" });
  assert.deepEqual(homeIntent(homeAction.start("talk")), { kind: "open", view: "chat" });
  assert.deepEqual(homeIntent(homeAction.attach()), { kind: "attach" });
  assert.deepEqual(homeIntent(homeAction.answeredBy("oncall")), { kind: "answered_by", agent: "oncall" });
  assert.deepEqual(homeIntent(homeAction.answeredBy()), { kind: "answered_by", agent: ANY_AGENT });
  assert.deepEqual(homeIntent(homeAction.connect("slack")), { kind: "open", view: "connections" });
  assert.deepEqual(homeIntent(homeAction.review({ id: "m1" })), { kind: "review", message: "m1" });
  assert.equal(homeIntent({ id: ActionId.retry, payload: { turnId: "t", target: "x" } }), null);
});

const reader = person(["viewer"], ["runs:read"]);

test("notificationsOf: who sees what, and where each one links", async (t) => {
  const cases: { name: string; over: Partial<HomeInput>; items: string[]; inbox?: number; alerts?: number }[] = [
    {
      name: "alerts and inbox together, the inbox item about an open alert left out",
      over: {
        waiting: [message("m1", "memory", "Notes"), message("m2", "alert", "Run of deployer failed", "al-1")],
        waitingCount: 2,
        alerts: [alert("al-1"), alert("al-2", { acked_by: "dana" })],
      },
      items: ["alert:al-1 /?view=alerts&alert=al-1", "inbox:m1 /?view=inbox&message=m1"],
      inbox: 1,
      alerts: 1,
    },
    {
      name: "alerts only",
      over: { waiting: [], waitingCount: 0, alerts: [alert("al-1", { severity: "warning" })] },
      items: ["alert:al-1 /?view=alerts&alert=al-1"],
      inbox: 0,
      alerts: 1,
    },
    {
      name: "nothing",
      over: { waiting: [], waitingCount: 0, alerts: [] },
      items: [],
      inbox: 0,
      alerts: 0,
    },
    {
      name: "no alerts without alerts:read, even when the console holds some",
      over: { me: reader, waiting: [message("m1", "failure", "A run failed")], waitingCount: 1, alerts: [alert("al-1")] },
      items: ["inbox:m1 /?view=inbox&message=m1"],
      inbox: 1,
    },
    {
      name: "an inbox item about an alert the person cannot see stays",
      over: { me: reader, waiting: [message("m2", "budget", "Refused", "al-1")], waitingCount: 1, alerts: null },
      items: ["inbox:m2 /?view=inbox&message=m2"],
      inbox: 1,
    },
    {
      name: "more open than loaded: the totals come from the server",
      over: { waiting: [message("m1", "memory", "Notes")], waitingCount: 40, alerts: [alert("al-1")] },
      items: ["alert:al-1 /?view=alerts&alert=al-1", "inbox:m1 /?view=inbox&message=m1"],
      inbox: 39,
      alerts: 1,
    },
  ];
  for (const c of cases) {
    await t.test(c.name, () => {
      const n = notificationsOf({ ...input(), ...c.over });
      assert.deepEqual(
        n.items.map((i) => `${i.kind}:${i.id} ${i.url}`),
        c.items,
      );
      assert.equal(n.inbox?.total, c.inbox);
      assert.equal(n.alerts?.total, c.alerts);
      if (c.inbox !== undefined) assert.equal(n.inbox?.url, "/?view=inbox");
      if (c.alerts !== undefined) assert.equal(n.alerts?.url, "/?view=alerts");
    });
  }
});

test("notificationsOf: with a public URL every link is absolute, for Slack", () => {
  const n = notificationsOf({ ...input(), alerts: [alert("al-1")], publicUrl: "https://collegium.example.com/" });
  assert.deepEqual(
    n.items.map((i) => i.url),
    ["https://collegium.example.com/?view=alerts&alert=al-1", "https://collegium.example.com/?view=inbox&message=m1"],
  );
  assert.equal(n.inbox?.url, "https://collegium.example.com/?view=inbox");
  assert.equal(n.alerts?.url, "https://collegium.example.com/?view=alerts");
  assert.equal(buildHome({ ...input(), publicUrl: "https://collegium.example.com" }).consoleUrl, "https://collegium.example.com");
  assert.equal(buildHome(input()).consoleUrl, undefined);
});

test("alertOf: severity, rule and time from the alert; a limit with no rule is named", () => {
  assert.deepEqual(alertOf(alert("al-7", { body: "Exit 1.", severity: "warning" })), {
    kind: "alert",
    id: "al-7",
    from: "deploys",
    summary: "fired: Run of deployer failed (al-7).",
    detail: "Exit 1.",
    at: "2026-10-13T01:00:00Z",
    severity: "warning",
    url: "/?view=alerts&alert=al-7",
  });
  assert.equal(alertOf(alert("al-8", { rule: "", kind: "budget" as Alert["kind"] })).from, "Limits");
  const long = alertOf(alert("al-9", { body: `${"x".repeat(300)}\nsecond line` })).detail ?? "";
  assert.equal(long.length, 140);
  assert.ok(long.endsWith("…"));
  assert.equal(alertOf(alert("al-10", { body: "First.\nSecond." })).detail, "First.");
});

test("buildHome: ask errors and attached files go under the ask box", () => {
  const notices = [{ tone: "bad" as const, text: "The question could not be sent." }];
  assert.deepEqual(buildHome(input({ notices })).ask?.notices, notices);
  assert.equal(buildHome(input()).ask?.notices, undefined);
});

test("Home renders the model: greeting, starts, notifications and why", () => {
  const markup = renderToStaticMarkup(
    <WebSurface>
      <Home home={buildHome(input({ alerts: [alert("al-1")], notices: [{ tone: "bad", text: "Could not send." }] }))} />
    </WebSurface>,
  );
  assert.match(markup, /<h1[^>]*>Hi Ada, I&#x27;m Collegium\.<\/h1>/);
  // Each notification's title links to its own page, in this tab.
  assert.match(markup, /<a href="\/\?view=alerts&amp;alert=al-1"><strong>deploys<\/strong> fired: Run of deployer failed \(al-1\)\.<\/a>/);
  assert.match(markup, /<a href="\/\?view=inbox&amp;message=m1">/);
  assert.match(markup, /Critical alert/);
  assert.doesNotMatch(markup, /target="_blank"[^>]*href="\//);
  assert.match(markup, /role="alert"[^>]*>.*Could not send\./);
  assert.match(markup, /Review what needs you/);
  assert.match(markup, /wants notes reviewed/);
  assert.match(markup, /eng-oncall/);
  assert.match(markup, /href="\/\?view=access&amp;permission=runs%3Awrite"/);
  const forViewer = renderToStaticMarkup(
    <WebSurface>
      <Home home={buildHome(input({ me: viewer, decision: null }))} />
    </WebSurface>,
  );
  assert.doesNotMatch(forViewer, /Talk to an agent/);
  assert.doesNotMatch(forViewer, /What should we work on\?/);
});
