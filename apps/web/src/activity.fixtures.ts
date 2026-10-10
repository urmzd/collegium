import type { Tree } from "./Activity.tsx";
import type { Run } from "./adapter.ts";
import type { Span } from "./api.ts";

const at = (secondsAgo: number) => new Date(Date.now() - secondsAgo * 1000).toISOString();

function run(id: string, agent: string, text: string, over: Partial<Run> = {}): Run {
  return {
    id,
    tenant: "default",
    agent,
    rev: 1,
    origin: "api",
    text,
    root: over.root ?? id,
    depth: 0,
    thread: "t-1",
    lease: 0,
    granted: 0,
    calls: 0,
    status: "completed",
    created_at: at(600),
    updated_at: at(590),
    ...over,
  };
}
const tool = (run_id: string, agent: string, name: string, from: number, to?: number, error?: string): Span => ({
  run_id,
  agent,
  kind: "tool",
  name,
  start: at(from),
  ...(to === undefined ? {} : { end: at(to) }),
  ...(error ? { error } : {}),
});

/**
 * A conversation of three turns for the gallery and the tests: one answered
 * alone, one that called two agents (one of which called a third and failed),
 * and one still running.
 */
export function sampleTrees(): Tree[] {
  return [
    {
      root: "r1",
      runs: [run("r1", "lead", "Who is on call this week?", { answer: "Priya is on call until Monday.", created_at: at(900), updated_at: at(896) })],
      spans: [tool("r1", "lead", "search_handbook", 899, 897)],
    },
    {
      root: "r2",
      runs: [
        run("r2", "lead", "Cut the 2.4 release and tell the on-call", { answer: "2.4 could not be tagged. The on-call has been told.", calls: 2, created_at: at(600), updated_at: at(540) }),
        run("r2a", "releaser", "Tag 2.4.0 and write the changelog", {
          root: "r2",
          parent: "r2",
          depth: 1,
          mode: "subagent",
          status: "failed",
          error: "run failed\ngit: tag 'v2.4.0' already exists",
          calls: 1,
          created_at: at(598),
          updated_at: at(560),
        }),
        run("r2a1", "helper", "List the commits since 2.3.0", { root: "r2", parent: "r2a", depth: 2, mode: "delegate", answer: "14 commits.", created_at: at(596), updated_at: at(590) }),
        run("r2b", "oncall", "Tell the on-call that 2.4 is delayed", { root: "r2", parent: "r2", depth: 1, mode: "handoff", answer: "Posted in #incidents.", created_at: at(558), updated_at: at(545) }),
      ],
      spans: [
        tool("r2a", "releaser", "git_tag", 580, 561, "git: tag 'v2.4.0' already exists"),
        tool("r2a1", "helper", "git_log", 595, 591),
        tool("r2b", "oncall", "post_message", 556, 546),
      ],
    },
    {
      root: "r3",
      runs: [
        run("r3", "lead", "Summarize the open incidents", { status: "running", calls: 1, created_at: at(20), updated_at: at(2) }),
        run("r3a", "oncall", "List incidents opened this week", { root: "r3", parent: "r3", depth: 1, mode: "subagent", status: "running", created_at: at(18), updated_at: at(2) }),
      ],
      spans: [tool("r3a", "oncall", "search_incidents", 15)],
    },
  ];
}
