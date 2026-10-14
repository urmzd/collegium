import { ANY_AGENT, type UiAction } from "@vultus/core";
import { Home } from "@vultus/components";
import { WebSurface } from "@vultus/render-web";
import { type MouseEvent, useEffect, useMemo, useRef, useState } from "react";
import { explainedPermission } from "./access.ts";
import type { Run } from "./adapter.ts";
import { type AccessDecision, ApiError, api, type Client, type Me, type Revision } from "./api.ts";
import { type Attached, readAttached, withAttached } from "./Composer.tsx";
import { isEnded } from "./format.ts";
import { buildHome, destinationOf, homeIntent } from "./home.ts";
import { type PlatformMessage, transport } from "./mail.ts";
import { Problem } from "./ui.tsx";

/**
 * Where a person lands. The page is vultus's `Home`; this fetches what it
 * needs that the console does not already hold, builds its model, and does
 * what its actions ask.
 */
export function HomePage({
  me,
  name,
  agents,
  runs,
  inboxCount,
  signal,
  onThread,
  onReview,
  onNavigate,
}: {
  me: Me;
  name: string;
  agents: Revision[];
  runs: Run[];
  inboxCount: number;
  /** Changes when the inbox may have changed. */
  signal: unknown;
  /** A conversation was started from the ask box. */
  onThread: (id: string) => void;
  onReview: (message: string) => void;
  /** A console view, by the name `?view=` uses. */
  onNavigate: (view: string, params?: URLSearchParams) => void;
}) {
  const can = (p: string) => (me.permissions ?? []).includes(p);
  const [waiting, setWaiting] = useState<PlatformMessage[]>([]);
  const [clients, setClients] = useState<Client[] | null>(null);
  const [decision, setDecision] = useState<AccessDecision | null>(null);
  const [answeredBy, setAnsweredBy] = useState(ANY_AGENT);
  const [files, setFiles] = useState<Attached[]>([]);
  const [notice, setNotice] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const picker = useRef<HTMLInputElement>(null);
  const readsInbox = can("runs:read");
  const readsClients = can("clients:read");

  // biome-ignore lint/correctness/useExhaustiveDependencies: signal is the reason to look again
  useEffect(() => {
    if (!readsInbox) return;
    let live = true;
    transport
      .list({ limit: 30, state: "open" })
      .then((page) => live && setWaiting(page.messages))
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [readsInbox, signal]);

  useEffect(() => {
    if (!readsClients) return;
    api
      .clients()
      .then((r) => setClients(r.clients))
      .catch(() => {});
  }, [readsClients]);

  useEffect(() => {
    api
      .explain(explainedPermission(me))
      .then(setDecision)
      .catch(() => {});
  }, [me]);

  const home = useMemo(
    () =>
      buildHome({
        me,
        name,
        agents,
        waiting,
        waitingCount: inboxCount,
        running: runs.filter((r) => !isEnded(r)).length,
        clients: readsClients ? (clients ?? []) : null,
        decision,
        answeredBy,
      }),
    [me, name, agents, waiting, inboxCount, runs, clients, readsClients, decision, answeredBy],
  );

  const ask = (text: string) => {
    if (!can("runs:write")) return setProblem("You cannot run agents in this workspace.");
    setProblem(null);
    api
      .createRun(answeredBy, withAttached(text, files))
      .then((run) => {
        setFiles([]);
        onThread(run.thread);
      })
      .catch((e) => setProblem(e instanceof ApiError ? e.message : "The question could not be sent."));
  };

  const onAction = (a: UiAction) => {
    const intent = homeIntent(a);
    if (!intent) return;
    switch (intent.kind) {
      case "ask":
        return ask(intent.text);
      case "attach":
        return picker.current?.click();
      case "answered_by":
        return setAnsweredBy(intent.agent);
      case "review":
        return onReview(intent.message);
      case "open":
        return onNavigate(intent.view);
    }
  };

  // Links into the console open in place, not in a new tab.
  const onClickCapture = (e: MouseEvent<HTMLDivElement>) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    const link = (e.target as HTMLElement).closest("a");
    const to = link && destinationOf(link.getAttribute("href") ?? "", window.location.origin);
    if (!to) return;
    e.preventDefault();
    onNavigate(to.view, to.params);
  };

  return (
    <div className="aui-scroll collegium-fade min-h-0 flex-1 overflow-y-auto">
      {/* biome-ignore lint/a11y/noStaticElementInteractions: delegates clicks on the links inside, which are the controls */}
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-3 px-5 pt-14 pb-16 md:px-8" onClickCapture={onClickCapture}>
        <input
          ref={picker}
          type="file"
          multiple
          className="sr-only"
          tabIndex={-1}
          aria-label="Attach files to your question"
          onChange={(e) => {
            const picked = e.target.files;
            e.target.value = "";
            if (!picked) return;
            void readAttached(picked, files).then(({ files: next, skipped }) => {
              setFiles(next);
              setNotice(skipped.length > 0 ? skipped.join(". ") : null);
            });
          }}
        />
        {/* Above the page, so what went wrong with a question is seen without scrolling. */}
        <div className="sticky top-0 z-10 flex flex-col gap-2 empty:hidden">
          <Problem message={problem} />
          {(files.length > 0 || notice) && (
            <p className="collegium-glass rounded-aui px-3 py-2 text-aui-xs text-aui-muted">
              {files.length > 0 && `Sent with your question: ${files.map((f) => f.name).join(", ")}. `}
              {notice}
            </p>
          )}
        </div>
        <p className="sr-only" aria-live="polite">
          {files.length > 0 ? `${files.length} attached` : ""}
        </p>
        <WebSurface onAction={onAction}>
          <Home home={home} />
        </WebSurface>
      </div>
    </div>
  );
}
