import { ActionId, type InboxItem, sortInbox, type UiAction } from "@agent-ui/core";
import { WebSurface } from "@agent-ui/render-web";
import { Inbox } from "@agent-ui/web";
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
import { Knowledge } from "./Knowledge.tsx";
import { RunList } from "./Runs.tsx";
import { Schedules } from "./Schedules.tsx";
import { Settings } from "./Settings.tsx";
import { Sidebar, type View } from "./Sidebar.tsx";
import { cause, primary } from "./ui.tsx";

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
        <h1 className="text-aui-lg font-medium text-aui-fg">Open Agent Platform</h1>
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

const SIDEBAR_KEY = "oap.sidebar";

/** A per-viewer convenience, so it lives in the browser and must never throw. */
function stored(key: string, fallback: boolean): boolean {
  try {
    const v = window.localStorage.getItem(key);
    return v === null ? fallback : v === "1";
  } catch {
    return fallback;
  }
}

/**
 * What is waiting on a person: failed runs and open alerts. Approvals and
 * budget requests join it when the platform can park a run on a person.
 */
function inboxOf(runs: Run[], alerts: Alert[], dismissed: Set<string>): InboxItem[] {
  // A failed run that already raised an alert appears once, as the alert.
  const alerted = new Set(alerts.map((a) => a.run_id));
  const failures: InboxItem[] = runs
    .filter((r) => r.status === "failed" && !alerted.has(r.id) && r.origin !== "eval")
    .map((r) => ({
      id: r.id,
      kind: "failure",
      state: dismissed.has(r.id) ? "dismissed" : "open",
      source: { label: `${r.agent}: ${r.text || "run"}`, turnId: r.id },
      at: r.updated_at,
      failure: { kind: "internal", message: cause(r.error) || "The run failed.", reference: r.id },
    }));
  const raised: InboxItem[] = alerts.map((a) => ({
    id: a.id,
    kind: "alert",
    state: a.acked_by ? "resolved" : "open",
    source: { label: `Rule: ${a.rule}`, turnId: a.run_id },
    at: a.at,
    alert: {
      id: a.id,
      service: a.agent ?? "Platform",
      status: a.acked_by ? "resolved" : a.severity === "critical" ? "down" : "degraded",
      title: a.title,
      body: cause(a.body) || undefined,
      startedAt: a.at,
      updatedAt: a.acked_at ?? a.at,
    },
  }));
  return sortInbox([...failures, ...raised]);
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
  const [open, setOpen] = useState(() => stored(SIDEBAR_KEY, window.innerWidth >= 768));
  const [project, setProject] = useState("");
  const [dismissed, setDismissed] = useState<Set<string>>(() => new Set());
  const [runs, setRuns] = useState<Run[]>([]);
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [agents, setAgents] = useState<Revision[]>([]);
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
    api.agents().then(setAgents).catch(() => {});
    api.settings().then(setWorkspace).catch(() => {});
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
    setTick((t) => t + 1);
  }, [can, loadAlerts, loadSchedules, loadSuites]);

  useEffect(() => {
    loadRuns();
    loadAgents();
    loadSources();
    api.preferences().then(setPrefs).catch(() => {});
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
  }, [can, loadRuns, loadAgents]);

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
  const newChat = () => {
    setThread(null);
    setView("chat");
  };
  const openAgent = (name: string | null) => {
    setAgentName(name);
    setView("agent");
  };

  const inbox = useMemo(() => inboxOf(runs, alerts, dismissed), [runs, alerts, dismissed]);
  const waiting = inbox.filter((i) => i.state === "open").length;
  const openAlerts = alerts.filter((a) => !a.acked_by).length;
  const stats = useMemo(() => new Map((day?.by_agent ?? []).map((g) => [g.name, g])), [day]);
  const editing = agents.find((a) => a.spec.name === agentName) ?? null;
  const shownName = prefs?.display_name || me.principal.name || "";

  const onInbox = (a: UiAction) => {
    const item = inbox.find((i) => i.id === a.payload.target || i.source.turnId === a.payload.turnId);
    const run = runs.find((r) => r.id === item?.source.turnId || r.id === a.payload.target);
    if (a.id === ActionId.dismiss) {
      // Dismissing an alert is acknowledging it, which the server records.
      if (item?.kind === "alert" && can("alerts:write")) api.ackAlert(item.id).then(loadAlerts).catch(() => {});
      else setDismissed((d) => new Set(d).add(a.payload.target));
    } else if (a.id === ActionId.open && run) {
      openRun(run.id);
    } else if (a.id === ActionId.retry && run && can("runs:write")) {
      api
        .createRun(run.agent, run.text, run.prev ? { after: run.prev } : {})
        .then((next) => {
          loadRuns();
          openThread(next.thread);
        })
        .catch(() => {});
    }
  };

  const setBaseline = (name: string) => {
    if (!workspace) return;
    api.saveSettings({ ...workspace, baseline: name }).then(setWorkspace).catch(() => {});
  };


  return (
    <div className="flex h-full text-aui-fg">
      <Sidebar
        open={open}
        onToggle={toggle}
        view={view}
        onView={setView}
        onNewChat={newChat}
        me={me}
        prefs={prefs}
        can={can}
        threads={threads}
        selected={view === "chat" ? thread : null}
        onSelect={openThread}
        projects={(workspace?.projects ?? []).map((p) => p.name)}
        project={project}
        onProject={setProject}
        inboxCount={waiting}
        alertCount={openAlerts}
        live={live}
        onSignOut={() => api.logout().then(onSignedOut).catch(onSignedOut)}
      />
      <main className="flex min-h-0 min-w-0 flex-1 flex-col">
          {view === "chat" && (
            <Chat
              key={thread ?? "new"}
              threadId={thread}
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
            <div className="aui-scroll min-h-0 flex-1 overflow-y-auto">
              <div className="mx-auto flex w-full max-w-3xl flex-col gap-3 px-4 py-6">
                <WebSurface onAction={onInbox}>
                  <Inbox items={inbox} onAction={onInbox} viewer={shownName} />
                </WebSurface>
                <p className="text-aui-xs text-aui-muted">
                  Open alerts and failed runs appear here. Approvals and budget requests will too, once a run can wait
                  on a person.
                </p>
              </div>
            </div>
          )}
          {view === "runs" && <RunList runs={runs} onSelect={openRun} />}
          {view === "agents" && (
            <Agents
              agents={agents}
              stats={stats}
              baseline={workspace?.baseline ?? ""}
              canWrite={can("agents:write")}
              onOpen={openAgent}
              onNew={() => openAgent(null)}
            />
          )}
          {view === "agent" && (
            <AgentDetail
              key={agentName ?? "new"}
              revision={editing}
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
              onOpenRun={openRun}
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
