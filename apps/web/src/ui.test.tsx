import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { Run } from "./adapter.ts";
import { ActivityPanel, ordered, ordinals, trailOf } from "./Activity.tsx";
import { isEnded } from "./format.ts";
import { sampleTrees } from "./activity.fixtures.ts";
import { Alerts } from "./Alerts.tsx";
import { StorageUsed, StoredDetail, StoredList } from "./Storage.tsx";
import { bytes, contentUrl, matches, showsInline } from "./storage.ts";
import { memoryStorage, sampleFiles, sampleUse } from "./storage.fixtures.ts";
import { RunItem, runManifest, runMarkdown, statusOf } from "./RunItem.tsx";
import { CopyAs } from "./CopyAs.tsx";
import { itemActions, itemOf } from "./itemActions.tsx";
import { AgentMessage } from "@agent-ui/web";
import { WebSurface } from "@agent-ui/render-web";
import { replay } from "./Runs.tsx";
import { formats } from "./copyas.ts";
import { emptyTurn } from "@agent-ui/core";
import type { Alert, AlertRule, Me, Preferences, Settings } from "./api.ts";
import { Onboarding, welcome } from "./Onboarding.tsx";
import { ActionId, messageState, partTarget, type UiAction } from "@agent-ui/core";
import { after, merge } from "./inbox.ts";
import { edgeOf, MailboxView } from "./InboxPage.tsx";
import { fromServer, type PlatformMessage, reached, toRequest } from "./mail.ts";
import { memoryTransport, sampleMail, wireMail } from "./mail.fixtures.ts";
import { counted, dollars, LimitField, PolicyBadge, parseDollars, Stroke, strokeOf, UsageMeter, usageOf } from "./ui.tsx";

const M = 1_000_000;
const html = (el: ReactElement) => renderToStaticMarkup(el);
/** The value of an attribute on the first element that has it. */
const attr = (markup: string, name: string) => markup.match(new RegExp(`${name}="([^"]*)"`))?.[1];

test("usageOf: none without a limit", () => {
  assert.equal(usageOf(5 * M, 0), "none");
  assert.equal(usageOf(5 * M, null), "none");
  assert.equal(usageOf(5 * M, undefined), "none");
});

test("usageOf: the warning starts exactly at the threshold", () => {
  assert.equal(usageOf(79.99 * M, 100 * M), "under");
  assert.equal(usageOf(80 * M, 100 * M), "warning");
  assert.equal(usageOf(50 * M, 100 * M, 0.5), "warning");
  assert.equal(usageOf(49 * M, 100 * M, 0.5), "under");
});

test("usageOf: at the limit is a warning, past it is over", () => {
  assert.equal(usageOf(100 * M, 100 * M), "warning");
  assert.equal(usageOf(100 * M + 1, 100 * M), "over");
});

test("UsageMeter: a real meter with its values", () => {
  const out = html(<UsageMeter label="Project Launch" spent={12 * M} limit={50 * M} period="month" />);
  assert.equal(attr(out, "role"), "meter");
  assert.equal(attr(out, "aria-label"), "Project Launch");
  assert.equal(attr(out, "aria-valuemin"), "0");
  assert.equal(attr(out, "aria-valuemax"), String(50 * M));
  assert.equal(attr(out, "aria-valuenow"), String(12 * M));
  assert.match(attr(out, "aria-valuetext") ?? "", /12\.00 of .*50\.00 a month, 24%\. Under limit\./);
  assert.equal(attr(out, "data-testid"), "usage-meter");
});

test("UsageMeter: each state is a data attribute and is written out", () => {
  const state = (spent: number, limit?: number) => html(<UsageMeter label="x" spent={spent} limit={limit} />);
  assert.equal(attr(state(1 * M, 10 * M), "data-usage"), "under");
  assert.match(state(1 * M, 10 * M), /Under limit/);
  assert.equal(attr(state(9 * M, 10 * M), "data-usage"), "warning");
  assert.match(state(9 * M, 10 * M), /Near limit/);
  assert.equal(attr(state(12 * M, 10 * M), "data-usage"), "over");
  assert.match(state(12 * M, 10 * M), /Over by .*2\.00/);
  assert.equal(attr(state(3 * M), "data-usage"), "none");
  assert.match(state(3 * M), /No limit/);
});

test("UsageMeter: exactly at the limit says so, and is still the warning state", () => {
  const out = html(<UsageMeter label="Runs" spent={10} limit={10} format={counted("runs")} />);
  assert.equal(attr(out, "data-usage"), "warning");
  assert.match(out, />At limit</);
  assert.match(attr(out, "aria-valuetext") ?? "", /100%\. At limit\./);
  assert.doesNotMatch(out, /Near limit/);
});

test("UsageMeter: a row shows its label only when asked", () => {
  assert.doesNotMatch(html(<UsageMeter label="Launch spend" spent={1 * M} limit={5 * M} />), />Launch spend</);
  assert.match(html(<UsageMeter labelled label="Launch spend" spent={1 * M} limit={5 * M} />), />Launch spend</);
});

test("UsageMeter: over the limit, the value stops at the limit and the limit line is drawn", () => {
  const out = html(<UsageMeter label="x" spent={12 * M} limit={10 * M} />);
  assert.equal(attr(out, "aria-valuenow"), String(10 * M));
  assert.match(out, /oap-meter-limit/);
  assert.match(attr(out, "aria-valuetext") ?? "", /Over limit by .*2\.00/);
});

test("UsageMeter: no limit draws no fill", () => {
  assert.doesNotMatch(html(<UsageMeter label="x" spent={3 * M} />), /oap-meter-fill/);
});

test("UsageMeter: a projected value is drawn and read out only when it is ahead of what is spent", () => {
  const ahead = html(<UsageMeter label="x" spent={40 * M} limit={50 * M} projected={58 * M} />);
  assert.match(ahead, /data-testid="usage-meter-projected"/);
  assert.match(attr(ahead, "aria-valuetext") ?? "", /Heading for .*58\.00/);
  assert.match(ahead, /oap-meter-limit/);
  const behind = html(<UsageMeter label="x" spent={40 * M} limit={50 * M} projected={30 * M} />);
  assert.doesNotMatch(behind, /usage-meter-projected/);
});

test("UsageMeter: counts are written with their unit", () => {
  const out = html(<UsageMeter label="Runs" spent={840} limit={1000} format={counted("runs")} />);
  assert.equal(attr(out, "data-usage"), "warning");
  assert.match(attr(out, "aria-valuetext") ?? "", /^840 runs of 1,000 runs, 84%\. Near limit\./);
});

test("strokeOf: every run status has a stroke state", () => {
  const want: Record<Run["status"], string> = {
    accepted: "working",
    running: "working",
    parked: "waiting",
    completed: "done",
    failed: "failed",
    // Stopped from outside: at rest, not working and not a failure.
    stopped: "idle",
  };
  for (const [status, state] of Object.entries(want)) assert.equal(strokeOf(status as Run["status"]), state);
});

test("Stroke: an agent's stroke carries its hue and state", () => {
  const out = html(
    <Stroke agent="oncall" state="waiting" edge="around">
      x
    </Stroke>,
  );
  assert.equal(attr(out, "data-agent"), "oncall");
  assert.equal(attr(out, "data-state"), "waiting");
  assert.equal(attr(out, "data-edge"), "around");
  assert.match(out, /--oap-hue:\d+/);
});

test("Stroke: without an agent it is neutral and has no state", () => {
  const out = html(<Stroke state="working">x</Stroke>);
  assert.doesNotMatch(out, /data-agent|data-state|--oap-hue/);
  assert.equal(attr(out, "data-edge"), "start");
});

test("Stroke: waiting never animates, in markup or in the stylesheet", () => {
  const out = html(
    <Stroke agent="oncall" state="waiting">
      x
    </Stroke>,
  );
  assert.doesNotMatch(out, /animate|aui-shimmer|aui-spinner/);
  const css = readFileSync(new URL("./styles.css", import.meta.url), "utf8");
  const rules = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].filter(([, selector]) => selector?.includes(".oap-stroke"));
  assert.ok(rules.length > 5, "the stroke rules were found");
  for (const [, selector = "", body = ""] of rules) {
    if (selector.includes('"waiting"')) assert.doesNotMatch(body, /animation/, `waiting animates in: ${selector.trim()}`);
  }
  assert.ok(
    rules.some(([, selector = "", body = ""]) => selector.includes('"working"') && /animation:\s*oap-stroke-work/.test(body)),
    "working is the state that animates",
  );
});

test("parseDollars: amounts become micro-USD", () => {
  assert.equal(parseDollars("49.50"), 49_500_000);
  assert.equal(parseDollars("49.5"), 49_500_000);
  assert.equal(parseDollars(" $1,250 "), 1_250_000_000);
  assert.equal(parseDollars(".5"), 500_000);
  assert.equal(parseDollars("0"), 0);
  assert.equal(parseDollars("0.07"), 70_000);
});

test("parseDollars: empty is no limit, and anything else is refused", () => {
  assert.equal(parseDollars(""), null);
  assert.equal(parseDollars("   "), null);
  for (const bad of ["abc", "1.234", "-5", "1e3", "12 dollars", "."]) assert.equal(parseDollars(bad), undefined, bad);
});

test("dollars and parseDollars round-trip", () => {
  for (const micro of [0, 70_000, 49_500_000, 1_250_000_000]) assert.equal(parseDollars(dollars(micro)), micro);
  assert.match(dollars(0), /0\.00/);
});

test("LimitField: empty shows what it inherits", () => {
  const out = html(
    <LimitField
      label="Project limit"
      value={null}
      period="month"
      inherits={{ scope: "the workspace", value: 50 * M, period: "month" }}
      onChange={() => {}}
    />,
  );
  assert.equal(attr(out, "data-empty"), "true");
  assert.match(out, /Inherits .*50\.00 a month from the workspace\./);
  assert.equal(attr(out, "aria-invalid"), "false");
});

test("LimitField: empty with nothing to inherit says there is no limit", () => {
  const out = html(<LimitField label="Workspace limit" value={null} period="day" onChange={() => {}} />);
  assert.match(out, /No limit\./);
  assert.doesNotMatch(out, /Inherits/);
});

test("LimitField: a set limit shows its dollars and period, and no inherits line", () => {
  const out = html(
    <LimitField
      label="Project limit"
      value={250 * M}
      period="day"
      inherits={{ scope: "the workspace", value: 50 * M, period: "month" }}
      onChange={() => {}}
    />,
  );
  assert.equal(attr(out, "data-empty"), "false");
  assert.match(out, /value="250"/);
  assert.match(out, /a day/);
  assert.doesNotMatch(out, /Inherits/);
});

test("PolicyBadge: says the scope and what happens at the limit", () => {
  const block = html(<PolicyBadge scope="Project" name="Launch" action="block" />);
  assert.equal(attr(block, "data-action"), "block");
  assert.match(block, /Project Launch/);
  assert.match(block, /blocks at the limit/);
  const approve = html(<PolicyBadge scope="Workspace" action="approve" />);
  assert.equal(attr(approve, "data-action"), "approve");
  assert.match(approve, /asks for approval/);
});

const person: Me = { principal: { subject: "u1", tenant: "default", kind: "user", name: "Ada Lovelace", email: "ada@example.com" }, permissions: [] };
const fresh: Preferences = { display_name: "", theme: "system", accent: "purple", has_photo: false, photo_version: 0 };
const space = { baseline: "", history: "recent", history_limit: 10, projects: [], title_agent: "", memory_agent: "", memory_auto_approve: false } as Settings;
const onboarding = (over: Partial<Parameters<typeof Onboarding>[0]> = {}) =>
  html(
    <Onboarding me={person} prefs={fresh} workspace={space} firstAdmin={false} onPrefs={() => {}} onWorkspace={() => {}} onDone={() => {}} {...over} />,
  );

test("welcome: greets by first name, and by time of day", () => {
  assert.equal(welcome("Ada Lovelace", 9), "Good morning, Ada");
  assert.equal(welcome("  Ada  ", 13), "Good afternoon, Ada");
  assert.equal(welcome("", 20), "Good evening");
});

test("Onboarding: the name is asked for, labelled, required and prefilled from sign-in", () => {
  const out = onboarding();
  assert.match(out, /<label[^>]*>What should we call you\?<\/label>/);
  assert.match(out, /<input[^>]*required[^>]*value="Ada Lovelace"|<input[^>]*value="Ada Lovelace"[^>]*required/);
  assert.match(out, /Good (morning|afternoon|evening), Ada/);
});

test("Onboarding: with no name from sign-in, the email's first part is offered", () => {
  const out = onboarding({ me: { ...person, principal: { ...person.principal, name: undefined } } });
  assert.match(out, /value="ada"/);
});

test("Onboarding: a name already chosen wins over sign-in", () => {
  assert.match(onboarding({ prefs: { ...fresh, display_name: "Countess" } }), /value="Countess"/);
});

test("Onboarding: everyone gets the personal part, with one main action and a way to skip", () => {
  const out = onboarding();
  assert.equal(attr(out, "data-first-admin"), "false");
  assert.match(out, /Upload Picture/);
  assert.match(out, /aria-pressed="true"[^>]*>System</);
  assert.equal(out.match(/type="submit"/g)?.length, 1);
  assert.match(out, />Get Started</);
  assert.match(out, />Skip for Now</);
  assert.doesNotMatch(out, /onboarding-workspace|Workspace name|first admin/);
});

test("Onboarding: the first admin also names the workspace and is told what an admin is", () => {
  const out = onboarding({ firstAdmin: true });
  assert.equal(attr(out, "data-first-admin"), "true");
  assert.match(out, /data-testid="onboarding-workspace"/);
  assert.match(out, /You are the first admin of this workspace/);
  assert.match(out, /<label[^>]*>Workspace name<\/label>/);
  assert.match(out, /<label[^>]*>First project<\/label>/);
  assert.match(out, /limits are set in Settings/);
  assert.doesNotMatch(out, /usage-meter|limit-field/);
});

test("Onboarding: every input has a label", () => {
  const out = onboarding({ firstAdmin: true });
  const labelled = new Set([...out.matchAll(/<label[^>]*for="([^"]+)"/g)].map((m) => m[1]));
  for (const [tag] of out.matchAll(/<input[^>]*>/g)) {
    const id = tag.match(/ id="([^"]+)"/)?.[1];
    assert.ok((id && labelled.has(id)) || /aria-label="/.test(tag), `unlabelled input: ${tag}`);
  }
});

const mailOf = (id: string): PlatformMessage => {
  const found = sampleMail().find((m) => m.id === id);
  assert.ok(found, id);
  return found;
};
const click = (message: PlatformMessage, id: UiAction["id"], target: string, extra: Partial<UiAction["payload"]> = {}): UiAction => ({
  id,
  payload: { turnId: message.id, target, ...extra },
});
const mailbox = (selected: string | null, messages = sampleMail()) =>
  html(<MailboxView messages={messages} selectedId={selected} onSelect={() => {}} onAction={() => {}} onOpen={() => {}} viewer="Ada" />);

test("fromServer: the platform's alert becomes a mailbox message", () => {
  const mail = mailOf("m-alert");
  assert.equal(mail.unread, true);
  assert.equal(mail.severity, "bad");
  assert.equal(mail.kind, "alert");
  assert.deepEqual(mail.about.map((s) => `${s.kind}:${s.id}`), ["rule:oncall-failure-rate", "agent:oncall"]);
  assert.equal(mail.source?.kind, "rule");
  const [part] = mail.parts;
  assert.ok(part?.type === "alert");
  assert.deepEqual(part.allowed, [ActionId.acknowledge], "what this reader may do rides on the part");
  assert.equal(part.alert.title, "6 of 10 runs failed in the last 15 minutes");
  assert.equal(part.alert.service, "oncall-failure-rate");
  assert.equal(part.alert.status, "down");
  assert.equal(messageState(mail), "open");
});

test("fromServer: failure, memory and a limit, and an outcome as a resolution", () => {
  const failure = mailOf("m-failure").parts[1];
  assert.ok(failure?.type === "failure" && failure.failure.reference === "api-7f3k" && failure.failure.retryable === true);
  const memory = mailOf("m-memory").parts[0];
  assert.ok(memory?.type === "memory" && memory.proposals.length === 3);
  const limit = mailOf("m-budget");
  const budget = limit.parts[0];
  assert.ok(budget?.type === "budget");
  assert.deepEqual(budget.meter, { used: 100, limit: 100, unit: "runs", period: "day" });
  assert.deepEqual(budget.allowed, [ActionId.raiseBudget, ActionId.acknowledge]);
  assert.equal(limit.tag, "Limit", "tagged by what it is about");
  const data = limit.budgets.p1;
  assert.ok(data);
  assert.deepEqual(reached(data), { field: "max_runs", value: 100 });
  assert.deepEqual(data.scope, { kind: "project", name: "Launch" });
  assert.equal(data.refused, true);

  // A warning says how much is used, and that nothing was turned away.
  const [raw] = wireMail().filter((m) => m.id === "m-budget");
  const warning = fromServer({
    ...raw,
    parts: [{ id: "p1", type: "budget", actions: ["acknowledge"], budget: { scope_kind: "project", scope_name: "Launch", limit: "runs", period: "day", version: 2, current: { max_runs: 100 }, used: 82, max: 100, refused: false } }],
  });
  const [soft] = warning.parts;
  assert.ok(soft?.type === "budget");
  assert.deepEqual(soft.meter, { used: 82, limit: 100, unit: "runs", period: "day" });
  assert.equal(warning.budgets.p1?.refused, false);
  const drawn = html(<MailboxView messages={[warning]} selectedId={warning.id} onSelect={() => {}} onAction={() => {}} onOpen={() => {}} />);
  assert.match(drawn, /close to its limit on runs\. Nothing has been refused yet\./);
  assert.match(drawn, /82 runs of 100 runs a day, 82%\. Near limit\./);

  const done = mailOf("m-done");
  assert.equal(messageState(done), "resolved");
  const [part] = done.parts;
  assert.ok(part?.type === "alert" && part.resolution?.outcome === "acknowledge" && part.resolution.by === "Ada");
});

test("fromServer: dismissed and snoozed states, and parts it does not know", () => {
  const [wire] = wireMail();
  const base = { ...wire, parts: [...(wire?.parts as unknown[]), { id: "x", type: "hologram", actions: [] }] };
  assert.equal(fromServer(base).parts.length, 1, "a part of an unknown type is left out");
  assert.equal(messageState(fromServer({ ...base, state: "dismissed" })), "dismissed");
  const later = new Date(Date.now() + 3_600_000).toISOString();
  assert.equal(messageState(fromServer({ ...base, state: "snoozed", snoozed_until: later }), new Date().toISOString()), "snoozed");
});

test("toRequest: each click becomes the platform's call", () => {
  const alert = mailOf("m-alert");
  assert.deepEqual(toRequest(alert, click(alert, ActionId.read, alert.id)), { call: "read" });
  assert.deepEqual(toRequest(alert, click(alert, ActionId.dismiss, alert.id)), { call: "dismiss" });
  assert.deepEqual(toRequest(alert, click(alert, ActionId.acknowledge, "p1")), { call: "resolve", partId: "p1", action: "acknowledge", values: undefined });
  assert.equal(toRequest(alert, click(alert, ActionId.snooze, "p1"))?.call, "snooze", "snoozing is never sent as a part's action");
  assert.deepEqual(toRequest(alert, click(alert, ActionId.snooze, alert.id, { option: "2026-10-11T00:00:00Z" })), { call: "snooze", until: "2026-10-11T00:00:00Z" });

  const failure = mailOf("m-failure");
  const part = failure.parts[1];
  assert.ok(part);
  assert.deepEqual(toRequest(failure, click(failure, ActionId.retry, partTarget(part))), { call: "resolve", partId: "p2", action: "retry", values: undefined });

  const memory = mailOf("m-memory");
  assert.deepEqual(toRequest(memory, click(memory, ActionId.keepMemories, "p1", { selection: ["n1", "n3"] })), {
    call: "resolve",
    partId: "p1",
    action: "keep",
    values: { ids: ["n1", "n3"] },
  });
  assert.equal((toRequest(memory, click(memory, ActionId.discardMemories, "p1")) as { action: string }).action, "discard");

  const budget = mailOf("m-budget");
  assert.deepEqual(toRequest(budget, click(budget, ActionId.raiseBudget, "p1", { option: "250" })), {
    call: "resolve",
    partId: "p1",
    action: "raise_limit",
    values: { limits: { max_runs: 250 } },
  });
  assert.equal((toRequest(budget, click(budget, ActionId.acknowledge, "p1")) as { action: string }).action, "acknowledge");
  assert.equal(toRequest(budget, click(budget, ActionId.raiseBudget, "p1")), null, "a raise with no amount is not sent");

  // A spend limit is shown in dollars and sent in micro-USD.
  const [wire] = wireMail().filter((m) => m.id === "m-budget");
  const spend = fromServer({
    ...wire,
    parts: [{ id: "p1", type: "budget", actions: ["raise_limit"], budget: { scope_kind: "tenant", limit: "spend", period: "month", version: 1, current: { spend_limit: 50_000_000 } } }],
  });
  const [money] = spend.parts;
  assert.ok(money?.type === "budget");
  assert.deepEqual(money.meter, { used: 50, limit: 50, unit: "usd", period: "month" });
  assert.deepEqual(toRequest(spend, click(spend, ActionId.raiseBudget, "p1", { option: "75.5" })), {
    call: "resolve",
    partId: "p1",
    action: "raise_limit",
    values: { limits: { spend_limit: 75_500_000 } },
  });

  assert.equal(toRequest(alert, click(budget, ActionId.acknowledge, "p1")), null, "an action for another message is not sent");
  assert.equal(toRequest(alert, click(alert, ActionId.approve, "p1")), null, "an action the platform has no call for is not sent");
});

test("after: a click shows at once, and resolving the last part resolves the message", () => {
  const alert = mailOf("m-alert");
  const shown = after(alert, click(alert, ActionId.acknowledge, "p1"), "Ada");
  assert.equal(messageState(shown), "resolved");
  assert.equal(shown.unread, false);
  assert.equal(shown.kind, "alert", "what only the platform knows is kept");
  assert.deepEqual(shown.about, alert.about);

  const budget = mailOf("m-budget");
  const raised = after(budget, click(budget, ActionId.raiseBudget, "p1", { option: "250" }), "Ada");
  const [part] = raised.parts;
  assert.ok(part?.type === "budget" && part.resolution?.outcome === "raised" && part.resolution.by === "Ada");
  assert.deepEqual(raised.budgets, budget.budgets);

  // What the reader may not do is refused before it is shown.
  const [only] = alert.parts;
  assert.ok(only);
  const reader = { ...alert, parts: [{ ...only, allowed: [] }] };
  assert.equal(messageState(after(reader, click(reader, ActionId.acknowledge, "p1"), "Ada")), "open");
});

test("inbox merge: a fresh first page keeps older pages, drops what it re-lists, and shows nothing twice", () => {
  const m = (id: string, at: string) => ({ ...mailOf("m-alert"), id, at });
  const loaded = [m("a", "5"), m("b", "4"), m("c", "3"), m("d", "2"), m("e", "1")];
  // d became active and moved to the top. c fell out of the first page's range without being re-listed.
  const fresh = [m("d", "9"), m("a", "5"), m("b", "4")];
  assert.deepEqual(merge(loaded, fresh, false).map((x) => x.id), ["d", "a", "b", "c", "e"]);
  assert.deepEqual(merge(loaded, fresh, true).map((x) => x.id), ["d", "a", "b"], "a first page that is the whole list replaces everything");
});

test("memoryTransport: filters, pages, resolves, and refuses on demand", async () => {
  const via = memoryTransport(sampleMail(), 2);
  const first = await via.list({ limit: 30, state: "all" });
  assert.equal(first.messages.length, 2);
  assert.ok(first.next);
  assert.equal(first.open, 4);
  assert.equal(first.unread, 2);
  const second = await via.list({ limit: 30, state: "all", before: first.next ?? undefined });
  assert.equal(new Set([...first.messages, ...second.messages].map((m) => m.id)).size, 4);
  assert.deepEqual((await via.list({ limit: 30, state: "resolved" })).messages.map((m) => m.id), ["m-done"]);
  assert.deepEqual((await via.list({ limit: 30, state: "all", kind: "memory" })).messages.map((m) => m.id), ["m-memory"]);

  const alert = mailOf("m-alert");
  const request = toRequest(alert, click(alert, ActionId.acknowledge, "p1"));
  assert.ok(request);
  via.fail.add("resolve");
  await assert.rejects(via.send(alert, request));
  assert.equal(messageState(await via.send(alert, request)), "resolved");
  assert.equal((await via.list({ limit: 30 })).open, 3);
});

test("edgeOf: an agent's message carries its edge, a rule's and the platform's stay neutral", () => {
  assert.deepEqual(edgeOf(mailOf("m-memory")), { agent: "oncall", state: "waiting" });
  assert.deepEqual(edgeOf(mailOf("m-failure")), { agent: "releaser", state: "failed" });
  assert.deepEqual(edgeOf(mailOf("m-alert")), { agent: undefined, state: "waiting" });
  assert.equal(edgeOf(mailOf("m-budget")).agent, undefined);
  assert.equal(edgeOf(mailOf("m-done")).state, "done");
});

test("MailboxView: hosts agent-ui's mailbox with every message listed", () => {
  const out = mailbox(null);
  for (const m of sampleMail().filter((x) => messageState(x) === "open")) assert.ok(out.includes(m.subject), m.subject);
  assert.equal(out.match(/data-message="/g)?.length, 4, "one row per open message");
});

test("MailboxView: an agent's message is framed in that agent's stroke, a rule's is neutral", () => {
  const memory = mailbox("m-memory");
  assert.match(memory, /data-testid="mail-frame"[^>]*data-agent="oncall"[^>]*data-state="waiting"/);
  assert.match(memory, /data-from-kind="agent"/);
  const alert = mailbox("m-alert");
  assert.match(alert, /data-testid="mail-frame"/);
  assert.doesNotMatch(alert, /data-testid="mail-frame"[^>]*data-agent=/);
});

test("MailboxView: the platform draws the alert, with its rule and what this reader may do", () => {
  const out = mailbox("m-alert");
  assert.match(out, /data-testid="part-alert"/);
  assert.match(out, /6 of 10 runs failed in the last 15 minutes/);
  assert.match(out, />Critical</);
  assert.match(out, /Raised by the rule/);
  assert.match(out, />Acknowledge</);
  assert.match(out, />Snooze for a Day</);
  assert.match(out, /Rule oncall-failure-rate/, "what the message is about can be opened");
  assert.match(out, /Agent oncall/);
});

test("MailboxView: the platform draws a reached limit with the meter and a way to raise it", () => {
  const out = mailbox("m-budget");
  assert.match(out, /data-testid="part-budget"/);
  assert.match(out, /role="meter"/);
  assert.match(out, /100 runs of 100 runs a day, 100%\. At limit\./);
  assert.match(out, />Raise Limit</);
  assert.match(out, /value="200"/, "the new limit starts at double");
});

test("MailboxView: failures and memory to review are agent-ui's own parts", () => {
  const failure = mailbox("m-failure");
  assert.match(failure, /data-aui-part="text"/);
  assert.match(failure, /data-aui-part="failure"/);
  assert.match(failure, /already exists/);
  const memory = mailbox("m-memory");
  assert.match(memory, /data-aui-part="memory"/);
  assert.match(memory, /Checkout pages go to the payments rota/);
});

test("MailboxView: a resolved part shows its record and offers nothing", () => {
  const out = mailbox("m-done");
  assert.match(out, /Acknowledged by Ada/);
  assert.doesNotMatch(out, />Acknowledge</);
});

test("MailboxView: a reader with no right to act is told so, and a message put away offers nothing", () => {
  const [alert, ...rest] = sampleMail();
  assert.ok(alert);
  const reader = mailbox("m-alert", [{ ...alert, parts: alert.parts.map((p) => ({ ...p, allowed: [] })) }, ...rest]);
  assert.match(reader, /someone with more access has to act on it/);
  assert.doesNotMatch(reader, />Acknowledge</);
  const away = mailbox("m-alert", [{ ...alert, dismissedAt: new Date().toISOString() }, ...rest]);
  assert.doesNotMatch(away, />Acknowledge<|someone with more access/);
});

test("MailboxView: nothing in the inbox animates", () => {
  for (const m of sampleMail()) assert.doesNotMatch(mailbox(m.id), /aui-shimmer|aui-spinner|animate-/, m.id);
});

const rule: AlertRule = { name: "any-run-fails", kind: "run_failed", severity: "critical", created_by: "ada" };
const quiet: AlertRule = { name: "slow-oncall", kind: "slow_run", severity: "warning", agent: "oncall", threshold: 60 };
const fired: Alert[] = [
  { id: "al-1", rule: "any-run-fails", kind: "run_failed", severity: "critical", title: "lead failed", at: "2026-10-09T10:00:00Z" },
  { id: "al-2", rule: "any-run-fails", kind: "run_failed", severity: "critical", title: "oncall failed", at: "2026-10-09T11:00:00Z", acked_by: "ada", acked_at: "2026-10-09T11:05:00Z" },
];
const alertsPage = (over: Partial<Parameters<typeof Alerts>[0]> = {}) =>
  html(<Alerts alerts={fired} rules={[rule, quiet]} agents={[]} canWrite onChanged={() => {}} onOpenInbox={() => {}} {...over} />);

test("Alerts: each rule says what it watches, how loud it is, and where its message goes", () => {
  const out = alertsPage();
  assert.equal(out.match(/data-testid="alert-rule"/g)?.length, 2);
  assert.match(out, /When a run of any agent fails\./);
  assert.match(out, /Watches every agent\. Raises a critical message for everyone who can see alerts\. Made by ada\./);
  assert.match(out, /Watches oncall\. Raises a warning message for everyone who can see alerts\./);
  assert.match(out, />Critical</);
  assert.match(out, />Warning</);
});

test("Alerts: recent firings are listed newest first, as links into the inbox", () => {
  const out = alertsPage();
  assert.match(out, /Fired 2 times, 1 still open\./);
  assert.ok(out.indexOf("oncall failed") < out.indexOf("lead failed"), "newest first");
  assert.match(out, /<button[^>]*>lead failed<\/button>/);
  assert.match(out, /acknowledged by ada/);
  assert.match(out, /Has not fired\./);
});

test("Alerts: open alerts are counted and linked, not acknowledged here", () => {
  const out = alertsPage();
  assert.match(out, /1 message a rule raised is open\./);
  assert.match(out, />Open the inbox</);
  assert.doesNotMatch(out, />Acknowledge</);
  assert.match(alertsPage({ alerts: [] }), /Nothing a rule raised is open\./);
});

test("Alerts: with no rules, it says nothing is watched, and who can change that", () => {
  assert.match(alertsPage({ rules: [], alerts: [] }), /Add one with New Rule\./);
  const reader = alertsPage({ rules: [], alerts: [], canWrite: false });
  assert.match(reader, /Ask someone who can change alerts/);
  assert.doesNotMatch(reader, />New Rule</);
});

test("Alerts: a reader who cannot write sees rules but cannot delete them", () => {
  assert.doesNotMatch(alertsPage({ canWrite: false }), />Delete</);
  assert.match(alertsPage(), /aria-label="Delete the rule any-run-fails"/);
});

const activity = (selected: string | null, trees = sampleTrees(), over: Partial<Parameters<typeof ActivityPanel>[0]> = {}) =>
  html(<ActivityPanel trees={trees} selected={selected} onSelect={() => {}} onClose={() => {}} onJump={() => {}} {...over} />);

test("activity: a tree is listed parent first, in the order things were called", () => {
  const [, second] = sampleTrees();
  assert.ok(second);
  assert.deepEqual(ordered({ ...second, runs: [...second.runs].reverse() }).map((r) => r.id), ["r2", "r2a", "r2a1", "r2b"]);
});

test("activity: an agent that runs again gets a number, and the trail leads from the turn to the run", () => {
  const trees = sampleTrees();
  const nth = ordinals(trees);
  assert.equal(nth.get("r1"), 1);
  assert.equal(nth.get("r2"), 2, "lead answers a second turn");
  assert.equal(nth.get("r3"), 3);
  assert.equal(nth.get("r2b"), 1);
  assert.equal(nth.get("r3a"), 2, "oncall is called again");
  const runs = new Map(trees.flatMap((t) => t.runs.map((r) => [r.id, r] as const)));
  assert.deepEqual(trailOf(runs, "r2a1").map((r) => r.agent), ["lead", "releaser", "helper"]);
});

test("ActivityPanel: with nothing run, it says so, and says Loading while it is finding out", () => {
  assert.match(activity(null, []), /Nothing has run yet/);
  assert.match(activity(null, [], { loading: true }), />Loading</);
  assert.doesNotMatch(activity(null, []), /data-testid="activity-row"/);
});

test("ActivityPanel: every run in the conversation is listed, running first, nested by who called whom", () => {
  const out = activity(null);
  assert.equal(out.match(/data-testid="activity-row"/g)?.length, 7);
  assert.match(out, /7 runs, 2 running, 1 failed/);
  assert.ok(out.indexOf('data-testid="activity-running"') < out.indexOf('data-testid="activity-done"'));
  assert.match(out, /Running \(2\)/);
  assert.match(out, /Done \(5\)/);
  assert.match(out, /padding-left:32px[^>]*>(?:(?!<\/li>).)*List the commits since 2\.3\.0/s, "a run two calls deep is indented twice");
  assert.match(out, /1 tool call/);
});

test("ActivityPanel: each row carries its agent's stroke and says how it stands in words", () => {
  const out = activity(null);
  assert.match(out, /data-agent="releaser"[^>]*data-state="failed"/);
  assert.match(out, /data-agent="oncall"[^>]*data-state="working"/);
  assert.match(out, /data-agent="helper"[^>]*data-state="done"/);
  assert.match(out, />Failed</);
  assert.match(out, />Running</);
});

test("ActivityPanel: a run shows its whole conversation, its tools, and the agents it called", () => {
  const out = activity("r2a");
  assert.match(out, /data-testid="activity-detail"[^>]*data-agent="releaser"[^>]*data-state="failed"|data-agent="releaser"[^>]*data-state="failed"[^>]*data-testid="activity-detail"/);
  assert.match(out, /Called as a subagent by lead\./);
  assert.match(out, /data-testid="activity-conversation"/);
  assert.match(out, /Tag 2\.4\.0 and write the changelog/, "what it was asked");
  assert.match(out, /already exists/, "why it failed");
  assert.match(out, /data-testid="activity-tools"/);
  assert.match(out, /git_tag/);
  assert.match(out, /data-testid="activity-called"/);
  assert.match(out, /List the commits since 2\.3\.0/);
  assert.match(out, />Jump to Message</);
});

test("ActivityPanel: the trail steps back out through each caller", () => {
  const out = activity("r2a1");
  const nav = out.match(/<nav aria-label="Called by".*?<\/nav>/s)?.[0] ?? "";
  assert.match(nav, />All activity</);
  assert.match(nav, /<button[^>]*>lead<\/button>/);
  assert.match(nav, /<button[^>]*>releaser<\/button>/);
  assert.match(nav, /aria-current="page"[^>]*>helper</);
  assert.match(out, /Delegated by releaser\./);
});

test("ActivityPanel: a run with no tools and no calls shows neither section", () => {
  const out = activity("r2");
  assert.doesNotMatch(out, /data-testid="activity-tools"/);
  assert.match(out, /data-testid="activity-called"/);
  const alone = activity("r1", [{ ...sampleTrees()[0]!, spans: [] }]);
  assert.doesNotMatch(alone, /activity-tools|activity-called/);
});

test("ActivityPanel: a selection that no longer exists falls back to the list", () => {
  assert.match(activity("gone"), /data-testid="activity-done"/);
});

const second = () => {
  const tree = sampleTrees()[1];
  assert.ok(tree);
  const root = tree.runs[0];
  assert.ok(root);
  return { tree, root };
};
const runOf = (over: Partial<Run>): Run => ({ ...second().root, ...over });

test("statusOf: every state a run can be in has its own words and stroke", () => {
  assert.deepEqual(statusOf(runOf({ status: "accepted" })), { word: "Queued", state: "idle" });
  assert.deepEqual(statusOf(runOf({ status: "running", calls: 0 })), { word: "Running", state: "working" });
  assert.deepEqual(statusOf(runOf({ status: "running", calls: 2 })), { word: "Running, calling others", state: "working" });
  assert.deepEqual(statusOf(runOf({ status: "parked" })), { word: "Waiting on you", state: "waiting" });
  assert.deepEqual(statusOf(runOf({ status: "parked", reason: "approval to deploy" })), { word: "Waiting: approval to deploy", state: "waiting" });
  assert.deepEqual(statusOf(runOf({ status: "failed" })), { word: "Failed", state: "failed" });
  assert.deepEqual(statusOf(runOf({ status: "completed" })), { word: "Done", state: "done" });
});

test("RunItem: the outline is the run's status in the agent's color, and the words say it", () => {
  const done = html(<RunItem run={runOf({})} onActivity={() => {}} />);
  assert.match(done, /data-testid="run-item"[^>]*data-agent="lead"[^>]*data-state="done"[^>]*data-edge="around"/);
  assert.match(done, />Done</);
  assert.match(done, /1m 00s/);
  assert.match(html(<RunItem run={runOf({ status: "parked" })} />), /data-state="waiting"/);
  assert.match(html(<RunItem run={runOf({ status: "failed" })} />), /data-state="failed"/);
  assert.match(html(<RunItem run={runOf({ status: "running" })} />), /data-state="working"/);
});

test("RunItem: a budget fill appears only for a run that has a budget", () => {
  assert.doesNotMatch(html(<RunItem run={runOf({ lease: 0 })} />), /run-item-budget/);
  const metered = html(<RunItem run={runOf({ lease: 2_000_000, granted: 500_000 })} />);
  assert.match(metered, /data-testid="run-item-budget"/);
  assert.match(metered, /role="meter"/);
  assert.match(metered, /0\.50 of .*2\.00, 25%\. Under limit\./);
});

test("runManifest: the run, its call tree, agents with revisions, tools, timings and statuses", () => {
  const { tree, root } = second();
  const m = runManifest(root, tree);
  assert.equal(m.manifest, 1);
  assert.equal(m.run, "r2");
  assert.deepEqual(m.agents.map((a) => a.name).sort(), ["helper", "lead", "oncall", "releaser"]);
  assert.ok(m.agents.every((a) => a.revision === 1));
  assert.equal(m.runs.length, 4);
  const releaser = m.runs.find((r) => r.id === "r2a");
  assert.ok(releaser);
  assert.equal(releaser.parent, "r2");
  assert.equal(releaser.mode, "subagent");
  assert.equal(releaser.status, "failed");
  assert.match(releaser.error ?? "", /already exists/);
  assert.equal(releaser.duration_ms, 38_000);
  assert.equal(m.spans.length, 3);
  assert.doesNotThrow(() => JSON.parse(JSON.stringify(m)));
  assert.equal(runManifest(root, { runs: [], spans: [] }).runs.length, 1, "with no tree, the run itself is still exported");
});

test("runMarkdown: the request, the answer and every call, nested by who called whom", () => {
  const { tree, root } = second();
  const md = runMarkdown(root, tree);
  assert.match(md, /^# lead\n/);
  assert.match(md, /> Cut the 2\.4 release and tell the on-call/);
  assert.match(md, /\*\*Answered\*\*\n\n2\.4 could not be tagged/);
  assert.match(md, /\n## releaser \(subagent\)\n/);
  assert.match(md, /\n### helper \(delegate\)\n/);
  assert.match(md, /\n## oncall \(handoff\)\n/);
  assert.ok(md.indexOf("## releaser") < md.indexOf("### helper") && md.indexOf("### helper") < md.indexOf("## oncall"));
  assert.match(md, /- Status: Failed/);
  assert.match(md, /\*\*Failed\*\*\n\ngit: tag 'v2\.4\.0' already exists/);
  assert.match(md, /- `git_tag`, failed: git: tag 'v2\.4\.0' already exists/);
  assert.match(md, /- `post_message`, 10s/);
  assert.ok(md.endsWith("\n") && !md.endsWith("\n\n"));
});

test("itemOf: a turn's answer, a tool call and a failed call become things to copy", () => {
  const run = runOf({});
  const turn = { ...emptyTurn(run.id, { name: run.agent }), text: "The streamed answer." };
  const answer = itemOf(run, { kind: "answer", turn });
  assert.ok(answer?.kind === "answer");
  assert.equal(answer.run.answer, "The streamed answer.", "what is on screen is what is copied");

  const done = itemOf(run, { kind: "task", turn, task: { id: "t1", title: "Git log", status: "complete", tool: "git_log", output: "14 commits" } });
  assert.ok(done?.kind === "tool");
  assert.deepEqual(done.tool, { name: "git_log", run: "r2", agent: "lead", result: "14 commits" });
  assert.deepEqual(formats(done).map((f) => f.id), ["call", "result", "both", "json"]);

  const failed = itemOf(run, { kind: "task", turn, task: { id: "t2", title: "Git tag", status: "error", tool: "git_tag", output: "tag exists" } });
  assert.ok(failed?.kind === "tool" && failed.tool.error === "tag exists" && failed.tool.result === undefined);

  const running = itemOf(run, { kind: "task", turn, task: { id: "t3", title: "Search", status: "in_progress", tool: "search" } });
  assert.ok(running?.kind === "tool");
  assert.deepEqual(formats(running).map((f) => f.id), ["call", "both", "json"], "nothing has come back yet, so no result is offered");

  const agent = itemOf(run, { kind: "subagent", turn, task: { id: "t4", title: "Tag oncall", status: "complete", tool: "tag_oncall", output: "Posted.", subagent: { name: "oncall" } } });
  assert.ok(agent?.kind === "tool" && agent.tool.name === "tag_oncall" && agent.tool.result === "Posted.");
  assert.equal(itemOf(run, { kind: "task", turn }), null);
});

test("replay: a finished run is rebuilt from its events, and stays undefined when there are none", () => {
  const run = runOf({});
  assert.equal(replay(run, []), undefined);
  const b64 = (v: unknown) => Buffer.from(JSON.stringify(v)).toString("base64");
  const ev = (seq: number, kind: string, payload: unknown) => ({ seq, kind, run_id: run.id, agent: run.agent, at: run.created_at, payload: b64(payload) }) as never;
  const turn = replay(run, [
    ev(1, "delta.tool.call", { id: "c1", name: "git_log", phase: "call" }),
    ev(2, "delta.tool.done", { id: "c1", name: "git_log", result: "14 commits" }),
    ev(3, "delta.text", { text: "Done." }),
  ]);
  assert.ok(turn);
  assert.equal(turn.id, "r2");
  assert.equal(turn.text, "Done.");
  assert.equal(turn.tasks.length, 1);
  assert.equal(turn.tasks[0]?.output, "14 commits");
});

const fileOf = (id: string) => {
  const f = sampleFiles().find((x) => x.id === id);
  assert.ok(f, id);
  return f;
};
const detail = (id: string, over: Partial<Parameters<typeof StoredDetail>[0]> = {}) =>
  html(<StoredDetail file={fileOf(id)} versions={[fileOf(id)]} canDelete onClose={() => {}} onDelete={() => {}} onOpenRun={() => {}} onOpenThread={() => {}} {...over} />);

test("storage: sizes, search and what may be shown inline", () => {
  assert.equal(bytes(512), "512 B");
  assert.equal(bytes(1536), "1.5 KB");
  assert.equal(bytes(18_240), "18 KB");
  assert.equal(bytes(1_240_000), "1.2 MB");
  assert.equal(bytes(3 * 1024 ** 3), "3.0 GB");
  assert.ok(matches(fileOf("a1"), "Launch-PLAN"));
  assert.ok(!matches(fileOf("a1"), "refunds"));
  assert.ok(showsInline(fileOf("a3")));
  assert.ok(!showsInline(fileOf("a4")), "HTML is never shown inline");
  assert.equal(contentUrl("a 1"), "/api/artifacts/a%201/content");
  assert.equal(contentUrl("a1", true), "/api/artifacts/a1/content?inline=1");
});

test("memoryStorage: filters, pages and deletes", async () => {
  const via = memoryStorage(sampleFiles(), sampleUse(), 3);
  const first = await via.list({ project: "", agent: "", kind: "", q: "", limit: 30 });
  assert.deepEqual(first.artifacts.map((f) => f.id), ["a1", "a2", "a3"], "newest first");
  assert.equal(first.next, "a3");
  assert.deepEqual((await via.list({ project: "", agent: "", kind: "", q: "", limit: 30, before: "a3" })).artifacts.map((f) => f.id), ["a4"]);
  assert.deepEqual((await via.list({ project: "Launch", agent: "", kind: "", q: "", limit: 30 })).artifacts.map((f) => f.id), ["a1", "a3"]);
  assert.deepEqual((await via.list({ project: "", agent: "billing", kind: "spreadsheet", q: "", limit: 30 })).artifacts.map((f) => f.id), ["a2"]);
  assert.deepEqual((await via.list({ project: "", agent: "", kind: "", q: "release", limit: 30 })).artifacts.map((f) => f.id), ["a4"]);
  assert.equal((await via.versions("a1")).length, 3);
  await via.remove("a2");
  assert.equal((await via.use()).count, 3);
});

test("StorageUsed: a real meter in bytes, under, over and with no limit, and where it is stored", () => {
  const under = html(<StorageUsed use={sampleUse()} />);
  assert.match(under, /role="meter"/);
  assert.match(under, /data-usage="under"/);
  assert.match(under, /1\.7 MB of 5\.0 MB, 33%\. Under limit\./);
  assert.match(under, /Storage, 4 files/);
  assert.match(under, /Launch/);
  assert.match(under, /No project/);
  const over = html(<StorageUsed use={sampleUse({ used_bytes: 6 * 1024 ** 2 })} />);
  assert.match(over, /data-usage="over"/);
  assert.match(over, /Over by 1\.0 MB/);
  const none = html(<StorageUsed use={sampleUse({ limit_bytes: null, by_project: [] })} />);
  assert.match(none, /data-usage="none"/);
  assert.match(none, /No limit/);
});

test("StoredList: each row says what the file is, who made it and when", () => {
  const out = html(<StoredList files={sampleFiles()} selected="a2" state="ready" narrowed={false} onSelect={() => {}} />);
  assert.equal(out.match(/data-testid="stored-row"/g)?.length, 4);
  assert.match(out, /launch-plan\.md/);
  assert.match(out, />Spreadsheet</);
  assert.match(out, />v3</);
  assert.match(out, /471 KB/);
  assert.match(out, /aria-current="true"[^>]*>(?:(?!<\/button>).)*refunds-q3\.xlsx/s);
});

test("StoredList: empty, no match, loading and failed each say what is going on", () => {
  const list = (state: "ready" | "loading" | "failed", narrowed = false) => html(<StoredList files={[]} selected={null} state={state} narrowed={narrowed} onSelect={() => {}} />);
  assert.match(list("ready"), /Nothing is stored yet/);
  assert.match(list("ready", true), /No files match/);
  assert.match(list("loading"), />Loading</);
  assert.match(list("failed"), /Storage could not be loaded/);
});

test("StoredDetail: a text file shows its preview, its versions, and where it came from", () => {
  const out = detail("a1", { versions: [fileOf("a1"), { ...fileOf("a1"), id: "a1-v2", version: 2 }] });
  assert.match(out, /<h2[^>]*>launch-plan\.md<\/h2>/);
  assert.match(out, /Version 3/);
  assert.match(out, /data-testid="stored-preview"/);
  assert.match(out, /Freeze scope on the 14th/);
  assert.match(out, /href="\/api\/artifacts\/a1\/content"[^>]*download="launch-plan\.md"|download="launch-plan\.md"[^>]*href="\/api\/artifacts\/a1\/content"/);
  assert.match(out, /Version 2/);
  assert.match(out, />Open the conversation</);
  assert.match(out, />Open the run</);
  assert.match(out, /SHA-256 9f2c1e7ab04d/);
});

test("StoredDetail: an image is shown inline, HTML never is, and a file with neither says so", () => {
  assert.match(detail("a3"), /<img[^>]*src="\/api\/artifacts\/a3\/content\?inline=1"/);
  const page = detail("a4");
  assert.doesNotMatch(page, /<img|<iframe/);
  assert.match(page, /&lt;h1&gt;2\.4&lt;\/h1&gt;/, "its preview is text, escaped");
  assert.match(detail("a2"), /No preview for this kind of file/);
});

test("StoredDetail: delete is offered only to those who may, and asks first", () => {
  assert.match(detail("a1"), />Delete</);
  assert.doesNotMatch(detail("a1"), />Delete Version</, "nothing is deleted on the first press");
  assert.doesNotMatch(detail("a1", { canDelete: false }), />Delete</);
  assert.match(detail("a1", { versions: null }), />Loading</);
});

test("motion: every class moves only transform and opacity, and reduced motion removes the travel", () => {
  const css = readFileSync(new URL("./styles.css", import.meta.url), "utf8");
  const frames = [...css.matchAll(/@keyframes (oap-[a-z-]+) \{((?:[^{}]|\{[^{}]*\})*)\}/g)];
  assert.ok(frames.length >= 7, "the motion keyframes were found");
  for (const [, name, body = ""] of frames) {
    const props = [...body.matchAll(/([a-z-]+)\s*:/g)].map((m) => m[1]);
    for (const prop of props) assert.ok(prop === "opacity" || prop === "transform", `${name} animates ${prop}`);
  }
  const reduced = css.slice(css.indexOf("/* With motion reduced, nothing travels."));
  for (const cls of [".oap-enter", ".oap-pop", ".oap-drawer", ".oap-note"]) assert.ok(reduced.includes(cls), `${cls} is covered when motion is reduced`);
  assert.match(reduced, /animation: oap-fade /, "what arrives only fades");
  assert.doesNotMatch(css, /animation:[^;]*infinite[^;]*;[^}]*\}\s*(?=[^]*\.oap-(enter|pop|drawer|note|fade)\b)(?!)/, "no motion class loops");
  for (const cls of ["oap-enter", "oap-fade", "oap-pop", "oap-drawer", "oap-note"]) {
    const rule = css.match(new RegExp(`\\.${cls} \\{([^}]*)\\}`))?.[1] ?? "";
    assert.doesNotMatch(rule, /infinite/, `${cls} plays once`);
  }
});

test("motion: a copy control keeps its label, and its confirmation is a live region beside it", () => {
  const out = html(<CopyAs item={{ kind: "answer", run: runOf({}) }} />);
  assert.match(out, />Copy as</);
  assert.doesNotMatch(out, />Copied</, "nothing reads Copied until something is copied, and then it is a note, not the label");
  assert.match(out, /role="status"[^>]*aria-live="polite"/);
  const item = html(<RunItem run={runOf({})} />);
  assert.match(item, />Done</);
  assert.match(item, /role="status"[^>]*aria-live="polite"/);
});

test("motion: loading lists are skeletons that still say Loading to a screen reader", () => {
  const out = html(<StoredList files={[]} selected={null} state="loading" narrowed={false} onSelect={() => {}} />);
  assert.match(out, /data-testid="skeleton"/);
  assert.match(out, /role="status"/);
  assert.match(out, /class="sr-only">Loading</);
  assert.ok((out.match(/aui-skeleton/g)?.length ?? 0) >= 3);
});

test("an answer has exactly one copy control: the console's Copy as, in place of agent-ui's button", () => {
  const run = runOf({ answer: "2.4 could not be tagged." });
  const turn = { ...emptyTurn(run.id, { name: run.agent }), status: "done" as const, text: "2.4 could not be tagged." };
  const draw = (withSlot: boolean) =>
    html(
      <WebSurface onAction={() => {}}>
        <AgentMessage turn={turn} itemActions={withSlot ? itemActions(run) : undefined} />
      </WebSurface>,
    );
  const hosted = draw(true);
  assert.equal(hosted.match(/data-testid="item-copy"/g)?.length, 1, "the console's control is there once");
  assert.doesNotMatch(hosted, /aria-label="Copy answer"/, "agent-ui's own button is not drawn beside it");
  assert.match(hosted, /aria-label="Copy answer as: none"/, "it is an icon with a name, opened on click");
  assert.match(hosted, /aria-haspopup="listbox"/);
  assert.match(hosted, /role="status"[^>]*aria-live="polite"/, "the confirmation is announced");
  assert.doesNotMatch(hosted, />Copied</);

  const plain = draw(false);
  assert.equal(plain.match(/aria-label="Copy answer"/g)?.length, 1, "with no host control, agent-ui's button is the one");
  assert.doesNotMatch(plain, /data-testid="item-copy"/);
});

test("a stopped run is over, is not a failure, and says why when the platform does", () => {
  const stopped = runOf({ status: "stopped", cause: "Stopped by Ada" });
  assert.equal(isEnded(stopped), true);
  assert.equal(isEnded(runOf({ status: "running" })), false);
  assert.deepEqual(statusOf(stopped), { word: "Stopped", state: "idle" });
  const item = html(<RunItem run={stopped} />);
  assert.match(item, /data-state="idle"/);
  assert.match(item, />Stopped</);
  assert.match(item, /1m 00s/, "it has a duration, because it ended");
  const md = runMarkdown(stopped, { runs: [stopped], spans: [] });
  assert.match(md, /- Status: Stopped/);
  assert.match(md, /\*\*Stopped\*\*\n\nStopped by Ada/);
  assert.doesNotMatch(md, /\*\*Failed\*\*/);

  const [tree] = sampleTrees().slice(1);
  assert.ok(tree);
  const trees = [{ ...tree, runs: tree.runs.map((r) => (r.id === "r2b" ? { ...r, status: "stopped" as const } : r)) }];
  const out = html(<ActivityPanel trees={trees} selected={null} onSelect={() => {}} onClose={() => {}} />);
  assert.doesNotMatch(out, /data-testid="activity-running"/, "a stopped run does not count as running");
  assert.match(out, /data-run="r2b"[^>]*data-status="stopped"/);
});
