import type { Run } from "./adapter.ts";
import { dollars } from "./format.ts";
import { UsageMeter } from "./ui.tsx";
import { contextFill, contextWords, costWords, count, tokenWords } from "./usage.ts";

/**
 * What one run used: its model calls, tokens, cost and how full its context
 * was, and, for a run with a budget, what it has spent of it. The figures
 * are the runtime's. While the run works they are the calls reported so far.
 *
 * vultus has no usage summary for a run yet, so this is the console's own;
 * see docs/clients.md. Presentational: it fetches nothing.
 */
export function RunUsageSummary({ run }: { run: Run }) {
  const u = run.usage;
  const fill = contextFill(u);
  const rows: [string, string][] = [
    ["Model calls", count(u?.total.model_calls ?? 0)],
    ["Tokens", tokenWords(u?.total)],
    ["Cost", costWords(u?.total)],
  ];
  if (u?.children?.model_calls) rows.push(["Of which agents it waited on", costWords(u.children)]);
  return (
    <section className="flex flex-col gap-1" data-testid="run-usage">
      <h4 className="text-aui-xs text-aui-muted">What it used</h4>
      {!u ? (
        <p className="text-aui-sm text-aui-muted">No model call reported yet.</p>
      ) : (
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-aui-sm">
          {rows.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-aui-muted">{k}</dt>
              <dd className="tabular-nums text-aui-fg">{v}</dd>
            </div>
          ))}
          <dt className="text-aui-muted">Context</dt>
          <dd className="flex flex-col gap-1 tabular-nums text-aui-fg">
            {contextWords(u)}
            {fill && <UsageMeter size="mini" label="Context" spent={fill.used} limit={fill.limit} format={count} testId="run-usage-context" />}
          </dd>
        </dl>
      )}
      {run.lease > 0 && (
        <UsageMeter
          label="Budget spent or handed on"
          labelled
          spent={(run.spent ?? 0) + run.granted}
          limit={run.lease}
          format={dollars}
          testId="run-usage-budget"
        />
      )}
    </section>
  );
}
