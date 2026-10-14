import assert from "node:assert/strict";
import { test } from "node:test";
import type { ReactElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import type { Run, RunUsage } from "./adapter.ts";
import { spendNote } from "./Analytics.tsx";
import { reached, unitOf } from "./mail.ts";
import type { ScopeUsage } from "./api.ts";
import { lowerBound, Meters } from "./Billing.tsx";
import { RunUsageSummary } from "./RunUsage.tsx";
import { SessionGaugeFor } from "./SessionGauge.tsx";
import { contextFill, contextWords, costWords, money, tokenWords } from "./usage.ts";

const html = (el: ReactElement) => renderToStaticMarkup(el);
const text = (s: string) => s.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ");

const usage: RunUsage = {
  own: { model_calls: 2, prompt_tokens: 48_000, completion_tokens: 900, cost: { micros: 157_500, currency: "USD" } },
  children: { model_calls: 1, prompt_tokens: 500, completion_tokens: 50, cost: { micros: 2_300, currency: "USD" } },
  total: {
    model_calls: 3,
    prompt_tokens: 48_500,
    cached_prompt_tokens: 30_000,
    completion_tokens: 950,
    cost: { micros: 159_700, currency: "USD" },
  },
  context: { model: "claude-test", window: 200_000, tokens: 48_000 },
};

function run(over: Partial<Run> = {}): Run {
  return {
    id: "r1",
    tenant: "acme",
    agent: "lead",
    rev: 1,
    origin: "api",
    text: "Plan the launch",
    root: "r1",
    depth: 0,
    thread: "r1",
    lease: 0,
    granted: 0,
    spent: 0,
    calls: 0,
    status: "completed",
    created_at: "2026-10-14T00:00:00Z",
    updated_at: "2026-10-14T00:00:05Z",
    ...over,
  };
}

test("usage words: cost in its own currency, never converted, and calls with no price said to have none", () => {
  assert.equal(money({ micros: 4_500, currency: "USD" }), "$0.0045");
  assert.equal(money({ micros: 12_340_000, currency: "USD" }), "$12.34");
  assert.equal(costWords({ cost: { micros: 4_500, currency: "USD" } }), "$0.0045");
  assert.equal(costWords({ unpriced_calls: 2 }), "No price: 2 calls with no price");
  assert.match(costWords({ cost: { micros: 4_500, currency: "USD" }, unpriced_calls: 1 }), /^At least \$0\.0045: 1 call with no price$/);
  assert.match(costWords({ cost: { micros: 4_500, currency: "USD" }, other_costs: [{ micros: 1_000_000, currency: "EUR" }] }), /\$0\.0045 and €1\.00/);
  assert.match(money({ micros: 2_500_000, currency: "not a code" }), /^2\.50 not a code$/);
  assert.match(lowerBound({ used: 0, limit: 1, metered: true, unpriced_calls: 0, other_costs: [{ micros: 1_000_000, currency: "XX1" }] }) ?? "", /1\.00 XX1 was not in US dollars/);
  assert.equal(tokenWords(usage.total), "48,500 in (30,000 cached), 950 out");
  assert.equal(tokenWords(undefined), "None reported");
});

test("context fill: the latest call's prompt against the model's window, and nothing invented without a window", () => {
  assert.deepEqual(contextFill(usage), { used: 48_000, limit: 200_000 });
  assert.equal(contextWords(usage), "48,000 of 200,000 tokens, 24%");
  const noWindow: RunUsage = { ...usage, context: { model: "local", tokens: 900 } };
  assert.equal(contextFill(noWindow), undefined);
  assert.match(contextWords(noWindow), /900 tokens\. The model's window is not in the catalog/);
  assert.equal(contextFill(undefined), undefined);
});

test("RunUsageSummary: a run's calls, tokens, cost, the agents it waited on, and its context fill", () => {
  const out = text(html(<RunUsageSummary run={run({ usage })} />));
  assert.match(out, /Model calls 3/);
  assert.match(out, /Tokens 48,500 in \(30,000 cached\), 950 out/);
  assert.match(out, /Cost \$0\.1597/);
  assert.match(out, /Of which agents it waited on \$0\.0023/);
  assert.match(out, /48,000 of 200,000 tokens, 24%/);
  assert.match(text(html(<RunUsageSummary run={run()} />)), /No model call reported yet/);
  const leased = html(<RunUsageSummary run={run({ usage, lease: 1_000_000, spent: 400_000, granted: 100_000 })} />);
  assert.match(leased, /data-testid="run-usage-budget"/);
});

test("SessionGaugeFor: the top edge is the latest turn's context, and says it in words", () => {
  const out = html(<SessionGaugeFor tail={run({ usage })} />);
  assert.match(out, /Context: 48k of 200k tokens, 24%/);
  assert.match(html(<SessionGaugeFor tail={run()} />), /Context: Not reported yet/);
});

test("spend that cannot be counted in dollars is said to be a lower bound, and the token limit is shown", () => {
  const scope: ScopeUsage = {
    scope: { kind: "tenant" },
    period: "day",
    period_start: "2026-10-14T00:00:00Z",
    period_end: "2026-10-15T00:00:00Z",
    version: 3,
    warn_at: 80,
    action: "block",
    runs: { used: 0, limit: 0 },
    concurrent: { used: 0, limit: 0 },
    spend: { used: 900_000, limit: 1_000_000, metered: true, unpriced_calls: 2, other_costs: [{ micros: 500_000, currency: "EUR" }] },
    tokens: { used: 40_000, limit: 100_000 },
    max_lease: 0,
    state: "warn",
  };
  assert.match(lowerBound(scope.spend) ?? "", /^At least this much: 2 calls had no price, €0\.50 was not in US dollars\. A token limit bounds every call\.$/);
  assert.equal(lowerBound({ ...scope.spend, unpriced_calls: 0, other_costs: [] }), null);
  const meters = html(<Meters usage={scope} />);
  assert.match(meters, /data-testid="spend-lower-bound"/);
  assert.match(text(meters), /Tokens a day/);
  assert.doesNotMatch(meters, /not enforced/);
});

test("Analytics: spend leaves out calls with no price and other currencies, and says so", () => {
  const group = { name: "all", runs: 3, completed: 3, failed: 0, stopped: 0, active: 0, p50_ms: 0, p95_ms: 0 };
  assert.equal(spendNote({ ...group, usage: { model_calls: 3, tokens: 10, spend: 5, unpriced_calls: 0, other_currency_runs: 0 } }), "Priced in US dollars");
  assert.equal(
    spendNote({ ...group, usage: { model_calls: 3, tokens: 10, spend: 5, unpriced_calls: 2, other_currency_runs: 1 } }),
    "At least this: 2 calls with no price, 1 run in another currency not counted",
  );
});

test("a token limit reached in the inbox raises the token limit, in tokens", () => {
  const data = { scope: { kind: "tenant" }, which: "tokens" as const, period: "day" as const, current: { max_runs: 10, max_tokens: 50_000 }, refused: true };
  assert.deepEqual(reached(data), { field: "max_tokens", value: 50_000 });
  assert.equal(unitOf("tokens"), "tokens");
});
