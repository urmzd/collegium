import { CheckIcon, ChevronDownIcon, FolderIcon, FolderOpenIcon, FolderPlusIcon, MessageIcon } from "@vultus/render-web";
import { useEffect, useRef, useState } from "react";
import { ApiError, api, type Project } from "./api.ts";
import { button, field, Label, Popover, primary, Problem, SearchField } from "./ui.tsx";

const choice = "flex min-h-11 w-full items-center gap-3 rounded-aui px-2.5 py-2 text-left text-aui-sm text-aui-fg hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent aria-pressed:bg-aui-hover";

export function ProjectChoices({ projects, value, onChange, onCreated, close }: {
  projects: Project[] | null;
  value: string | null;
  onChange: (name: string) => void;
  onCreated: (project: Project) => void;
  close: () => void;
}) {
  const [query, setQuery] = useState("");
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [saving, setSaving] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => { input.current?.focus(); }, [creating]);

  const select = (name: string) => { onChange(name); close(); };
  const create = async () => {
    if (saving || !name.trim()) return;
    setSaving(true);
    setProblem(null);
    try {
      const project = { name: name.trim(), description: description.trim() };
      await api.createProject(project.name, project.description);
      onCreated(project);
      select(project.name);
    } catch (err) {
      setProblem(err instanceof ApiError && err.status === 409
        ? `A project named "${name.trim()}" already exists. Choose another name.`
        : err instanceof ApiError ? err.message : "The project could not be created. Try again.");
    } finally {
      setSaving(false);
    }
  };

  if (creating) return (
    // This panel lives inside the composer's form. Enter creates the project
    // without submitting the message, and there is no nested form.
    <div className="flex flex-col gap-3 p-2" onKeyDown={(e) => {
      if (e.key === "Enter" && e.target instanceof HTMLInputElement && !e.nativeEvent.isComposing) {
        e.preventDefault();
        if (input.current?.reportValidity()) void create();
      }
    }}>
      <h2 className="text-aui-sm font-medium">Create project</h2>
      <p className="text-aui-xs text-aui-muted">Keep related conversations and shared memory together.</p>
      <Label text="Project name">
        <input ref={input} className={field} value={name} onChange={(e) => setName(e.target.value)} required maxLength={63}
          pattern="[A-Za-z0-9][A-Za-z0-9 _\-]*" title="Start with a letter or digit. Use letters, digits, spaces, underscores or dashes." disabled={saving} />
      </Label>
      <Label text="Description (optional)">
        <input className={field} value={description} onChange={(e) => setDescription(e.target.value)} disabled={saving} />
      </Label>
      <Problem message={problem} />
      <div className="flex justify-end gap-2">
        <button type="button" className={button} disabled={saving} onClick={() => { setCreating(false); setProblem(null); }}>Back</button>
        <button type="button" className={primary} disabled={saving || !name.trim()} onClick={() => { if (input.current?.reportValidity()) void create(); }}>
          {saving ? "Creating…" : "Create project"}
        </button>
      </div>
    </div>
  );

  const q = query.trim().toLowerCase();
  const shown = projects?.filter((p) => `${p.name} ${p.description ?? ""}`.toLowerCase().includes(q));
  return (
    <div className="flex flex-col gap-1" onKeyDown={(e) => {
      if (e.key === "Enter" && e.target instanceof HTMLInputElement) e.preventDefault();
    }}>
      <div className="p-1">
        <SearchField label="Search projects" value={query} onChange={setQuery} inputRef={input} />
      </div>
      <button type="button" className={choice} aria-pressed={value === ""} onClick={() => select("")}>
        <MessageIcon size={18} className="text-aui-muted" /><span className="min-w-0 flex-1"><span className="block">Projectless</span><span className="block text-aui-xs text-aui-muted">Start without a project</span></span>
        {value === "" && <CheckIcon size={14} />}
      </button>
      <div className="my-1 h-px bg-aui-border" />
      <ul className="aui-scroll max-h-48 overflow-y-auto" aria-label="Projects">
        {shown?.map((p) => <li key={p.name}>
          <button type="button" className={choice} aria-pressed={p.name === value} onClick={() => select(p.name)}>
            {p.name === value ? <FolderOpenIcon size={18} /> : <FolderIcon size={18} className="text-aui-muted" />}
            <span className="min-w-0 flex-1"><span className="block truncate">{p.name}</span>{p.description && <span className="block truncate text-aui-xs text-aui-muted">{p.description}</span>}</span>
            {p.name === value && <CheckIcon size={14} />}
          </button>
        </li>)}
      </ul>
      {(!projects || shown?.length === 0) && <p role="status" className="px-2.5 py-3 text-aui-xs text-aui-muted">
        {!projects ? "Loading projects…" : projects.length === 0 ? "No projects yet. Create your first one below." : "No matching projects."}
      </p>}
      <div className="my-1 h-px bg-aui-border" />
      <button type="button" className={choice} onClick={() => { setName(query.trim()); setCreating(true); }}><FolderPlusIcon size={18} />Create project</button>
    </div>
  );
}

/** Choose where new work belongs without leaving or clearing the message. */
export function ProjectPicker({ projects, value, onChange, onCreated }: {
  projects: Project[] | null;
  /** null is untouched; an empty string explicitly chooses projectless work. */
  value: string | null;
  onChange: (name: string) => void;
  onCreated: (project: Project) => void;
}) {
  const label = value === null ? "Select project" : value || "Projectless";
  return <div className="collegium-project-picker min-w-0"><Popover label={value === null ? "Select project" : `Select project: ${label}`} up
    className="inline-flex h-8 max-w-32 items-center gap-1 rounded-aui px-1.5 text-aui-sm text-aui-muted hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent sm:max-w-40"
    button={<>{value === "" ? <MessageIcon size={16} /> : value ? <FolderOpenIcon size={16} /> : <FolderIcon size={16} />}<span className="truncate">{label}</span><ChevronDownIcon size={14} className="hidden shrink-0 sm:block" /></>}>
    {(close) => <ProjectChoices projects={projects} value={value} onChange={onChange} onCreated={onCreated} close={close} />}
  </Popover></div>;
}
