import { ActionId, type InboxMessage, messageState, openParts, type UiAction } from "@agent-ui/core";
import { Mailbox, type MailboxShow } from "@agent-ui/web";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { useInbox } from "./inbox.ts";
import { platformParts } from "./MailParts.tsx";
import { kinds, type PlatformMessage, type Source, type Transport, transport as platform } from "./mail.ts";
import { Empty, Menu, Problem, SkeletonRows, Stroke, type StrokeState } from "./ui.tsx";

/** The id in the address, so a message can be linked to. */
function linked(): string | null {
  try {
    return new URLSearchParams(window.location.search).get("message");
  } catch {
    return null;
  }
}
function link(id: string | null) {
  try {
    const url = new URL(window.location.href);
    if (id) url.searchParams.set("message", id);
    else url.searchParams.delete("message");
    window.history.replaceState(null, "", url);
  } catch {
    // The address just does not follow the selection.
  }
}

/** The edge a message is drawn with: its agent's color if an agent sent it, and how it stands. */
export function edgeOf(message: InboxMessage): { agent?: string; state: StrokeState } {
  const state = messageState(message);
  const failed = message.parts.some((p) => p.type === "failure" && !p.resolution);
  return {
    agent: message.from.kind === "agent" ? message.from.name : undefined,
    state: state !== "open" ? "done" : failed ? "failed" : openParts(message).length > 0 ? "waiting" : "idle",
  };
}

/**
 * agent-ui's mailbox with the platform's own pieces on it: the parts it draws
 * itself, the agent's edge on what an agent sent, and everything a message is
 * about as things to open.
 */
export function MailboxView({
  messages,
  selectedId,
  onSelect,
  onAction,
  onOpen,
  viewer,
  toolbar,
  listEnd,
  openCount,
  show,
  onShow,
}: {
  /** Open, resolved or all, when the host filters on the server. Leave out to let the mailbox filter what it is given. */
  show?: MailboxShow;
  onShow?: (show: MailboxShow) => void;
  /** Open messages across the whole inbox, when more exist than are loaded. */
  openCount?: number;
  messages: PlatformMessage[];
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  onAction: (action: UiAction) => void;
  onOpen: (source: Source) => void;
  viewer?: string;
  toolbar?: ReactNode;
  listEnd?: ReactNode;
}) {
  const renderers = useMemo(() => platformParts(onOpen), [onOpen]);
  return (
    <Mailbox
      className="min-h-0 flex-1"
      messages={messages}
      selectedId={selectedId}
      onSelect={onSelect}
      onAction={onAction}
      viewer={viewer}
      renderers={renderers}
      toolbar={toolbar}
      listEnd={listEnd}
      openCount={openCount}
      show={show}
      onShow={onShow}
      renderFrame={(message, children) => {
        const edge = edgeOf(message);
        const sources = (message as PlatformMessage).about ?? [];
        return (
          <Stroke agent={edge.agent} state={edge.state} testId="mail-frame" className="flex min-h-0 flex-1 flex-col pl-4">
            {children}
            {sources.length > 0 && (
              <ul className="flex shrink-0 flex-wrap gap-x-4 gap-y-1 border-[var(--oap-rule)] border-t pt-2 text-aui-xs" aria-label="About">
                {sources.map((s) => (
                  <li key={`${s.kind}:${s.id}`}>
                    <button
                      type="button"
                      onClick={() => onOpen(s)}
                      className="rounded-aui-sm text-aui-muted underline underline-offset-2 hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent"
                    >
                      {s.label}
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </Stroke>
        );
      }}
    />
  );
}

/**
 * The inbox. Alerts, failed runs, memory to review and limits that were
 * reached all arrive here as messages, and are resolved here where they can
 * be. The mailbox is agent-ui's. This page loads it from the platform, keeps
 * it current, and sends each click back.
 */
export function InboxPage({
  signal,
  viewer,
  openId,
  onOpen,
  onCounts,
  via = platform,
}: {
  /** Changes whenever the inbox may have. The first page is fetched again. */
  signal: unknown;
  /** Who is reading, shown on what they resolve until the platform confirms it. */
  viewer?: string;
  /** A message to open first, when the page was reached from a link to one. */
  openId?: string | null;
  /** Open what a message is about: a run, a rule, an agent, a project. */
  onOpen: (source: Source) => void;
  /** Reports the open and unread totals, for the sidebar's badge. */
  onCounts?: (counts: { open: number; unread: number }) => void;
  via?: Transport;
}) {
  const [kind, setKind] = useState("");
  const [show, setShow] = useState<MailboxShow>("open");
  const [selected, setSelected] = useState<string | null>(() => openId ?? linked());
  // The message being read, held apart from the list. A linked message may not be on a
  // loaded page, and one just resolved leaves the open list while its record is still
  // being read. Either way it stays on screen until another is chosen.
  const [held, setHeld] = useState<PlatformMessage | null>(null);
  const [missing, setMissing] = useState(false);
  const inbox = useInbox(kind, show, signal, via, viewer);

  const found = inbox.messages.find((m) => m.id === selected);
  const listed = found !== undefined;
  const messages = held && held.id === selected && !listed ? [held, ...inbox.messages] : inbox.messages;

  useEffect(() => {
    if (found) setHeld(found);
  }, [found]);

  useEffect(() => {
    if (openId) setSelected(openId);
  }, [openId]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: fetch once per selection that the list does not hold
  useEffect(() => {
    setMissing(false);
    if (!selected || inbox.loading === "first" || listed || held?.id === selected) return;
    let live = true;
    via
      .get(selected)
      .then((m) => live && setHeld(m))
      .catch(() => live && setMissing(true));
    return () => {
      live = false;
    };
  }, [selected, inbox.loading]);

  useEffect(() => link(selected), [selected]);

  useEffect(() => () => link(null), []);

  const { open, unread } = inbox;
  // biome-ignore lint/correctness/useExhaustiveDependencies: report when the totals change, not when the callback does
  useEffect(() => {
    onCounts?.({ open, unread });
  }, [open, unread]);

  function onAction(action: UiAction) {
    const message = messages.find((m) => m.id === action.payload.turnId);
    if (!message) return;
    // The message being read is kept in step even when the list no longer holds it.
    inbox.act(message, action).then((server) => {
      if (server) setHeld((was) => (was?.id === server.id ? server : was));
    });
    if (action.id === ActionId.dismiss && selected === message.id) setSelected(null);
  }

  return (
    <div data-testid="inbox" className="oap-fade flex min-h-0 flex-1 flex-col gap-3 px-5 pt-8 pb-4 md:px-10">
      <p className="sr-only" aria-live="polite">
        {open === 0 ? "Nothing is waiting on you." : `${open} open, ${unread} unread.`}
      </p>
      <Problem message={inbox.problem} />
      {selected && missing && <Problem message="That message is not here. It may have been removed, or you may not have access to it." />}

      {inbox.failed ? (
        <Empty title="The inbox could not be loaded">It will be tried again when something changes. Reload the page to try now.</Empty>
      ) : inbox.loading === "first" && messages.length === 0 ? (
        <SkeletonRows count={5} height="3rem" className="max-w-md" />
      ) : (
        <MailboxView
          messages={messages}
          selectedId={selected}
          onSelect={setSelected}
          onAction={onAction}
          onOpen={onOpen}
          viewer={viewer}
          openCount={open}
          show={show}
          onShow={setShow}
          toolbar={
            <Menu
              quiet
              label="Kind"
              value={kind}
              options={[{ value: "", label: "Every kind" }, ...kinds.map((k) => ({ value: k.id, label: k.label }))]}
              onChange={setKind}
            />
          }
          listEnd={
            (inbox.hasMore || inbox.loading === "more") && (
              <div className="flex justify-center py-3">
                <button
                  type="button"
                  onClick={inbox.more}
                  disabled={inbox.loading === "more"}
                  className="inline-flex h-6 items-center rounded-full border border-[var(--oap-rule)] px-2.5 text-aui-xs text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent disabled:cursor-default"
                >
                  {inbox.loading === "more" ? "Loading" : inbox.moreFailed ? "Could not load. Try again" : "See more"}
                </button>
              </div>
            )
          }
        />
      )}
    </div>
  );
}
