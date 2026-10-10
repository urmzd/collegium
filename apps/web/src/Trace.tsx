import { useEffect, useState } from "react";
import type { Run } from "./adapter.ts";
import { api, type Span } from "./api.ts";
import { cause, took } from "./ui.tsx";

const indent: Record<Span["kind"], number> = { run: 0, attempt: 1, parked: 1, tool: 2, output: 2 };
const noun: Record<Span["kind"], string> = {
  run: "Run",
  attempt: "Attempt",
  parked: "Waiting",
  tool: "Tool",
  output: "Answer",
};

/**
 * What happened inside a run and the runs it called, on one time axis.
 *
 * Every bar is the same hue: what a span is reads from its label and its
 * indent, never from color. A span that failed says so in words, with a mark.
 */
export function Trace({ run, stamp }: { run: Run; stamp: string }) {
  const [data, setData] = useState<{ runs: Run[]; spans: Span[] } | null>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: stamp changes when the run's record does
  useEffect(() => {
    let live = true;
    api
      .trace(run.id)
      .then((d) => live && setData(d))
      .catch(() => live && setData({ runs: [], spans: [] }));
    return () => {
      live = false;
    };
  }, [run.id, stamp]);

  if (!data) return null;
  if (data.spans.length === 0) {
    return (
      <p className="px-4 py-3 text-aui-xs text-aui-muted">
        No trace. This run's events are no longer in the journal, which holds recent events in memory.
      </p>
    );
  }

  const now = Date.now();
  const t0 = Math.min(...data.spans.map((s) => Date.parse(s.start)));
  const t1 = Math.max(...data.spans.map((s) => (s.end ? Date.parse(s.end) : now)), t0 + 1);
  const total = t1 - t0;
  const depth = new Map(data.runs.map((r) => [r.id, r.depth]));

  return (
    <div className="border-aui-border border-b px-4 py-3">
      <div className="flex items-baseline justify-between pb-1.5">
        <p className="text-aui-xs text-aui-muted">Trace</p>
        <p className="text-aui-2xs tabular-nums text-aui-muted">{took(total)} end to end</p>
      </div>
      <ul className="aui-scroll flex max-h-64 flex-col gap-px overflow-y-auto">
        {data.spans.map((s, i) => {
          const start = Date.parse(s.start);
          const end = s.end ? Date.parse(s.end) : now;
          const left = ((start - t0) / total) * 100;
          const width = Math.max(((end - start) / total) * 100, 0.4);
          const pad = ((depth.get(s.run_id) ?? 0) * 2 + indent[s.kind]) * 10;
          const label = s.kind === "run" ? s.agent : s.name;
          return (
            <li
              // biome-ignore lint/suspicious/noArrayIndexKey: spans have no id and the list is rebuilt whole
              key={i}
              className="grid grid-cols-[minmax(8rem,14rem)_1fr_4.5rem] items-center gap-2 text-aui-xs"
              title={s.error ? cause(s.error) : undefined}
            >
              <span className="flex min-w-0 items-center gap-1.5" style={{ paddingLeft: pad }}>
                <span className="shrink-0 text-aui-2xs text-aui-muted">{noun[s.kind]}</span>
                <span className={`truncate ${s.kind === "run" ? "font-medium text-aui-fg" : "text-aui-fg"}`}>{label}</span>
                {s.error && <span className="shrink-0 text-aui-2xs text-aui-muted">✕ failed</span>}
              </span>
              <span className="relative h-3 rounded-aui-sm bg-aui-border/40">
                <span
                  className={`absolute inset-y-0 rounded-aui-sm ${s.kind === "run" ? "bg-aui-accent/45" : "bg-aui-accent"} ${
                    s.end ? "" : "opacity-60"
                  }`}
                  style={{ left: `${left}%`, width: `${Math.min(width, 100 - left)}%` }}
                />
              </span>
              <span className="text-right tabular-nums text-aui-muted">{s.end ? took(end - start) : "running"}</span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
