import type { Run } from "./adapter.ts";
import type { Group, Revision } from "./api.ts";
import { AgentMark, Chip, Empty, longest, Page, primary, rows, Stroke, Tape, took } from "./ui.tsx";

/**
 * Every agent in the tenant, one lane each: who it is, its recent runs on a
 * tape, and how the last day went. The tapes share one scale, so a slow agent
 * reads as slow next to the others. Opening a lane shows the agent's editor.
 */
export function Agents({
  agents,
  stats,
  runs = [],
  builtin = [],
  baseline,
  canWrite,
  onOpen,
  onNew,
}: {
  agents: Revision[];
  /** The last 24 hours, by agent name. */
  stats: Map<string, Group>;
  /** Recent runs across the tenant, drawn on each agent's tape. */
  runs?: Run[];
  /** Agents the platform provides. They can be opened and read, not changed. */
  builtin?: string[];
  baseline: string;
  canWrite: boolean;
  onOpen: (name: string) => void;
  onNew: () => void;
}) {
  const scale = longest(runs);
  return (
    <Page
      title="Agents"
      lede="An agent is a prompt with a setup around it. Open one to edit its prompt, measure it, and watch it."
      actions={
        canWrite ? (
          <button type="button" className={primary} onClick={onNew}>
            New Agent
          </button>
        ) : undefined
      }
    >
      {agents.length === 0 ? (
        <Empty title="No agents yet">{canWrite ? "Create one to start running work." : "Ask an admin to define one."}</Empty>
      ) : (
        <ul className={rows}>
          {agents.map((a) => {
            const name = a.spec.name;
            const g = stats.get(name);
            const ended = g ? g.completed + g.failed : 0;
            const own = runs.filter((r) => r.agent === name);
            const busy = own.some((r) => r.status === "running" || r.status === "accepted");
            const waiting = own.some((r) => r.status === "parked");
            const figures: [string, string][] =
              g && g.runs > 0
                ? [
                    ["Runs, 24h", String(g.runs)],
                    ["Success", ended ? `${Math.round((100 * g.completed) / ended)}%` : "No data"],
                    ["Median", g.p50_ms ? took(g.p50_ms) : "–"],
                  ]
                : [];
            return (
              <li key={name} className="collegium-enter">
                <Stroke agent={name} state={waiting ? "waiting" : busy ? "working" : "idle"}>
                <button
                  type="button"
                  onClick={() => onOpen(name)}
                  className="grid w-full grid-cols-1 items-center gap-x-8 gap-y-3 py-4 pr-1 pl-4 text-left transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent focus-visible:-outline-offset-2 @4xl:grid-cols-[minmax(0,5fr)_minmax(0,4fr)_15rem]"
                >
                  <span className="flex min-w-0 items-start gap-3">
                    <span className="pt-0.5">
                      <AgentMark name={name} size={24} state={busy ? "thinking" : "idle"} />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="flex items-center gap-2">
                        <span className="collegium-display truncate text-aui-lg font-semibold text-aui-fg">{name}</span>
                        {name === baseline && <Chip>Baseline</Chip>}
                        {builtin.includes(name) && <Chip>Built in, read only</Chip>}
                      </span>
                      <span className="line-clamp-1 text-aui-sm text-aui-muted">
                        {a.spec.description || a.spec.system_prompt || "No description or prompt yet."}
                      </span>
                      <span className="block pt-0.5 text-aui-xs text-aui-muted">
                        Revision {a.rev}, {a.spec.provider} / {a.spec.model}
                      </span>
                    </span>
                  </span>

                  <Tape runs={own} slots={36} scale={scale} />

                  {figures.length > 0 ? (
                    <span className="grid grid-cols-3 gap-4">
                      {figures.map(([label, value]) => (
                        <span key={label} className="flex min-w-0 flex-col">
                          <span className="collegium-display text-aui-lg font-semibold text-aui-fg">{value}</span>
                          <span className="truncate text-aui-2xs text-aui-muted">{label}</span>
                        </span>
                      ))}
                    </span>
                  ) : (
                    <span className="text-aui-xs text-aui-muted">No runs in the last 24 hours</span>
                  )}
                </button>
                </Stroke>
              </li>
            );
          })}
        </ul>
      )}
    </Page>
  );
}
