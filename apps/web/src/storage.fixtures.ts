import { matches, type Stored, type StorageTransport, type StorageUse } from "./storage.ts";

const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();

const file = (id: string, title: string, kind: Stored["kind"], over: Partial<Stored> = {}): Stored => ({
  id,
  title,
  kind,
  mime: "text/plain",
  size: 4_096,
  sha256: "9f2c1e7ab04d55c3e1f0a7b6c2d4e8f90112233445566778899aabbccddeeff0",
  version: 1,
  run: "api-7f3k",
  thread: "t-204",
  agent: "lead",
  created_by: "dev",
  created_at: ago(30),
  ...over,
});

/** A handful of stored files of different kinds, for the gallery and the tests. */
export function sampleFiles(): Stored[] {
  return [
    file("a1", "launch-plan.md", "document", {
      version: 3,
      size: 18_240,
      mime: "text/markdown",
      project: "Launch",
      description: "The Q4 launch plan, with owners and dates.",
      preview: "# Q4 launch plan\n\n1. Freeze scope on the 14th.\n2. Dry run on the 21st.\n3. Ship on the 28th.",
      created_at: ago(12),
    }),
    file("a2", "refunds-q3.xlsx", "spreadsheet", { agent: "billing", size: 482_000, mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", description: "3 sheets: Summary, Refunds, Disputes.", created_at: ago(95) }),
    file("a3", "incident-timeline.png", "image", { agent: "oncall", size: 1_240_000, mime: "image/png", project: "Launch", created_at: ago(300) }),
    file("a4", "release-notes-2.4.html", "html", { agent: "releaser", version: 2, size: 9_300, mime: "text/html", preview: "<h1>2.4</h1><p>Fixes and one new command.</p>", created_at: ago(1500) }),
  ];
}

export const sampleUse = (over: Partial<StorageUse> = {}): StorageUse => ({
  used_bytes: 1_753_636,
  count: 4,
  limit_bytes: 5 * 1024 ** 2,
  by_project: [
    { name: "Launch", bytes: 1_258_240, count: 2 },
    { name: "", bytes: 495_396, count: 2 },
  ],
  by_agent: [],
  ...over,
});

/** Storage held in memory: it filters, pages and deletes the way the platform does. */
export function memoryStorage(seed: Stored[] = sampleFiles(), use: StorageUse = sampleUse(), size = 30): StorageTransport {
  let all = [...seed].sort((a, b) => b.created_at.localeCompare(a.created_at));
  return {
    list: async (q) => {
      const kept = all.filter((f) => (!q.project || f.project === q.project) && (!q.agent || f.agent === q.agent) && (!q.kind || f.kind === q.kind) && (!q.q || matches(f, q.q)));
      const from = q.before ? kept.findIndex((f) => f.id === q.before) + 1 : 0;
      const artifacts = kept.slice(from, from + Math.min(q.limit, size));
      const last = artifacts[artifacts.length - 1];
      return { artifacts, next: last && from + artifacts.length < kept.length ? last.id : null };
    },
    versions: async (id) => {
      const f = all.find((x) => x.id === id);
      if (!f) return [];
      return Array.from({ length: f.version }, (_, i) => ({ ...f, id: i === 0 ? f.id : `${f.id}-v${f.version - i}`, version: f.version - i }));
    },
    remove: async (id) => {
      all = all.filter((f) => f.id !== id);
    },
    use: async () => ({ ...use, count: all.length, used_bytes: all.reduce((n, f) => n + f.size, 0) }),
  };
}
