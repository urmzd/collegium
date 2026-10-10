import { ActionId, emptyTurn, reduce, type Turn, type UiAction } from "@agent-ui/core";
import { WebSurface } from "@agent-ui/render-web";
import { AgentMessage, PromptInput, Thread } from "@agent-ui/web";
import { useEffect, useMemo, useReducer, useState } from "react";
import { adapter, type BusEvent, type Run, turnOf } from "./adapter.ts";
import { ApiError, api, type Revision, subscribe } from "./api.ts";
import { Trace } from "./Trace.tsx";
import { ago, button, cause, Chip, Empty, Page, Problem, Status, took, usd } from "./ui.tsx";


type Action = { type: "seed"; turn: Turn } | { type: "open" } | { type: "event"; event: BusEvent };

/**
 * One run's live turn. It starts from the run record, then the stream takes
 * over: every connect replays the run from its first event, so the turn is
 * rebuilt from empty on each open rather than appended to.
 */
function useRunTurn(run: Run | undefined): Turn | undefined {
  const [state, dispatch] = useReducer(
    (s: { turn?: Turn; to: ReturnType<typeof adapter>; fresh: boolean }, a: Action) => {
      switch (a.type) {
        case "seed":
          return { turn: a.turn, to: adapter(), fresh: true };
        case "open":
          return { ...s, to: adapter(), fresh: true };
        case "event": {
          const base = s.fresh ? emptyTurn(a.event.run_id, { name: a.event.agent }) : s.turn;
          if (!base) return s;
          return { ...s, fresh: false, turn: s.to(a.event).reduce(reduce, base) };
        }
      }
    },
    { to: adapter(), fresh: true },
  );

  const id = run?.id;
  const agent = run?.agent;
  // biome-ignore lint/correctness/useExhaustiveDependencies: reseed only when the selected run changes, not on every poll of its record
  useEffect(() => {
    if (!run || !id || !agent) return;
    dispatch({ type: "seed", turn: turnOf(run) });
    return subscribe(`agent:${agent}:${id}`, {
      onOpen: () => dispatch({ type: "open" }),
      onEvent: (event) => dispatch({ type: "event", event }),
    });
  }, [id, agent]);

  return run ? state.turn : undefined;
}

/**
 * One run, live, with the box to start another. `run` undefined is the new-run
 * screen.
 */
export function RunView({
  run: current,
  agents,
  canRun,
  onSelect: setSelected,
  onStarted,
}: {
  run: Run | undefined;
  agents: Revision[];
  canRun: boolean;
  onSelect: (id: string) => void;
  onStarted: () => void;
}) {
  const [agent, setAgent] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  const [tree, setTree] = useState<Run[]>([]);
  const [showTrace, setShowTrace] = useState(false);

  const turn = useRunTurn(current);
  const target = agent || agents[0]?.spec.name || "";

  // The tree is re-read whenever the selected run's record changes, which is
  // also when a child could have appeared under it.
  const stamp = current ? `${current.id}:${current.calls}:${current.status}` : "";
  // biome-ignore lint/correctness/useExhaustiveDependencies: stamp carries every field of current that matters here
  useEffect(() => {
    if (!current) return setTree([]);
    let live = true;
    api
      .tree(current.id)
      .then((t) => live && setTree(t))
      .catch(() => live && setTree([]));
    return () => {
      live = false;
    };
  }, [stamp]);

  const start = async (text: string, to = target) => {
    setProblem(null);
    try {
      const run = await api.createRun(to, text);
      setSelected(run.id);
      onStarted();
    } catch (e) {
      setProblem(e instanceof ApiError ? e.message : "The run could not be started.");
    }
  };

  // Retry sends the same request to the same agent as a new run. The failed
  // run stays in the list as the record of what happened.
  const onAction = (a: UiAction) => {
    if (a.id === ActionId.retry && current && canRun) void start(current.text, current.agent);
  };

  const busy = turn?.status === "working" || turn?.status === "queued";

  return (
      <section className="flex min-h-0 flex-1 flex-col">
        {current && turn ? (
          <>
            <RunHeader run={current} trace={showTrace} onTrace={() => setShowTrace(!showTrace)} />
            {showTrace && <Trace run={current} stamp={stamp} />}
            {tree.length > 1 && <Tree runs={tree} current={current.id} onSelect={setSelected} />}
            <WebSurface onAction={onAction} className="flex min-h-0 flex-1 flex-col">
              <Thread className="mx-auto w-full max-w-3xl flex-1 px-4" busy={busy} label={`Run of ${current.agent}`}>
                <Request run={current} />
                <AgentMessage turn={turn} />
              </Thread>
            </WebSurface>
          </>
        ) : (
          <div className="flex flex-1 items-center justify-center">
            <Empty title="Start a run">
              {agents.length === 0
                ? "Define an agent first, then start a run here."
                : "Pick an agent below and send it a request."}
            </Empty>
          </div>
        )}

        {canRun && agents.length > 0 && (
          <div className="mx-auto flex w-full max-w-3xl flex-col gap-2 px-4 pb-4">
            <Problem message={problem} />
            <PromptInput
              config={{ placeholder: `Ask ${target}` }}
              showSuggestions={false}
              onSubmit={(text) => void start(text)}
              footer={<AgentPicker agents={agents} value={target} onChange={setAgent} />}
            />
          </div>
        )}
      </section>
  );
}

function AgentPicker({
  agents,
  value,
  onChange,
}: {
  agents: Revision[];
  value: string;
  onChange: (name: string) => void;
}) {
  return (
    <label className="flex items-center gap-2 text-aui-xs text-aui-muted">
      Agent
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="h-7 rounded-aui border border-aui-border bg-aui-bg px-2 text-aui-xs text-aui-fg outline-none focus-visible:border-aui-accent focus-visible:ring-2 focus-visible:ring-aui-ring"
      >
        {agents.map((a) => (
          <option key={a.spec.name} value={a.spec.name}>
            {a.spec.name}
          </option>
        ))}
      </select>
    </label>
  );
}

function RunHeader({ run, trace, onTrace }: { run: Run; trace: boolean; onTrace: () => void }) {
  const who = run.actor?.display || run.actor?.id;
  const ended = run.status === "completed" || run.status === "failed";
  return (
    <header className="flex flex-wrap items-center gap-x-3 gap-y-1 border-aui-border border-b px-4 py-2.5">
      <h2 className="text-aui-base font-medium text-aui-fg">{run.agent}</h2>
      <Status status={run.status} />
      <Chip>rev {run.rev}</Chip>
      <Chip>{run.origin}</Chip>
      {run.mode && <Chip>{run.mode}</Chip>}
      <span className="text-aui-xs text-aui-muted">Budget: {usd(run.lease)}</span>
      {run.calls > 0 && <span className="text-aui-xs text-aui-muted">Calls: {run.calls}</span>}
      {who && <span className="text-aui-xs text-aui-muted">By {who}</span>}
      {run.schedule && <Chip>schedule {run.schedule}</Chip>}
      {ended && (
        <span className="text-aui-xs tabular-nums text-aui-muted">
          Took {took(Date.parse(run.updated_at) - Date.parse(run.created_at))}
        </span>
      )}
      {run.reason && <span className="text-aui-xs text-aui-muted">Waiting on: {run.reason}</span>}
      {!ended && run.error && (
        <span className="basis-full text-aui-xs text-aui-muted">Last attempt failed, retrying: {cause(run.error)}</span>
      )}
      <button type="button" aria-pressed={trace} onClick={onTrace} className={`${button} ml-auto h-7 px-2 text-aui-xs`}>
        {trace ? "Hide Trace" : "Show Trace"}
      </button>
    </header>
  );
}

/** The request that started the run, shown the way a person's message is. */
function Request({ run }: { run: Run }) {
  if (!run.text) return null;
  return (
    <div className="flex justify-end">
      <p className="max-w-[80%] whitespace-pre-wrap rounded-aui-lg bg-aui-surface px-3.5 py-2 text-aui-sm text-aui-fg">
        {run.text}
      </p>
    </div>
  );
}

/** Every run under the same root, indented by depth. */
function Tree({ runs, current, onSelect }: { runs: Run[]; current: string; onSelect: (id: string) => void }) {
  const ordered = useMemo(() => order(runs), [runs]);
  return (
    <nav aria-label="Call tree" className="border-aui-border border-b px-4 py-2">
      <p className="pb-1 text-aui-2xs font-medium uppercase tracking-wide text-aui-muted">Call tree</p>
      <ul className="flex flex-col">
        {ordered.map((r) => (
          <li key={r.id} style={{ paddingLeft: `${r.depth * 16}px` }}>
            <button
              type="button"
              onClick={() => onSelect(r.id)}
              aria-current={r.id === current}
              className="flex w-full items-center gap-2 rounded-aui px-2 py-1 text-left text-aui-sm text-aui-fg hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent aria-[current=true]:bg-aui-surface"
            >
              <span className="font-medium">{r.agent}</span>
              {r.mode && <Chip>{r.mode}</Chip>}
              <Status status={r.status} />
              <span className="truncate text-aui-xs text-aui-muted">{r.text}</span>
            </button>
          </li>
        ))}
      </ul>
    </nav>
  );
}

/** Depth-first, so each run sits directly under the run that called it. */
function order(runs: Run[]): Run[] {
  const children = new Map<string, Run[]>();
  for (const r of runs) {
    const key = r.parent ?? "";
    children.set(key, [...(children.get(key) ?? []), r]);
  }
  const out: Run[] = [];
  const walk = (parent: string) => {
    for (const r of children.get(parent) ?? []) {
      out.push(r);
      walk(r.id);
    }
  };
  walk("");
  return out.length === runs.length ? out : runs;
}

/** Every run in the tenant, newest first. */
export function RunList({ runs, onSelect }: { runs: Run[]; onSelect: (id: string) => void }) {
  return (
    <Page title="Runs" lede="Every run in this tenant, from people, schedules and other agents.">
        {runs.length === 0 ? (
          <Empty title="No runs yet">Start one with New Run.</Empty>
        ) : (
          <ul className="overflow-hidden rounded-aui-lg border border-aui-border">
            {runs.map((r) => (
              <li key={r.id} className="border-aui-border border-b last:border-b-0">
                <button
                  type="button"
                  onClick={() => onSelect(r.id)}
                  className="flex w-full items-center gap-3 bg-aui-surface px-4 py-2.5 text-left transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent focus-visible:-outline-offset-2"
                >
                  <span className="w-28 shrink-0 truncate text-aui-sm font-medium text-aui-fg">
                    {r.depth > 0 && <span aria-hidden>↳ </span>}
                    {r.agent}
                  </span>
                  <span className="min-w-0 flex-1 truncate text-aui-sm text-aui-muted">
                    {r.text || "No request text"}
                  </span>
                  <Chip>{r.origin}</Chip>
                  <span className="hidden w-24 shrink-0 text-right text-aui-xs text-aui-muted sm:inline">
                    {ago(r.created_at)}
                  </span>
                  <span className="w-20 shrink-0">
                    <Status status={r.status} />
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
    </Page>
  );
}
