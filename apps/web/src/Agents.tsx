import type { Group, Revision } from "./api.ts";
import { AgentMark, card, Chip, Empty, Page, primary, took } from "./ui.tsx";

/** Every agent in the tenant. Opening one shows its editor. */
export function Agents({
  agents,
  stats,
  baseline,
  canWrite,
  onOpen,
  onNew,
}: {
  agents: Revision[];
  /** The last 24 hours, by agent name. */
  stats: Map<string, Group>;
  baseline: string;
  canWrite: boolean;
  onOpen: (name: string) => void;
  onNew: () => void;
}) {
  return (
    <Page
      wide
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
        <div className={card}>
          <Empty title="No agents yet">{canWrite ? "Create one to start running work." : "Ask an admin to define one."}</Empty>
        </div>
      ) : (
        <ul className="grid grid-cols-1 gap-3 md:grid-cols-2">
          {agents.map((a) => {
            const g = stats.get(a.spec.name);
            const ended = g ? g.completed + g.failed : 0;
            return (
              <li key={a.spec.name}>
                <button
                  type="button"
                  onClick={() => onOpen(a.spec.name)}
                  className={`${card} flex h-full w-full flex-col gap-3 text-left transition-colors hover:border-aui-accent/50 focus-visible:outline-2 focus-visible:outline-aui-accent`}
                >
                  <span className="flex items-center gap-3">
                    <AgentMark name={a.spec.name} size={36} />
                    <span className="min-w-0 flex-1">
                      <span className="flex items-center gap-2">
                        <span className="truncate text-aui-base font-medium text-aui-fg">{a.spec.name}</span>
                        {a.spec.name === baseline && <Chip>Baseline</Chip>}
                      </span>
                      <span className="block text-aui-xs text-aui-muted">
                        Revision {a.rev}, {a.spec.provider} / {a.spec.model}
                      </span>
                    </span>
                  </span>
                  <span className="line-clamp-2 min-h-[2.5rem] text-aui-sm text-aui-muted">
                    {a.spec.description || a.spec.system_prompt || "No description or prompt yet."}
                  </span>
                  <span className="flex flex-wrap gap-x-5 gap-y-1 text-aui-xs">
                    {g && g.runs > 0 ? (
                      [
                        ["Runs, 24h", String(g.runs)],
                        ["Success", ended ? `${Math.round((100 * g.completed) / ended)}%` : "No data"],
                        ["Median", g.p50_ms ? took(g.p50_ms) : "–"],
                      ].map(([label, value]) => (
                        <span key={label} className="flex items-baseline gap-1.5">
                          <span className="text-aui-muted">{label}</span>
                          <span className="tabular-nums text-aui-fg">{value}</span>
                        </span>
                      ))
                    ) : (
                      <span className="text-aui-muted">No runs in the last 24 hours</span>
                    )}
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </Page>
  );
}
