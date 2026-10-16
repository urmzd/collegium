import { FolderIcon, FolderOpenIcon, FolderPlusIcon } from "@vultus/render-web";
import { type FormEvent, useEffect, useState } from "react";
import { ApiError, api, type Note, type ProjectView, type Revision, type ScheduleView, type ThreadSummary } from "./api.ts";
import { ScopeLimits } from "./Billing.tsx";
import { AgentMark, agentOptions, ago, button, field, Label, Menu, Page, Popover, Problem, primary } from "./ui.tsx";

const iconButton =
  "inline-flex size-6 items-center justify-center rounded-aui text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent";
const row =
  "flex h-8 w-full items-center gap-2 rounded-aui px-2 text-left text-aui-sm text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-[current=page]:bg-aui-hover aria-[current=page]:text-aui-fg";


/**
 * The sidebar's list of projects. A project is opened from here, and made from
 * here: the plus turns the heading into a one-line form.
 */
export function ProjectNav({
  open,
  projects,
  current,
  canCreate,
  onOpen,
  onCreated,
}: {
  /** Collapsed, a folder opens the same list in a flyout. */
  open: boolean;
  projects: ProjectView[];
  current: string | null;
  canCreate: boolean;
  onOpen: (name: string) => void;
  onCreated: (name: string) => void;
}) {
  const [adding, setAdding] = useState(false);
  const [saving, setSaving] = useState(false);
  const [name, setName] = useState("");
  const [problem, setProblem] = useState<string | null>(null);

  const create = (e: FormEvent, close?: () => void) => {
    e.preventDefault();
    if (saving) return;
    setSaving(true);
    setProblem(null);
    api
      .createProject(name.trim(), "")
      .then(() => {
        close?.();
        onCreated(name.trim());
        setName("");
        setAdding(false);
      })
      .catch((err) => setProblem(err instanceof ApiError ? err.message : "The project could not be created."))
      .finally(() => setSaving(false));
  };

  const contents = (close?: () => void) => (
    <>
      <div className={`flex items-center justify-between gap-1 pr-2 pb-1 pl-2 ${open ? "pt-4" : "pt-1"}`}>
        <p className="px-2 text-aui-xs text-aui-muted">Projects</p>
        {canCreate && (
          <button type="button" className={iconButton} aria-label="New project" aria-expanded={adding} onClick={() => setAdding(!adding)}>
            <FolderPlusIcon size={16} />
          </button>
        )}
      </div>
      {adding && (
        <form onSubmit={(e) => create(e, close)} className="flex flex-col gap-1 px-2 pb-1">
          <input
            // biome-ignore lint/a11y/noAutofocus: the form appears because the person asked to type a name
            autoFocus
            aria-label="Project name"
            className={`${field} h-8`}
            placeholder="Project name"
            value={name}
            required
            maxLength={63}
            disabled={saving}
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") setAdding(false);
            }}
          />
          <button type="submit" className={button} disabled={saving || !name.trim()}>{saving ? "Creating…" : "Create project"}</button>
          {problem && <p role="alert" className="px-1 text-aui-2xs text-aui-muted">{problem}</p>}
        </form>
      )}
      <ul className={`aui-scroll overflow-y-auto px-2 ${open ? "" : "max-h-[50vh]"}`}>
        {projects.length === 0 && !adding && <li className="px-2 py-1.5 text-aui-xs text-aui-muted">No projects yet.</li>}
        {projects.map((p) => (
          <li key={p.name}>
            <button type="button" className={row} aria-current={current === p.name ? "page" : undefined} onClick={() => { close?.(); onOpen(p.name); }}>
              {current === p.name ? <FolderOpenIcon size={18} /> : <FolderIcon size={18} />}
              <span className="min-w-0 flex-1 truncate">{p.name}</span>
              {/* A count only when something is happening or waiting. */}
              {p.active > 0 ? (
                <span className="shrink-0 text-aui-2xs tabular-nums">{p.active} running</span>
              ) : p.proposed > 0 ? (
                <span className="shrink-0 text-aui-2xs tabular-nums">{p.proposed} to review</span>
              ) : null}
            </button>
          </li>
        ))}
      </ul>
    </>
  );

  if (!open) return (
    <section aria-label="Projects" className="collegium-project-nav flex shrink-0 justify-center py-2">
      <Popover label="Projects" tooltip={current ? `Projects: ${current}` : "Projects"} side="right"
        className={`relative inline-flex size-9 items-center justify-center rounded-aui hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-expanded:bg-aui-hover aria-expanded:text-aui-fg ${current ? "bg-aui-hover text-aui-fg" : "text-aui-muted"}`}
        button={current ? <FolderOpenIcon size={18} /> : <FolderIcon size={18} />}>
        {(close) => contents(close)}
      </Popover>
    </section>
  );
  return <section aria-label="Projects" className="shrink-0">{contents()}</section>;
}

const section = "flex flex-col gap-2 border-aui-border border-t pt-5";
const heading = "text-aui-base font-medium text-aui-fg";

/** What every agent in the project is sent, and what is waiting to become that. */
function SharedMemory({ project, canWrite, onChanged }: { project: string; canWrite: boolean; onChanged: () => void }) {
  const [notes, setNotes] = useState<Note[]>([]);
  const [text, setText] = useState("");
  const [picked, setPicked] = useState<Set<string>>(() => new Set());
  const [problem, setProblem] = useState<string | null>(null);

  const load = () => {
    api
      .projectMemory(project)
      .then(setNotes)
      .catch(() => setNotes([]));
    onChanged();
  };
  // biome-ignore lint/correctness/useExhaustiveDependencies: reload when the project changes
  useEffect(() => {
    api
      .projectMemory(project)
      .then(setNotes)
      .catch(() => setNotes([]));
  }, [project]);

  const kept = notes.filter((n) => n.status === "approved");
  const proposed = notes.filter((n) => n.status === "pending");
  const fail = (fallback: string) => (e: unknown) => setProblem(e instanceof ApiError ? e.message : fallback);

  return (
    <section className={section}>
      <h2 className={heading}>Shared memory</h2>
      <p className="max-w-xl text-aui-sm text-aui-muted">
        Every agent answering in this project is sent these, in every conversation. Keep them few and lasting.
      </p>
      <Problem message={problem} />
      {proposed.length > 0 && (
        <div className="flex flex-col gap-2 rounded-aui-lg border border-aui-border p-3">
          <h3 className="text-aui-sm font-medium text-aui-fg">Proposed ({proposed.length})</h3>
          <p className="text-aui-xs text-aui-muted">Not in use yet. Tick what the project should keep.</p>
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
                  <span>{n.text}</span>
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
                    proposed.filter((n) => picked.size === 0 || picked.has(n.id)).map((n) => api.removeProjectMemory(project, n.id)),
                  )
                    .then(() => {
                      setPicked(new Set());
                      load();
                    })
                    .catch(fail("Those could not be discarded."))
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
                    .approveProjectMemory(project, [...picked])
                    .then(() => {
                      setPicked(new Set());
                      load();
                    })
                    .catch(fail("Those could not be kept."))
                }
              >
                Keep {picked.size > 0 ? `${picked.size} Selected` : "Selected"}
              </button>
            </div>
          )}
        </div>
      )}
      {kept.length === 0 ? (
        <p className="text-aui-sm text-aui-muted">Nothing shared yet.</p>
      ) : (
        <ul>
          {kept.map((n) => (
            <li key={n.id} className="collegium-enter flex items-start justify-between gap-3 border-aui-border border-t py-2 first:border-t-0">
              <span className="min-w-0">
                <span className="block text-aui-sm text-aui-fg">{n.text}</span>
                <span className="block text-aui-xs text-aui-muted">
                  {n.source === "person" ? `Written by ${n.by}` : "Proposed by the memory agent and approved"}, {ago(n.at)}
                </span>
              </span>
              {canWrite && (
                <button type="button" className={button} onClick={() => api.removeProjectMemory(project, n.id).then(load).catch(fail("That could not be removed."))}>
                  Forget
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {canWrite && (
        <form
          className="flex gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            setProblem(null);
            api
              .addProjectMemory(project, text)
              .then(() => {
                setText("");
                load();
              })
              .catch(fail("The note could not be saved."));
          }}
        >
          <input
            aria-label="Something everyone in this project should know"
            className={field}
            value={text}
            maxLength={300}
            required
            placeholder="A lasting fact, such as: The launch date is the first Tuesday of November."
            onChange={(e) => setText(e.target.value)}
          />
          <button type="submit" className={button}>
            Share
          </button>
        </form>
      )}
    </section>
  );
}

/** One project: its conversations, what it knows, and what runs in it on a timer. */
export function ProjectPage({
  project,
  threads,
  schedules,
  agents,
  canWrite,
  canSchedule,
  canLimit,
  tick,
  onNewChat,
  onOpenThread,
  onChanged,
  onSchedulesChanged,
  onDeleted,
}: {
  project: ProjectView;
  threads: ThreadSummary[];
  schedules: ScheduleView[];
  agents: Revision[];
  canWrite: boolean;
  canSchedule: boolean;
  /** May change what the project is allowed to use. */
  canLimit: boolean;
  /** Changes when runs do, so usage is read again. */
  tick?: number;
  onNewChat: () => void;
  onOpenThread: (id: string) => void;
  onChanged: () => void;
  onSchedulesChanged: () => void;
  onDeleted: () => void;
}) {
  const mine = threads.filter((t) => t.project === project.name);
  const timed = schedules.filter((s) => s.project === project.name);
  const [draft, setDraft] = useState({ name: "", expression: "0 9 * * 1-5", agent: "", text: "" });
  const [adding, setAdding] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const agent = draft.agent || agents[0]?.spec.name || "";
  const fail = (fallback: string) => (e: unknown) => setProblem(e instanceof ApiError ? e.message : fallback);

  return (
    <Page
      title={project.name}
      lede={project.description || "Tasks, shared memory and schedules that belong together."}
      actions={
        canWrite ? (
          <>
            <button
              type="button"
              className={button}
              onClick={() => api.deleteProject(project.name).then(onDeleted).catch(fail("The project could not be deleted."))}
            >
              Delete project
            </button>
            <button type="button" className={primary} onClick={onNewChat}>
              New conversation
            </button>
          </>
        ) : undefined
      }
    >
      <Problem message={problem} />

      <section className={section}>
        <h2 className={heading}>Tasks ({mine.length})</h2>
        {mine.length === 0 ? (
          <p className="text-aui-sm text-aui-muted">Start a task here to keep its conversation, execution history and shared context in this project.</p>
        ) : (
          <ul>
            {mine.map((t) => (
              <li key={t.id} className="collegium-enter border-aui-border border-t first:border-t-0">
                <button
                  type="button"
                  onClick={() => onOpenThread(t.id)}
                  className="flex w-full items-center gap-3 py-2 text-left text-aui-sm hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent"
                >
                  <AgentMark
                    name={t.agents[t.agents.length - 1] ?? "?"}
                    size={14}
                    state={t.active > 0 ? "thinking" : t.failed > 0 ? "error" : "idle"}
                  />
                  <span className="min-w-0 flex-1 truncate text-aui-fg">{t.title}</span>
                  <span className="shrink-0 text-aui-xs text-aui-muted">
                    {t.origin === "cron" || t.origin === "manual" ? "Scheduled, " : ""}
                    {t.active > 0 ? "In progress" : t.failed > 0 ? "Needs attention" : "Finished"} · {ago(t.updated_at)}
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <SharedMemory project={project.name} canWrite={canWrite} onChanged={onChanged} />

      <section className={section}>
        <h2 className={heading}>Limits</h2>
        <ScopeLimits key={project.name} scope={{ kind: "project", name: project.name }} canWrite={canLimit} tick={tick} />
      </section>

      <section className={section}>
        <div className="flex items-center justify-between gap-3">
          <h2 className={heading}>Scheduled ({timed.length})</h2>
          {canSchedule && agents.length > 0 && !adding && (
            <button type="button" className={button} onClick={() => setAdding(true)}>
              New Schedule
            </button>
          )}
        </div>
        <p className="max-w-xl text-aui-sm text-aui-muted">Each time one fires, its run is filed in this project and is sent what the project knows.</p>
        {adding && (
          <form
            className="flex flex-col gap-3 rounded-aui-lg border border-aui-border p-3"
            onSubmit={(e) => {
              e.preventDefault();
              setProblem(null);
              api
                .createSchedule({ ...draft, agent, project: project.name })
                .then(() => {
                  setDraft({ ...draft, name: "", text: "" });
                  setAdding(false);
                  onSchedulesChanged();
                  onChanged();
                })
                .catch(fail("The schedule could not be saved."));
            }}
          >
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
              <Label text="Name">
                <input className={field} value={draft.name} required onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
              </Label>
              <Label text="Send to agent">
                <Menu label="Send to agent" value={agent} options={agentOptions(agents.map((a) => a.spec.name))} onChange={(v) => setDraft({ ...draft, agent: v })} />
              </Label>
              <Label text="How often" hint="minute hour day month weekday, in UTC">
                <input
                  className={`${field} font-aui-mono`}
                  value={draft.expression}
                  required
                  onChange={(e) => setDraft({ ...draft, expression: e.target.value })}
                />
              </Label>
            </div>
            <Label text="What to ask">
              <textarea className={`${field} min-h-14`} value={draft.text} required onChange={(e) => setDraft({ ...draft, text: e.target.value })} />
            </Label>
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
        {timed.length === 0 && !adding ? (
          <p className="text-aui-sm text-aui-muted">Nothing runs on a timer here.</p>
        ) : (
          <ul>
            {timed.map((s) => (
              <li key={s.name} className="flex items-center justify-between gap-3 border-aui-border border-t py-2 first:border-t-0">
                <span className="min-w-0">
                  <span className="block text-aui-sm text-aui-fg">
                    {s.name}
                    <span className="pl-2 text-aui-xs text-aui-muted">
                      <code className="font-aui-mono">{s.expression}</code> to {s.agent}
                      {s.paused ? ", paused" : ""}
                    </span>
                  </span>
                  <span className="block truncate text-aui-xs text-aui-muted">{s.text}</span>
                </span>
                {canSchedule && (
                  <button
                    type="button"
                    className={button}
                    onClick={() =>
                      api
                        .runSchedule(s.name)
                        .then((run) => {
                          onSchedulesChanged();
                          onOpenThread(run.thread);
                        })
                        .catch(fail("The schedule could not be run."))
                    }
                  >
                    Run Now
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
    </Page>
  );
}
