import { type FormEvent, useState } from "react";
import type { Run } from "./adapter.ts";
import { ApiError, api, type Revision, type Schedule, type ScheduleView } from "./api.ts";
import { agentOptions, ago, button, card, Chip, Empty, field, Label, Menu, Page, Problem, primary } from "./ui.tsx";

const presets: { label: string; expression: string }[] = [
  { label: "Every minute", expression: "* * * * *" },
  { label: "Hourly", expression: "0 * * * *" },
  { label: "Daily at 09:00 UTC", expression: "0 9 * * *" },
  { label: "Weekdays at 09:00 UTC", expression: "0 9 * * 1-5" },
];

const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

/** A cron expression in words, for the common shapes. Otherwise the expression itself. */
function inWords(expression: string): string {
  const preset = presets.find((p) => p.expression === expression);
  if (preset) return preset.label;
  const [min, hour, dom, month, dow] = expression.trim().split(/\s+/);
  const at = (h: string, m: string) => `${h.padStart(2, "0")}:${m.padStart(2, "0")} UTC`;
  const num = (s?: string) => s !== undefined && /^\d+$/.test(s);
  if (!num(min) || dom !== "*" || month !== "*") return expression;
  if (hour === "*" && dow === "*") return `Hourly at minute ${min}`;
  if (!num(hour)) return expression;
  if (dow === "*") return `Daily at ${at(hour as string, min as string)}`;
  if (num(dow)) return `${days[Number(dow) % 7]}s at ${at(hour as string, min as string)}`;
  return expression;
}

/** In UTC, because that is the zone the expression is written in. */
function when(iso: string): string {
  const at = new Date(iso).toLocaleString(undefined, {
    timeZone: "UTC",
    weekday: "short",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
  return `${at} UTC`;
}

const outcome: Record<Run["status"], { fill: string; word: string }> = {
  completed: { fill: "bg-aui-ok", word: "done" },
  failed: { fill: "bg-aui-bad", word: "failed" },
  parked: { fill: "bg-aui-warn", word: "waiting" },
  running: { fill: "bg-aui-neutral", word: "running" },
  accepted: { fill: "bg-aui-neutral", word: "queued" },
};

/**
 * The last occurrences, oldest to newest, one mark each. Height separates a
 * failure from a success as well as color does, and every mark is a button
 * that names its outcome.
 */
function History({ runs, onOpenRun }: { runs: Run[]; onOpenRun: (id: string) => void }) {
  if (runs.length === 0) return <span className="text-aui-xs text-aui-muted">Has not run yet</span>;
  const ordered = runs.slice().reverse();
  const failed = runs.filter((r) => r.status === "failed").length;
  return (
    <div className="flex items-end gap-3">
      <ul className="flex h-5 items-end gap-0.5" aria-label="Recent runs, oldest first">
        {ordered.map((r) => (
          <li key={r.id} className="flex h-full items-end">
            <button
              type="button"
              onClick={() => onOpenRun(r.id)}
              title={`${outcome[r.status].word}, ${ago(r.created_at)}`}
              aria-label={`Run ${ago(r.created_at)}, ${outcome[r.status].word}`}
              className={`w-1.5 rounded-t-sm ${outcome[r.status].fill} ${
                r.status === "failed" ? "h-full" : "h-3"
              } hover:opacity-70 focus-visible:outline-2 focus-visible:outline-offset-1 focus-visible:outline-aui-accent`}
            />
          </li>
        ))}
      </ul>
      <span className="text-aui-xs text-aui-muted">
        Last {runs.length}: {runs.length - failed} ok, {failed} failed
      </span>
    </div>
  );
}

export function Schedules({
  schedules,
  agents,
  canWrite,
  onChanged,
  onOpenRun,
}: {
  schedules: ScheduleView[];
  agents: Revision[];
  canWrite: boolean;
  onChanged: () => void;
  onOpenRun: (id: string) => void;
}) {
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState<Schedule>({ name: "", expression: "0 9 * * *", agent: "", text: "" });
  const [problem, setProblem] = useState<string | null>(null);
  const set = (patch: Partial<Schedule>) => setDraft((d) => ({ ...d, ...patch }));
  const agent = draft.agent || agents[0]?.spec.name || "";

  const fail = (fallback: string) => (e: unknown) => setProblem(e instanceof ApiError ? e.message : fallback);
  const act = (p: Promise<unknown>, fallback: string) => {
    setProblem(null);
    p.then(onChanged).catch(fail(fallback));
  };

  const create = (e: FormEvent) => {
    e.preventDefault();
    setProblem(null);
    api
      .createSchedule({ ...draft, agent })
      .then(() => {
        setDraft({ name: "", expression: draft.expression, agent, text: "" });
        setAdding(false);
        onChanged();
      })
      .catch(fail("The schedule could not be saved."));
  };

  return (
    <Page
      title="Schedules"
      lede="A schedule sends an agent the same request on a timer. Each occurrence is a run you can open."
      actions={
        canWrite && agents.length > 0 && !adding ? (
          <button type="button" className={primary} onClick={() => setAdding(true)}>
            New Schedule
          </button>
        ) : undefined
      }
    >
      <Problem message={problem} />
      {adding && (
        <form onSubmit={create} className={`${card} flex flex-col gap-3`}>
          <h2 className="text-aui-base font-medium text-aui-fg">New schedule</h2>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <Label text="Name" hint="Letters, digits, dashes.">
              <input className={field} value={draft.name} required onChange={(e) => set({ name: e.target.value })} />
            </Label>
            <Label text="Send to agent">
              <Menu
                label="Send to agent"
                value={agent}
                options={agentOptions(agents.map((a) => a.spec.name))}
                onChange={(next) => set({ agent: next })}
              />
            </Label>
            <Label text="How often" hint={`${inWords(draft.expression)}. Fields: minute hour day month weekday.`}>
              <input
                className={`${field} font-aui-mono`}
                value={draft.expression}
                required
                onChange={(e) => set({ expression: e.target.value })}
              />
            </Label>
          </div>
          <div className="flex flex-wrap gap-1.5">
            {presets.map((p) => (
              <button
                key={p.expression}
                type="button"
                aria-pressed={draft.expression === p.expression}
                className={`${button} h-6 px-2 text-aui-xs aria-pressed:border-aui-accent`}
                onClick={() => set({ expression: p.expression })}
              >
                {p.label}
              </button>
            ))}
          </div>
          <Label text="What to ask" hint="Nobody is typing when a schedule fires, so this is the message the agent gets.">
            <textarea className={`${field} min-h-16`} value={draft.text} required onChange={(e) => set({ text: e.target.value })} />
          </Label>
          <p className="text-aui-xs text-aui-muted">
            Answers appear here as runs. Sending them to Slack, email or another system needs connections, which are not
            built yet.
          </p>
          <div className="flex justify-end gap-2">
            <button type="button" className={button} onClick={() => setAdding(false)}>
              Cancel
            </button>
            <button type="submit" className={primary}>
              Create Schedule
            </button>
          </div>
        </form>
      )}

      {schedules.length === 0 && !adding ? (
        <div className={card}>
          <Empty title="No schedules">{agents.length === 0 ? "Define an agent first." : "Nothing runs on a timer yet."}</Empty>
        </div>
      ) : (
        <ul className="flex flex-col gap-2">
          {schedules.map((s) => (
            <li key={s.name} className={`${card} flex flex-col gap-3`}>
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <h2 className="text-aui-base font-medium text-aui-fg">{s.name}</h2>
                    <Chip>{s.paused ? "Paused" : "Active"}</Chip>
                  </div>
                  <p className="text-aui-sm text-aui-muted">
                    {inWords(s.expression)} <code className="font-aui-mono text-aui-xs">({s.expression})</code>
                  </p>
                </div>
                {canWrite && (
                  <div className="flex shrink-0 flex-wrap gap-2">
                    <button
                      type="button"
                      className={button}
                      onClick={() => {
                        setProblem(null);
                        api
                          .runSchedule(s.name)
                          .then((run) => {
                            onChanged();
                            onOpenRun(run.id);
                          })
                          .catch(fail("The schedule could not be run."));
                      }}
                    >
                      Run Now
                    </button>
                    <button
                      type="button"
                      className={button}
                      onClick={() => act(api.pauseSchedule(s.name, !s.paused), "The schedule could not be changed.")}
                    >
                      {s.paused ? "Resume" : "Pause"}
                    </button>
                    <button
                      type="button"
                      className={button}
                      onClick={() => act(api.deleteSchedule(s.name), "The schedule could not be deleted.")}
                    >
                      Delete
                    </button>
                  </div>
                )}
              </div>

              <dl className="grid grid-cols-1 gap-x-6 gap-y-2 text-aui-sm sm:grid-cols-[auto_1fr]">
                <dt className="text-aui-xs text-aui-muted">Sends to</dt>
                <dd className="text-aui-fg">{s.agent}</dd>
                <dt className="text-aui-xs text-aui-muted">Asks</dt>
                <dd className="whitespace-pre-wrap text-aui-fg">{s.text}</dd>
                <dt className="text-aui-xs text-aui-muted">Next</dt>
                <dd className="text-aui-fg">
                  {s.paused ? (
                    <span className="text-aui-muted">Paused. It will not fire until resumed.</span>
                  ) : s.next[0] ? (
                    <>
                      {when(s.next[0])}
                      <span className="text-aui-muted">
                        {s.next.slice(1).map((n) => (
                          <span key={n}>, then {when(n)}</span>
                        ))}
                      </span>
                    </>
                  ) : (
                    <span className="text-aui-muted">Never, with this expression.</span>
                  )}
                </dd>
                <dt className="text-aui-xs text-aui-muted">History</dt>
                <dd>
                  <History runs={s.recent} onOpenRun={onOpenRun} />
                </dd>
              </dl>
            </li>
          ))}
        </ul>
      )}
    </Page>
  );
}
