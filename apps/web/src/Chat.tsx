import { answerOfAction, answers } from "@collegium/adapter/question";
import { defaultCopy } from "@vultus/components";
import { ActionId, type UiAction } from "@vultus/core";
import { CheckIcon, ChevronDownIcon, WebSurface } from "@vultus/render-web";
import { AgentMessage, Thread, useQueue } from "@vultus/web";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Activity } from "./Activity.tsx";
import type { Run } from "./adapter.ts";
import { Composer } from "./Composer.tsx";
import { ApiError, api, type Choice, type Project, type Revision, type Settings, type ThreadSummary } from "./api.ts";
import { HomePage } from "./HomePage.tsx";
import { ProjectPicker } from "./ProjectPicker.tsx";
import { useRunTurn } from "./Runs.tsx";
import { itemActions } from "./itemActions.tsx";
import { SessionGaugeFor } from "./SessionGauge.tsx";
import { AgentMark, AgentTint, cause, Popover, Presence, Problem } from "./ui.tsx";

const AUTO = "auto";
// Controls in the dock are text, not boxes: they sit under the composer and
// should not compete with it.
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
  onSwitch,
}: {
  run: Run;
  siblings: Run[];
  /** Follow the run's events. Off for settled turns, whose record is enough. */
  live: boolean;
  onSwitch: (id: string) => void;
}) {
  const turn = useRunTurn(live ? run : undefined);
  const index = siblings.findIndex((s) => s.id === run.id);
  const ended = run.status === "completed" || run.status === "failed";
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
    <article id={`turn-${run.id}`} className="flex flex-col gap-3" aria-label={`Turn answered by ${run.agent}`}>
      <div className="flex flex-col items-end gap-1">
        <p className="collegium-glass max-w-[80%] whitespace-pre-wrap rounded-aui-lg px-3.5 py-2 text-aui-sm text-aui-fg">{run.text}</p>
        <p className="flex flex-wrap items-center justify-end gap-x-2 text-aui-2xs text-aui-muted">
          <span className="inline-flex items-center gap-1">
            <AgentMark name={run.agent} size={14} />
            {run.agent}
          </span>
          {run.picked && <span>{run.picked}</span>}
        </p>
      </div>

      <AgentTint name={run.agent}>
        <AgentMessage turn={shown} itemActions={itemActions(run)} />
      </AgentTint>

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
        {run.calls > 0 && <span>{run.calls} calls to other agents</span>}
        {!ended && run.error && <span>Last attempt failed, retrying: {cause(run.error)}</span>}
      </div>
    </article>
  );
}

/**
 * One conversation: its turns along the branch on screen, and the box that
 * adds the next one. `threadId` null is a conversation not yet started.
 */
export function Chat({
  threadId,
  startIn,
  summary,
  tick,
  agents,
  settings,
  canRun,
  greeting,
  onThread,
  onChanged,
  onProjectCreated,
}: {
  threadId: string | null;
  /** The project a new conversation starts in, when it was started from one. */
  startIn?: string;
  /** The conversation at a glance, once it exists. */
  summary: ThreadSummary | undefined;
  /** Changes whenever any run starts or ends. */
  tick: number;
  agents: Revision[];
  settings: Settings | null;
  canRun: boolean;
  greeting: string;
  onThread: (id: string) => void;
  onChanged: () => void;
  onProjectCreated: (project: Project) => void;
}) {
  const [turns, setTurns] = useState<Run[]>([]);
  const [chosen, setChosen] = useState<Record<string, string>>({});
  const [auto, setAuto] = useState(true);
  const [agent, setAgent] = useState("");
  const [project, setProject] = useState<string | null>(startIn ?? null);
  const [preview, setPreview] = useState<Choice | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  // The drawer of every run in the conversation, open on one of them or on the list.
  const [activity, setActivity] = useState<{ focus: string | null } | null>(null);
  // What the drawer was showing, kept for the moment it takes to leave.
  const lastActivity = useRef(activity);
  if (activity) lastActivity.current = activity;

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
    // An agent drawn inside a turn opens the drawer on its run.
    if (a.id === ActionId.openRun) return setActivity({ focus: a.payload.target });
    // A question the run waits on is answered in place. The run's own events
    // settle the card once the answer is recorded, whoever's it was.
    if (answers(a)) {
      const answer = answerOfAction(a);
      if (!answer) return;
      setProblem(null);
      api.answer(answer).catch((e) =>
        setProblem(e instanceof ApiError && e.status === 403 ? defaultCopy.noPermission : e instanceof ApiError ? e.message : defaultCopy.failure.internal()),
      );
      return;
    }
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
    <div className="mx-auto flex w-full max-w-4xl flex-col gap-2 px-4 pb-4">
      <Problem message={problem} />
      <Composer
        placeholder={auto ? "Ask anything" : `Ask ${named}`}
        queue={queue}
        autoFocus={!threadId}
        onDraft={setDraft}
        leading={
          <>
        {threadId ? (
          <span className="max-w-40 truncate px-1.5 text-aui-sm text-aui-muted" title={tail?.project || "Projectless"}>{tail?.project || "Projectless"}</span>
        ) : (
          <ProjectPicker
            value={project}
            projects={settings?.projects ?? null}
            onChange={setProject}
            onCreated={onProjectCreated}
          />
        )}
          </>
        }
        trailing={
          <>
        {/* The reason for a pick is one hover away, and announced, without taking a line. */}
        <span className="sr-only" aria-live="polite">
          {auto && preview ? `${preview.agent}. ${preview.reason}` : ""}
        </span>
        <Popover
          label={auto && preview ? `Choose who answers. ${preview.reason}` : "Choose who answers"}
          align="right"
          up
          className="inline-flex h-7 items-center gap-1.5 rounded-aui px-1.5 text-aui-sm text-aui-fg transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent"
          button={
            <>
              {(!auto || preview) && <AgentMark name={auto ? (preview?.agent ?? "?") : named} size={16} />}
              <span className="max-w-16 truncate sm:max-w-32">{auto ? (preview?.agent ?? "Auto") : named}</span>
              {auto && preview && <span className="hidden text-aui-xs text-aui-muted sm:inline">Auto</span>}
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
                <input type="checkbox" className="collegium-switch" checked={auto} onChange={(e) => setAuto(e.target.checked)} />
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
          </>
        }
      />
    </div>
  );

  if (!threadId || path.length === 0) {
    return (
      <HomePage greeting={greeting} canRun={canRun} hasAgents={agents.length > 0}>
        {composer}
      </HomePage>
    );
  }

  const running = turns.filter((t) => t.status !== "completed" && t.status !== "failed").length;

  return (
    <section className="flex min-h-0 flex-1">
      <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      {/* Arrives with the conversation: its name, then who has answered in it. */}
      {/*
        Above the thread, so a panel opened from it is not painted over by the first message.
        The arrival animation is on the title and the agents, not the header: an animated
        ancestor stops a glass panel from blurring the page behind it.
      */}
      <header className="relative z-10 mx-auto flex w-full max-w-4xl flex-wrap items-center gap-x-4 gap-y-1 px-4 pt-4 pb-2">
        <h1 className="aui-fade-up min-w-0 flex-1 truncate text-aui-lg font-semibold text-aui-fg">{summary?.title ?? path[0]?.text}</h1>
        <ul className="aui-fade-up flex shrink-0 items-center gap-3 text-aui-xs text-aui-muted" aria-label="Agents in this conversation">
          {(summary?.agents ?? []).map((a) => (
            <li key={a} className="inline-flex items-center gap-1">
              <AgentMark name={a} size={14} />
              {a}
            </li>
          ))}
          {tail?.project && <li>{tail.project}</li>}
        </ul>
        <SessionGaugeFor tail={tail} />
        <button
          type="button"
          aria-expanded={activity !== null}
          onClick={() => setActivity(activity ? null : { focus: null })}
          className="shrink-0 rounded-aui px-2 py-1 text-aui-xs text-aui-muted hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-expanded:text-aui-fg"
        >
          Execution history{running > 0 ? ` ${running}` : ""}
        </button>
      </header>
      <WebSurface onAction={onAction} className="flex min-h-0 flex-1 flex-col">
        <Thread className="mx-auto w-full max-w-4xl flex-1 px-4" busy={!tailEnded} label="Conversation">
          {path.map((run, i) => (
            <TurnView
              key={run.id}
              run={run}
              siblings={siblingsOf(turns, run)}
              live={i === path.length - 1 || (run.status !== "completed" && run.status !== "failed")}
              onSwitch={(id) => setChosen((c) => ({ ...c, [run.prev ?? ""]: id }))}
            />
          ))}
        </Thread>
      </WebSurface>
      {composer}
      </div>
      <Presence when={activity !== null} ms={180}>
        {(leaving) => (
          <Activity
            leaving={leaving}
            turns={path}
            stamp={turns.map((t) => `${t.id}:${t.calls}:${t.status}`).join("|")}
            focus={(activity ?? lastActivity.current)?.focus ?? null}
            onClose={() => setActivity(null)}
            onJump={(id) => document.getElementById(`turn-${id}`)?.scrollIntoView({ block: "start", behavior: "smooth" })}
          />
        )}
      </Presence>
    </section>
  );
}
