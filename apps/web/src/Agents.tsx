import { type FormEvent, useState } from "react";
import { type AgentSpec, ApiError, api, type Group, type Revision, type Source } from "./api.ts";
import { button, card, Chip, Empty, field, Label, Page, Problem, primary, took } from "./ui.tsx";

const blank: AgentSpec = {
  name: "",
  description: "",
  provider: "offline",
  model: "offline",
  system_prompt: "",
  access: { spawn: [] },
};

const models: Record<string, string> = {
  offline: "offline",
  anthropic: "claude-sonnet-5-5",
  ollama: "llama3.2",
};

export function Agents({
  agents,
  sources,
  stats,
  canWrite,
  onChanged,
}: {
  agents: Revision[];
  sources: Source[];
  /** The last 24 hours, by agent name. */
  stats: Map<string, Group>;
  canWrite: boolean;
  onChanged: () => void;
}) {
  // null is closed, a revision is an edit, "new" is a create.
  const [editing, setEditing] = useState<Revision | "new" | null>(null);

  return (
    <Page
      title="Agents"
      lede="An agent is a spec. Revising it adds a revision, and runs already in flight keep theirs."
      actions={
        canWrite && !editing ? (
          <button type="button" className={primary} onClick={() => setEditing("new")}>
            New Agent
          </button>
        ) : undefined
      }
    >
        {editing && (
          <AgentForm
            key={editing === "new" ? "new" : `${editing.spec.name}:${editing.rev}`}
            revision={editing === "new" ? null : editing}
            others={agents.map((a) => a.spec.name)}
            sources={sources}
            onClose={() => setEditing(null)}
            onSaved={() => {
              setEditing(null);
              onChanged();
            }}
          />
        )}

        {agents.length === 0 && !editing ? (
          <Empty title="No agents defined">
            {canWrite ? "Create one to start running work." : "Ask an admin to define one."}
          </Empty>
        ) : (
          <ul className="flex flex-col gap-2">
            {agents.map((a) => (
              <li key={a.spec.name} className={card}>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <h2 className="text-aui-base font-medium text-aui-fg">{a.spec.name}</h2>
                    {a.spec.description && <p className="text-aui-sm text-aui-muted">{a.spec.description}</p>}
                  </div>
                  {canWrite && (
                    <button type="button" className={button} onClick={() => setEditing(a)}>
                      Revise
                    </button>
                  )}
                </div>
                <div className="flex flex-wrap items-center gap-1.5 pt-3">
                  <Chip>rev {a.rev}</Chip>
                  <Chip>
                    {a.spec.provider} / {a.spec.model}
                  </Chip>
                  {(a.spec.access?.spawn ?? []).map((s) => (
                    <Chip key={s}>may call {s}</Chip>
                  ))}
                  {(a.spec.tools ?? []).map((t) => (
                    <Chip key={t}>{t}</Chip>
                  ))}
                  {!(a.spec.access?.spawn ?? []).length && <Chip>may call no one</Chip>}
                  {(a.spec.sources ?? []).map((k) => (
                    <Chip key={k.name}>reads {k.name}</Chip>
                  ))}
                </div>
                <AgentStats g={stats.get(a.spec.name)} />
              </li>
            ))}
          </ul>
        )}
    </Page>
  );
}

/** How the agent has done over the last day, in one line. */
function AgentStats({ g }: { g: Group | undefined }) {
  if (!g || g.runs === 0) return <p className="pt-3 text-aui-xs text-aui-muted">No runs in the last 24 hours.</p>;
  const ended = g.completed + g.failed;
  return (
    <dl className="flex flex-wrap gap-x-6 gap-y-1 pt-3 text-aui-xs">
      {[
        ["Runs, 24h", String(g.runs)],
        ["Success", ended ? `${Math.round((100 * g.completed) / ended)}%` : "No data"],
        ["Failed", String(g.failed)],
        ["Median", g.p50_ms ? took(g.p50_ms) : "–"],
        ["p95", g.p95_ms ? took(g.p95_ms) : "–"],
      ].map(([label, value]) => (
        <div key={label} className="flex items-baseline gap-1.5">
          <dt className="text-aui-muted">{label}</dt>
          <dd className="tabular-nums text-aui-fg">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function AgentForm({
  revision,
  others,
  sources,
  onClose,
  onSaved,
}: {
  revision: Revision | null;
  others: string[];
  sources: Source[];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [spec, setSpec] = useState<AgentSpec>(revision?.spec ?? blank);
  const [problem, setProblem] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const set = (patch: Partial<AgentSpec>) => setSpec((s) => ({ ...s, ...patch }));
  const spawn = spec.access?.spawn ?? [];
  const callable = others.filter((n) => n !== spec.name);
  const reads = spec.sources ?? [];

  const save = async (e: FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setProblem(null);
    try {
      if (revision) await api.reviseAgent(spec, revision.rev);
      else await api.createAgent(spec);
      onSaved();
    } catch (err) {
      setProblem(err instanceof ApiError ? err.message : "The agent could not be saved.");
      setSaving(false);
    }
  };

  return (
    <form onSubmit={save} className={`${card} flex flex-col gap-3`}>
      <h2 className="text-aui-base font-medium text-aui-fg">
        {revision ? `Revise ${revision.spec.name}` : "New agent"}
      </h2>
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Label text="Name" hint={revision ? "A name cannot change once runs refer to it." : "Letters, digits and dashes."}>
          <input
            className={field}
            value={spec.name}
            disabled={!!revision}
            required
            onChange={(e) => set({ name: e.target.value })}
          />
        </Label>
        <Label text="Description">
          <input
            className={field}
            value={spec.description ?? ""}
            onChange={(e) => set({ description: e.target.value })}
          />
        </Label>
        <Label text="Provider" hint="Offline calls no model and needs no key.">
          <select
            className={field}
            value={spec.provider}
            onChange={(e) => set({ provider: e.target.value, model: models[e.target.value] ?? spec.model })}
          >
            <option value="offline">offline</option>
            <option value="anthropic">anthropic</option>
            <option value="ollama">ollama</option>
          </select>
        </Label>
        <Label text="Model">
          <input className={field} value={spec.model} required onChange={(e) => set({ model: e.target.value })} />
        </Label>
      </div>
      <Label text="System prompt">
        <textarea
          className={`${field} min-h-24 font-aui-mono`}
          value={spec.system_prompt}
          required
          onChange={(e) => set({ system_prompt: e.target.value })}
        />
      </Label>
      <fieldset className="flex flex-col gap-1.5">
        <legend className="pb-1 text-aui-xs font-medium text-aui-muted">May call</legend>
        {callable.length === 0 ? (
          <p className="text-aui-sm text-aui-muted">No other agents exist yet. An agent can call no one by default.</p>
        ) : (
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {callable.map((name) => (
              <label key={name} className="flex items-center gap-1.5 text-aui-sm text-aui-fg">
                <input
                  type="checkbox"
                  checked={spawn.includes(name)}
                  onChange={(e) =>
                    set({
                      access: {
                        ...spec.access,
                        spawn: e.target.checked ? [...spawn, name] : spawn.filter((s) => s !== name),
                      },
                    })
                  }
                />
                {name}
              </label>
            ))}
          </div>
        )}
      </fieldset>
      <fieldset className="flex flex-col gap-1.5">
        <legend className="pb-1 text-aui-xs font-medium text-aui-muted">Knowledge</legend>
        {sources.length === 0 ? (
          <p className="text-aui-sm text-aui-muted">No sources exist yet. Add one under Knowledge.</p>
        ) : (
          <div className="flex flex-wrap gap-x-4 gap-y-1">
            {sources.map((k) => (
              <label key={k.name} className="flex items-center gap-1.5 text-aui-sm text-aui-fg">
                <input
                  type="checkbox"
                  checked={reads.some((r) => r.name === k.name)}
                  onChange={(e) =>
                    set({
                      sources: e.target.checked
                        ? [...reads, { name: k.name, uri: `knowledge://${k.name}` }]
                        : reads.filter((r) => r.name !== k.name),
                    })
                  }
                />
                {k.name}
              </label>
            ))}
          </div>
        )}
      </fieldset>
      <Problem message={problem} />
      <div className="flex justify-end gap-2">
        <button type="button" className={button} onClick={onClose}>
          Cancel
        </button>
        <button type="submit" className={primary} disabled={saving}>
          {revision ? "Save Revision" : "Create Agent"}
        </button>
      </div>
    </form>
  );
}
