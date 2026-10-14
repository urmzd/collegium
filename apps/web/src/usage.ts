/**
 * What runs used, in words. The figures are the runtime's, through the
 * platform: tokens, cost from saige's price catalog in integer micro-units of
 * a currency, and how full the context was. Nothing here estimates: a call
 * with no price is said to have none, and money in another currency is
 * written in that currency, never converted. No React.
 */
import type { Cost, RunUsage, Usage } from "./adapter.ts";
import type { Fill } from "./SessionGauge.tsx";

/**
 * Micro-units of a currency, written in it. Small amounts keep enough digits
 * to be seen. A code Intl does not know, which a deployment's own rate card
 * could carry, is written after the amount rather than thrown on.
 */
export function money(c: Cost): string {
  const value = c.micros / 1_000_000;
  const small = Math.abs(value) > 0 && Math.abs(value) < 1;
  const digits = { minimumFractionDigits: 2, maximumFractionDigits: small ? 4 : 2 };
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: c.currency || "USD", ...digits }).format(value);
  } catch {
    return `${new Intl.NumberFormat(undefined, digits).format(value)} ${c.currency}`;
  }
}

/** 1234567 as "1,234,567". */
export const count = (n: number) => n.toLocaleString();

/** What some calls cost, in words: "$0.0045", "$0.01 and 2 calls with no price", "No price". */
export function costWords(u: Usage | undefined): string {
  if (!u) return "Nothing yet";
  const amounts = [u.cost, ...(u.other_costs ?? [])].filter((c): c is Cost => !!c).map(money);
  const unpriced = u.unpriced_calls ?? 0;
  const none = `${count(unpriced)} ${unpriced === 1 ? "call" : "calls"} with no price`;
  if (amounts.length === 0) return unpriced > 0 ? `No price: ${none}` : "Nothing yet";
  return unpriced > 0 ? `At least ${amounts.join(" and ")}: ${none}` : amounts.join(" and ");
}

/** Tokens in and out, in words: "12,000 in (9,000 cached), 400 out". */
export function tokenWords(u: Usage | undefined): string {
  if (!u || (!u.prompt_tokens && !u.completion_tokens)) return "None reported";
  const cached = u.cached_prompt_tokens ? ` (${count(u.cached_prompt_tokens)} cached)` : "";
  return `${count(u.prompt_tokens ?? 0)} in${cached}, ${count(u.completion_tokens ?? 0)} out`;
}

/** How full a run's context was at its latest model call. Undefined when the catalog gives no window. */
export function contextFill(u: RunUsage | undefined): Fill | undefined {
  const c = u?.context;
  if (!c?.window || c.window <= 0) return undefined;
  return { used: c.tokens ?? 0, limit: c.window };
}

/** A context fill in words: "48,000 of 200,000 tokens, 24%". */
export function contextWords(u: RunUsage | undefined): string {
  const fill = contextFill(u);
  if (!fill) return u?.context?.tokens ? `${count(u.context.tokens)} tokens. The model's window is not in the catalog.` : "Not reported yet";
  return `${count(fill.used)} of ${count(fill.limit)} tokens, ${Math.round((100 * fill.used) / fill.limit)}%`;
}
