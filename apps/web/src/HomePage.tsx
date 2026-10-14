import { ANY_AGENT, type Tone, type UiAction } from "@vultus/core";
import { Home } from "@vultus/components";
import { WebSurface } from "@vultus/render-web";
import { useEffect, useMemo, useRef, useState } from "react";
import { explainedPermission } from "./access.ts";
import type { Run } from "./adapter.ts";
import { type AccessDecision, type Alert, ApiError, api, type Client, type Me, type Revision } from "./api.ts";
import { type Attached, readAttached, withAttached } from "./Composer.tsx";
import { isEnded } from "./format.ts";
import { buildHome, homeIntent, NOTIFICATION_WINDOW } from "./home.ts";
import { type PlatformMessage, transport } from "./mail.ts";

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
  alerts,
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
  /** The tenant's alerts as the console holds them, or null when the person may not read alerts. */
  alerts: Alert[] | null;
  /** Changes when the inbox may have changed. */
  signal: unknown;
  /** A conversation was started from the ask box. */
  onThread: (id: string) => void;
  onReview: (message: string) => void;
  /** A console view, by the name `?view=` uses. */
  onNavigate: (view: string) => void;
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
  // Shown under the ask box, by vultus's Prompt.
  const notices = useMemo(() => {
    const out: { tone: Tone; text: string }[] = [];
    if (problem) out.push({ tone: "bad", text: problem });
    if (files.length > 0) out.push({ tone: "info", text: `Sent with your question: ${files.map((f) => f.name).join(", ")}.` });
    if (notice) out.push({ tone: "warn", text: notice });
    return out;
  }, [problem, files, notice]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: signal is the reason to look again
  useEffect(() => {
    if (!readsInbox) return;
    let live = true;
    transport
      .list({ limit: NOTIFICATION_WINDOW, state: "open" })
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
        alerts,
        running: runs.filter((r) => !isEnded(r)).length,
        clients: readsClients ? (clients ?? []) : null,
        decision,
        answeredBy,
        notices,
      }),
    [me, name, agents, waiting, inboxCount, alerts, runs, clients, readsClients, decision, answeredBy, notices],
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

  return (
    <div className="aui-scroll collegium-fade min-h-0 flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-3 px-5 pt-14 pb-16 md:px-8">
        <input
          ref={picker}
          type="file"
          multiple
          // Opened by the page's own "Attach a File", so it is kept out of the tab order and the tree.
          hidden
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
        <WebSurface onAction={onAction}>
          <Home home={home} />
        </WebSurface>
      </div>
    </div>
  );
}
