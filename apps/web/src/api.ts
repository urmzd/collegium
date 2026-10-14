import type { BusEvent, Run } from "./adapter.ts";

export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
  }
}

export interface Principal {
  subject: string;
  tenant: string;
  kind: "user" | "machine";
  name?: string;
  email?: string;
  roles?: string[];
}

export interface Me {
  principal: Principal;
  permissions: string[];
  /** The caller is an admin in a workspace nobody has set up yet. */
  first_admin?: boolean;
}

/** Why the caller may or may not do one thing. Only ever about the caller. */
export interface AccessDecision {
  permission: string;
  allowed: boolean;
  /** "scope" when a machine's token decided rather than the policy. */
  outcome: "allowed" | "denied" | "no match" | "scope";
  role?: string;
  path?: string[];
  rule?: string;
  /** The grant that gave `role`, when the sign-in kept it. */
  grant?: string;
  held: { role: string; grant?: string; group?: string }[];
  unknown?: string[];
}

export interface AgentSpec {
  name: string;
  description?: string;
  model: string;
  provider: string;
  system_prompt: string;
  tools?: string[];
  sources?: { name: string; uri: string }[];
  access?: { spawn?: string[]; workspace_areas?: string[] };
}

export interface Revision {
  spec: AgentSpec;
  rev: number;
  hash: string;
  created_at: string;
  created_by: string;
}

export interface Schedule {
  name: string;
  expression: string;
  agent: string;
  text: string;
  /** Files every run the schedule raises under a project. */
  project?: string;
}

/** A schedule plus what makes it trustworthy: state, next fires, recent runs. */
export interface ScheduleView extends Schedule {
  paused: boolean;
  created_by?: string;
  next: string[];
  recent: Run[];
}

export interface Group {
  name: string;
  runs: number;
  completed: number;
  failed: number;
  /** Runs stopped on request. Neither completed, failed nor in flight. */
  stopped: number;
  active: number;
  p50_ms: number;
  p95_ms: number;
  /** What the runs' own model calls used. Each run counts only its own calls. */
  usage?: { model_calls: number; tokens: number; spend: number; unpriced_calls: number; other_currency_runs: number };
}

export interface Bucket {
  start: string;
  completed: number;
  failed: number;
  stopped: number;
  active: number;
}

export interface Summary {
  since: string;
  until: string;
  bucket_ns: number;
  total: Group;
  buckets: Bucket[];
  by_agent: Group[];
  by_origin: Group[];
  slowest: Run[] | null;
  delegated: number;
}

export interface Entry {
  seq: number;
  run_id: string;
  agent: string;
  rev: number;
  origin: string;
  kind: string;
  at: string;
  detail?: string;
}

export interface Span {
  run_id: string;
  agent: string;
  kind: "run" | "attempt" | "tool" | "output" | "parked";
  name: string;
  start: string;
  end?: string;
  error?: string;
}

export interface AlertRule {
  name: string;
  kind: "run_failed" | "failure_rate" | "slow_run" | "eval_below";
  agent?: string;
  severity: "warning" | "critical";
  threshold?: number;
  window_seconds?: number;
  min_runs?: number;
  created_by?: string;
}

export interface Alert {
  id: string;
  rule: string;
  kind: AlertRule["kind"];
  severity: AlertRule["severity"];
  agent?: string;
  run_id?: string;
  suite?: string;
  title: string;
  body?: string;
  at: string;
  acked_by?: string;
  acked_at?: string;
}

export interface Source {
  name: string;
  description?: string;
  documents: number;
  passages: number;
  /** Documents held but left out of search. */
  excluded: number;
  variants: { name: string; excluded: string[] }[];
  updated_at: string;
}

export interface Doc {
  id: string;
  title: string;
  uri?: string;
  bytes: number;
  passages: number;
  excluded: boolean;
  added_by?: string;
  added_at: string;
}

export interface Hit {
  source: string;
  document_id: string;
  title: string;
  uri?: string;
  index: number;
  text: string;
  score: number;
}

export interface ThreadSummary {
  id: string;
  title: string;
  /** The title is a name, not just the first message. */
  named: boolean;
  origin: string;
  project?: string;
  agents: string[];
  turns: number;
  branches: number;
  active: number;
  failed: number;
  created_at: string;
  updated_at: string;
}

export interface Choice {
  agent: string;
  reason: string;
  matched?: string[];
}

export interface Project {
  name: string;
  description?: string;
}

export interface Settings {
  baseline: string;
  history: "recent" | "none";
  history_limit: number;
  projects: Project[];
  title_agent: string;
  memory_agent: string;
  memory_auto_approve: boolean;
  workspace_name?: string;
  /** Set once by the server when an admin first saves settings. */
  setup_at?: string;
  setup_by?: string;
}

/** A proposed memory, for an agent or for a project. */
export interface Proposal {
  agent?: string;
  project?: string;
  note: Note;
}

export interface PolicyScope {
  kind: "tenant" | "project" | "agent" | "client";
  name?: string;
}

/** The limits for one scope. A limit of 0 or absent means the scope sets none. */
export interface Policy {
  scope: PolicyScope;
  period: "day" | "month";
  max_runs?: number;
  max_concurrent?: number;
  /** Micro-USD a period, enforced. Only money priced in US dollars counts. */
  spend_limit?: number;
  max_lease?: number;
  /** Model tokens a period, enforced, priced or not. */
  max_tokens?: number;
  warn_at?: number;
  action: "block" | "approve";
  version: number;
  updated_by?: string;
  updated_at: string;
}

/** Where one of a client's secrets is. The secret itself is never sent. */
export interface SecretRef {
  set: boolean;
  /** "env:NAME" when the secret lives in the server's environment. */
  ref?: string;
}

/** A Slack app or GitHub installation the workspace answers through. */
export interface Client {
  id: string;
  name: string;
  surface: "slack" | "github";
  identity: { app_id?: string; team?: string; installation?: string; owners?: string[] };
  credentials: { token: SecretRef; signing_secret: SecretRef };
  default_agent?: string;
  /** The agents it may route to besides its default. "*" alone means any. */
  agents?: string[];
  bots?: Record<string, string>;
  slack?: { sessions?: boolean; commands?: { name: string; description: string; argument_hint?: string }[] };
  github?: {
    auto_labels?: Record<string, Record<string, string>>;
    checks?: boolean;
    guides?: boolean;
    mutations?: boolean;
    read_repositories?: string[];
  };
  enabled: boolean;
  /** Set on a client the server's configuration owns and the API cannot change. */
  managed?: string;
  version: number;
  /** The client's own policy, set through the policies API with a client scope. */
  limits?: Policy;
}

export interface ClientCaps {
  max: number;
  max_per_surface: number;
}

export interface Measure {
  used: number;
  limit: number;
  projected?: number;
}

/** One policy and where its scope stands against it. */
export interface ScopeUsage {
  scope: PolicyScope;
  period: "day" | "month";
  period_start: string;
  period_end: string;
  version: number;
  warn_at: number;
  action: "block" | "approve";
  runs: Measure;
  concurrent: Measure;
  /** Micro-USD priced in US dollars. A lower bound when calls had no price or another currency. */
  spend: Measure & { metered: boolean; unpriced_calls: number; other_costs?: { micros: number; currency: string }[] };
  tokens: Measure;
  max_lease: number;
  state: "ok" | "warn" | "over";
}

export interface PolicyChange {
  scope: PolicyScope;
  by: string;
  at: string;
  before?: Policy;
  after?: Policy;
}

/** A project and what is in it. */
export interface ProjectView {
  name: string;
  description?: string;
  conversations: number;
  active: number;
  schedules: number;
  notes: number;
  proposed: number;
  updated_at?: string;
}

export interface Note {
  id: string;
  text: string;
  source: "person" | "consolidated";
  /** Pending notes are proposals: not used until a person approves them. */
  status: "approved" | "pending";
  by?: string;
  at: string;
}

export interface Preferences {
  display_name: string;
  theme: "system" | "light" | "dark";
  accent: string;
  has_photo: boolean;
  photo_version: number;
  /** When the person finished or skipped the first-run questions. Absent means not yet. */
  onboarded_at?: string;
}

export interface Check {
  kind: "contains" | "not_contains" | "matches" | "equals" | "within_ms";
  value: string;
}

export interface EvalCase {
  id: string;
  input: string;
  checks?: Check[];
  expect?: string[];
}

export interface EvalResult {
  case_id: string;
  input: string;
  passed: boolean;
  output?: string;
  verdicts?: { check: string; passed: boolean; reason?: string }[];
  hits?: { title: string; score: number; expected: boolean }[];
  rank?: number;
  run_id?: string;
  duration_ms: number;
  error?: string;
}

export interface EvalRun {
  id: string;
  suite: string;
  target: "agent" | "knowledge";
  subject: string;
  rev?: number;
  status: "running" | "passed" | "failed" | "errored";
  passed: number;
  total: number;
  pass_rate: number;
  threshold: number;
  metrics?: Record<string, number>;
  results: EvalResult[];
  started_by?: string;
  started_at: string;
  ended_at?: string;
}

export interface Suite {
  name: string;
  description?: string;
  target: "agent" | "knowledge";
  subject: string;
  cases: EvalCase[];
  k?: number;
  pass_rate?: number;
}

export interface SuiteView extends Suite {
  runs: EvalRun[];
}

// Same-origin only. The session is an HttpOnly cookie the page cannot read,
// so there is no token here to leak.
async function call<T>(method: string, path: string, json?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: json === undefined ? undefined : { "Content-Type": "application/json" },
    body: json === undefined ? undefined : JSON.stringify(json),
  });
  if (res.status === 204) return undefined as T;
  const data = (await res.json().catch(() => ({}))) as { error?: string };
  if (!res.ok) throw new ApiError(res.status, data.error ?? `Request failed (${res.status})`);
  return data as T;
}

export const api = {
  me: () => call<Me>("GET", "/api/me"),
  explain: (permission: string) =>
    call<AccessDecision>("GET", `/api/access/explain?${new URLSearchParams({ permission })}`),
  agents: () => call<{ agents: Revision[] }>("GET", "/api/agents").then((r) => r.agents),
  /** Every agent, and the names of those the platform provides, which cannot be edited. */
  agentList: () => call<{ agents: Revision[]; builtin?: string[] }>("GET", "/api/agents"),
  createAgent: (spec: AgentSpec) => call<Revision>("POST", "/api/agents", spec),
  reviseAgent: (spec: AgentSpec, expectedRev: number) =>
    call<Revision>("PUT", `/api/agents/${encodeURIComponent(spec.name)}`, {
      spec,
      expected_rev: expectedRev,
    }),
  runs: () => call<{ runs: Run[] }>("GET", "/api/runs").then((r) => r.runs),
  tree: (id: string) =>
    call<{ runs: Run[] }>("GET", `/api/runs/${encodeURIComponent(id)}/tree`).then((r) => r.runs),
  createRun: (agent: string, text: string, opts: { after?: string; project?: string } = {}) =>
    call<Run>("POST", "/api/runs", { agent, text, ...opts }),
  threads: () => call<{ threads: ThreadSummary[] }>("GET", "/api/threads").then((r) => r.threads),
  /** One page of conversations, newest first. `next` is the cursor for the page after it, null at the end, and absent from a server that does not page. */
  threadsPage: (q: { limit: number; before?: string; origin?: string; agent?: string; standing?: string }) => {
    const params = new URLSearchParams({ limit: String(q.limit) });
    for (const k of ["before", "origin", "agent", "standing"] as const) if (q[k]) params.set(k, q[k]);
    return call<{ threads: ThreadSummary[]; next?: string | null }>("GET", `/api/threads?${params}`);
  },
  thread: (id: string) =>
    call<{ turns: Run[] }>("GET", `/api/threads/${encodeURIComponent(id)}`).then((r) => r.turns),
  pick: (q: string) => call<Choice>("GET", `/api/pick?${new URLSearchParams({ q })}`),
  settings: () => call<Settings>("GET", "/api/settings"),
  saveSettings: (s: Settings) => call<Settings>("PUT", "/api/settings", s),
  history: (name: string) =>
    call<{ revisions: Revision[] }>("GET", `/api/agents/${encodeURIComponent(name)}/history`).then((r) => r.revisions),

  memory: (agent: string) =>
    call<{ notes: Note[] }>("GET", `/api/agents/${encodeURIComponent(agent)}/memory`).then((r) => r.notes),
  addMemory: (agent: string, text: string) =>
    call<Note>("POST", `/api/agents/${encodeURIComponent(agent)}/memory`, { text }),
  approveMemory: (agent: string, ids: string[]) =>
    call<{ approved: number }>("POST", `/api/agents/${encodeURIComponent(agent)}/memory/approve`, { ids }),
  pendingMemory: () => call<{ proposals: Proposal[] }>("GET", "/api/memory/pending").then((r) => r.proposals),
  /** Stop a run and everything attached to it. Runs it delegated keep going. */
  stopRun: (id: string) => call<Run>("POST", `/api/runs/${encodeURIComponent(id)}/stop`),
  /** A run's events as they were published, for a run that is no longer streaming. */
  runEvents: (id: string) =>
    call<{ events: BusEvent[]; complete: boolean }>("GET", `/api/runs/${encodeURIComponent(id)}/events`),
  /** Everything recorded about a run and the runs under it, as one document. */
  manifest: (id: string) => call<unknown>("GET", `/api/runs/${encodeURIComponent(id)}/manifest`),
  inboxCount: () => call<{ open: number; unread: number }>("GET", "/api/inbox/count"),
  /** The id of the message an alert raised, or null when it raised none. */
  inboxFor: (alert: string) =>
    call<{ messages: { id: string }[] }>("GET", `/api/inbox?alert=${encodeURIComponent(alert)}`).then((r) => r.messages[0]?.id ?? null),
  clients: () => call<{ clients: Client[]; caps: ClientCaps }>("GET", "/api/clients"),
  /** Replace a client's record. Secrets are not sent, so the server keeps the ones it has. */
  saveClient: (c: Client, enabled: boolean) =>
    call<Client>("PUT", `/api/clients/${encodeURIComponent(c.id)}`, {
      name: c.name,
      surface: c.surface,
      identity: c.identity,
      default_agent: c.default_agent ?? "",
      agents: c.agents ?? [],
      bots: c.bots ?? {},
      slack: c.slack ?? null,
      github: c.github ?? null,
      enabled,
    }),
  deleteClient: (id: string) => call<void>("DELETE", `/api/clients/${encodeURIComponent(id)}`),
  policies: () => call<{ policies: Policy[] }>("GET", "/api/policies").then((r) => r.policies),
  savePolicy: (p: Omit<Policy, "version" | "updated_at" | "updated_by" | "action"> & { action?: Policy["action"] }) =>
    call<Policy>("PUT", "/api/policies", p),
  deletePolicy: (scope: PolicyScope) =>
    call<void>("DELETE", `/api/policies?${new URLSearchParams({ kind: scope.kind, name: scope.name ?? "" })}`),
  policyHistory: () => call<{ changes: PolicyChange[] }>("GET", "/api/policies/history").then((r) => r.changes),
  usage: () => call<{ scopes: ScopeUsage[] }>("GET", "/api/usage").then((r) => r.scopes),
  projects: () => call<{ projects: ProjectView[] }>("GET", "/api/projects").then((r) => r.projects),
  createProject: (name: string, description: string) => call<void>("POST", "/api/projects", { name, description }),
  deleteProject: (name: string) => call<void>("DELETE", `/api/projects/${encodeURIComponent(name)}`),
  projectMemory: (project: string) =>
    call<{ notes: Note[] }>("GET", `/api/projects/${encodeURIComponent(project)}/memory`).then((r) => r.notes),
  addProjectMemory: (project: string, text: string) =>
    call<Note>("POST", `/api/projects/${encodeURIComponent(project)}/memory`, { text }),
  approveProjectMemory: (project: string, ids: string[]) =>
    call<{ approved: number }>("POST", `/api/projects/${encodeURIComponent(project)}/memory/approve`, { ids }),
  removeProjectMemory: (project: string, id: string) =>
    call<void>("DELETE", `/api/projects/${encodeURIComponent(project)}/memory/${encodeURIComponent(id)}`),
  removeMemory: (agent: string, id: string) =>
    call<void>("DELETE", `/api/agents/${encodeURIComponent(agent)}/memory/${encodeURIComponent(id)}`),

  preferences: () => call<Preferences>("GET", "/api/preferences"),
  savePreferences: (p: Pick<Preferences, "display_name" | "theme" | "accent"> & { onboarded?: boolean }) =>
    call<Preferences>("PUT", "/api/preferences", p),
  removePhoto: () => call<Preferences>("DELETE", "/api/preferences/photo"),
  // The picture is sent as its own bytes. The server decides what it is.
  uploadPhoto: async (file: Blob) => {
    const res = await fetch("/api/preferences/photo", { method: "PUT", credentials: "same-origin", body: file });
    const data = (await res.json().catch(() => ({}))) as Preferences & { error?: string };
    if (!res.ok) throw new ApiError(res.status, data.error ?? "The picture could not be uploaded.");
    return data as Preferences;
  },

  suites: () => call<{ suites: SuiteView[] }>("GET", "/api/evals").then((r) => r.suites),
  createSuite: (s: Suite) => call<Suite>("POST", "/api/evals", s),
  saveSuite: (s: Suite) => call<Suite>("PUT", `/api/evals/${encodeURIComponent(s.name)}`, s),
  deleteSuite: (name: string) => call<void>("DELETE", `/api/evals/${encodeURIComponent(name)}`),
  runSuite: (name: string) => call<EvalRun>("POST", `/api/evals/${encodeURIComponent(name)}/run`),
  schedules: () => call<{ schedules: ScheduleView[] }>("GET", "/api/schedules").then((r) => r.schedules),
  createSchedule: (s: Schedule) => call<ScheduleView>("POST", "/api/schedules", s),
  deleteSchedule: (name: string) => call<void>("DELETE", `/api/schedules/${encodeURIComponent(name)}`),
  pauseSchedule: (name: string, paused: boolean) =>
    call<ScheduleView>("POST", `/api/schedules/${encodeURIComponent(name)}/${paused ? "pause" : "resume"}`),
  runSchedule: (name: string) => call<Run>("POST", `/api/schedules/${encodeURIComponent(name)}/run`),

  analytics: (window: string) => call<Summary>("GET", `/api/analytics?window=${window}`),
  journal: (params: Record<string, string>) =>
    call<{ entries: Entry[] }>("GET", `/api/journal?${new URLSearchParams(params)}`).then((r) => r.entries),
  trace: (id: string) => call<{ runs: Run[]; spans: Span[] }>("GET", `/api/runs/${encodeURIComponent(id)}/trace`),

  alerts: () => call<{ alerts: Alert[] }>("GET", "/api/alerts").then((r) => r.alerts),
  ackAlert: (id: string) => call<Alert>("POST", `/api/alerts/${encodeURIComponent(id)}/ack`),
  alertRules: () => call<{ rules: AlertRule[] }>("GET", "/api/alert-rules").then((r) => r.rules),
  createAlertRule: (r: AlertRule) => call<AlertRule>("POST", "/api/alert-rules", r),
  deleteAlertRule: (name: string) => call<void>("DELETE", `/api/alert-rules/${encodeURIComponent(name)}`),

  sources: () => call<{ sources: Source[] }>("GET", "/api/knowledge").then((r) => r.sources),
  createSource: (name: string, description: string) => call<Source>("POST", "/api/knowledge", { name, description }),
  deleteSource: (name: string) => call<void>("DELETE", `/api/knowledge/${encodeURIComponent(name)}`),
  documents: (name: string) =>
    call<{ documents: Doc[] }>("GET", `/api/knowledge/${encodeURIComponent(name)}/documents`).then((r) => r.documents),
  addDocument: (name: string, d: { title: string; uri: string; text: string }) =>
    call<Doc>("POST", `/api/knowledge/${encodeURIComponent(name)}/documents`, d),
  removeDocument: (name: string, id: string) =>
    call<void>("DELETE", `/api/knowledge/${encodeURIComponent(name)}/documents/${encodeURIComponent(id)}`),
  excludeDocument: (name: string, id: string, excluded: boolean) =>
    call<Doc>("PUT", `/api/knowledge/${encodeURIComponent(name)}/documents/${encodeURIComponent(id)}`, { excluded }),
  saveVariant: (name: string, variant: string) =>
    call<{ name: string }>("POST", `/api/knowledge/${encodeURIComponent(name)}/variants`, { name: variant }),
  deleteVariant: (name: string, variant: string) =>
    call<void>("DELETE", `/api/knowledge/${encodeURIComponent(name)}/variants/${encodeURIComponent(variant)}`),
  searchKnowledge: (q: string, source: string, variant = "") =>
    call<{ hits: Hit[] }>(
      "GET",
      `/api/knowledge/search?${new URLSearchParams(variant ? { q, source, variant } : { q, source })}`,
    ).then((r) => r.hits),
  logout: () => call<void>("POST", "/auth/logout"),
};

/**
 * Subscribe to the tenant's events. `onOpen` fires on every (re)connect, and
 * the stream then replays what the bus still holds, so a caller that folds
 * events into state resets that state in `onOpen`.
 */
export function subscribe(
  topic: string,
  handlers: {
    onOpen?: () => void;
    onEvent: (e: BusEvent) => void;
    onState?: (live: boolean) => void;
    /** The inbox changed. It arrives as a named event, apart from run events. */
    onInbox?: () => void;
  },
): () => void {
  const source = new EventSource(`/api/events?topic=${encodeURIComponent(topic)}`);
  source.onopen = () => {
    handlers.onOpen?.();
    handlers.onState?.(true);
  };
  source.onerror = () => handlers.onState?.(false);
  source.onmessage = (m) => {
    try {
      handlers.onEvent(JSON.parse(m.data as string) as BusEvent);
    } catch {
      // A frame that is not an event is ignored rather than fatal.
    }
  };
  source.addEventListener("inbox", () => handlers.onInbox?.());
  return () => source.close();
}
