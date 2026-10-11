import { useState } from "react";
import { amount, binding, byAgent, byRun, type Limit, type Limits, type Measure, measureWord, scopeWords, share } from "./limits.ts";
import { AgentMark, button, hueOf, LimitField, type Period, UsageMeter } from "./ui.tsx";

const linkish =
  "rounded-aui-sm text-aui-fg underline underline-offset-2 hover:no-underline focus-visible:outline-2 focus-visible:outline-aui-accent";

const statusWord: Record<string, string> = {
  accepted: "Queued",
  running: "Running",
  parked: "Waiting",
  completed: "Done",
  failed: "Failed",
  stopped: "Stopped",
};

/** How many rows of a breakdown show before the rest fold away. */
const SHOWN = 5;

/** One limit: whose it is and what it measures, then how full it is. */
function LimitRow({ limit }: { limit: Limit }) {
  return (
    <li data-testid="limit-row" data-measure={limit.measure} data-scope={limit.scope.kind}>
      <UsageMeter
        labelled
        testId={`limit-${limit.measure}`}
        label={`${scopeWords(limit.scope)}, ${measureWord[limit.measure].toLowerCase()}`}
        spent={limit.used}
        limit={limit.limit}
        projected={limit.projected}
        period={limit.period === "session" ? undefined : limit.period}
        format={(v) => amount(limit.measure, v)}
      />
    </li>
  );
}

/** The measure worth breaking down: the binding limit's, else the first one anything was used of. */
function measureOf(data: Limits): Measure | null {
  const first = binding(data.limits)?.measure;
  if (first && data.by_run.some((r) => (r.used[first] ?? 0) > 0)) return first;
  for (const m of ["spend", "runs", "tokens", "storage", "concurrent"] as const) {
    if (data.by_run.some((r) => (r.used[m] ?? 0) > 0)) return m;
  }
  return null;
}

/**
 * The limits a conversation or a run is governed by, and where the usage
 * went. Each limit says whose it is, because a run draws on the nearest one:
 * its session's, its parent's, its project's, its agent's or the workspace's.
 * Only what the platform reports is shown: a measure with no figure has no
 * row, and never an empty meter. Presentational: it fetches nothing.
 */
export function LimitsPanel({
  data,
  onAnalytics,
  onRaise,
}: {
  data: Limits;
  /** Open Analytics on this conversation. Leave out where there is nowhere to go. */
  onAnalytics?: () => void;
  /** Raise the session's spend limit. Offered only when the reader may and a session limit exists. */
  onRaise?: (microUsd: number, period: Period) => void;
}) {
  const limits = [...data.limits].filter((l) => l.limit > 0).sort((a, b) => share(b) - share(a));
  const first = binding(data.limits);
  const measure = measureOf(data);
  const agents = measure ? byAgent(data.by_run, measure) : [];
  const runs = measure ? byRun(data.by_run, measure) : [];
  const total = agents.reduce((n, a) => n + a.used, 0);
  const session = data.limits.find((l) => l.scope.kind === "session" && l.measure === "spend");
  const [raise, setRaise] = useState<number | null>(session ? session.limit * 2 : null);
  const [period, setPeriod] = useState<Period>("month");

  return (
    <div data-testid="limits-panel" data-binding={first ? `${first.scope.kind}:${first.measure}` : "none"} className="flex flex-col gap-3 px-2 py-1.5">
      <section className="flex flex-col gap-2" aria-label="Limits">
        <h3 className="text-aui-xs text-aui-muted">Limits</h3>
        {limits.length === 0 ? (
          <p className="text-aui-sm text-aui-fg">Nothing limits this. Limits are set in Settings, for the workspace, a project or an agent.</p>
        ) : (
          <>
            <ul className="flex flex-col gap-3">
              {limits.map((l) => (
                <LimitRow key={`${l.scope.kind}:${l.scope.name ?? ""}:${l.measure}`} limit={l} />
              ))}
            </ul>
            {limits.length > 1 && first && (
              <p className="text-aui-xs text-aui-muted">
                {scopeWords(first.scope)} on {measureWord[first.measure].toLowerCase()} is the fullest, so it is the one that stops things first.
              </p>
            )}
          </>
        )}
      </section>

      {session && (data.can_raise && onRaise ? (
        <div data-testid="limits-raise" className="flex flex-col gap-2">
          <LimitField label="Raise the session limit" value={raise} period={period} onChange={(v, p) => (setRaise(v), setPeriod(p))} />
          <button
            type="button"
            className={`${button} h-7 self-start px-2 text-aui-xs`}
            disabled={raise === null || raise <= session.limit}
            onClick={() => raise !== null && onRaise(raise, period)}
          >
            Raise Limit
          </button>
        </div>
      ) : (
        <p data-testid="limits-raise-by" className="text-aui-xs text-aui-muted">
          {data.raise_by ? `${data.raise_by} can raise the session limit.` : "Someone who can change limits can raise the session limit."}
        </p>
      ))}

      {measure && agents.length > 0 && (
        <section className="flex flex-col gap-2" aria-label="Where it went">
          <h3 className="text-aui-xs text-aui-muted">Where the {measureWord[measure].toLowerCase()} went</h3>
          {/* One bar, a segment per agent in its own color. The list under it says the same in words. */}
          <div aria-hidden className="flex h-2 gap-px overflow-hidden rounded-[2px]" data-testid="limits-bar">
            {agents.map((a) => (
              <span
                key={a.agent}
                title={`${a.agent}: ${amount(measure, a.used)}`}
                style={{ width: `${(100 * a.used) / total}%`, background: `oklch(var(--collegium-stroke-l) var(--collegium-stroke-c) ${hueOf(a.agent)})` }}
              />
            ))}
          </div>
          <ul className="flex flex-col">
            {agents.slice(0, SHOWN).map((a) => (
              <li key={a.agent} data-testid="limits-agent" className="flex items-center gap-2 py-0.5 text-aui-sm">
                <AgentMark name={a.agent} size={12} />
                <span className="min-w-0 flex-1 truncate text-aui-fg">{a.agent}</span>
                <span className="shrink-0 text-aui-xs text-aui-muted">{a.runs === 1 ? "1 run" : `${a.runs} runs`}</span>
                <span className="w-20 shrink-0 text-right text-aui-xs tabular-nums text-aui-fg">{amount(measure, a.used)}</span>
              </li>
            ))}
          </ul>
          {agents.length > SHOWN && <p className="text-aui-xs text-aui-muted">And {agents.length - SHOWN} more, in Analytics.</p>}

          <details className="group">
            <summary className="cursor-pointer list-none rounded-aui-sm text-aui-xs text-aui-muted hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent">
              By run ({runs.length})
            </summary>
            <ul className="flex flex-col pt-1">
              {runs.slice(0, SHOWN * 2).map((r) => (
                <li key={r.run} data-testid="limits-run" className="flex items-center gap-2 py-0.5 text-aui-sm">
                  <AgentMark name={r.agent} size={12} />
                  <span className="min-w-0 flex-1 truncate text-aui-fg">{r.agent}</span>
                  <span className="shrink-0 text-aui-xs text-aui-muted">
                    {r.lifecycle === "delegated" ? "Delegated, " : r.lifecycle === "attached" ? "Attached, " : ""}
                    {(statusWord[r.status] ?? r.status).toLowerCase()}
                  </span>
                  <span className="w-20 shrink-0 text-right text-aui-xs tabular-nums text-aui-fg">{amount(measure, r.used[measure] ?? 0)}</span>
                </li>
              ))}
            </ul>
          </details>
        </section>
      )}

      {onAnalytics && (
        <button type="button" className={`${linkish} self-start text-aui-xs`} onClick={onAnalytics}>
          Open in Analytics
        </button>
      )}
    </div>
  );
}
