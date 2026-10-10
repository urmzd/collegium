import { type FormEvent, useState } from "react";
import { type Alert, type AlertRule, ApiError, api, type Revision } from "./api.ts";
import { agentOptions, ago, button, card, cause, Chip, Empty, field, Label, Menu, Page, Problem, primary } from "./ui.tsx";

const kindLabel: Record<AlertRule["kind"], string> = {
  run_failed: "A run fails",
  failure_rate: "Failure rate passes a threshold",
  slow_run: "A run takes too long",
  eval_below: "An eval's pass rate falls short",
};

function describe(r: AlertRule): string {
  const scope = r.agent ? r.agent : "any agent";
  switch (r.kind) {
    case "run_failed":
      return `When a run of ${scope} fails.`;
    case "slow_run":
      return `When a run of ${scope} takes longer than ${r.threshold}s.`;
    case "eval_below":
      return `When an eval of ${scope} passes fewer than ${r.threshold}% of its cases.`;
    case "failure_rate":
      return `When ${r.threshold}% or more of ${scope}'s runs fail over ${Math.round((r.window_seconds ?? 0) / 60)} minutes, with at least ${r.min_runs} runs.`;
  }
}

/** Severity as a word and a mark, so it never depends on color alone. */
function Severity({ level }: { level: AlertRule["severity"] }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-aui-xs text-aui-muted">
      <span aria-hidden className={`size-1.5 rounded-full ${level === "critical" ? "bg-aui-bad" : "bg-aui-warn"}`} />
      {level === "critical" ? "Critical" : "Warning"}
    </span>
  );
}

export function Alerts({
  alerts,
  rules,
  agents,
  canWrite,
  onChanged,
  onOpenRun,
}: {
  alerts: Alert[];
  rules: AlertRule[];
  agents: Revision[];
  canWrite: boolean;
  onChanged: () => void;
  onOpenRun: (id: string) => void;
}) {
  const [adding, setAdding] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const open = alerts.filter((a) => !a.acked_by);
  const seen = alerts.filter((a) => a.acked_by);

  const act = (p: Promise<unknown>, fallback: string) => {
    setProblem(null);
    p.then(onChanged).catch((e) => setProblem(e instanceof ApiError ? e.message : fallback));
  };

  return (
    <Page
      title="Alerts"
      lede="Rules watch runs as they end. When one holds it raises an alert, which stays open until someone acknowledges it."
      actions={
        canWrite && !adding ? (
          <button type="button" className={primary} onClick={() => setAdding(true)}>
            New Rule
          </button>
        ) : undefined
      }
    >
      <Problem message={problem} />
      {adding && (
        <RuleForm
          agents={agents}
          onClose={() => setAdding(false)}
          onSaved={() => {
            setAdding(false);
            onChanged();
          }}
        />
      )}

      <section className="flex flex-col gap-2">
        <h2 className="text-aui-sm font-medium text-aui-fg">Open ({open.length})</h2>
        {open.length === 0 ? (
          <div className={card}>
            <Empty title="Nothing needs attention">
              {rules.length === 0 ? "No rules exist, so nothing can be raised." : "No rule has fired since the last one was acknowledged."}
            </Empty>
          </div>
        ) : (
          <ul className="flex flex-col gap-2">
            {open.map((a) => (
              <li key={a.id} className={`${card} flex items-start justify-between gap-3`}>
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                    <h3 className="text-aui-base font-medium text-aui-fg">{a.title}</h3>
                    <Severity level={a.severity} />
                    <span className="text-aui-xs text-aui-muted">{ago(a.at)}</span>
                  </div>
                  {a.body && <p className="pt-1 text-aui-sm text-aui-muted">{cause(a.body)}</p>}
                  <p className="pt-1 text-aui-2xs text-aui-muted">Rule: {a.rule}</p>
                </div>
                <div className="flex shrink-0 gap-2">
                  {a.run_id && (
                    <button type="button" className={button} onClick={() => onOpenRun(a.run_id as string)}>
                      Open Run
                    </button>
                  )}
                  {canWrite && (
                    <button type="button" className={button} onClick={() => act(api.ackAlert(a.id), "Could not acknowledge.")}>
                      Acknowledge
                    </button>
                  )}
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="flex flex-col gap-2">
        <h2 className="text-aui-sm font-medium text-aui-fg">Rules ({rules.length})</h2>
        {rules.length === 0 ? (
          <p className="text-aui-sm text-aui-muted">No rules. Without one, nothing raises an alert.</p>
        ) : (
          <ul className="oap-glass overflow-hidden rounded-aui-lg">
            {rules.map((r) => (
              <li
                key={r.name}
                className="flex items-center justify-between gap-3 border-aui-border border-b px-4 py-2.5 last:border-b-0"
              >
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-x-3">
                    <span className="text-aui-sm font-medium text-aui-fg">{r.name}</span>
                    <Severity level={r.severity} />
                  </div>
                  <p className="text-aui-xs text-aui-muted">{describe(r)}</p>
                </div>
                {canWrite && (
                  <button
                    type="button"
                    className={button}
                    onClick={() => act(api.deleteAlertRule(r.name), "Could not delete the rule.")}
                  >
                    Delete
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      {seen.length > 0 && (
        <section className="flex flex-col gap-2">
          <h2 className="text-aui-sm font-medium text-aui-fg">Acknowledged ({seen.length})</h2>
          <ul className="oap-glass overflow-hidden rounded-aui-lg">
            {seen.slice(0, 50).map((a) => (
              <li
                key={a.id}
                className="flex items-center gap-3 border-aui-border border-b px-4 py-2 text-aui-sm last:border-b-0"
              >
                <span className="min-w-0 flex-1 truncate text-aui-muted">{a.title}</span>
                <Chip>{a.rule}</Chip>
                <span className="shrink-0 text-aui-xs text-aui-muted">
                  {a.acked_by}, {ago(a.acked_at ?? a.at)}
                </span>
              </li>
            ))}
          </ul>
        </section>
      )}
    </Page>
  );
}

function RuleForm({ agents, onClose, onSaved }: { agents: Revision[]; onClose: () => void; onSaved: () => void }) {
  const [rule, setRule] = useState<AlertRule>({ name: "", kind: "run_failed", severity: "critical", agent: "" });
  const [minutes, setMinutes] = useState(15);
  const [problem, setProblem] = useState<string | null>(null);
  const set = (patch: Partial<AlertRule>) => setRule((r) => ({ ...r, ...patch }));

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setProblem(null);
    const body: AlertRule = { name: rule.name, kind: rule.kind, severity: rule.severity };
    if (rule.agent) body.agent = rule.agent;
    if (rule.kind !== "run_failed") body.threshold = rule.threshold;
    if (rule.kind === "failure_rate") {
      body.window_seconds = minutes * 60;
      body.min_runs = rule.min_runs;
    }
    try {
      await api.createAlertRule(body);
      onSaved();
    } catch (err) {
      setProblem(err instanceof ApiError ? err.message : "The rule could not be saved.");
    }
  };

  return (
    <form onSubmit={save} className={`${card} flex flex-col gap-3`}>
      <h2 className="text-aui-base font-medium text-aui-fg">New rule</h2>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Label text="Name">
          <input className={field} value={rule.name} required onChange={(e) => set({ name: e.target.value })} />
        </Label>
        <Label text="Raise an alert when">
          <Menu
            label="Raise an alert when"
            value={rule.kind}
            options={Object.entries(kindLabel).map(([k, label]) => ({ value: k as AlertRule["kind"], label }))}
            onChange={(kind) =>
              set({
                kind,
                threshold: kind === "failure_rate" ? 50 : kind === "slow_run" ? 60 : kind === "eval_below" ? 80 : undefined,
                min_runs: kind === "failure_rate" ? 5 : undefined,
              })
            }
          />
        </Label>
        <Label text="For">
          <Menu
            label="For"
            value={rule.agent ?? ""}
            options={agentOptions(agents.map((a) => a.spec.name), "Any agent")}
            onChange={(agent) => set({ agent })}
          />
        </Label>
        <Label text="Severity">
          <Menu
            label="Severity"
            value={rule.severity}
            options={[
              { value: "critical", label: "Critical" },
              { value: "warning", label: "Warning" },
            ]}
            onChange={(severity) => set({ severity })}
          />
        </Label>
        {rule.kind === "slow_run" && (
          <Label text="Longer than (seconds)">
            <input
              className={field}
              type="number"
              min={1}
              value={rule.threshold ?? 60}
              onChange={(e) => set({ threshold: Number(e.target.value) })}
            />
          </Label>
        )}
        {rule.kind === "eval_below" && (
          <Label text="Pass rate under (%)">
            <input
              className={field}
              type="number"
              min={1}
              max={100}
              value={rule.threshold ?? 80}
              onChange={(e) => set({ threshold: Number(e.target.value) })}
            />
          </Label>
        )}
        {rule.kind === "failure_rate" && (
          <>
            <Label text="Failure rate at or above (%)">
              <input
                className={field}
                type="number"
                min={1}
                max={100}
                value={rule.threshold ?? 50}
                onChange={(e) => set({ threshold: Number(e.target.value) })}
              />
            </Label>
            <Label text="Over the last (minutes)">
              <input className={field} type="number" min={1} value={minutes} onChange={(e) => setMinutes(Number(e.target.value))} />
            </Label>
            <Label text="With at least (runs)" hint="So one failure out of one run does not count as 100%.">
              <input
                className={field}
                type="number"
                min={1}
                value={rule.min_runs ?? 5}
                onChange={(e) => set({ min_runs: Number(e.target.value) })}
              />
            </Label>
          </>
        )}
      </div>
      <Problem message={problem} />
      <div className="flex justify-end gap-2">
        <button type="button" className={button} onClick={onClose}>
          Cancel
        </button>
        <button type="submit" className={primary}>
          Create Rule
        </button>
      </div>
    </form>
  );
}
