import { ActionId, type InboxItem, sortInbox, type UiAction } from "@agent-ui/core";
import { WebSurface } from "@agent-ui/render-web";
import { Inbox } from "@agent-ui/web";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
  type Revision,
  type ScheduleView,
  type Source,
  type Summary,
  subscribe,
} from "./api.ts";
import { Knowledge } from "./Knowledge.tsx";
import { RunList, RunView } from "./Runs.tsx";
import { Schedules } from "./Schedules.tsx";
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
    .filter((r) => r.status === "failed" && !alerted.has(r.id))
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

function Console({ me, onSignedOut }: { me: Me; onSignedOut: () => void }) {
  const can = useCallback((p: string) => me.permissions.includes(p), [me]);
  const [view, setView] = useState<View>(can("runs:write") ? "new" : can("runs:read") ? "runs" : "agents");
  const [selected, setSelected] = useState<string | null>(null);
  const [open, setOpen] = useState(() => stored(SIDEBAR_KEY, window.innerWidth >= 768));
  const [dismissed, setDismissed] = useState<Set<string>>(() => new Set());
  const [runs, setRuns] = useState<Run[]>([]);
  const [agents, setAgents] = useState<Revision[]>([]);
  const [schedules, setSchedules] = useState<ScheduleView[]>([]);
  const [alerts, setAlerts] = useState<Alert[]>([]);
  const [rules, setRules] = useState<AlertRule[]>([]);
  const [sources, setSources] = useState<Source[]>([]);
  const [day, setDay] = useState<Summary | null>(null);
  const [live, setLive] = useState(false);
  // Bumped whenever a run starts or ends, so pages that aggregate refetch.
  const [tick, setTick] = useState(0);

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
    if (can("agents:read")) api.agents().then(setAgents).catch(() => {});
  }, [can]);
  const loadSources = useCallback(() => {
    if (can("knowledge:read")) api.sources().then(setSources).catch(() => {});
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
    api.analytics("24h").then(setDay).catch(() => {});
    loadAlerts();
    loadSchedules();
    setTick((t) => t + 1);
  }, [can, loadAlerts, loadSchedules]);

  useEffect(() => {
    loadRuns();
    loadAgents();
    loadSources();
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

  const select = (id: string) => {
    setSelected(id);
    setView("run");
  };

  const inbox = useMemo(() => inboxOf(runs, alerts, dismissed), [runs, alerts, dismissed]);
  const waiting = inbox.filter((i) => i.state === "open").length;
  const openAlerts = alerts.filter((a) => !a.acked_by).length;
  const stats = useMemo(() => new Map((day?.by_agent ?? []).map((g) => [g.name, g])), [day]);

  const onInbox = (a: UiAction) => {
    const item = inbox.find((i) => i.id === a.payload.target || i.source.turnId === a.payload.turnId);
    const run = runs.find((r) => r.id === item?.source.turnId || r.id === a.payload.target);
    if (a.id === ActionId.dismiss) {
      // Dismissing an alert is acknowledging it, which the server records.
      if (item?.kind === "alert" && can("alerts:write")) api.ackAlert(item.id).then(loadAlerts).catch(() => {});
      else setDismissed((d) => new Set(d).add(a.payload.target));
    } else if (a.id === ActionId.open && run) {
      select(run.id);
    } else if (a.id === ActionId.retry && run && can("runs:write")) {
      api
        .createRun(run.agent, run.text)
        .then((next) => {
          loadRuns();
          select(next.id);
        })
        .catch(() => {});
    }
  };

  return (
    <div className="flex h-full bg-aui-bg text-aui-fg">
      <Sidebar
        open={open}
        onToggle={toggle}
        view={view}
        onView={setView}
        me={me}
        can={can}
        runs={runs}
        selected={selected}
        onSelect={select}
        inboxCount={waiting}
        alertCount={openAlerts}
        live={live}
        onSignOut={() => api.logout().then(onSignedOut).catch(onSignedOut)}
      />
      <main className="flex min-h-0 min-w-0 flex-1 flex-col">
        {(view === "new" || view === "run") && (
          <RunView
            run={view === "run" ? runs.find((r) => r.id === selected) : undefined}
            agents={agents}
            canRun={can("runs:write")}
            onSelect={select}
            onStarted={loadRuns}
          />
        )}
        {view === "inbox" && (
          <div className="aui-scroll min-h-0 flex-1 overflow-y-auto">
            <div className="mx-auto flex w-full max-w-3xl flex-col gap-3 px-4 py-6">
              <WebSurface onAction={onInbox}>
                <Inbox items={inbox} onAction={onInbox} viewer={me.principal.name} />
              </WebSurface>
              <p className="text-aui-xs text-aui-muted">
                Open alerts and failed runs appear here. Approvals and budget requests will too, once a run can wait on
                a person.
              </p>
            </div>
          </div>
        )}
        {view === "runs" && <RunList runs={runs} onSelect={select} />}
        {view === "agents" && (
          <Agents agents={agents} sources={sources} stats={stats} canWrite={can("agents:write")} onChanged={loadAgents} />
        )}
        {view === "knowledge" && (
          <Knowledge sources={sources} agents={agents} canWrite={can("knowledge:write")} onChanged={loadSources} />
        )}
        {view === "schedules" && (
          <Schedules
            schedules={schedules}
            agents={agents}
            canWrite={can("schedules:write")}
            onChanged={loadSchedules}
            onOpenRun={select}
          />
        )}
        {view === "analytics" && <Analytics agents={agents} openAlerts={openAlerts} tick={tick} onOpenRun={select} />}
        {view === "alerts" && (
          <Alerts
            alerts={alerts}
            rules={rules}
            agents={agents}
            canWrite={can("alerts:write")}
            onChanged={loadAlerts}
            onOpenRun={select}
          />
        )}
      </main>
    </div>
  );
}
