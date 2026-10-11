import type { Limits } from "./limits.ts";

const M = 1_000_000;

const runs: Limits["by_run"] = [
  { run: "r2", agent: "lead", status: "completed", used: { spend: 0.42 * M, runs: 1, tokens: 18_400 } },
  { run: "r2a", agent: "releaser", parent: "r2", lifecycle: "attached", status: "failed", used: { spend: 1.1 * M, runs: 1, tokens: 41_000 } },
  { run: "r2a1", agent: "helper", parent: "r2a", lifecycle: "delegated", status: "completed", used: { spend: 0.18 * M, runs: 1, tokens: 6_200 } },
  { run: "r2b", agent: "oncall", parent: "r2", lifecycle: "attached", status: "completed", used: { spend: 0.3 * M, runs: 1, tokens: 9_900 } },
  { run: "r3", agent: "lead", status: "running", used: { spend: 0.25 * M, runs: 1, tokens: 7_000 } },
];

/** A session with its own spend limit, which is fuller than the project's run limit: the session binds. */
export const sessionBound = (): Limits => ({
  limits: [
    { measure: "spend", used: 2.25 * M, limit: 3 * M, period: "session", scope: { kind: "session" } },
    { measure: "runs", used: 5, limit: 100, period: "day", scope: { kind: "project", name: "Launch" } },
    { measure: "concurrent", used: 1, limit: 4, scope: { kind: "tenant" } },
  ],
  by_run: runs,
  can_raise: true,
});

/** A child run with no limit of its own: it draws on the run that called it. */
export const parentBound = (): Limits => ({
  limits: [{ measure: "spend", used: 1.28 * M, limit: 1.5 * M, scope: { kind: "run", name: "lead" } }],
  by_run: runs.filter((r) => r.run === "r2a" || r.run === "r2a1"),
  can_raise: false,
});

/** Nothing governs it, though runs happened. */
export const unlimited = (): Limits => ({ limits: [], by_run: runs.map((r) => ({ ...r, used: { runs: r.used.runs } })) });

/** The session is past its limit, and this reader cannot raise it. */
export const overLimit = (): Limits => ({
  limits: [
    { measure: "spend", used: 3.6 * M, limit: 3 * M, period: "session", scope: { kind: "session" } },
    { measure: "runs", used: 5, limit: 100, period: "day", scope: { kind: "project", name: "Launch" } },
  ],
  by_run: runs,
  can_raise: false,
  raise_by: "An admin",
});
