import { type FormEvent, useCallback, useEffect, useState } from "react";
import { api, type Bucket, type Entry, type Group, type Revision, type Summary } from "./api.ts";
import { agentOptions, button, card, Chip, Empty, field, figures, Menu, Page, Stat, took } from "./ui.tsx";

const windows = [
  { id: "15m", label: "15 minutes" },
  { id: "1h", label: "1 hour" },
  { id: "24h", label: "24 hours" },
  { id: "168h", label: "7 days" },
];

// Finished work is ink, as it is on the run tape. Only what is in flight or
// failed takes a color, each paired with a label wherever it appears. Failed
// sits on top of the stack and completed at the base, so the two are told
// apart by position as well as hue.
const series = [
  { key: "completed", label: "Completed", fill: "var(--oap-ink)" },
  { key: "active", label: "In flight", fill: "var(--aui-accent)" },
  { key: "failed", label: "Failed", fill: "var(--aui-bad)" },
] as const;

function rate(g: Group): string {
  const ended = g.completed + g.failed;
  return ended === 0 ? "No data" : `${Math.round((100 * g.completed) / ended)}%`;
}

function clock(iso: string, wide: boolean): string {
  const d = new Date(iso);
  return wide
    ? d.toLocaleDateString(undefined, { month: "short", day: "numeric" })
    : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

/** Round a maximum up to a tick the eye can read: 1, 2, 5, 10, 20, 50. */
function ceiling(n: number): number {
  if (n <= 4) return Math.max(n, 1);
  const pow = 10 ** Math.floor(Math.log10(n));
  for (const m of [1, 2, 5, 10]) if (n <= m * pow) return m * pow;
  return 10 * pow;
}

/** Runs started per interval, stacked by how they ended. */
function RunsChart({ buckets, wide }: { buckets: Bucket[]; wide: boolean }) {
  const [hover, setHover] = useState<number | null>(null);
  const W = 920;
  const H = 220;
  const pad = { top: 8, right: 8, bottom: 22, left: 30 };
  const plotW = W - pad.left - pad.right;
  const plotH = H - pad.top - pad.bottom;
  const max = ceiling(Math.max(...buckets.map((b) => b.completed + b.failed + b.active), 1));
  const slot = plotW / Math.max(buckets.length, 1);
  const bar = Math.max(Math.min(slot - 2, 24), 1);
  const y = (v: number) => (v / max) * plotH;
  const ticks = max <= 4 ? Array.from({ length: max + 1 }, (_, i) => i) : [0, max / 2, max];
  const labelEvery = Math.max(1, Math.ceil(buckets.length / 6));
  const hovered = hover === null ? undefined : buckets[hover];

  return (
    <div className="relative">
      <svg viewBox={`0 0 ${W} ${H}`} className="block w-full" role="img" aria-label="Runs started over time, by outcome">
        {ticks.map((t) => (
          <g key={t}>
            <line
              x1={pad.left}
              x2={W - pad.right}
              y1={pad.top + plotH - y(t)}
              y2={pad.top + plotH - y(t)}
              stroke="var(--aui-border)"
              strokeWidth={1}
            />
            <text
              x={pad.left - 6}
              y={pad.top + plotH - y(t) + 3}
              textAnchor="end"
              fontSize={10}
              fill="var(--aui-muted)"
              style={{ fontVariantNumeric: "tabular-nums" }}
            >
              {t}
            </text>
          </g>
        ))}
        {buckets.map((b, i) => {
          const x = pad.left + i * slot + (slot - bar) / 2;
          let base = pad.top + plotH;
          const parts = series.filter((s) => b[s.key] > 0);
          return (
            // biome-ignore lint/a11y/noStaticElementInteractions: hover reveals a tooltip; the same values are in the table below
            <g key={b.start} onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)}>
              {/* The hit target is the whole slot, not the thin bar. */}
              <rect x={pad.left + i * slot} y={pad.top} width={slot} height={plotH} fill="transparent" />
              {parts.map((s, n) => {
                // A 2px gap in the surface color separates touching segments.
                const h = Math.max(y(b[s.key]) - (n > 0 ? 2 : 0), 1);
                base -= h + (n > 0 ? 2 : 0);
                const top = n === parts.length - 1;
                return (
                  <path
                    key={s.key}
                    d={segment(x, base, bar, h, top ? Math.min(4, h, bar / 2) : 0)}
                    fill={s.fill}
                    opacity={hover === null || hover === i ? 1 : 0.45}
                  />
                );
              })}
              {i % labelEvery === 0 && (
                <text x={pad.left + i * slot + slot / 2} y={H - 6} textAnchor="middle" fontSize={10} fill="var(--aui-muted)">
                  {clock(b.start, wide)}
                </text>
              )}
            </g>
          );
        })}
      </svg>
      {hovered && hover !== null && (
        <div
          className="aui-glass pointer-events-none absolute top-1 rounded-aui px-2.5 py-1.5 text-aui-xs text-aui-fg shadow-aui-pop"
          style={{
            left: `${((pad.left + hover * slot + slot / 2) / W) * 100}%`,
            transform: hover > buckets.length / 2 ? "translateX(-105%)" : "translateX(8px)",
          }}
        >
          <div className="pb-0.5 font-medium">
            {new Date(hovered.start).toLocaleString(undefined, {
              month: "short",
              day: "numeric",
              hour: "2-digit",
              minute: "2-digit",
            })}
          </div>
          {series.map((s) => (
            <div key={s.key} className="flex items-center gap-1.5">
              <span aria-hidden className="size-2 rounded-sm" style={{ background: s.fill }} />
              {s.label}
              <span className="ml-auto pl-3 tabular-nums">{hovered[s.key]}</span>
            </div>
          ))}
        </div>
      )}
      <ul className="flex flex-wrap gap-x-4 gap-y-1 pt-2">
        {series.map((s) => (
          <li key={s.key} className="flex items-center gap-1.5 text-aui-xs text-aui-muted">
            <span aria-hidden className="size-2 rounded-sm" style={{ background: s.fill }} />
            {s.label}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** A column segment: square at the bottom, rounded at the top when r > 0. */
function segment(x: number, y: number, w: number, h: number, r: number): string {
  return `M${x},${y + h} V${y + r} Q${x},${y} ${x + r},${y} H${x + w - r} Q${x + w},${y} ${x + w},${y + r} V${y + h} Z`;
}

/** One measure per row, on one scale, so rows compare by length. */
function Breakdown({ title, groups, onPick }: { title: string; groups: Group[]; onPick?: (name: string) => void }) {
  const max = Math.max(...groups.map((g) => g.runs), 1);
  return (
    <section className={card}>
      <h2 className="pb-2 text-aui-sm font-medium text-aui-fg">{title}</h2>
      {groups.length === 0 ? (
        <p className="text-aui-sm text-aui-muted">Nothing ran in this window.</p>
      ) : (
        <table className="w-full text-aui-sm">
          <thead>
            <tr className="text-left text-aui-2xs text-aui-muted">
              <th className="pb-1 font-medium">Name</th>
              <th className="w-2/5 pb-1 font-medium">Runs</th>
              <th className="pb-1 text-right font-medium">Success</th>
              <th className="pb-1 text-right font-medium">p50</th>
              <th className="pb-1 text-right font-medium">p95</th>
            </tr>
          </thead>
          <tbody>
            {groups.map((g) => (
              <tr key={g.name} className="border-aui-border border-t">
                <td className="py-1.5 pr-2">
                  {onPick ? (
                    <button
                      type="button"
                      onClick={() => onPick(g.name)}
                      className="rounded-aui-sm text-aui-fg underline-offset-2 hover:underline focus-visible:outline-2 focus-visible:outline-aui-accent"
                    >
                      {g.name}
                    </button>
                  ) : (
                    g.name
                  )}
                </td>
                <td className="py-1.5 pr-3">
                  <span className="flex items-center gap-2">
                    <span
                      aria-hidden
                      className="h-2 rounded-r-aui-sm bg-[var(--oap-ink)]"
                      style={{ width: `${Math.max((100 * g.runs) / max, 1.5)}%`, maxWidth: "calc(100% - 2.5rem)" }}
                    />
                    <span className="tabular-nums text-aui-fg">{g.runs}</span>
                    {g.failed > 0 && <span className="text-aui-xs text-aui-muted">{g.failed} failed</span>}
                  </span>
                </td>
                <td className="py-1.5 text-right tabular-nums">{rate(g)}</td>
                <td className="py-1.5 text-right tabular-nums text-aui-muted">{g.p50_ms ? took(g.p50_ms) : "–"}</td>
                <td className="py-1.5 text-right tabular-nums text-aui-muted">{g.p95_ms ? took(g.p95_ms) : "–"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

const kinds = ["", "lifecycle", "lifecycle.failed", "lifecycle.completed", "delta.tool", "delta.text", "action.taken"];

/** Ask the journal what happened. */
function Journal({
  window,
  agents,
  agent,
  onAgent,
  onOpenRun,
}: {
  window: string;
  agents: Revision[];
  agent: string;
  onAgent: (name: string) => void;
  onOpenRun: (id: string) => void;
}) {
  const [kind, setKind] = useState("lifecycle");
  const [text, setText] = useState("");
  const [entries, setEntries] = useState<Entry[] | null>(null);

  const search = useCallback(() => {
    const params: Record<string, string> = { window, limit: "100" };
    if (agent) params.agent = agent;
    if (kind) params.kind = kind;
    if (text.trim()) params.q = text.trim();
    api
      .journal(params)
      .then(setEntries)
      .catch(() => setEntries([]));
  }, [window, agent, kind, text]);

  // Filters apply as they change. Free text waits for Enter, so a search is
  // not sent per keystroke.
  // biome-ignore lint/correctness/useExhaustiveDependencies: text is applied on submit
  useEffect(search, [window, agent, kind]);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    search();
  };

  return (
    <section className={card}>
      <h2 className="text-aui-sm font-medium text-aui-fg">What happened</h2>
      <p className="pb-3 text-aui-xs text-aui-muted">
        Every event agents published in this window, newest first. Held in memory, so it starts empty after a restart.
      </p>
      <form onSubmit={submit} className="flex flex-wrap items-center gap-2 pb-3">
        <Menu
          label="Agent"
          className="w-44"
          value={agent}
          options={agentOptions(agents.map((a) => a.spec.name), "All agents")}
          onChange={onAgent}
        />
        <Menu
          label="Event kind"
          className="w-48"
          value={kind}
          options={kinds.map((k) => ({ value: k, label: k || "All events" }))}
          onChange={setKind}
        />
        <input
          aria-label="Contains"
          className={`${field} min-w-40 flex-1`}
          placeholder="Contains text, such as an error"
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        <button type="submit" className={button}>
          Search
        </button>
      </form>
      {entries === null ? null : entries.length === 0 ? (
        <p className="text-aui-sm text-aui-muted">No events match.</p>
      ) : (
        <div className="aui-scroll max-h-96 overflow-y-auto">
          <table className="w-full text-aui-xs">
            <thead className="sticky top-0 bg-aui-surface">
              <tr className="text-left text-aui-2xs text-aui-muted">
                <th className="pb-1 font-medium">When</th>
                <th className="pb-1 font-medium">Agent</th>
                <th className="pb-1 font-medium">Event</th>
                <th className="pb-1 font-medium">Detail</th>
                <th className="pb-1" />
              </tr>
            </thead>
            <tbody>
              {entries.map((e) => (
                <tr key={`${e.run_id}:${e.seq}`} className="border-aui-border border-t align-top">
                  <td className="whitespace-nowrap py-1.5 pr-3 tabular-nums text-aui-muted">
                    {new Date(e.at).toLocaleTimeString(undefined, { hour12: false })}
                  </td>
                  <td className="py-1.5 pr-3 text-aui-fg">{e.agent}</td>
                  <td className="whitespace-nowrap py-1.5 pr-3 font-aui-mono text-aui-muted">{e.kind}</td>
                  <td className="max-w-0 w-full truncate py-1.5 pr-3 text-aui-fg" title={e.detail}>
                    {e.detail}
                  </td>
                  <td className="py-1.5 text-right">
                    <button
                      type="button"
                      onClick={() => onOpenRun(e.run_id)}
                      className="whitespace-nowrap rounded-aui-sm text-aui-muted underline-offset-2 hover:text-aui-fg hover:underline focus-visible:outline-2 focus-visible:outline-aui-accent"
                    >
                      Open run
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

export function Analytics({
  agents,
  openAlerts,
  tick,
  onOpenRun,
}: {
  agents: Revision[];
  openAlerts: number;
  /** Changes whenever a run starts or ends, so the page stays current. */
  tick: number;
  onOpenRun: (id: string) => void;
}) {
  const [win, setWin] = useState("1h");
  const [agent, setAgent] = useState("");
  const [summary, setSummary] = useState<Summary | null>(null);
  const [table, setTable] = useState(false);

  // biome-ignore lint/correctness/useExhaustiveDependencies: tick is the refresh signal
  useEffect(() => {
    let live = true;
    api
      .analytics(win)
      .then((s) => live && setSummary(s))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [win, tick]);

  const wide = win === "168h";
  const t = summary?.total;

  return (
    <Page
      wide
      title="Analytics"
      lede="How runs are going: volume, outcomes and duration, then the events behind them."
      actions={
        <fieldset className="flex rounded-aui border border-aui-border p-0.5">
          <legend className="sr-only">Time range</legend>
          {windows.map((w) => (
            <button
              key={w.id}
              type="button"
              aria-pressed={win === w.id}
              onClick={() => setWin(w.id)}
              className="h-7 rounded-aui-sm px-2.5 text-aui-xs text-aui-muted transition-colors hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-pressed:bg-aui-surface aria-pressed:text-aui-fg"
            >
              {w.label}
            </button>
          ))}
        </fieldset>
      }
    >
      {!summary || !t ? (
        <Empty title="Loading" />
      ) : (
        <>
          <div className={figures}>
            <Stat label="Runs" value={String(t.runs)} note={`${summary.delegated} called by another agent`} />
            <Stat label="Success rate" value={rate(t)} note={`${t.failed} failed, ${t.active} in flight`} />
            <Stat label="Median duration" value={t.p50_ms ? took(t.p50_ms) : "–"} note="Of runs that ended" />
            <Stat label="95th percentile" value={t.p95_ms ? took(t.p95_ms) : "–"} note="Of runs that ended" />
            <Stat label="Open alerts" value={String(openAlerts)} note="Across all time" />
          </div>

          <section className={card}>
            <div className="flex items-center justify-between pb-2">
              <h2 className="text-aui-sm font-medium text-aui-fg">Runs started, by outcome</h2>
              <button type="button" className={`${button} h-7 px-2 text-aui-xs`} aria-pressed={table} onClick={() => setTable(!table)}>
                {table ? "Show Chart" : "Show Table"}
              </button>
            </div>
            {t.runs === 0 ? (
              <p className="py-8 text-center text-aui-sm text-aui-muted">Nothing ran in this window.</p>
            ) : table ? (
              <div className="aui-scroll max-h-64 overflow-y-auto">
                <table className="w-full text-aui-xs tabular-nums">
                  <thead className="sticky top-0 bg-aui-surface text-left text-aui-2xs text-aui-muted">
                    <tr>
                      <th className="pb-1 font-medium">Interval</th>
                      <th className="pb-1 text-right font-medium">Completed</th>
                      <th className="pb-1 text-right font-medium">In flight</th>
                      <th className="pb-1 text-right font-medium">Failed</th>
                    </tr>
                  </thead>
                  <tbody>
                    {summary.buckets
                      .filter((b) => b.completed + b.active + b.failed > 0)
                      .map((b) => (
                        <tr key={b.start} className="border-aui-border border-t">
                          <td className="py-1">{new Date(b.start).toLocaleString()}</td>
                          <td className="py-1 text-right">{b.completed}</td>
                          <td className="py-1 text-right">{b.active}</td>
                          <td className="py-1 text-right">{b.failed}</td>
                        </tr>
                      ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <RunsChart buckets={summary.buckets} wide={wide} />
            )}
          </section>

          <div className="grid grid-cols-1 gap-3 lg:grid-cols-2">
            <Breakdown title="By agent" groups={summary.by_agent} onPick={setAgent} />
            <Breakdown title="By origin" groups={summary.by_origin} />
          </div>

          {(summary.slowest ?? []).length > 0 && (
            <section className={card}>
              <h2 className="pb-2 text-aui-sm font-medium text-aui-fg">Slowest runs</h2>
              <ul>
                {(summary.slowest ?? []).map((r) => (
                  <li key={r.id} className="border-aui-border border-t first:border-t-0">
                    <button
                      type="button"
                      onClick={() => onOpenRun(r.id)}
                      className="flex w-full items-center gap-3 py-1.5 text-left text-aui-sm hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent"
                    >
                      <span className="w-24 shrink-0 truncate font-medium text-aui-fg">{r.agent}</span>
                      <span className="min-w-0 flex-1 truncate text-aui-muted">{r.text}</span>
                      <Chip>{r.status === "failed" ? "Failed" : "Done"}</Chip>
                      <span className="w-16 shrink-0 text-right tabular-nums text-aui-fg">
                        {took(Date.parse(r.updated_at) - Date.parse(r.created_at))}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          )}

          <Journal window={win} agents={agents} agent={agent} onAgent={setAgent} onOpenRun={onOpenRun} />
        </>
      )}
    </Page>
  );
}
