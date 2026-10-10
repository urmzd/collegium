import { useCallback, useEffect, useRef, useState } from "react";
import { api, type ThreadSummary } from "./api.ts";

export type Standing = "" | "running" | "failed";

/** How many conversations one page holds. Small, so the first paint is quick. */
const PAGE = 30;

const keep = (t: ThreadSummary, agent: string, standing: Standing) =>
  t.origin === "api" && (!agent || t.agents.includes(agent)) && (standing === "" || (standing === "running" ? t.active > 0 : t.failed > 0));

/**
 * The Recents list, a page at a time.
 *
 * - The first page is fetched again whenever `signal` changes, which is how a
 *   run starting or ending reaches the list. The fresh page replaces the top
 *   of what is loaded, and older pages a person already asked for stay where
 *   they are, so neither the rows nor the scroll position jump.
 * - A conversation that moves to the top between pages is shown once, in its
 *   new place.
 * - Changing a filter starts again from the first page.
 * - If "See more" fails, the rows already loaded stay, and it can be tried again.
 *
 * A server that does not page yet answers with everything and no cursor. The
 * same list is then paged here instead, so the screen behaves the same.
 */
export function useRecents(agent: string, standing: Standing, signal: unknown) {
  const [rows, setRows] = useState<ThreadSummary[]>([]);
  // A cursor for the next page, null when there is none.
  const [next, setNext] = useState<string | null>(null);
  // Set when the server sent everything at once: how many of `rows` are shown.
  const [local, setLocal] = useState<number | null>(null);
  const [loading, setLoading] = useState<"first" | "more" | null>("first");
  const [failed, setFailed] = useState(false);

  // Every request carries the filters it was made for. An answer for filters
  // that have since changed is dropped.
  const key = `${agent}|${standing}`;
  const current = useRef(key);
  const loaded = useRef({ key: "", pages: 0 });

  // biome-ignore lint/correctness/useExhaustiveDependencies: signal is the reason to run again, not a value read here
  useEffect(() => {
    current.current = key;
    const fresh = loaded.current.key !== key;
    if (fresh) {
      setRows([]);
      setNext(null);
      setLocal(null);
      setFailed(false);
      setLoading("first");
    }
    api
      .threadsPage({ limit: PAGE, origin: "api", agent, standing })
      .then((page) => {
        if (current.current !== key) return;
        if (page.next === undefined) {
          // Not paged by the server. Filter and page what it sent.
          setRows(page.threads.filter((t) => keep(t, agent, standing)));
          setLocal((n) => (fresh || n === null ? PAGE : n));
          setNext(null);
        } else if (fresh || loaded.current.pages <= 1 || page.next === null) {
          // Also when the fresh page is the whole list: nothing older is left to keep.
          setRows(page.threads);
          setNext(page.next);
          setLocal(null);
          loaded.current = { key, pages: 1 };
        } else {
          // Older pages are loaded. Keep the ones past the end of the fresh page.
          const seen = new Set(page.threads.map((t) => t.id));
          const edge = page.threads[page.threads.length - 1]?.updated_at ?? "";
          setRows((old) => [...page.threads, ...old.filter((t) => !seen.has(t.id) && t.updated_at < edge)]);
        }
        loaded.current = { key, pages: Math.max(fresh ? 1 : loaded.current.pages, 1) };
      })
      .catch(() => {
        // A failed refresh leaves what is on screen. The next signal tries again.
      })
      .finally(() => {
        if (current.current === key) setLoading((l) => (l === "first" ? null : l));
      });
  }, [key, signal]);

  const more = useCallback(() => {
    if (local !== null) {
      setLocal(local + PAGE);
      return;
    }
    if (!next || loading) return;
    setLoading("more");
    setFailed(false);
    api
      .threadsPage({ limit: PAGE, origin: "api", agent, standing, before: next })
      .then((page) => {
        if (current.current !== key) return;
        setRows((old) => {
          const seen = new Set(old.map((t) => t.id));
          return [...old, ...page.threads.filter((t) => !seen.has(t.id))];
        });
        setNext(page.next ?? null);
        loaded.current = { key, pages: loaded.current.pages + 1 };
      })
      .catch(() => {
        if (current.current === key) setFailed(true);
      })
      .finally(() => {
        if (current.current === key) setLoading(null);
      });
  }, [agent, standing, key, next, local, loading]);

  return {
    threads: local === null ? rows : rows.slice(0, local),
    hasMore: local === null ? next !== null : rows.length > local,
    loading,
    failed,
    more,
  };
}
