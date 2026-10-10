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
  active: number;
  p50_ms: number;
  p95_ms: number;
}

export interface Bucket {
  start: string;
  completed: number;
  failed: number;
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
}

export interface Note {
  id: string;
  text: string;
  source: "person" | "consolidated";
  by?: string;
  at: string;
}

export interface Preferences {
  display_name: string;
  theme: "system" | "light" | "dark";
  accent: string;
  has_photo: boolean;
  photo_version: number;
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
  agents: () => call<{ agents: Revision[] }>("GET", "/api/agents").then((r) => r.agents),
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
  removeMemory: (agent: string, id: string) =>
    call<void>("DELETE", `/api/agents/${encodeURIComponent(agent)}/memory/${encodeURIComponent(id)}`),

  preferences: () => call<Preferences>("GET", "/api/preferences"),
  savePreferences: (p: Pick<Preferences, "display_name" | "theme" | "accent">) =>
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
  handlers: { onOpen?: () => void; onEvent: (e: BusEvent) => void; onState?: (live: boolean) => void },
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
  return () => source.close();
}
