import assert from "node:assert/strict";
import { test } from "node:test";
import { ActionId, ANY_AGENT, homeAction, type UiAction } from "@vultus/core";
import { Home } from "@vultus/components";
import { WebSurface } from "@vultus/render-web";
import { renderToStaticMarkup } from "react-dom/server";
import { PERMISSIONS } from "./access.ts";
import type { AccessDecision, Client, Me, Revision } from "./api.ts";
import { ASSISTANT, buildHome, connectionsOf, destinationOf, type HomeInput, homeIntent, waitingOf } from "./home.ts";
import type { PlatformMessage } from "./mail.ts";

const reads = PERMISSIONS.filter((p) => p.endsWith(":read"));
const person = (roles: string[], permissions: string[]): Me => ({
  principal: { subject: "00u-ada", tenant: "acme", kind: "user", name: "Ada Lovelace", roles },
  permissions,
});
const operator = person(["viewer", "operator"], [...reads, "runs:write", "schedules:write", "alerts:write", "evals:write", "artifacts:write"]);
const viewer = person(["viewer"], [...reads]);

const agent = (name: string) => ({ spec: { name } }) as Revision;
const message = (id: string, kind: string, subject: string): PlatformMessage =>
  ({ id, kind, subject, from: { kind: "agent", name: "oncall" }, at: "2026-10-13T00:00:00Z", parts: [], about: [], budgets: {} }) as PlatformMessage;
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
  assert.deepEqual(h.waiting, [
    { id: "m1", messageId: "m1", from: "oncall", summary: "wants notes reviewed: 3 things to remember about on-call.", detail: "Waiting for your approval." },
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
  assert.equal(h.ask?.attach, false);
  assert.equal(h.access.role, "viewer");
  assert.equal(h.access.denies, "run agents");
});

test("buildHome: someone who reads nothing gets no starts, no waiting and no connections", () => {
  const h = buildHome(input({ me: person([], []), clients: null, decision: null }));
  assert.deepEqual(h.starts, []);
  assert.deepEqual(h.waiting, []);
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
  assert.deepEqual(homeIntent(homeAction.review({ id: "m1", from: "oncall", summary: "x" })), { kind: "review", message: "m1" });
  assert.equal(homeIntent({ id: ActionId.retry, payload: { turnId: "t", target: "x" } }), null);
});

test("destinationOf: only links into the console are opened in place", () => {
  const origin = "http://127.0.0.1:5173";
  assert.equal(destinationOf("/?view=access&permission=runs:write", origin)?.view, "access");
  assert.equal(destinationOf("/?view=access&permission=runs:write", origin)?.params.get("permission"), "runs:write");
  assert.equal(destinationOf("https://github.com/urmzd/collegium", origin), null);
  assert.equal(destinationOf("/auth/login", origin), null);
  assert.equal(destinationOf("/?message=m1", origin), null);
});

test("Home renders the model: greeting, starts, the waiting item and why", () => {
  const markup = renderToStaticMarkup(
    <WebSurface>
      <Home home={buildHome(input())} />
    </WebSurface>,
  );
  assert.match(markup, /Hi Ada, I&#x27;m Collegium\./);
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
});
