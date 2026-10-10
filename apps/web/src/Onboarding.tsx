import { type FormEvent, useId, useRef, useState } from "react";
import { ApiError, api, type Me, type Preferences, type Settings } from "./api.ts";
import { Avatar } from "./Settings.tsx";
import { button, field, Problem, primary, Segmented } from "./ui.tsx";

type Theme = Preferences["theme"];

const themes: { id: Theme; label: string }[] = [
  { id: "system", label: "System" },
  { id: "light", label: "Light" },
  { id: "dark", label: "Dark" },
];

/** The greeting the console will use, so a person sees what their name is for. */
export function welcome(name: string, hour = new Date().getHours()): string {
  const part = hour < 12 ? "Good morning" : hour < 18 ? "Good afternoon" : "Good evening";
  const first = name.trim().split(/\s+/)[0];
  return first ? `${part}, ${first}` : part;
}

/**
 * The first screen a person sees: what to call them, and how the console
 * should look. The first admin of a workspace also names it. It is one screen
 * with one main action, and it can always be skipped, so nobody is ever held
 * here. Finishing or skipping both call `onDone`.
 */
export function Onboarding({
  me,
  prefs,
  workspace,
  firstAdmin,
  onPrefs,
  onWorkspace,
  onDone,
}: {
  me: Me;
  prefs: Preferences;
  workspace: Settings | null;
  /** The first admin in a workspace nobody has set up yet. */
  firstAdmin: boolean;
  /** Called with every change, saved or not, so the theme is seen as it is picked. */
  onPrefs: (next: Preferences) => void;
  onWorkspace: (next: Settings) => void;
  onDone: () => void;
}) {
  const who = me.principal;
  const [name, setName] = useState(prefs.display_name || who.name || who.email?.split("@")[0] || "");
  const [space, setSpace] = useState(workspace?.workspace_name ?? "");
  const [project, setProject] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  const [missing, setMissing] = useState(false);
  const [busy, setBusy] = useState(false);
  const file = useRef<HTMLInputElement>(null);
  const id = useId();

  const said = (fallback: string) => (e: unknown) => setProblem(e instanceof ApiError ? e.message : fallback);
  const mark = (display_name: string) =>
    api.savePreferences({ display_name, theme: prefs.theme, accent: prefs.accent, onboarded: true });

  const finish = async (e: FormEvent) => {
    e.preventDefault();
    const shown = name.trim();
    if (!shown) {
      setMissing(true);
      document.getElementById(`${id}-name`)?.focus();
      return;
    }
    setBusy(true);
    setProblem(null);
    try {
      onPrefs(await mark(shown));
    } catch (err) {
      // The name is the one thing this screen is for. If it did not save, say so and stay.
      said("Your name could not be saved. Try again, or skip for now.")(err);
      setBusy(false);
      return;
    }
    // The rest is optional. A failure here is not worth holding a person back for.
    if (firstAdmin && workspace && space.trim()) {
      await api
        .saveSettings({ ...workspace, workspace_name: space.trim() })
        .then(onWorkspace)
        .catch(() => {});
    }
    if (firstAdmin && project.trim()) await api.createProject(project.trim(), "").catch(() => {});
    onDone();
  };

  // Skipping records that the screen was seen, keeps whatever name is already known, and leaves.
  const skip = () => {
    mark(prefs.display_name)
      .then(onPrefs)
      .catch(() => {});
    onDone();
  };

  return (
    <main data-testid="onboarding" data-first-admin={firstAdmin} className="aui-scroll flex h-full flex-col overflow-y-auto text-aui-fg">
      <form onSubmit={finish} noValidate className="m-auto flex w-full max-w-xl flex-col gap-8 px-5 py-12">
        <header>
          <p className="text-aui-sm text-aui-muted">Open Agent Platform</p>
          {/* The greeting the console opens with, changing as the name is typed. */}
          <h1 aria-live="polite" className="oap-display pt-1 text-[2.25rem] leading-[1.1] font-semibold text-aui-fg">
            {welcome(name)}
          </h1>
          <p className="max-w-[52ch] pt-2 text-aui-sm text-aui-muted">
            {firstAdmin
              ? "A few basics before you start. Everything here can be changed later in Settings."
              : "Two things before you start. Both can be changed later in Settings."}
          </p>
        </header>

        <Problem message={problem} />

        <div className="flex flex-col gap-5">
          <div className="flex flex-col gap-1">
            <label htmlFor={`${id}-name`} className="text-aui-sm font-medium text-aui-fg">
              What should we call you?
            </label>
            <div className="flex items-center gap-3">
              <Avatar name={name || "?"} prefs={prefs} size={32} />
              <input
                id={`${id}-name`}
                className={field}
                value={name}
                required
                autoFocus
                autoComplete="name"
                aria-invalid={missing}
                aria-describedby={`${id}-name-hint`}
                onChange={(e) => {
                  setName(e.target.value);
                  setMissing(false);
                }}
              />
            </div>
            <p id={`${id}-name-hint`} role={missing ? "alert" : undefined} className="text-aui-xs text-aui-muted">
              {missing ? "Enter a name to continue, or skip for now." : "Only you see this. Agents and the console greet you by it."}
            </p>
          </div>

          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="text-aui-sm font-medium text-aui-fg">Picture</p>
              <p className="text-aui-xs text-aui-muted">Optional. A PNG, JPEG or WebP image up to 512 KB.</p>
            </div>
            <input
              ref={file}
              type="file"
              accept="image/png,image/jpeg,image/webp"
              className="sr-only"
              tabIndex={-1}
              aria-label="Choose a profile picture"
              onChange={(e) => {
                const picked = e.target.files?.[0];
                e.target.value = "";
                if (!picked) return;
                setProblem(null);
                api.uploadPhoto(picked).then(onPrefs).catch(said("The picture could not be uploaded."));
              }}
            />
            <button type="button" className={button} onClick={() => file.current?.click()}>
              {prefs.has_photo ? "Change Picture" : "Upload Picture"}
            </button>
          </div>

          <div className="flex flex-wrap items-center justify-between gap-3">
            <div className="min-w-0">
              <p className="text-aui-sm font-medium text-aui-fg">Theme</p>
              <p className="text-aui-xs text-aui-muted">System follows your device.</p>
            </div>
            <Segmented label="Theme" value={prefs.theme} options={themes} onChange={(theme) => onPrefs({ ...prefs, theme })} />
          </div>
        </div>

        {firstAdmin && (
          <fieldset data-testid="onboarding-workspace" className="flex flex-col gap-5 border-[var(--oap-rule)] border-t pt-6">
            <legend className="sr-only">Workspace</legend>
            <p className="text-aui-sm text-aui-muted">
              You are the first admin of this workspace. Admins create and change agents, connect knowledge, set limits and
              decide who has access.
            </p>
            <div className="flex flex-col gap-1">
              <label htmlFor={`${id}-space`} className="text-aui-sm font-medium text-aui-fg">
                Workspace name
              </label>
              <input
                id={`${id}-space`}
                className={field}
                value={space}
                placeholder="Such as your team or company"
                autoComplete="organization"
                maxLength={80}
                onChange={(e) => setSpace(e.target.value)}
              />
            </div>
            <div className="flex flex-col gap-1">
              <label htmlFor={`${id}-project`} className="text-aui-sm font-medium text-aui-fg">
                First project
              </label>
              <input
                id={`${id}-project`}
                className={field}
                value={project}
                placeholder="Optional"
                aria-describedby={`${id}-project-hint`}
                onChange={(e) => setProject(e.target.value)}
              />
              <p id={`${id}-project-hint`} className="text-aui-xs text-aui-muted">
                A project keeps related conversations, memory and schedules together. Leave it empty to add one later.
              </p>
            </div>
            <p className="text-aui-xs text-aui-muted">Spending and run limits are set in Settings, once you are in.</p>
          </fieldset>
        )}

        <div className="flex flex-wrap items-center gap-2">
          <button type="submit" className={primary} disabled={busy}>
            Get Started
          </button>
          <button type="button" className={button} onClick={skip} disabled={busy}>
            Skip for Now
          </button>
        </div>
      </form>
    </main>
  );
}
