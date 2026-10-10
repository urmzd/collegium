import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { AgentDetail } from "./AgentDetail.tsx";
import { Agents } from "./Agents.tsx";
import { Alerts } from "./Alerts.tsx";
import { Analytics } from "./Analytics.tsx";
import type { Run } from "./adapter.ts";
import {
  type Alert,
  type AlertRule,
  ApiError,
  api,
  type Me,
  type Preferences,
  type ProjectView,
  type Revision,
  type ScheduleView,
  type Settings as Workspace,
  type Source,
  type SuiteView,
  type Summary,
  subscribe,
  type ThreadSummary,
} from "./api.ts";
import { Chat } from "./Chat.tsx";
import { InboxPage } from "./InboxPage.tsx";
import { Knowledge } from "./Knowledge.tsx";
import { Onboarding } from "./Onboarding.tsx";
import { ProjectNav, ProjectPage } from "./Projects.tsx";
import { RunList } from "./Runs.tsx";
import { Schedules } from "./Schedules.tsx";
import { Settings } from "./Settings.tsx";
import { Storage } from "./Storage.tsx";
import { Sidebar, type View } from "./Sidebar.tsx";
import { primary } from "./ui.tsx";

type Session = { state: "loading" } | { state: "signed-out" } | { state: "error"; message: string } | { state: "ready"; me: Me };

export function App() {
  const [session, setSession] = useState<Session>({ state: "loading" });

  useEffect(() => {
    api
      .me()
      .then((me) => setSession({ state: "ready", me }))
      .catch((e) =>
        setSession(
          e instanceof ApiError && e.status === 401
            ? { state: "signed-out" }
            : { state: "error", message: "The platform is not reachable. Is the server running?" },
        ),
      );
  }, []);

  if (session.state === "loading") return <Centered>Loading</Centered>;
  if (session.state === "error") return <Centered>{session.message}</Centered>;
  if (session.state === "signed-out") {
    const back = encodeURIComponent(window.location.pathname + window.location.search);
    return (
      <Centered>
        <h1 className="text-aui-lg font-medium text-aui-fg">Collegium</h1>
        <p className="text-aui-sm text-aui-muted">Sign in with your organization's account to continue.</p>
        <a className={primary} href={`/auth/login?return_to=${back}`}>
          Sign In
        </a>
      </Centered>
    );
  }
  return <Console me={session.me} onSignedOut={() => setSession({ state: "signed-out" })} />;
}

function Centered({ children }: { children: React.ReactNode }) {
  return (
    <main className="flex h-full flex-col items-center justify-center gap-3 px-6 text-center text-aui-sm text-aui-muted">
      {children}
    </main>
  );
}

const SIDEBAR_KEY = "collegium.sidebar";

/** A per-viewer convenience, so it lives in the browser and must never throw. */
function stored(key: string, fallback: boolean): boolean {
  try {
    const v = window.localStorage.getItem(key);
    return v === null ? fallback : v === "1";
  } catch {
    return fallback;
  }
}

function greeting(name: string): string {
  const h = new Date().getHours();
  const part = h < 12 ? "Good morning" : h < 18 ? "Good afternoon" : "Good evening";
  return name ? `${part}, ${name.split(" ")[0]}` : part;
}

/** Apply a person's theme and accent to the page. System follows the device. */
function useAppearance(prefs: Preferences | null) {
  useEffect(() => {
    const root = document.documentElement;
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const apply = () => {
      const theme = prefs?.theme ?? "system";
      root.dataset.theme = theme === "system" ? (media.matches ? "dark" : "light") : theme;
      root.dataset.accent = prefs?.accent ?? "purple";
    };
    apply();
    media.addEventListener("change", apply);
    return () => media.removeEventListener("change", apply);
  }, [prefs?.theme, prefs?.accent]);
}

function Console({ me, onSignedOut }: { me: Me; onSignedOut: () => void }) {
  const can = useCallback((p: string) => me.permissions.includes(p), [me]);
  const [view, setView] = useState<View>(can("runs:read") ? "chat" : "agents");
  const [thread, setThread] = useState<string | null>(null);
  const [agentName, setAgentName] = useState<string | null>(null);
  const [agentTab, setAgentTab] = useState<"memory" | undefined>(undefined);
  // The inbox is the server's. This holds only its totals for the badge, a
  // counter that tells the page to look again, and a message to open first.
  const [inboxCounts, setInboxCounts] = useState({ open: 0, unread: 0 });
  const [inboxTick, setInboxTick] = useState(0);
  const [inboxMessage, setInboxMessage] = useState<string | null>(null);
  const [projects, setProjects] = useState<ProjectView[]>([]);
  const [projectName, setProjectName] = useState<string | null>(null);
  // The project a new chat was started from, so it is filed there.
  const [startIn, setStartIn] = useState<string | undefined>(undefined);
  const [open, setOpen] = useState(() => stored(SIDEBAR_KEY, window.innerWidth >= 768));
  const [runs, setRuns] = useState<Run[]>([]);
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [agents, setAgents] = useState<Revision[]>([]);
  // The agents the platform provides. They run like any other and cannot be edited.
  const [builtin, setBuiltin] = useState<string[]>([]);
  const [schedules, setSchedules] = useState<ScheduleView[]>([]);
  const [alerts, setAlerts] = useState<Alert[]>([]);
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [sources, setSources] = useState<Source[]>([]);
  const [suites, setSuites] = useState<SuiteView[]>([]);
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [prefs, setPrefs] = useState<Preferences | null>(null);
  const [day, setDay] = useState<Summary | null>(null);
  const [live, setLive] = useState(false);
  // Bumped whenever a run starts or ends, so pages that aggregate refetch.
  const [tick, setTick] = useState(0);

  useAppearance(prefs);
  // Set when onboarding ends, whether or not saving that fact worked, so
  // nobody is asked twice in one sitting or left unable to get in.
  const [welcomed, setWelcomed] = useState(false);

  const toggle = () =>
    setOpen((was) => {
      try {
        window.localStorage.setItem(SIDEBAR_KEY, was ? "0" : "1");
      } catch {
        // The choice just does not persist.
      }
      return !was;
    });

  const loadAgents = useCallback(() => {
    if (!can("agents:read")) return;
    api
      .agentList()
      .then((r) => {
        setAgents(r.agents);
        setBuiltin(r.builtin ?? []);
      })
      .catch(() => {});
    api.settings().then(setWorkspace).catch(() => {});
  }, [can]);
  const loadInbox = useCallback(() => {
    if (!can("runs:read")) return;
    api.inboxCount().then(setInboxCounts).catch(() => {});
    setInboxTick((t) => t + 1);
    // Settling a message settles its alert, which the Alerts badge counts.
    if (can("alerts:read")) api.alerts().then(setAlerts).catch(() => {});
  }, [can]);
  const loadProjects = useCallback(() => {
    if (can("runs:read")) api.projects().then(setProjects).catch(() => {});
  }, [can]);
  const loadSources = useCallback(() => {
    if (can("knowledge:read")) api.sources().then(setSources).catch(() => {});
  }, [can]);
  const loadSuites = useCallback(() => {
    if (can("evals:read")) api.suites().then(setSuites).catch(() => {});
  }, [can]);
  const loadAlerts = useCallback(() => {
    if (!can("alerts:read")) return;
    api.alerts().then(setAlerts).catch(() => {});
    api.alertRules().then(setRules).catch(() => {});
  }, [can]);
  const loadSchedules = useCallback(() => {
    if (can("schedules:read")) api.schedules().then(setSchedules).catch(() => {});
  }, [can]);
  // Everything a run starting or ending can change.
  const loadRuns = useCallback(() => {
    if (!can("runs:read")) return;
    api.runs().then(setRuns).catch(() => {});
    api.threads().then(setThreads).catch(() => {});
    api.analytics("24h").then(setDay).catch(() => {});
    loadAlerts();
    loadSchedules();
    loadSuites();
    loadInbox();
    loadProjects();
    setTick((t) => t + 1);
  }, [can, loadAlerts, loadSchedules, loadSuites, loadInbox, loadProjects]);

  useEffect(() => {
    loadRuns();
    loadAgents();
    loadSources();
    api
      .preferences()
      .then(setPrefs)
      // Preferences that cannot be read cannot be saved either, so use the
      // defaults and do not ask first-run questions.
      .catch(() =>
        setPrefs({
          display_name: "",
          theme: "system",
          accent: "purple",
          has_photo: false,
          photo_version: 0,
          onboarded_at: new Date().toISOString(),
        }),
      );
  }, [loadRuns, loadAgents, loadSources]);

  // One stream for the whole tenant keeps every list current. Lifecycle and
  // control events are rare next to text deltas, so the reload is coalesced
  // rather than run per event.
  const pending = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (!can("runs:read")) return;
    const close = subscribe("agent", {
      onOpen: loadRuns,
      onState: setLive,
      onInbox: loadInbox,
      onEvent: (e) => {
        if (!e.kind.startsWith("lifecycle.") && !e.kind.startsWith("control.")) return;
        if (e.kind.startsWith("control.")) loadAgents();
        window.clearTimeout(pending.current);
        pending.current = window.setTimeout(loadRuns, 150);
      },
    });
    return () => {
      window.clearTimeout(pending.current);
      close();
    };
  }, [can, loadRuns, loadAgents, loadInbox]);

  // An eval in flight reports progress on its own record, not on the bus.
  const evalRunning = suites.some((s) => s.runs[0]?.status === "running");
  useEffect(() => {
    if (!evalRunning) return;
    const timer = window.setInterval(loadSuites, 700);
    return () => window.clearInterval(timer);
  }, [evalRunning, loadSuites]);

  const openThread = (id: string) => {
    setThread(id);
    setView("chat");
  };
  // Opening a run opens the conversation it belongs to.
  const openRun = (id: string) => {
    const run = runs.find((r) => r.id === id);
    openThread(run?.thread ?? id);
  };
  const newChat = (inProject?: string) => {
    setThread(null);
    setStartIn(inProject);
    setView("chat");
  };
  const openProject = (name: string) => {
    setProjectName(name);
    setView("project");
  };
  const openAgent = (name: string | null, tab?: "memory") => {
    setAgentName(name);
    setAgentTab(tab);
    setView("agent");
  };
  const openAlerts = alerts.filter((a) => !a.acked_by).length;
  const stats = useMemo(() => new Map((day?.by_agent ?? []).map((g) => [g.name, g])), [day]);
  const editing = agents.find((a) => a.spec.name === agentName) ?? null;
  const shownName = prefs?.display_name || me.principal.name || "";

  const setBaseline = (name: string) => {
    if (!workspace) return;
    api.saveSettings({ ...workspace, baseline: name }).then(setWorkspace).catch(() => {});
  };


  // Wait for preferences, so the console never shows for a moment before the
  // first-run questions do.
  if (!prefs) return <Centered>Loading</Centered>;
  if (!prefs.onboarded_at && !welcomed) {
    return (
      <Onboarding
        me={me}
        prefs={prefs}
        workspace={workspace}
        firstAdmin={can("agents:write") && workspace !== null && !workspace.setup_at}
        onPrefs={setPrefs}
        onWorkspace={setWorkspace}
        onDone={() => {
          setWelcomed(true);
          loadAgents();
          loadProjects();
        }}
      />
    );
  }

  return (
    <div className="flex h-full text-aui-fg">
      <Sidebar
        open={open}
        onToggle={toggle}
        view={view}
        onView={setView}
        onNewChat={() => newChat()}
        projectsSlot={
          <ProjectNav
            open={open}
            projects={projects}
            current={view === "project" ? projectName : null}
            canCreate={can("runs:write")}
            onOpen={openProject}
            onCreated={(name) => {
              loadProjects();
              loadAgents();
              openProject(name);
            }}
          />
        }
        me={me}
        prefs={prefs}
        can={can}
        threads={threads}
        selected={view === "chat" ? thread : null}
        onSelect={openThread}
        inboxCount={inboxCounts.open}
        alertCount={openAlerts}
        live={live}
        onSignOut={() => api.logout().then(onSignedOut).catch(onSignedOut)}
      />
      <main className="flex min-h-0 min-w-0 flex-1 flex-col">
          {view === "chat" && (
            <Chat
              key={thread ?? `new:${startIn ?? ""}`}
              threadId={thread}
              startIn={startIn}
              summary={threads.find((t) => t.id === thread)}
              tick={tick}
              agents={agents}
              settings={workspace}
              canRun={can("runs:write")}
              greeting={greeting(shownName)}
              onThread={setThread}
              onChanged={loadRuns}
            />
          )}
          {view === "inbox" && (
            <InboxPage
              signal={inboxTick}
              viewer={shownName}
              openId={inboxMessage}
              onCounts={setInboxCounts}
              onOpen={(source) =>
                source.kind === "run"
                  ? openRun(source.id)
                  : source.kind === "thread"
                    ? openThread(source.id)
                    : source.kind === "agent"
                      ? openAgent(source.id)
                      : source.kind === "project"
                        ? openProject(source.id)
                        : setView("alerts")
              }
            />
          )}
          {view === "project" &&
            (() => {
              const current = projects.find((p) => p.name === projectName);
              return current ? (
                <ProjectPage
                  key={current.name}
                  project={current}
                  threads={threads}
                  schedules={schedules}
                  agents={agents}
                  canWrite={can("runs:write")}
                  canSchedule={can("schedules:write")}
                  canLimit={can("billing:write")}
                  tick={tick}
                  onNewChat={() => newChat(current.name)}
                  onOpenThread={openThread}
                  onChanged={() => {
                    loadProjects();
                    loadInbox();
                  }}
                  onSchedulesChanged={loadSchedules}
                  onDeleted={() => {
                    loadProjects();
                    loadAgents();
                    setView("chat");
                  }}
                />
              ) : null;
            })()}
          {view === "runs" && <RunList runs={runs} onSelect={openRun} />}
          {view === "storage" && (
            <Storage
              signal={tick}
              agents={agents.map((a) => a.spec.name)}
              projects={projects.map((p) => p.name)}
              canDelete={can("artifacts:write")}
              onOpenRun={openRun}
              onOpenThread={openThread}
            />
          )}
          {view === "agents" && (
            <Agents
              builtin={builtin}
              agents={agents}
              runs={runs}
              stats={stats}
              baseline={workspace?.baseline ?? ""}
              canWrite={can("agents:write")}
              onOpen={openAgent}
              onNew={() => openAgent(null)}
            />
          )}
          {view === "agent" && (
            <AgentDetail
              key={`${agentName ?? "new"}:${agentTab ?? ""}`}
              revision={editing}
              startOn={agentTab}
              agents={agents}
              sources={sources}
              suites={suites}
              rules={rules}
              alerts={alerts}
              stats={agentName ? stats.get(agentName) : undefined}
              baseline={workspace?.baseline ?? ""}
              canWrite={can("agents:write")}
              canEval={can("evals:write")}
              canAlert={can("alerts:write")}
              onBack={() => setView("agents")}
              onChanged={(name) => {
                loadAgents();
                setAgentName(name);
              }}
              onRulesChanged={loadAlerts}
              onSuitesChanged={loadSuites}
              onOpenRun={openRun}
              onBaseline={setBaseline}
            />
          )}
          {view === "knowledge" && (
            <Knowledge
              sources={sources}
              agents={agents}
              suites={suites}
              canWrite={can("knowledge:write")}
              canEval={can("evals:write")}
              onChanged={loadSources}
              onSuitesChanged={loadSuites}
            />
          )}
          {view === "schedules" && (
            <Schedules
              schedules={schedules}
              agents={agents}
              canWrite={can("schedules:write")}
              onChanged={loadSchedules}
              onOpenRun={openRun}
            />
          )}
          {view === "analytics" && <Analytics agents={agents} openAlerts={openAlerts} tick={tick} onOpenRun={openRun} />}
          {view === "alerts" && (
            <Alerts
              alerts={alerts}
              rules={rules}
              agents={agents}
              canWrite={can("alerts:write")}
              onChanged={loadAlerts}
              onOpenInbox={(alert) => {
                setInboxMessage(null);
                setView("inbox");
                // The message about an alert is found by the alert, in whatever state it is.
                if (alert) api.inboxFor(alert.id).then(setInboxMessage).catch(() => {});
              }}
            />
          )}
          {view === "settings" && prefs && (
            <Settings
              me={me}
              prefs={prefs}
              workspace={workspace}
              agents={agents}
              canAdmin={can("agents:write")}
              onPrefs={setPrefs}
              onWorkspace={setWorkspace}
            />
          )}
        </main>
    </div>
  );
}
