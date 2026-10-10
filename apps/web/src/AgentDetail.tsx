import { useEffect, useMemo, useState } from "react";
import {
  type AgentSpec,
  type Alert,
  type AlertRule,
  ApiError,
  api,
  type Group,
  type Note,
  type Revision,
  type Source,
  type SuiteView,
} from "./api.ts";
import { Evals } from "./Evals.tsx";
import { AgentMark, ago, button, card, Chip, field, Label, Menu, Problem, primary, Switch, Tabs, took } from "./ui.tsx";

type Tab = "prompt" | "setup" | "memory" | "evals" | "alerts" | "revisions";

const models: Record<string, string> = {
  offline: "offline",
  anthropic: "claude-sonnet-5-5",
  ollama: "llama3.2",
};

/** A rough size. Models count tokens differently, so this is a guide, not a bill. */
function approxTokens(text: string): number {
  return Math.ceil(text.length / 4);
}

/** Lines only in one side, in order: enough to see what a revision changed. */
function diff(before: string, after: string): { sign: "+" | "-" | " "; line: string }[] {
  const a = before.split("\n");
  const b = after.split("\n");
  // Longest common subsequence, by line.
  const lcs: number[][] = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      const row = lcs[i] as number[];
      row[j] = a[i] === b[j] ? ((lcs[i + 1] as number[])[j + 1] ?? 0) + 1 : Math.max((lcs[i + 1] as number[])[j] ?? 0, row[j + 1] ?? 0);
    }
  }
  const out: { sign: "+" | "-" | " "; line: string }[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ sign: " ", line: a[i] as string });
      i++;
      j++;
    } else if (((lcs[i + 1] as number[])[j] ?? 0) >= ((lcs[i] as number[])[j + 1] ?? 0)) {
      out.push({ sign: "-", line: a[i++] as string });
    } else {
      out.push({ sign: "+", line: b[j++] as string });
    }
  }
  while (i < a.length) out.push({ sign: "-", line: a[i++] as string });
  while (j < b.length) out.push({ sign: "+", line: b[j++] as string });
  return out;
}

function Diff({ before, after }: { before: string; after: string }) {
  const lines = diff(before, after);
  if (lines.every((l) => l.sign === " ")) return <p className="text-aui-xs text-aui-muted">The prompt did not change.</p>;
  return (
    <pre className="aui-scroll max-h-72 overflow-auto rounded-aui border border-aui-border p-2 font-aui-mono text-aui-xs">
      {lines.map((l, n) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: a diff line has no identity beyond its position
        <div key={n} className={l.sign === "+" ? "bg-aui-ok/15 text-aui-fg" : l.sign === "-" ? "bg-aui-bad/15 text-aui-fg" : "text-aui-muted"}>
          <span className="inline-block w-4 select-none">{l.sign}</span>
          {l.line || " "}
        </div>
      ))}
    </pre>
  );
}

const presets: { label: string; rule: (agent: string) => AlertRule }[] = [
  { label: "A run fails", rule: (a) => ({ name: `${a}-fails`, kind: "run_failed", agent: a, severity: "critical" }) },
  {
    label: "Half its runs fail in 15 minutes",
    rule: (a) => ({ name: `${a}-failure-rate`, kind: "failure_rate", agent: a, severity: "critical", threshold: 50, window_seconds: 900, min_runs: 5 }),
  },
  { label: "A run takes over a minute", rule: (a) => ({ name: `${a}-slow`, kind: "slow_run", agent: a, severity: "warning", threshold: 60 }) },
  { label: "An eval passes under 80%", rule: (a) => ({ name: `${a}-quality`, kind: "eval_below", agent: a, severity: "warning", threshold: 80 }) },
];

/** One agent: its prompt, its setup, how it is measured, and its history. */
export function AgentDetail({
  revision,
  startOn,
  agents,
  sources,
  suites,
  rules,
  alerts,
  stats,
  baseline,
  canWrite,
  canEval,
  canAlert,
  onBack,
  onChanged,
  onRulesChanged,
  onSuitesChanged,
  onOpenRun,
  onBaseline,
}: {
  /** null creates a new agent. */
  revision: Revision | null;
  /** Open on the Memory tab, for someone arriving to review proposals. */
  startOn?: "memory";
  agents: Revision[];
  sources: Source[];
  suites: SuiteView[];
  rules: AlertRule[];
  alerts: Alert[];
  stats: Group | undefined;
  baseline: string;
  canWrite: boolean;
  canEval: boolean;
  canAlert: boolean;
  onBack: () => void;
  onChanged: (name: string) => void;
  onRulesChanged: () => void;
  onSuitesChanged: () => void;
  onOpenRun: (id: string) => void;
  onBaseline: (name: string) => void;
}) {
  const saved: AgentSpec = revision?.spec ?? {
    name: "",
    description: "",
    provider: "offline",
    model: "offline",
    system_prompt: "",
    access: { spawn: [] },
  };
  const [spec, setSpec] = useState<AgentSpec>(saved);
  const [tab, setTab] = useState<Tab>(startOn === "memory" && revision ? "memory" : "prompt");
  const [problem, setProblem] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [history, setHistory] = useState<Revision[]>([]);
  const [notes, setNotes] = useState<Note[]>([]);
  const [note, setNote] = useState("");
  const [picked, setPicked] = useState<Set<string>>(() => new Set());
  const set = (patch: Partial<AgentSpec>) => setSpec((s) => ({ ...s, ...patch }));
  const name = revision?.spec.name ?? "";

  // A new revision of the same agent replaces the draft with what was saved.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset when the saved revision changes
  useEffect(() => {
    setSpec(saved);
    setProblem(null);
  }, [revision?.spec.name, revision?.rev]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: reload when a revision lands
  useEffect(() => {
    if (!name) return setHistory([]);
    let live = true;
    api
      .history(name)
      .then((h) => live && setHistory(h))
      .catch(() => live && setHistory([]));
    return () => {
      live = false;
    };
  }, [name, revision?.rev]);

  const loadNotes = () => {
    if (name) api.memory(name).then(setNotes).catch(() => setNotes([]));
  };
  // biome-ignore lint/correctness/useExhaustiveDependencies: reload when the agent changes or its tab is opened
  useEffect(loadNotes, [name, tab]);

  const kept = notes.filter((n) => n.status === "approved");
  const proposed = notes.filter((n) => n.status === "pending");

  const dirty = useMemo(() => JSON.stringify(spec) !== JSON.stringify(saved), [spec, saved]);
  const myRules = rules.filter((r) => r.agent === name);
  const myOpen = alerts.filter((a) => a.agent === name && !a.acked_by).length;
  const mySuites = suites.filter((s) => s.target === "agent" && s.subject === name);
  const spawn = spec.access?.spawn ?? [];
  const reads = spec.sources ?? [];
  const callable = agents.map((a) => a.spec.name).filter((n) => n !== spec.name);

  const save = async () => {
    setSaving(true);
    setProblem(null);
    try {
      const next = revision ? await api.reviseAgent(spec, revision.rev) : await api.createAgent(spec);
      onChanged(next.spec.name);
    } catch (e) {
      setProblem(e instanceof ApiError ? e.message : "The agent could not be saved.");
    } finally {
      setSaving(false);
    }
  };

  const restore = async (old: Revision) => {
    if (!revision) return;
    setProblem(null);
    try {
      const next = await api.reviseAgent(old.spec, revision.rev);
      onChanged(next.spec.name);
    } catch (e) {
      setProblem(e instanceof ApiError ? e.message : "The revision could not be restored.");
    }
  };

  const addRule = (rule: AlertRule) => {
    setProblem(null);
    api
      .createAlertRule(rule)
      .then(onRulesChanged)
      .catch((e) => setProblem(e instanceof ApiError ? e.message : "The rule could not be created."));
  };

  const ended = stats ? stats.completed + stats.failed : 0;

  return (
    <div className="aui-scroll min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-4xl flex-col gap-4 px-4 py-6">
        <button
          type="button"
          onClick={onBack}
          className="self-start rounded-aui text-aui-xs text-aui-muted hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent"
        >
          ‹ All agents
        </button>

        <header className="flex flex-wrap items-start justify-between gap-4">
          <div className="flex min-w-0 items-center gap-3">
            <AgentMark name={spec.name || "?"} size={32} />
            <div className="min-w-0">
              <h1 className="truncate text-aui-xl font-semibold text-aui-fg">{revision ? name : spec.name || "New agent"}</h1>
              <p className="flex flex-wrap items-center gap-x-3 text-aui-xs text-aui-muted">
                {revision ? (
                  <>
                    <span>Revision {revision.rev}</span>
                    <span>
                      {revision.spec.provider} / {revision.spec.model}
                    </span>
                    <span>
                      Saved {ago(revision.created_at)} by {revision.created_by}
                    </span>
                  </>
                ) : (
                  <span>Not saved yet</span>
                )}
              </p>
            </div>
          </div>
          {canWrite && (
            <div className="flex shrink-0 items-center gap-2">
              {dirty && <span className="text-aui-xs text-aui-muted">Unsaved changes</span>}
              {dirty && revision && (
                <button type="button" className={button} onClick={() => setSpec(saved)}>
                  Discard
                </button>
              )}
              <button type="button" className={primary} disabled={!dirty || saving} onClick={save}>
                {revision ? `Save as Revision ${revision.rev + 1}` : "Create Agent"}
              </button>
            </div>
          )}
        </header>

        {revision && (
          <dl className="grid grid-cols-2 gap-3 sm:grid-cols-5">
            {[
              ["Runs, 24h", String(stats?.runs ?? 0)],
              ["Success", ended ? `${Math.round((100 * (stats?.completed ?? 0)) / ended)}%` : "No data"],
              ["Median", stats?.p50_ms ? took(stats.p50_ms) : "–"],
              ["Open alerts", String(myOpen)],
              ["Evals", String(mySuites.length)],
            ].map(([label, value]) => (
              <div key={label} className={`${card} py-3`}>
                <dt className="text-aui-xs text-aui-muted">{label}</dt>
                <dd className="text-aui-lg font-semibold tabular-nums text-aui-fg">{value}</dd>
              </div>
            ))}
          </dl>
        )}

        <Problem message={problem} />

        <Tabs
          label="Agent sections"
          value={tab}
          onChange={setTab}
          tabs={[
            { id: "prompt", label: "Prompt" },
            { id: "setup", label: "Setup" },
            ...(revision
              ? ([
                  { id: "memory", label: "Memory", count: proposed.length },
                  { id: "evals", label: "Evals", count: mySuites.length },
                  { id: "alerts", label: "Alerts", count: myRules.length },
                  { id: "revisions", label: "Revisions", count: history.length },
                ] as const)
              : []),
          ]}
        />

        {tab === "prompt" && (
          <section className="flex flex-col gap-3">
            {!revision && (
              <Label text="Name" hint="Letters, digits and dashes. It cannot change later.">
                <input className={field} value={spec.name} required onChange={(e) => set({ name: e.target.value })} />
              </Label>
            )}
            <Label
              text="Description"
              hint="One or two lines on what this agent is for. Auto reads it first when deciding who answers, the way a skill's description is read. Leave it empty and the prompt is searched instead."
            >
              <textarea
                className={`${field} min-h-14`}
                value={spec.description ?? ""}
                disabled={!canWrite}
                maxLength={400}
                onChange={(e) => set({ description: e.target.value })}
              />
            </Label>
            <div className="flex flex-col gap-1">
              <div className="flex items-baseline justify-between">
                <label htmlFor="agent-prompt" className="text-aui-xs font-medium text-aui-muted">
                  System prompt
                </label>
                <span className="text-aui-2xs tabular-nums text-aui-muted">
                  {spec.system_prompt.split("\n").length} lines, {spec.system_prompt.length.toLocaleString()} characters, about{" "}
                  {approxTokens(spec.system_prompt).toLocaleString()} tokens
                </span>
              </div>
              <textarea
                id="agent-prompt"
                className={`${field} oap-glass min-h-[22rem] resize-y font-aui-mono text-aui-sm leading-relaxed`}
                value={spec.system_prompt}
                disabled={!canWrite}
                spellCheck={false}
                placeholder="Who the agent is, what it is for, how it should answer, and what it must never do."
                onChange={(e) => set({ system_prompt: e.target.value })}
              />
            </div>
            {revision && dirty && spec.system_prompt !== saved.system_prompt && (
              <details className={card} open>
                <summary className="cursor-pointer text-aui-sm text-aui-fg">Changes since revision {revision.rev}</summary>
                <div className="pt-2">
                  <Diff before={saved.system_prompt} after={spec.system_prompt} />
                </div>
              </details>
            )}
            {revision && (
              <p className="text-aui-xs text-aui-muted">
                Runs and evals use the saved revision. Save your changes, then run the evals to compare this revision with
                the last.
              </p>
            )}
          </section>
        )}

        {tab === "setup" && (
          <section className="flex flex-col gap-3">
            <div className={`${card} grid grid-cols-1 gap-3 sm:grid-cols-2`}>
              <Label text="Provider" hint="Offline calls no model and needs no key.">
                <Menu
                  label="Provider"
                  value={spec.provider}
                  disabled={!canWrite}
                  options={[
                    { value: "offline", label: "offline", hint: "Calls no model. For trying the platform out." },
                    { value: "anthropic", label: "anthropic" },
                    { value: "ollama", label: "ollama", hint: "A model running on this machine." },
                  ]}
                  onChange={(provider) => set({ provider, model: models[provider] ?? spec.model })}
                />
              </Label>
              <Label text="Model">
                <input className={field} value={spec.model} disabled={!canWrite} onChange={(e) => set({ model: e.target.value })} />
              </Label>
            </div>

            <div className={`${card} flex flex-col gap-3`}>
              <h3 className="text-aui-sm font-medium text-aui-fg">May call</h3>
              {callable.length === 0 ? (
                <p className="text-aui-sm text-aui-muted">No other agents exist. An agent can call no one unless it is allowed here.</p>
              ) : (
                callable.map((n) => (
                  <Switch
                    key={n}
                    label={n}
                    hint={agents.find((a) => a.spec.name === n)?.spec.description || undefined}
                    checked={spawn.includes(n)}
                    disabled={!canWrite}
                    onChange={(on) => set({ access: { ...spec.access, spawn: on ? [...spawn, n] : spawn.filter((s) => s !== n) } })}
                  />
                ))
              )}
            </div>

            <div className={`${card} flex flex-col gap-3`}>
              <h3 className="text-aui-sm font-medium text-aui-fg">Knowledge</h3>
              {sources.length === 0 ? (
                <p className="text-aui-sm text-aui-muted">No sources exist yet. Add one under Knowledge.</p>
              ) : (
                sources.map((k) => (
                  <Switch
                    key={k.name}
                    label={k.name}
                    hint={k.description || `${k.documents} documents`}
                    checked={reads.some((r) => r.name === k.name)}
                    disabled={!canWrite}
                    onChange={(on) =>
                      set({
                        sources: on ? [...reads, { name: k.name, uri: `knowledge://${k.name}` }] : reads.filter((r) => r.name !== k.name),
                      })
                    }
                  />
                ))
              )}
              <p className="text-aui-xs text-aui-muted">
                Assigning a source records the intent. Retrieval inside a run is not wired yet.
              </p>
            </div>

            {revision && (
              <div className={`${card} flex flex-col gap-3`}>
                <Switch
                  label="Baseline agent"
                  hint="Answers when Auto finds no agent that fits. One agent holds this at a time."
                  checked={baseline === name}
                  disabled={!canWrite}
                  onChange={(on) => onBaseline(on ? name : "")}
                />
              </div>
            )}

            <p className="text-aui-xs text-aui-muted">
              Temperature, token limits and step limits are not settings yet: the agent spec this platform runs on has no
              fields for them.
            </p>
          </section>
        )}

        {tab === "memory" && revision && (
          <section className="flex flex-col gap-3">
            <p className="max-w-xl text-aui-sm text-aui-muted">
              What {name} carries from one conversation to the next. Every note is sent with each turn, so keep them few
              and lasting.
            </p>
            {canWrite && (
              <form
                className="flex gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  setProblem(null);
                  api
                    .addMemory(name, note)
                    .then(() => {
                      setNote("");
                      loadNotes();
                    })
                    .catch((err) => setProblem(err instanceof ApiError ? err.message : "The note could not be saved."));
                }}
              >
                <input
                  aria-label="Something to remember"
                  className={field}
                  value={note}
                  maxLength={300}
                  required
                  placeholder="A lasting fact, such as: Releases go out on Tuesdays."
                  onChange={(e) => setNote(e.target.value)}
                />
                <button type="submit" className={primary}>
                  Remember
                </button>
              </form>
            )}
            {proposed.length > 0 && (
              <div className={`${card} flex flex-col gap-2`}>
                <h3 className="text-aui-sm font-medium text-aui-fg">Proposed by the memory agent ({proposed.length})</h3>
                <p className="text-aui-xs text-aui-muted">
                  Not in use yet. Tick what {name} should remember, then keep it. Discard the rest.
                </p>
                <ul>
                  {proposed.map((n) => (
                    <li key={n.id} className="border-aui-border border-t py-2 first:border-t-0">
                      <label className="flex items-start gap-2.5 text-aui-sm text-aui-fg">
                        <input
                          type="checkbox"
                          className="mt-1"
                          disabled={!canWrite}
                          checked={picked.has(n.id)}
                          onChange={(e) =>
                            setPicked((p) => {
                              const next = new Set(p);
                              if (e.target.checked) next.add(n.id);
                              else next.delete(n.id);
                              return next;
                            })
                          }
                        />
                        <span className="min-w-0">
                          <span className="block">{n.text}</span>
                          <span className="block text-aui-xs text-aui-muted">Proposed {ago(n.at)}</span>
                        </span>
                      </label>
                    </li>
                  ))}
                </ul>
                {canWrite && (
                  <div className="flex flex-wrap justify-end gap-2">
                    <button
                      type="button"
                      className={button}
                      onClick={() =>
                        Promise.all(
                          proposed.filter((n) => picked.size === 0 || picked.has(n.id)).map((n) => api.removeMemory(name, n.id)),
                        )
                          .then(() => {
                            setPicked(new Set());
                            loadNotes();
                          })
                          .catch(() => {})
                      }
                    >
                      {picked.size > 0 ? `Discard ${picked.size} Selected` : "Discard All"}
                    </button>
                    <button
                      type="button"
                      className={primary}
                      disabled={picked.size === 0}
                      onClick={() =>
                        api
                          .approveMemory(name, [...picked])
                          .then(() => {
                            setPicked(new Set());
                            loadNotes();
                          })
                          .catch((err) => setProblem(err instanceof ApiError ? err.message : "Those could not be kept."))
                      }
                    >
                      Keep {picked.size > 0 ? `${picked.size} Selected` : "Selected"}
                    </button>
                  </div>
                )}
              </div>
            )}
            {kept.length === 0 ? (
              <p className={`${card} text-aui-sm text-aui-muted`}>
                {name} remembers nothing yet. Write a note here, or choose a memory agent under Settings to have notes
                proposed after each turn.
              </p>
            ) : (
              <ul className="oap-glass overflow-hidden rounded-aui-lg">
                {kept.map((n) => (
                  <li key={n.id} className="flex items-start justify-between gap-3 border-aui-border border-b px-4 py-2.5 last:border-b-0">
                    <span className="min-w-0">
                      <span className="block text-aui-sm text-aui-fg">{n.text}</span>
                      <span className="block text-aui-xs text-aui-muted">
                        {n.source === "person" ? `Written by ${n.by}` : "Proposed by the memory agent and approved"}, {ago(n.at)}
                      </span>
                    </span>
                    {canWrite && (
                      <button
                        type="button"
                        className={button}
                        onClick={() => api.removeMemory(name, n.id).then(loadNotes).catch(() => {})}
                      >
                        Forget
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>
        )}

        {tab === "evals" && revision && (
          <Evals target="agent" subject={name} suites={suites} canWrite={canEval} onChanged={onSuitesChanged} onOpenRun={onOpenRun} />
        )}

        {tab === "alerts" && revision && (
          <section className="flex flex-col gap-3">
            <p className="max-w-xl text-aui-sm text-aui-muted">
              Rules that watch this agent. When one holds, an alert lands in the inbox and stays until someone acknowledges
              it.
            </p>
            {canAlert && (
              <div className={`${card} flex flex-col gap-2`}>
                <h3 className="text-aui-sm font-medium text-aui-fg">Alert me when</h3>
                <div className="flex flex-wrap gap-2">
                  {presets.map((p) => {
                    const rule = p.rule(name);
                    const have = myRules.some((r) => r.name === rule.name);
                    return (
                      <button key={rule.name} type="button" className={button} disabled={have} onClick={() => addRule(rule)}>
                        {have ? `On: ${p.label}` : p.label}
                      </button>
                    );
                  })}
                </div>
                <p className="text-aui-xs text-aui-muted">For other thresholds, add a rule under Alerts.</p>
              </div>
            )}
            {myRules.length === 0 ? (
              <p className={`${card} text-aui-sm text-aui-muted`}>
                No rules watch this agent. Rules for any agent still apply.
              </p>
            ) : (
              <ul className="oap-glass overflow-hidden rounded-aui-lg">
                {myRules.map((r) => (
                  <li key={r.name} className="flex items-center justify-between gap-3 border-aui-border border-b px-4 py-2.5 last:border-b-0">
                    <span className="min-w-0">
                      <span className="block text-aui-sm text-aui-fg">{r.name}</span>
                      <span className="block text-aui-xs text-aui-muted">
                        {r.kind.replace("_", " ")}
                        {r.threshold ? `, threshold ${r.threshold}` : ""}, {r.severity}
                      </span>
                    </span>
                    {canAlert && (
                      <button
                        type="button"
                        className={button}
                        onClick={() => api.deleteAlertRule(r.name).then(onRulesChanged).catch(() => {})}
                      >
                        Remove
                      </button>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>
        )}

        {tab === "revisions" && revision && (
          <ol className="flex flex-col gap-2">
            {history.map((h, i) => {
              const older = history[i + 1];
              return (
                <li key={h.rev} className={`${card} flex flex-col gap-2`}>
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                      <span className="text-aui-sm font-medium text-aui-fg">Revision {h.rev}</span>
                      {h.rev === revision.rev && <Chip>Current</Chip>}
                      <span className="text-aui-xs text-aui-muted">
                        {ago(h.created_at)} by {h.created_by}
                      </span>
                      <span className="text-aui-xs text-aui-muted">
                        {h.spec.provider} / {h.spec.model}
                      </span>
                    </div>
                    {canWrite && h.rev !== revision.rev && (
                      <button type="button" className={button} onClick={() => restore(h)}>
                        Restore as Revision {revision.rev + 1}
                      </button>
                    )}
                  </div>
                  {older ? (
                    <details>
                      <summary className="cursor-pointer text-aui-xs text-aui-muted">What changed from revision {older.rev}</summary>
                      <div className="pt-2">
                        <Diff before={older.spec.system_prompt} after={h.spec.system_prompt} />
                      </div>
                    </details>
                  ) : (
                    <p className="text-aui-xs text-aui-muted">The first revision.</p>
                  )}
                </li>
              );
            })}
          </ol>
        )}
      </div>
    </div>
  );
}
