import type { Run } from "./adapter.ts";

/** Micro-USD as dollars. Zero is an unmetered run, not a free one. */
export function usd(micro: number): string {
  if (micro === 0) return "Unmetered";
  return new Intl.NumberFormat(undefined, { style: "currency", currency: "USD" }).format(micro / 1_000_000);
}

/** A duration a person can read at a glance: 840ms, 4.2s, 3m 05s. */
export function took(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return `${m}m ${String(s).padStart(2, "0")}s`;
}

/** The last line of an error. Runtime errors nest, and the cause is at the end. */
export function cause(error: string | undefined): string {
  const lines = (error ?? "").split("\n").filter((l) => l.trim());
  return lines[lines.length - 1]?.trim() ?? "";
}

/** A run that is over, however it ended: it finished, it failed, or it was stopped from outside. */
export function isEnded(run: Pick<Run, "status">): boolean {
  return run.status === "completed" || run.status === "failed" || run.status === "stopped";
}

/** How long a run took, or has taken so far. */
export function elapsed(run: Run): number {
  return Math.max((isEnded(run) ? Date.parse(run.updated_at) : Date.now()) - Date.parse(run.created_at), 0);
}

/** Micro-USD as dollars, with zero shown as $0.00. For budgets, where zero is a real amount. */
export function dollars(micro: number): string {
  return new Intl.NumberFormat(undefined, { style: "currency", currency: "USD" }).format(micro / 1_000_000);
}

/** 1536 as "1.5 KB". Whole bytes below a kilobyte, one decimal above. */
export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(1)} GB`;
}
