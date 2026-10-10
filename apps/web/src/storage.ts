import { useCallback, useEffect, useRef, useState } from "react";
import { within } from "./timeout.ts";

/** A file an agent published from its sandbox. One row per title; `version` is its latest. */
export interface Stored {
  id: string;
  title: string;
  kind: "document" | "spreadsheet" | "presentation" | "image" | "pdf" | "html" | "code" | "data" | "archive";
  mime: string;
  size: number;
  sha256: string;
  version: number;
  run: string;
  thread: string;
  agent: string;
  project?: string;
  description?: string;
  /** The first few hundred characters, for text types only. */
  preview?: string;
  created_by: string;
  created_at: string;
}

export interface StorageUse {
  used_bytes: number;
  count: number;
  /** Null when nothing caps storage. */
  limit_bytes: number | null;
  by_project: { name: string; bytes: number; count: number }[];
  by_agent: { name: string; bytes: number; count: number }[];
}

export type Filter = { project: string; agent: string; kind: string; /** Part of a title. */ q: string };

/** Everything the storage page asks of the platform. Tests and the gallery pass their own. */
export interface StorageTransport {
  list(q: Filter & { limit: number; before?: string }): Promise<{ artifacts: Stored[]; next: string | null }>;
  versions(id: string): Promise<Stored[]>;
  remove(id: string): Promise<void>;
  use(): Promise<StorageUse>;
}

async function http<T>(method: string, path: string): Promise<T> {
  const res = await fetch(path, { method, credentials: "same-origin" });
  if (res.status === 204) return undefined as T;
  const data = (await res.json().catch(() => ({}))) as { error?: string };
  if (!res.ok) throw new Error(data.error ?? `Request failed (${res.status})`);
  return data as T;
}

const at = (id: string) => `/api/artifacts/${encodeURIComponent(id)}`;

export const transport: StorageTransport = {
  list: (q) => {
    const params = new URLSearchParams({ limit: String(q.limit) });
    for (const k of ["project", "agent", "kind", "q", "before"] as const) if (q[k]) params.set(k, q[k] as string);
    return http<{ artifacts: Stored[]; next: string | null }>("GET", `/api/artifacts?${params}`).then((r) => ({
      artifacts: r.artifacts ?? [],
      next: r.next ?? null,
    }));
  },
  versions: (id) => http<{ versions: Stored[] }>("GET", `${at(id)}/versions`).then((r) => r.versions ?? []),
  remove: (id) => http<void>("DELETE", at(id)),
  use: () => http<StorageUse>("GET", "/api/storage"),
};

/** Where a file's bytes are. A download, unless `inline` is asked for and the type is safe to show. */
export const contentUrl = (id: string, inline = false) => `${at(id)}/content${inline ? "?inline=1" : ""}`;

/** Types the platform will serve inline. Everything else, HTML and SVG included, is a download. */
const INLINE = new Set(["image/png", "image/jpeg", "image/gif", "image/webp"]);
export const showsInline = (file: Stored) => INLINE.has(file.mime);

/** 1536 as "1.5 KB". Whole bytes below a kilobyte, one decimal above. */
export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(1)} GB`;
}

export const kindWord: Record<Stored["kind"], string> = {
  document: "Document",
  spreadsheet: "Spreadsheet",
  presentation: "Presentation",
  image: "Image",
  pdf: "PDF",
  html: "HTML",
  code: "Code",
  data: "Data",
  archive: "Archive",
};

/** The platform's search: part of a title, in any case. Used by the in-memory transport. */
export function matches(file: Stored, query: string): boolean {
  return file.title.toLowerCase().includes(query.trim().toLowerCase());
}

const PAGE = 30;

/**
 * Stored files, a page at a time, newest first. The filters are the
 * platform's, so changing one starts from the first page. `signal` changing
 * fetches the first page again and the storage totals with it.
 */
export function useStorage(filter: Filter, signal: unknown, via: StorageTransport = transport) {
  const [files, setFiles] = useState<Stored[]>([]);
  const [next, setNext] = useState<string | null>(null);
  const [use, setUse] = useState<StorageUse | null>(null);
  const [loading, setLoading] = useState<"first" | "more" | null>("first");
  const [failed, setFailed] = useState(false);
  const [moreFailed, setMoreFailed] = useState(false);
  const [bump, setBump] = useState(0);

  const key = `${filter.project}|${filter.agent}|${filter.kind}|${filter.q}`;
  const current = useRef(key);
  const loaded = useRef("");

  // biome-ignore lint/correctness/useExhaustiveDependencies: signal and bump are reasons to run again, and the filter follows key
  useEffect(() => {
    current.current = key;
    const fresh = loaded.current !== key;
    if (fresh) {
      setFiles([]);
      setNext(null);
      setFailed(false);
      setLoading("first");
    }
    within(via.list({ ...filter, limit: PAGE }))
      .then((page) => {
        if (current.current !== key) return;
        setFiles(page.artifacts);
        setNext(page.next);
        setFailed(false);
        loaded.current = key;
      })
      .catch(() => {
        if (current.current === key && fresh) setFailed(true);
      })
      .finally(() => {
        if (current.current === key) setLoading((l) => (l === "first" ? null : l));
      });
    via
      .use()
      .then(setUse)
      .catch(() => {});
  }, [key, signal, bump, via]);

  const more = useCallback(() => {
    if (!next || loading) return;
    setLoading("more");
    setMoreFailed(false);
    within(via.list({ ...filter, limit: PAGE, before: next }))
      .then((page) => {
        if (current.current !== key) return;
        setFiles((old) => {
          const seen = new Set(old.map((f) => f.id));
          return [...old, ...page.artifacts.filter((f) => !seen.has(f.id))];
        });
        setNext(page.next);
      })
      .catch(() => {
        if (current.current === key) setMoreFailed(true);
      })
      .finally(() => {
        if (current.current === key) setLoading(null);
      });
    // biome-ignore lint/correctness/useExhaustiveDependencies: the filter follows key
  }, [key, next, loading, via]);

  return { files, hasMore: next !== null, use, loading, failed, moreFailed, more, reload: () => setBump((n) => n + 1) };
}
