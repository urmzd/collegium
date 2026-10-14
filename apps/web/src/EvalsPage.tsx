import type { SuiteView } from "./api.ts";
import { AgentMark, card } from "./ui.tsx";

/**
 * Every eval in the workspace, in one list. Each is kept and run where its
 * subject is, so a row opens that agent's Evals tab or the knowledge page.
 */
export function EvalsPage({ suites, onOpen }: { suites: SuiteView[]; onOpen: (suite: SuiteView) => void }) {
  return (
    <div className="aui-scroll collegium-fade min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-4 px-5 pt-8 pb-6 md:px-8">
        <header>
          <h1 className="text-aui-xl font-semibold text-aui-fg">Evals</h1>
          <p className="text-aui-sm text-aui-muted">Checks that an agent's answers, or a source's search, still hold. Each opens where it is run.</p>
        </header>
        {suites.length === 0 ? (
          <p className={`${card} text-aui-sm text-aui-muted`}>No evals yet. Add one from an agent's Evals tab or a knowledge source.</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {suites.map((s) => {
              const last = s.runs[0];
              return (
                <li key={s.name}>
                  <button
                    type="button"
                    onClick={() => onOpen(s)}
                    className={`${card} flex w-full items-center gap-3 text-left transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent`}
                  >
                    {s.target === "agent" && <AgentMark name={s.subject} size={16} />}
                    <span className="min-w-0 flex-1">
                      <span className="block truncate text-aui-sm font-medium text-aui-fg">{s.name}</span>
                      <span className="block truncate text-aui-xs text-aui-muted">
                        {s.target === "agent" ? `Agent ${s.subject}` : `Knowledge source ${s.subject}`}, {s.cases.length}{" "}
                        {s.cases.length === 1 ? "case" : "cases"}
                      </span>
                    </span>
                    <span className="shrink-0 text-aui-xs tabular-nums text-aui-muted">
                      {!last ? "Not run yet" : last.status === "running" ? "Running" : `${last.passed} of ${last.total} passed`}
                    </span>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </div>
  );
}
