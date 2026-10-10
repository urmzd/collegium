import { ActionId, type UiAction } from "@agent-ui/core";
import { CheckIcon, ChevronDownIcon, WebSurface } from "@agent-ui/render-web";
import { AgentMessage, PromptInput, Thread, useQueue } from "@agent-ui/web";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Run } from "./adapter.ts";
import { ApiError, api, type Choice, type Revision, type Settings } from "./api.ts";
import { useRunTurn } from "./Runs.tsx";
import { Trace } from "./Trace.tsx";
import { AgentMark, cause, Popover, Problem } from "./ui.tsx";

const AUTO = "auto";
// Controls in the dock are text, not boxes: they sit under the composer and
// should not compete with it.
const select =
  "h-7 max-w-60 cursor-pointer rounded-aui bg-transparent px-1.5 text-aui-sm text-aui-muted outline-none transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent disabled:opacity-50";

/** The answers to one point in the conversation, oldest first. */
function siblingsOf(turns: Run[], run: Run): Run[] {
  return turns.filter((t) => (t.prev ?? "") === (run.prev ?? ""));
}

/**
 * Walk the conversation from its start, taking one answer at each point: the
 * one the reader chose, or the newest. That walk is the branch on screen.
 */
function pathOf(turns: Run[], chosen: Record<string, string>): Run[] {
  const path: Run[] = [];
  let prev = "";
  for (;;) {
    const options = turns.filter((t) => (t.prev ?? "") === prev);
    const next = options.find((t) => t.id === chosen[prev]) ?? options[options.length - 1];
    if (!next || path.includes(next)) return path;
    path.push(next);
    prev = next.id;
  }
}

function TurnView({
  run,
  siblings,
  live,
  agents,
  canRun,
  onSwitch,
  onBranch,
}: {
  run: Run;
  siblings: Run[];
  /** Follow the run's events. Off for settled turns, whose record is enough. */
  live: boolean;
  agents: Revision[];
  canRun: boolean;
  onSwitch: (id: string) => void;
  onBranch: (agent: string) => void;
}) {
  const turn = useRunTurn(live ? run : undefined);
  const [trace, setTrace] = useState(false);
  const index = siblings.findIndex((s) => s.id === run.id);
  const ended = run.status === "completed" || run.status === "failed";
  const stamp = `${run.id}:${run.calls}:${run.status}`;
  const shown = useMemo(
    () =>
      turn ?? {
        id: run.id,
        agent: { name: run.agent },
        status: run.status === "completed" ? ("done" as const) : run.status === "failed" ? ("failed" as const) : ("working" as const),
        text: run.answer ?? "",
        tasks: [],
        citations: [],
        interrupts: [],
        notices: [],
        failure:
          run.status === "failed"
            ? { kind: "internal" as const, message: cause(run.error) || "The run failed.", reference: run.id }
            : undefined,
      },
    [turn, run],
  );

  return (
    <article className="flex flex-col gap-3" aria-label={`Turn answered by ${run.agent}`}>
      <div className="flex flex-col items-end gap-1">
        <p className="oap-glass max-w-[80%] whitespace-pre-wrap rounded-aui-lg px-3.5 py-2 text-aui-sm text-aui-fg">{run.text}</p>
        <p className="flex flex-wrap items-center justify-end gap-x-2 text-aui-2xs text-aui-muted">
          <span className="inline-flex items-center gap-1">
            <AgentMark name={run.agent} size={14} />
            {run.agent}
          </span>
          {run.picked && <span>{run.picked}</span>}
        </p>
      </div>

      <AgentMessage turn={shown} />

      <div className="flex flex-wrap items-center gap-2 pl-11 text-aui-xs text-aui-muted">
        {siblings.length > 1 && (
          <span className="inline-flex items-center gap-0.5">
            <button
              type="button"
              aria-label="Previous answer"
              disabled={index <= 0}
              onClick={() => onSwitch(siblings[index - 1]?.id ?? run.id)}
              className="inline-flex size-6 items-center justify-center rounded-aui hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent disabled:opacity-40"
            >
              ‹
            </button>
            <span className="tabular-nums" title={siblings.map((s) => s.agent).join(", ")}>
              {index + 1} / {siblings.length}
            </span>
            <button
              type="button"
              aria-label="Next answer"
              disabled={index >= siblings.length - 1}
              onClick={() => onSwitch(siblings[index + 1]?.id ?? run.id)}
              className="inline-flex size-6 items-center justify-center rounded-aui hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent disabled:opacity-40"
            >
              ›
            </button>
          </span>
        )}
        {canRun && ended && (
          <label className="inline-flex items-center gap-1.5">
            <span className="sr-only">Answer this again with another agent</span>
            <select
              className={select}
              value=""
              onChange={(e) => {
                if (e.target.value) onBranch(e.target.value);
              }}
            >
              <option value="">Branch: answer again with…</option>
              {agents.map((a) => (
                <option key={a.spec.name} value={a.spec.name}>
                  {a.spec.name}
                </option>
              ))}
            </select>
          </label>
        )}
        <button
          type="button"
          aria-pressed={trace}
          onClick={() => setTrace(!trace)}
          className="rounded-aui px-1.5 py-0.5 hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent"
        >
          {trace ? "Hide trace" : "Trace"}
        </button>
        {run.calls > 0 && <span>{run.calls} calls to other agents</span>}
        {!ended && run.error && <span>Last attempt failed, retrying: {cause(run.error)}</span>}
      </div>
      {trace && (
        <div className="oap-glass overflow-hidden rounded-aui-lg">
          <Trace run={run} stamp={stamp} />
        </div>
      )}
    </article>
  );
}

/**
 * One conversation: its turns along the branch on screen, and the box that
 * adds the next one. `threadId` null is a conversation not yet started.
 */
export function Chat({
  threadId,
  tick,
  agents,
  settings,
  canRun,
  greeting,
  onThread,
  onChanged,
}: {
  threadId: string | null;
  /** Changes whenever any run starts or ends. */
  tick: number;
  agents: Revision[];
  settings: Settings | null;
  canRun: boolean;
  greeting: string;
  onThread: (id: string) => void;
  onChanged: () => void;
}) {
  const [turns, setTurns] = useState<Run[]>([]);
  const [chosen, setChosen] = useState<Record<string, string>>({});
  const [auto, setAuto] = useState(true);
  const [agent, setAgent] = useState("");
  const [project, setProject] = useState("");
  const [preview, setPreview] = useState<Choice | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: tick is the refresh signal
  useEffect(() => {
    if (!threadId) return setTurns([]);
    let live = true;
    api
      .thread(threadId)
      .then((t) => live && setTurns(t))
      .catch(() => live && setTurns([]));
    return () => {
      live = false;
    };
  }, [threadId, tick]);

  // A different conversation starts from its newest branch.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset only when the conversation changes
  useEffect(() => {
    setChosen({});
    setProblem(null);
  }, [threadId]);

  const path = useMemo(() => pathOf(turns, chosen), [turns, chosen]);
  const tail = path[path.length - 1];
  const tailEnded = !tail || tail.status === "completed" || tail.status === "failed";
  const named = agent || settings?.baseline || agents[0]?.spec.name || "";
  const target = auto ? AUTO : named;

  // What this conversation's queue is waiting on, so it knows when to go on.
  const waiting = useRef<string | null>(null);

  const post = useCallback(
    async (text: string, to: string, after: string | undefined) => {
      setProblem(null);
      const run = await api.createRun(to, text, after ? { after } : project ? { project } : {});
      setTurns((t) => (t.some((x) => x.id === run.id) ? t : [...t, run]));
      setChosen((c) => ({ ...c, [run.prev ?? ""]: run.id }));
      onChanged();
      if (!threadId) onThread(run.thread);
      return run;
    },
    [threadId, project, onThread, onChanged],
  );

  const queue = useQueue({
    onStart: (m) => {
      post(m.text, target, tail?.id)
        .then((run) => {
          waiting.current = run.id;
        })
        .catch((e) => {
          setProblem(e instanceof ApiError ? e.message : "The message could not be sent.");
          queue.turnEnded("failed");
        });
    },
    // Stopping a run in flight is not built: the queue waits for it to end.
    onInterrupt: () => {},
  });

  // When the run the queue started settles, the queue may send the next one.
  // biome-ignore lint/correctness/useExhaustiveDependencies: react to the watched run's status only
  useEffect(() => {
    const id = waiting.current;
    const run = id ? turns.find((t) => t.id === id) : undefined;
    if (!run || (run.status !== "completed" && run.status !== "failed")) return;
    waiting.current = null;
    queue.turnEnded(run.status === "completed" ? "done" : "failed");
  }, [turns]);

  const branch = (run: Run, to: string) => {
    post(run.text, to, run.prev).catch((e) =>
      setProblem(e instanceof ApiError ? e.message : "The branch could not be started."),
    );
  };

  const onAction = (a: UiAction) => {
    if (a.id !== ActionId.retry || !canRun) return;
    const run = path.find((r) => r.id === a.payload.turnId || r.id === a.payload.target) ?? tail;
    if (run) branch(run, run.agent);
  };

  // Show who Auto would send the next message to, before it is sent.
  const [draft, setDraft] = useState("");
  useEffect(() => {
    if (!auto || draft.trim().length < 3) return setPreview(null);
    const timer = window.setTimeout(() => {
      api
        .pick(draft)
        .then(setPreview)
        .catch(() => setPreview(null));
    }, 250);
    return () => window.clearTimeout(timer);
  }, [auto, draft]);

  const composer = canRun && agents.length > 0 && (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-2 px-4 pb-4">
      <Problem message={problem} />
      {/* biome-ignore lint/a11y/noStaticElementInteractions: listens for typing that bubbles from the composer, to preview the pick */}
      <div onInput={(e) => setDraft((e.target as HTMLTextAreaElement).value ?? "")}>
        <PromptInput
          config={{ placeholder: auto ? "Ask anything" : `Ask ${named}` }}
          showSuggestions={false}
          autoFocus={!threadId}
          queue={queue}
        />
      </div>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-1 text-aui-xs text-aui-muted">
        {threadId ? (
          tail?.project && <span className="px-1.5 text-aui-sm">In {tail.project}</span>
        ) : (
          <label className="inline-flex items-center gap-1.5">
            <span className="sr-only">Project</span>
            <select className={select} value={project} onChange={(e) => setProject(e.target.value)}>
              <option value="">No project</option>
              {(settings?.projects ?? []).map((p) => (
                <option key={p.name}>{p.name}</option>
              ))}
            </select>
          </label>
        )}
        <span className="min-w-0 flex-1 truncate text-right" aria-live="polite">
          {auto
            ? preview
              ? preview.reason
              : settings?.baseline
                ? "Picks the agent that fits"
                : "No baseline agent is set"
            : ""}
        </span>
        <Popover
          label="Choose who answers"
          align="right"
          up
          className="inline-flex h-7 items-center gap-1.5 rounded-aui px-1.5 text-aui-sm text-aui-fg transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent"
          button={
            <>
              {(!auto || preview) && <AgentMark name={auto ? (preview?.agent ?? "?") : named} size={16} />}
              <span className="max-w-32 truncate">{auto ? (preview?.agent ?? "Auto") : named}</span>
              {auto && preview && <span className="text-aui-xs text-aui-muted">Auto</span>}
              <ChevronDownIcon size={14} className="text-aui-muted" />
            </>
          }
        >
          {(close) => (
            <div className="flex flex-col">
              <label className="flex items-center justify-between gap-3 rounded-aui px-2.5 py-2">
                <span className="min-w-0">
                  <span className="block text-aui-sm text-aui-fg">Auto</span>
                  <span className="block text-aui-xs text-aui-muted">
                    Send each message to the agent it fits best
                    {settings?.baseline ? `, or ${settings.baseline} when none does.` : "."}
                  </span>
                </span>
                <input type="checkbox" className="oap-switch" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
              </label>
              <div className="my-1 h-px bg-aui-border" />
              <ul className="aui-scroll max-h-72 overflow-y-auto" aria-label="Agents">
                {agents.map((a) => {
                  const on = !auto && a.spec.name === named;
                  return (
                    <li key={a.spec.name}>
                      <button
                        type="button"
                        aria-pressed={on}
                        onClick={() => {
                          setAuto(false);
                          setAgent(a.spec.name);
                          close();
                        }}
                        className="flex w-full items-center gap-2.5 rounded-aui px-2.5 py-2 text-left transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent aria-pressed:bg-aui-hover"
                      >
                        <AgentMark name={a.spec.name} size={18} />
                        <span className="min-w-0 flex-1">
                          <span className="flex items-center gap-2 text-aui-sm text-aui-fg">
                            {a.spec.name}
                            {a.spec.name === settings?.baseline && (
                              <span className="rounded-full border border-aui-border px-1.5 text-aui-2xs text-aui-muted">Baseline</span>
                            )}
                          </span>
                          <span className="block truncate text-aui-xs text-aui-muted">
                            {a.spec.description || "No description"}
                          </span>
                        </span>
                        {on && <CheckIcon size={14} className="text-aui-fg" />}
                      </button>
                    </li>
                  );
                })}
              </ul>
            </div>
          )}
        </Popover>
      </div>
    </div>
  );

  if (!threadId || path.length === 0) {
    return (
      <section className="flex min-h-0 flex-1 flex-col justify-center">
        <div className="flex flex-col items-center gap-1 px-6 pb-6 text-center">
          <h1 className="text-aui-xl font-semibold text-aui-fg">{greeting}</h1>
          <p className="max-w-md text-aui-sm text-aui-muted">
            {agents.length === 0
              ? "Define an agent first, then start a conversation here."
              : "Ask anything. Auto sends it to the agent whose description fits, or choose one yourself."}
          </p>
        </div>
        {composer}
      </section>
    );
  }

  return (
    <section className="flex min-h-0 flex-1 flex-col">
      <WebSurface onAction={onAction} className="flex min-h-0 flex-1 flex-col">
        <Thread className="mx-auto w-full max-w-3xl flex-1 px-4" busy={!tailEnded} label="Conversation">
          {path.map((run, i) => (
            <TurnView
              key={run.id}
              run={run}
              siblings={siblingsOf(turns, run)}
              live={i === path.length - 1 || (run.status !== "completed" && run.status !== "failed")}
              agents={agents}
              canRun={canRun}
              onSwitch={(id) => setChosen((c) => ({ ...c, [run.prev ?? ""]: id }))}
              onBranch={(to) => branch(run, to)}
            />
          ))}
        </Thread>
      </WebSurface>
      {composer}
    </section>
  );
}
