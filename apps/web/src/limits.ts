/**
 * The limits that govern a conversation or a run, and where the usage went.
 *
 * A run draws on whatever limit applies nearest to it: its session's if the
 * session has one, else its parent's, its project's, its agent's or the
 * workspace's. The platform resolves that and says which scope each limit
 * comes from. This file holds the shape, and the arithmetic over it. No React.
 */
import type { Run } from "./adapter.ts";
import { bytes, dollars } from "./format.ts";

export type Measure = "spend" | "runs" | "concurrent" | "storage" | "tokens";

/** Where a limit comes from. A run scope is a parent run: what a child draws on. */
export type LimitScope = { kind: "session" | "run" | "project" | "agent" | "tenant"; name?: string };

/** One limit in force, for one measure. Spend is micro-USD, storage is bytes, the rest are counts. */
export interface Limit {
  measure: Measure;
  used: number;
  limit: number;
  period?: "day" | "month" | "session";
  scope: LimitScope;
  /** What the period is heading for, when the platform projects it. */
  projected?: number;
}

/** What one run used, of each measure the platform reports. */
export interface RunDraw {
  run: string;
  agent: string;
  parent?: string;
  lifecycle?: "attached" | "delegated";
  status: Run["status"];
  used: Partial<Record<Measure, number>>;
}

export interface Limits {
  /** Only the limits that exist, and only measures the platform reports. Empty when nothing governs. */
  limits: Limit[];
  by_run: RunDraw[];
  /** Whether this reader may raise the session's limit here, and if not, who can. */
  can_raise?: boolean;
  raise_by?: string;
}

/** How full a limit is, from 0. Past 1 is over. */
export const share = (l: Limit) => (l.limit > 0 ? l.used / l.limit : 0);

/** The limit that will stop things first: the fullest one. Null when nothing governs. */
export function binding(limits: Limit[]): Limit | null {
  return limits.reduce<Limit | null>((worst, l) => (l.limit > 0 && (!worst || share(l) > share(worst)) ? l : worst), null);
}

/** Whose limit it is, in a few words: "Session limit", "From lead", "Project Launch". */
export function scopeWords(scope: LimitScope): string {
  switch (scope.kind) {
    case "session":
      return "Session limit";
    case "run":
      return scope.name ? `From ${scope.name}` : "From the run that called it";
    case "project":
      return scope.name ? `Project ${scope.name}` : "Project limit";
    case "agent":
      return scope.name ? `Agent ${scope.name}` : "Agent limit";
    case "tenant":
      return "Workspace limit";
  }
}

export const measureWord: Record<Measure, string> = {
  spend: "Spend",
  runs: "Runs",
  concurrent: "Runs at once",
  storage: "Storage",
  tokens: "Tokens",
};

const count = (n: number, unit: string) => `${n.toLocaleString()} ${unit}`;
const thousands = (n: number) => (n >= 1000 ? `${Math.round(n / 100) / 10}k` : String(n));

/** An amount of a measure, as a person reads it. */
export function amount(measure: Measure, value: number): string {
  switch (measure) {
    case "spend":
      return dollars(value);
    case "storage":
      return bytes(value);
    case "tokens":
      return `${thousands(value)} tokens`;
    case "concurrent":
      return count(value, "at once");
    case "runs":
      return count(value, value === 1 ? "run" : "runs");
  }
}

/** What was used of a measure, by agent, largest first. Agents that used none are left out. */
export function byAgent(runs: RunDraw[], measure: Measure): { agent: string; used: number; runs: number }[] {
  const by = new Map<string, { agent: string; used: number; runs: number }>();
  for (const r of runs) {
    const used = r.used[measure] ?? 0;
    if (used <= 0) continue;
    const was = by.get(r.agent) ?? { agent: r.agent, used: 0, runs: 0 };
    by.set(r.agent, { agent: r.agent, used: was.used + used, runs: was.runs + 1 });
  }
  return [...by.values()].sort((a, b) => b.used - a.used || a.agent.localeCompare(b.agent));
}

/** The runs that used some of a measure, largest first. */
export function byRun(runs: RunDraw[], measure: Measure): RunDraw[] {
  return runs.filter((r) => (r.used[measure] ?? 0) > 0).sort((a, b) => (b.used[measure] ?? 0) - (a.used[measure] ?? 0));
}

/** The limits one run draws on, with only that run and the runs under it in the breakdown. */
export function forRun(all: Limits, runId: string): Limits {
  const keep = new Set([runId]);
  let grew = true;
  while (grew) {
    grew = false;
    for (const r of all.by_run) {
      if (r.parent && keep.has(r.parent) && !keep.has(r.run)) {
        keep.add(r.run);
        grew = true;
      }
    }
  }
  return { ...all, by_run: all.by_run.filter((r) => keep.has(r.run)) };
}
