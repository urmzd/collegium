import { ChevronRightIcon, WebSurface, XIcon } from "@agent-ui/render-web";
import { AgentMessage } from "@agent-ui/web";
import { useEffect, useMemo, useState } from "react";
import { type Run, turnOf } from "./adapter.ts";
import { api, type Span } from "./api.ts";
import { CopyAs } from "./CopyAs.tsx";
import { itemActions } from "./itemActions.tsx";
import { toolOf } from "./copyas.ts";
import { useRunTurn } from "./Runs.tsx";
import { AgentMark, AgentTint, button, cause, elapsed, Empty, isEnded, SkeletonRows, Status, Stroke, strokeOf, took } from "./ui.tsx";

/** One turn of the conversation and everything it called: the runs, and what each did. */
export type Tree = { root: string; runs: Run[]; spans: Span[] };

const live = (r: Run) => !isEnded(r);

/** How a run came to be, as a tag on its row and as a sentence in its detail. */
const modeWord: Record<NonNullable<Run["mode"]>, string> = { subagent: "Subagent", handoff: "Handoff", delegate: "Delegated" };
const calledAs: Record<NonNullable<Run["mode"]>, string> = {
  subagent: "Called as a subagent by",
  handoff: "Handed off from",
  delegate: "Delegated by",
};

/** A tree's runs with each parent before its children, so indentation reads as who called whom. */
export function ordered(tree: Tree): Run[] {
  const kids = new Map<string, Run[]>();
  for (const r of tree.runs) {
    const key = r.id === tree.root ? "" : (r.parent ?? tree.root);
    kids.set(key, [...(kids.get(key) ?? []), r]);
  }
  const out: Run[] = [];
  const walk = (key: string) => {
    for (const r of (kids.get(key) ?? []).sort((a, b) => a.created_at.localeCompare(b.created_at))) {
      if (out.includes(r)) continue;
      out.push(r);
      walk(r.id);
    }
  };
  walk("");
  // A run whose parent is missing from the tree is still shown, at the end.
  for (const r of tree.runs) if (!out.includes(r)) out.push(r);
  return out;
}

/** A run and everything it called, directly or through others. */
export function descendants(all: Run[], run: Run): Run[] {
  const out = [run];
  for (let i = 0; i < out.length; i++) for (const r of all) if (r.parent === out[i]?.id && !out.includes(r)) out.push(r);
  return out;
}

/** "oncall 2" for the second time an agent runs in a conversation. The first time is just its name. */
export function ordinals(trees: Tree[]): Map<string, number> {
  const seen = new Map<string, number>();
  const out = new Map<string, number>();
  for (const t of trees) {
    for (const r of ordered(t)) {
      const n = (seen.get(r.agent) ?? 0) + 1;
      seen.set(r.agent, n);
      out.set(r.id, n);
    }
  }
  return out;
}

/** From the turn that started it down to a run: the trail shown above its conversation. */
export function trailOf(runs: Map<string, Run>, id: string): Run[] {
  const path: Run[] = [];
  const guard = new Set<string>();
  let at = runs.get(id);
  while (at && !guard.has(at.id)) {
    guard.add(at.id);
    path.unshift(at);
    at = at.parent ? runs.get(at.parent) : undefined;
  }
  return path;
}

function Name({ run, nth }: { run: Run; nth: number }) {
  return (
    <span className="truncate text-aui-sm font-medium text-aui-fg">
      {run.agent}
      {nth > 1 && <span className="font-normal text-aui-muted"> {nth}</span>}
    </span>
  );
}

/** One run in the list: who, what it was asked, how it stands, and how long it took. */
function Row({ run, nth, tools, base, onOpen }: { run: Run; nth: number; tools: number; base: number; onOpen: () => void }) {
  return (
    <li className="oap-enter" style={{ paddingLeft: Math.max(run.depth - base, 0) * 16 }}>
      <Stroke agent={run.agent} state={strokeOf(run.status)}>
        <button
          type="button"
          data-testid="activity-row"
          data-run={run.id}
          data-status={run.status}
          onClick={onOpen}
          className="flex w-full items-start gap-2.5 py-2 pr-2 pl-3 text-left transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent focus-visible:-outline-offset-2"
        >
          <span className="pt-0.5">
            <AgentMark name={run.agent} size={14} state={run.status === "failed" ? "error" : run.status === "parked" ? "waiting" : live(run) ? "thinking" : "idle"} />
          </span>
          <span className="min-w-0 flex-1">
            <span className="flex items-baseline justify-between gap-3">
              <Name run={run} nth={nth} />
              <span className="shrink-0 text-aui-2xs tabular-nums text-aui-muted">{live(run) ? "Running" : took(elapsed(run))}</span>
            </span>
            <span className="block truncate text-aui-xs text-aui-muted">{run.text || "No request text"}</span>
            <span className="flex flex-wrap items-center gap-x-3 pt-0.5 text-aui-2xs text-aui-muted">
              {run.mode && run.depth > 0 && <span>{modeWord[run.mode]}</span>}
              {tools > 0 && <span>{tools === 1 ? "1 tool call" : `${tools} tool calls`}</span>}
              {run.status === "failed" && <span className="font-medium text-aui-fg">Failed</span>}
              {run.status === "stopped" && <span className="font-medium text-aui-fg">Stopped</span>}
              {run.status === "parked" && <span className="font-medium text-aui-fg">Waiting on a person</span>}
            </span>
          </span>
          <ChevronRightIcon size={14} className="mt-1 text-aui-muted" />
        </button>
      </Stroke>
    </li>
  );
}

/**
 * One run's whole conversation: what it was asked, then its reasoning, tool
 * calls and answer, drawn by the same component the chat uses. It follows the
 * run's events while it is running, and falls back to the run's record when
 * the events are no longer held.
 */
function Conversation({ run }: { run: Run }) {
  const streamed = useRunTurn(run);
  const turn = useMemo(() => streamed ?? turnOf(run), [streamed, run]);
  return (
    <div data-testid="activity-conversation" className="flex flex-col gap-3">
      <div className="flex flex-col items-end gap-1">
        <p className="oap-glass max-w-[90%] whitespace-pre-wrap rounded-aui-lg px-3.5 py-2 text-aui-sm text-aui-fg">{run.text || "No request text"}</p>
      </div>
      <AgentTint name={run.agent}>
        <WebSurface onAction={() => {}}>
          <AgentMessage turn={turn} itemActions={itemActions(run)} />
        </WebSurface>
      </AgentTint>
    </div>
  );
}

/**
 * Everything a conversation set in motion: each turn's agent, the agents it
 * called, and the tools they used. Choosing one shows its whole conversation.
 * This is the panel with its data given. `Activity` loads the data.
 */
export function ActivityPanel({
  trees,
  selected,
  loading,
  leaving,
  onSelect,
  onClose,
  onJump,
}: {
  /** The panel is closing: it slides back out before it is removed. See `Presence` in ui.tsx. */
  leaving?: boolean;
  /** One per turn of the conversation, oldest first. */
  trees: Tree[];
  selected: string | null;
  loading?: boolean;
  onSelect: (id: string | null) => void;
  onClose: () => void;
  /** Scroll the conversation to the turn a run belongs to. */
  onJump?: (turnId: string) => void;
}) {
  const runs = useMemo(() => new Map(trees.flatMap((t) => t.runs.map((r) => [r.id, r] as const))), [trees]);
  const nth = useMemo(() => ordinals(trees), [trees]);
  const toolsBy = useMemo(() => {
    const by = new Map<string, Span[]>();
    for (const t of trees) for (const s of t.spans) if (s.kind === "tool") by.set(s.run_id, [...(by.get(s.run_id) ?? []), s]);
    return by;
  }, [trees]);

  const run = selected ? runs.get(selected) : undefined;
  const all = [...runs.values()];
  const running = all.filter(live).length;
  const failed = all.filter((r) => r.status === "failed").length;
  // A turn is in flight if anything it called still is. Newest first within each group.
  const active = trees.filter((t) => t.runs.some(live)).reverse();
  const done = trees.filter((t) => !t.runs.some(live)).reverse();

  const section = (title: string, group: Tree[]) =>
    group.length > 0 && (
      <section data-testid={`activity-${title.toLowerCase()}`} className="flex flex-col">
        <h3 className="px-3 pt-3 pb-1 text-aui-xs text-aui-muted">
          {title} ({group.reduce((n, t) => n + t.runs.length, 0)})
        </h3>
        <ul className="flex flex-col divide-y divide-[var(--oap-rule)]">
          {group.map((t) => {
            const rows = ordered(t);
            const base = Math.min(...rows.map((r) => r.depth));
            return rows.map((r) => (
              <Row key={r.id} run={r} nth={nth.get(r.id) ?? 1} tools={toolsBy.get(r.id)?.length ?? 0} base={base} onOpen={() => onSelect(r.id)} />
            ));
          })}
        </ul>
      </section>
    );

  const trail = run ? trailOf(runs, run.id) : [];
  const turn = trail[0];
  const called = run ? all.filter((r) => r.parent === run.id).sort((a, b) => a.created_at.localeCompare(b.created_at)) : [];
  const tools = run ? (toolsBy.get(run.id) ?? []) : [];

  return (
    <aside
      data-testid="activity"
      aria-label="Activity"
      data-leaving={leaving}
      className="oap-drawer fixed inset-0 z-30 flex min-h-0 flex-col bg-aui-bg lg:static lg:z-auto lg:w-[27rem] lg:shrink-0 lg:border-[var(--oap-rule)] lg:border-l"
    >
      <header className="flex h-12 shrink-0 items-center justify-between gap-3 border-[var(--oap-rule)] border-b pr-2 pl-4">
        <div className="min-w-0">
          <h2 className="oap-display text-aui-base font-semibold text-aui-fg">Activity</h2>
        </div>
        <p className="min-w-0 flex-1 truncate text-right text-aui-xs text-aui-muted" aria-live="polite">
          {all.length === 0
            ? ""
            : [`${all.length} ${all.length === 1 ? "run" : "runs"}`, running > 0 ? `${running} running` : "", failed > 0 ? `${failed} failed` : ""]
                .filter(Boolean)
                .join(", ")}
        </p>
        <button
          type="button"
          onClick={onClose}
          aria-label="Close activity"
          className="inline-flex size-8 shrink-0 items-center justify-center rounded-aui text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent"
        >
          <XIcon size={16} />
        </button>
      </header>

      {run ? (
        <div key={run.id} className="oap-fade aui-scroll flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-4 py-3">
          <nav aria-label="Called by" className="flex flex-wrap items-center gap-x-1 gap-y-0.5 text-aui-xs text-aui-muted">
            <button
              type="button"
              onClick={() => onSelect(null)}
              className="rounded-aui-sm underline underline-offset-2 hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent"
            >
              All activity
            </button>
            {trail.map((r, i) => (
              <span key={r.id} className="inline-flex items-center gap-1">
                <ChevronRightIcon size={12} />
                {i === trail.length - 1 ? (
                  <span aria-current="page" className="text-aui-fg">
                    {r.agent}
                  </span>
                ) : (
                  <button
                    type="button"
                    onClick={() => onSelect(r.id)}
                    className="rounded-aui-sm underline underline-offset-2 hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent"
                  >
                    {r.agent}
                  </button>
                )}
              </span>
            ))}
          </nav>

          <Stroke agent={run.agent} state={strokeOf(run.status)} testId="activity-detail" className="flex flex-col gap-1 pl-3">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <AgentMark name={run.agent} size={18} />
              <h3 className="oap-display text-aui-lg font-semibold text-aui-fg">
                {run.agent}
                {(nth.get(run.id) ?? 1) > 1 && <span className="font-normal text-aui-muted"> {nth.get(run.id)}</span>}
              </h3>
              <Status status={run.status} />
            </div>
            <p className="text-aui-xs text-aui-muted">
              {run.mode && run.depth > 0 ? `${calledAs[run.mode]} ${runs.get(run.parent ?? "")?.agent ?? "another agent"}. ` : ""}
              {live(run) ? "Running" : `Took ${took(elapsed(run))}`}
              {tools.length > 0 ? `, ${tools.length === 1 ? "1 tool call" : `${tools.length} tool calls`}` : ""}.
            </p>
            <div className="mt-1 flex flex-wrap items-center gap-2">
              {onJump && turn && (
                <button type="button" className={`${button} h-7 px-2 text-aui-xs`} onClick={() => onJump(turn.id)}>
                  Jump to Message
                </button>
              )}
              <CopyAs
                testId="activity-copy"
                item={{ kind: run.depth > 0 ? "call" : "run", run, data: { runs: descendants(all, run), spans: trees.flatMap((t) => t.spans) } }}
              />
            </div>
          </Stroke>

          <Conversation key={run.id} run={run} />

          {tools.length > 0 && (
            <section className="flex flex-col gap-1" data-testid="activity-tools">
              <h4 className="text-aui-xs text-aui-muted">Tools it used</h4>
              <ul className="divide-y divide-[var(--oap-rule)] border-[var(--oap-rule)] border-y">
                {tools.map((s, i) => (
                  // biome-ignore lint/suspicious/noArrayIndexKey: spans have no id and the list is rebuilt whole
                  <li key={i} className="flex items-center justify-between gap-3 py-1 text-aui-sm">
                    <span className="min-w-0 flex-1 truncate text-aui-fg">{s.name}</span>
                    <span className="shrink-0 text-aui-xs tabular-nums text-aui-muted">
                      {s.error ? `Failed: ${cause(s.error)}` : s.end ? took(Date.parse(s.end) - Date.parse(s.start)) : "Running"}
                    </span>
                    <CopyAs align="right" testId="activity-copy-tool" item={{ kind: "tool", tool: toolOf(s) }} />
                  </li>
                ))}
              </ul>
            </section>
          )}

          {called.length > 0 && (
            <section className="flex flex-col gap-1" data-testid="activity-called">
              <h4 className="text-aui-xs text-aui-muted">Agents it called</h4>
              <ul className="flex flex-col divide-y divide-[var(--oap-rule)] border-[var(--oap-rule)] border-y">
                {called.map((r) => (
                  <Row key={r.id} run={r} nth={nth.get(r.id) ?? 1} tools={toolsBy.get(r.id)?.length ?? 0} base={r.depth} onOpen={() => onSelect(r.id)} />
                ))}
              </ul>
            </section>
          )}
        </div>
      ) : (
        <div className="aui-scroll min-h-0 flex-1 overflow-y-auto pb-4">
          {all.length === 0 ? (
            <div className="px-4">
              {loading ? (
                <SkeletonRows count={4} height="3rem" />
              ) : (
                <Empty title="Nothing has run yet">Each answer, and every agent and tool it calls, is listed here once the conversation starts.</Empty>
              )}
            </div>
          ) : (
            <>
              {section("Running", active)}
              {section("Done", done)}
            </>
          )}
        </div>
      )}
    </aside>
  );
}

/**
 * The conversation's activity, loaded and kept current. One panel for the
 * whole conversation: it takes the turns on screen and fetches what each one
 * called. `stamp` should change whenever a run in the conversation does.
 */
export function Activity({
  turns,
  stamp,
  focus,
  leaving,
  onClose,
  onJump,
}: {
  leaving?: boolean;
  turns: Run[];
  stamp: string;
  /** A run to open first, such as the turn whose activity was asked for. */
  focus?: string | null;
  onClose: () => void;
  onJump?: (turnId: string) => void;
}) {
  const [trees, setTrees] = useState<Tree[]>([]);
  const [loading, setLoading] = useState(true);
  const [selected, setSelected] = useState<string | null>(focus ?? null);
  const ids = turns.map((t) => t.id).join(",");

  // biome-ignore lint/correctness/useExhaustiveDependencies: the turns are named by ids, and stamp says when they changed
  useEffect(() => {
    let on = true;
    Promise.all(
      turns.map((t) =>
        api
          .trace(t.id)
          .then((d): Tree => ({ root: t.id, runs: d.runs.length > 0 ? d.runs : [t], spans: d.spans }))
          // A turn whose events are no longer held is still listed, from its record.
          .catch((): Tree => ({ root: t.id, runs: [t], spans: [] })),
      ),
    ).then((next) => {
      if (!on) return;
      setTrees(next);
      setLoading(false);
    });
    return () => {
      on = false;
    };
  }, [ids, stamp]);

  useEffect(() => {
    if (focus) setSelected(focus);
  }, [focus]);

  // Escape steps back out: from a run to the list, from the list to the conversation.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (selected) setSelected(null);
      else onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [selected, onClose]);

  return <ActivityPanel trees={trees} selected={selected} loading={loading} leaving={leaving} onSelect={setSelected} onClose={onClose} onJump={onJump} />;
}
