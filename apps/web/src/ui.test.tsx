import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { Run } from "./adapter.ts";
import type { Me, Preferences, Settings } from "./api.ts";
import { Onboarding, welcome } from "./Onboarding.tsx";
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
