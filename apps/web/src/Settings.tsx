import { type FormEvent, useRef, useState } from "react";
import { AccessExplain } from "./AccessExplain.tsx";
import { ApiError, api, type Me, type Preferences, type Revision, type Settings as Workspace } from "./api.ts";
import { BillingSettings } from "./Billing.tsx";
import { ClientSettings } from "./Clients.tsx";
import { agentOptions, button, card, field, Label, Menu, Problem, primary, Segmented, Switch } from "./ui.tsx";

export type Section = "configuration" | "routing" | "profile" | "appearance" | "workspace" | "limits" | "connections" | "access";

const accents: { id: string; label: string }[] = [
  { id: "purple", label: "Purple" },
  { id: "blue", label: "Blue" },
  { id: "yellow", label: "Yellow" },
  { id: "teal", label: "Teal" },
];

/** A person's picture, or their initial when they have none. */
export function Avatar({ name, prefs, size = 28 }: { name: string; prefs: Preferences | null; size?: number }) {
  if (prefs?.has_photo) {
    return (
      <img
        src={`/api/preferences/photo?v=${prefs.photo_version}`}
        alt=""
        width={size}
        height={size}
        className="shrink-0 rounded-full object-cover"
        style={{ width: size, height: size }}
      />
    );
  }
  return (
    <span
      aria-hidden
      className="inline-flex shrink-0 items-center justify-center rounded-full bg-aui-hover font-medium text-aui-fg"
      style={{ width: size, height: size, fontSize: Math.round(size * 0.42) }}
    >
      {(name.trim()[0] ?? "?").toUpperCase()}
    </span>
  );
}

export function Settings({
  me,
  prefs,
  workspace,
  agents,
  canAdmin,
  section,
  onSection,
  explain,
  onExplain,
  onPrefs,
  onWorkspace,
  overview,
}: {
  overview: React.ReactNode;
  me: Me;
  prefs: Preferences;
  workspace: Workspace | null;
  agents: Revision[];
  canAdmin: boolean;
  /** The section on screen. The sidebar links to some of them, so the caller holds it. */
  section: Section;
  onSection: (next: Section) => void;
  /** The permission the Access section explains. */
  explain: string;
  onExplain: (permission: string) => void;
  onPrefs: (next: Preferences) => void;
  onWorkspace: (next: Workspace) => void;
}) {
  const setSection = onSection;
  const [problem, setProblem] = useState<string | null>(null);
  const [name, setName] = useState(prefs.display_name);
  const [project, setProject] = useState({ name: "", description: "" });
  const file = useRef<HTMLInputElement>(null);
  const who = me.principal;
  const shown = prefs.display_name || who.name || who.email || who.subject;

  const fail = (fallback: string) => (e: unknown) => setProblem(e instanceof ApiError ? e.message : fallback);
  const savePrefs = (patch: Partial<Pick<Preferences, "display_name" | "theme" | "accent">>) => {
    setProblem(null);
    api
      .savePreferences({ display_name: prefs.display_name, theme: prefs.theme, accent: prefs.accent, ...patch })
      .then(onPrefs)
      .catch(fail("Your preferences could not be saved."));
  };
  const saveWorkspace = (patch: Partial<Workspace>) => {
    if (!workspace) return;
    setProblem(null);
    api
      .saveSettings({ ...workspace, ...patch })
      .then(onWorkspace)
      .catch(fail("The workspace settings could not be saved."));
  };

  const sections: { id: Section; label: string }[] = [
    { id: "configuration", label: "Configuration" },
    { id: "routing", label: "Agent routing" },
    { id: "profile", label: "Profile" },
    { id: "appearance", label: "Appearance" },
    { id: "workspace", label: "Workspace" },
    { id: "limits", label: "Limits" },
    { id: "connections", label: "Connections" },
    { id: "access", label: "Access" },
  ];

  return (
    <div className="collegium-fade flex min-h-0 flex-1 flex-col md:flex-row">
      <nav aria-label="Settings sections" className="flex shrink-0 gap-0.5 overflow-x-auto border-aui-border border-b p-2 md:w-52 md:flex-col md:border-r md:border-b-0 md:p-3">
        {sections.map((s) => (
          <button
            key={s.id}
            type="button"
            aria-current={section === s.id ? "page" : undefined}
            onClick={() => {
              setSection(s.id);
              setProblem(null);
            }}
            className="h-8 shrink-0 rounded-aui px-2.5 text-left text-aui-sm text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-[current=page]:bg-aui-hover aria-[current=page]:text-aui-fg"
          >
            {s.label}
          </button>
        ))}
      </nav>

      <div className="aui-scroll min-h-0 flex-1 overflow-y-auto">
        <div className="flex w-full max-w-2xl flex-col gap-4 px-5 pt-8 pb-6 md:px-10">
          <Problem message={problem} />

          {section === "configuration" && (
            <>
              <header>
                <h1 className="text-aui-xl font-semibold text-aui-fg">Configuration</h1>
                <p className="text-aui-sm text-aui-muted">Your workspace overview and tools.</p>
              </header>
              {overview}
            </>
          )}
          {section === "routing" && (
            <>
              <header>
                <h1 className="text-aui-xl font-semibold text-aui-fg">Agent routing</h1>
                <p className="text-aui-sm text-aui-muted">New requests use the automatic router. You can also choose an agent in the prompt.</p>
              </header>
              <section className={`${card} flex flex-col gap-3`}>
                <h2 className="text-aui-sm font-medium text-aui-fg">Automatic router</h2>
                <p className="text-aui-sm text-aui-muted">
                  Matches requests to agent names, descriptions and instructions. Names and descriptions carry more weight.
                  When agents tie or none match, the fallback answers.
                </p>
                {workspace && (
                  <Label text="Fallback agent" hint="Set an available agent so unmatched requests can run.">
                    <Menu
                      label="Fallback agent"
                      value={workspace.baseline}
                      disabled={!canAdmin}
                      options={agentOptions(agents.map((a) => a.spec.name), "None")}
                      onChange={(baseline) => saveWorkspace({ baseline })}
                    />
                  </Label>
                )}
              </section>
              <section className={`${card} flex flex-col gap-3`}>
                <h2 className="text-aui-sm font-medium text-aui-fg">Agent capabilities</h2>
                <p className="text-aui-xs text-aui-muted">Edit descriptions and instructions in Agents to change how requests are matched.</p>
                {agents.length === 0 && <p className="text-aui-sm text-aui-muted">No agents configured.</p>}
                {agents.map((a) => (
                  <div key={a.spec.name}>
                    <p className="text-aui-sm font-medium text-aui-fg">{a.spec.name}</p>
                    <p className="text-aui-xs text-aui-muted">{a.spec.description || "Matches using its name and instructions."}</p>
                  </div>
                ))}
              </section>
            </>
          )}

          {section === "profile" && (
            <>
              <header>
                <h1 className="text-aui-xl font-semibold text-aui-fg">Profile</h1>
                <p className="text-aui-sm text-aui-muted">How you appear to yourself in this console. Only you see these.</p>
              </header>
              <section className={`${card} flex flex-wrap items-center gap-4`}>
                <Avatar name={shown} prefs={prefs} size={72} />
                <div className="flex min-w-0 flex-1 flex-col gap-2">
                  <p className="text-aui-sm text-aui-fg">Profile picture</p>
                  <p className="text-aui-xs text-aui-muted">A PNG, JPEG or WebP image up to 512 KB.</p>
                  <div className="flex flex-wrap gap-2">
                    <input
                      ref={file}
                      type="file"
                      accept="image/png,image/jpeg,image/webp"
                      className="sr-only"
                      aria-label="Choose a profile picture"
                      onChange={(e) => {
                        const picked = e.target.files?.[0];
                        e.target.value = "";
                        if (!picked) return;
                        setProblem(null);
                        api.uploadPhoto(picked).then(onPrefs).catch(fail("The picture could not be uploaded."));
                      }}
                    />
                    <button type="button" className={button} onClick={() => file.current?.click()}>
                      {prefs.has_photo ? "Change Picture" : "Upload Picture"}
                    </button>
                    {prefs.has_photo && (
                      <button
                        type="button"
                        className={button}
                        onClick={() => api.removePhoto().then(onPrefs).catch(fail("The picture could not be removed."))}
                      >
                        Remove
                      </button>
                    )}
                  </div>
                </div>
              </section>
              <form
                className={`${card} flex flex-col gap-3`}
                onSubmit={(e: FormEvent) => {
                  e.preventDefault();
                  savePrefs({ display_name: name });
                }}
              >
                <Label text="Display name" hint={`Leave empty to use the name from sign-in${who.name ? `: ${who.name}` : ""}.`}>
                  <input className={field} value={name} maxLength={80} onChange={(e) => setName(e.target.value)} />
                </Label>
                <div className="flex justify-end">
                  <button type="submit" className={primary} disabled={name === prefs.display_name}>
                    Save Name
                  </button>
                </div>
              </form>
            </>
          )}

          {section === "appearance" && (
            <>
              <header>
                <h1 className="text-aui-xl font-semibold text-aui-fg">Appearance</h1>
                <p className="text-aui-sm text-aui-muted">These follow you to any browser you sign in from.</p>
              </header>
              <section className={`${card} flex flex-col gap-4`}>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <span>
                    <span className="block text-aui-sm text-aui-fg">Theme</span>
                    <span className="block text-aui-xs text-aui-muted">System follows your device.</span>
                  </span>
                  <Segmented
                    label="Theme"
                    value={prefs.theme}
                    onChange={(theme) => savePrefs({ theme })}
                    options={[
                      { id: "system", label: "System" },
                      { id: "light", label: "Light" },
                      { id: "dark", label: "Dark" },
                    ]}
                  />
                </div>
                <div className="flex flex-wrap items-center justify-between gap-3">
                  <span>
                    <span className="block text-aui-sm text-aui-fg">Accent</span>
                    <span className="block text-aui-xs text-aui-muted">Used for focus, selection and progress.</span>
                  </span>
                  <Segmented label="Accent" value={prefs.accent} onChange={(accent) => savePrefs({ accent })} options={accents} />
                </div>
              </section>
            </>
          )}

          {section === "workspace" && (
            <>
              <header>
                <h1 className="text-aui-xl font-semibold text-aui-fg">Workspace</h1>
                <p className="text-aui-sm text-aui-muted">
                  Shared by everyone in {who.tenant}. {canAdmin ? "" : "Only an admin can change these."}
                </p>
              </header>
              {!workspace ? null : (
                <>
                  <section className={`${card} flex flex-col gap-3`}>
                    <Label
                      text="Title agent"
                      hint="After a conversation's first turn, this agent is asked to name it. It needs a real model: nothing happens on the offline provider."
                    >
                      <Menu
                        label="Naming agent"
                        value={workspace.title_agent}
                        disabled={!canAdmin}
                        options={agentOptions(agents.map((a) => a.spec.name), "None")}
                        onChange={(next) => saveWorkspace({ title_agent: next })}
                      />
                    </Label>
                    <Label
                      text="Memory agent"
                      hint="After each turn, this agent is asked what the answering agent should remember. What it writes is shown on that agent's Memory tab, where anyone who can edit the agent can remove it."
                    >
                      <Menu
                        label="Memory agent"
                        value={workspace.memory_agent}
                        disabled={!canAdmin}
                        options={agentOptions(agents.map((a) => a.spec.name), "None")}
                        onChange={(next) => saveWorkspace({ memory_agent: next })}
                      />
                    </Label>
                    <Switch
                      label="Keep what the memory agent writes without asking"
                      hint="Off, every note it writes waits for a person to approve it. Notes that carry personal data are refused either way."
                      checked={workspace.memory_auto_approve}
                      disabled={!canAdmin || !workspace.memory_agent}
                      onChange={(on) => saveWorkspace({ memory_auto_approve: on })}
                    />
                  </section>

                  <section className={`${card} flex flex-col gap-3`}>
                    <Switch
                      label="Carry the conversation forward"
                      hint="Send the agent answering the next turn what was said before it. Off sends only the new message."
                      checked={workspace.history === "recent"}
                      disabled={!canAdmin}
                      onChange={(on) => saveWorkspace({ history: on ? "recent" : "none" })}
                    />
                    <Label
                      text="Keep at most (characters)"
                      hint="When a conversation is longer, the oldest turns are left out of what the agent is sent. They are dropped, not summarized, and stay on record."
                    >
                      <input
                        className={field}
                        type="number"
                        min={1000}
                        max={200000}
                        step={1000}
                        disabled={!canAdmin || workspace.history !== "recent"}
                        defaultValue={workspace.history_limit}
                        key={workspace.history_limit}
                        onBlur={(e) => {
                          const n = Number(e.target.value);
                          if (n !== workspace.history_limit) saveWorkspace({ history_limit: n });
                        }}
                      />
                    </Label>
                  </section>

                  <section className={`${card} flex flex-col gap-3`}>
                    <h3 className="text-aui-sm font-medium text-aui-fg">Areas</h3>
                    <p className="text-aui-xs text-aui-muted">An area groups tasks and their shared context.</p>
                    {workspace.projects.length === 0 ? (
                      <p className="text-aui-sm text-aui-muted">No areas yet.</p>
                    ) : (
                      <ul>
                        {workspace.projects.map((p) => (
                          <li key={p.name} className="flex items-center justify-between gap-3 border-aui-border border-t py-2 first:border-t-0">
                            <span className="min-w-0">
                              <span className="block text-aui-sm text-aui-fg">{p.name}</span>
                              {p.description && <span className="block truncate text-aui-xs text-aui-muted">{p.description}</span>}
                            </span>
                            {canAdmin && (
                              <button
                                type="button"
                                className={button}
                                onClick={() => saveWorkspace({ projects: workspace.projects.filter((x) => x.name !== p.name) })}
                              >
                                Remove
                              </button>
                            )}
                          </li>
                        ))}
                      </ul>
                    )}
                    {canAdmin && (
                      <form
                        className="flex flex-wrap items-end gap-2 border-aui-border border-t pt-3"
                        onSubmit={(e: FormEvent) => {
                          e.preventDefault();
                          saveWorkspace({ projects: [...workspace.projects, project] });
                          setProject({ name: "", description: "" });
                        }}
                      >
                        <span className="min-w-36 flex-1">
                          <Label text="Name">
                            <input className={field} value={project.name} required onChange={(e) => setProject({ ...project, name: e.target.value })} />
                          </Label>
                        </span>
                        <span className="min-w-36 flex-[2]">
                          <Label text="Description">
                            <input
                              className={field}
                              value={project.description}
                              onChange={(e) => setProject({ ...project, description: e.target.value })}
                            />
                          </Label>
                        </span>
                        <button type="submit" className={button}>
                          Add Project
                        </button>
                      </form>
                    )}
                  </section>
                </>
              )}
            </>
          )}

          {section === "limits" && (
            <BillingSettings
              projects={workspace?.projects.map((p) => p.name) ?? []}
              agents={agents.map((a) => a.spec.name)}
              canWrite={me.permissions.includes("billing:write")}
            />
          )}

          {section === "connections" && <ClientSettings canWrite={me.permissions.includes("clients:write")} />}

          {section === "access" && (
            <>
              <header>
                <h1 className="text-aui-xl font-semibold text-aui-fg">Access</h1>
                <p className="text-aui-sm text-aui-muted">What you are signed in as and what you may do. Your identity provider decides this.</p>
              </header>
              <section className={`${card} flex flex-col gap-2`}>
                <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1.5 text-aui-sm">
                  <dt className="text-aui-muted">Signed in as</dt>
                  <dd className="text-aui-fg">{who.email || who.name || who.subject}</dd>
                  <dt className="text-aui-muted">Workspace</dt>
                  <dd className="text-aui-fg">{who.tenant}</dd>
                  <dt className="text-aui-muted">Roles</dt>
                  <dd className="text-aui-fg">{who.roles?.length ? who.roles.join(", ") : "None"}</dd>
                  <dt className="text-aui-muted">Sign-in</dt>
                  <dd className="text-aui-fg">
                    {who.subject === "dev" ? "Dev mode. No sign-in, and everyone on this machine is an admin." : "Through your identity provider"}
                  </dd>
                </dl>
              </section>
              <section className={`${card} flex flex-col gap-2`}>
                <h3 className="text-aui-sm font-medium text-aui-fg">You may</h3>
                <ul className="flex flex-wrap gap-1.5">
                  {me.permissions.map((p) => (
                    <li key={p} className="rounded-full border border-aui-border px-2 py-0.5 font-aui-mono text-aui-2xs text-aui-muted">
                      {p}
                    </li>
                  ))}
                </ul>
              </section>
              <AccessExplain name={shown} permission={explain} onPermission={onExplain} />
            </>
          )}
        </div>
      </div>
    </div>
  );
}
