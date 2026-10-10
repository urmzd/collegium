import { BotIcon, CheckIcon, ClockIcon, MessageIcon } from "@agent-ui/render-web";
import { type ReactNode, type SVGProps, useState } from "react";
import type { Me, Preferences, ThreadSummary } from "./api.ts";
import { Avatar } from "./Settings.tsx";
import { type Standing, useRecents } from "./recents.ts";
import { AgentMark, Popover, SkeletonRows } from "./ui.tsx";

export type View =
  | "chat"
  | "inbox"
  | "runs"
  | "agents"
  | "agent"
  | "knowledge"
  | "schedules"
  | "analytics"
  | "alerts"
  | "project"
  | "storage"
  | "settings";

// Glyphs agent-ui does not ship. Same grid and stroke as its set.
function glyph(d: ReactNode) {
  return function Glyph({ size = 16, ...rest }: SVGProps<SVGSVGElement> & { size?: number }) {
    return (
      <svg
        aria-hidden="true"
        focusable="false"
        width={size}
        height={size}
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth={2}
        strokeLinecap="round"
        strokeLinejoin="round"
        className="shrink-0"
        {...rest}
      >
        {d}
      </svg>
    );
  };
}
const PlusIcon = glyph(<path d="M5 12h14M12 5v14" />);
const InboxIcon = glyph(
  <>
    <path d="M22 12h-6l-2 3h-4l-2-3H2" />
    <path d="M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z" />
  </>,
);
const PanelIcon = glyph(
  <>
    <rect x="3" y="3" width="18" height="18" rx="2" />
    <path d="M9 3v18" />
  </>,
);
const BookIcon = glyph(<path d="M4 19.5v-15A2.5 2.5 0 0 1 6.5 2H20v20H6.5a2.5 2.5 0 0 1 0-5H20" />);
const ChartIcon = glyph(<path d="M3 3v18h18M8 17v-5M13 17V8M18 17v-8" />);
const BellIcon = glyph(
  <>
    <path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9" />
    <path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" />
  </>,
);
const BoxIcon = glyph(
  <>
    <path d="M21 8v13H3V8M1 3h22v5H1z" />
    <path d="M10 12h4" />
  </>,
);
const FilterIcon = glyph(<path d="M3 5h18M7 12h10M10 19h4" />);
const LogOutIcon = glyph(
  <>
    <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
    <path d="m16 17 5-5-5-5M21 12H9" />
  </>,
);

function Item({
  icon,
  label,
  active,
  open,
  badge,
  onClick,
}: {
  icon: ReactNode;
  label: string;
  active: boolean;
  open: boolean;
  badge?: number;
  onClick: () => void;
}) {
  const count = badge && badge > 0 ? badge : 0;
  return (
    <button
      type="button"
      onClick={onClick}
      aria-current={active ? "page" : undefined}
      aria-label={count ? `${label}, ${count} need you` : label}
      title={open ? undefined : label}
      className={`relative flex h-8 items-center gap-2.5 rounded-aui text-aui-sm text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-[current=page]:bg-aui-hover aria-[current=page]:text-aui-fg ${
        open ? "px-2.5" : "w-8 justify-center"
      }`}
    >
      {icon}
      {open && <span className="flex-1 truncate text-left">{label}</span>}
      {count > 0 &&
        (open ? (
          <span className="rounded-full bg-aui-accent-soft px-1.5 text-aui-2xs font-medium text-aui-fg">{count}</span>
        ) : (
          <span aria-hidden className="absolute top-1 right-1 size-1.5 rounded-full bg-aui-accent" />
        ))}
    </button>
  );
}

/** One group in the filter panel: a title, then choices where exactly one is on. */
function Choices<T extends string>({
  title,
  value,
  options,
  onChange,
}: {
  title: string;
  value: T;
  options: { value: T; label: string; mark?: ReactNode }[];
  onChange: (next: T) => void;
}) {
  return (
    <fieldset className="flex flex-col pb-1">
      <legend className="px-2.5 pt-2 pb-1 text-aui-xs text-aui-muted">{title}</legend>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={o.value === value}
          onClick={() => onChange(o.value)}
          className="flex h-8 items-center gap-2 rounded-aui px-2.5 text-left text-aui-sm text-aui-fg transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent"
        >
          {o.mark}
          <span className="min-w-0 flex-1 truncate">{o.label}</span>
          {o.value === value && <CheckIcon size={14} />}
        </button>
      ))}
    </fieldset>
  );
}

/** A group label when open, a hairline when the sidebar is a rail. */
function Heading({ open, children }: { open: boolean; children: ReactNode }) {
  return open ? (
    <p className="px-2.5 pt-4 pb-1 text-aui-xs text-aui-muted">{children}</p>
  ) : (
    <span aria-hidden className="my-2 h-px w-5 bg-aui-border" />
  );
}

/**
 * The console's navigation. Open, it shows labels and recent conversations.
 * Closed, it is a rail of icons. The choice is the caller's to persist.
 */
export function Sidebar({
  open,
  onToggle,
  view,
  onView,
  onNewChat,
  me,
  prefs,
  can,
  threads,
  selected,
  onSelect,
  inboxCount,
  alertCount,
  live,
  onSignOut,
  projectsSlot,
}: {
  open: boolean;
  onToggle: () => void;
  view: View;
  onView: (v: View) => void;
  onNewChat: () => void;
  me: Me;
  prefs: Preferences | null;
  can: (permission: string) => boolean;
  threads: ThreadSummary[];
  selected: string | null;
  onSelect: (id: string) => void;
  inboxCount: number;
  alertCount: number;
  /** Updates are arriving. When false, the sidebar says so. */
  live: boolean;
  onSignOut: () => void;
  /** Drawn between the sections and Recents, in both the open and the rail state. It handles its own collapsed look. */
  projectsSlot?: ReactNode;
}) {
  const who = me.principal;
  const dev = who.subject === "dev";
  const name = prefs?.display_name || who.name || who.email || who.subject;
  // Recents is what people asked for. Scheduled and eval runs live on their own
  // pages, so a timer cannot push a person's work off the list.
  const [standing, setStanding] = useState<Standing>("");
  const [agent, setAgent] = useState("");
  const answering = [...new Set(threads.filter((t) => t.origin === "api").flatMap((t) => t.agents))].sort();
  // The list pages itself. The caller's threads change when a run starts or
  // ends, which is the signal to fetch the first page again.
  const { threads: recents, hasMore, loading, failed, stalled, retry, more } = useRecents(agent, standing, threads);
  const filters = (agent ? 1 : 0) + (standing ? 1 : 0);
  const clear = () => {
    setStanding("");
    setAgent("");
  };

  return (
    <aside
      aria-label="Navigation"
      className={`oap-glass-bar flex shrink-0 flex-col border-[var(--oap-rule)] border-r transition-[width] duration-200 motion-reduce:transition-none ${
        open ? "w-64" : "w-12"
      }`}
    >
      <div className={`flex h-12 items-center ${open ? "justify-between pr-2 pl-3.5" : "justify-center"}`}>
        {open && <span className="oap-display truncate text-aui-base font-semibold text-aui-fg">Open Agent Platform</span>}
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={open}
          aria-label={open ? "Close sidebar" : "Open sidebar"}
          className="inline-flex size-8 items-center justify-center rounded-aui text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent"
        >
          <PanelIcon />
        </button>
      </div>

      {/* Everything between the top row and the account scrolls as one, so a short window never squeezes Recents into a sliver. */}
      <div className="aui-scroll flex min-h-0 flex-1 flex-col overflow-x-hidden overflow-y-auto">
      <nav aria-label="Sections" className={`flex shrink-0 flex-col gap-0.5 ${open ? "px-2" : "items-center"}`}>
        {can("runs:write") && (
          <Item icon={<PlusIcon />} label="New Chat" open={open} active={view === "chat" && !selected} onClick={onNewChat} />
        )}
        {can("runs:read") && (
          <>
            <Item
              icon={<InboxIcon />}
              label="Inbox"
              open={open}
              active={view === "inbox"}
              badge={inboxCount}
              onClick={() => onView("inbox")}
            />
            <Item icon={<MessageIcon />} label="Runs" open={open} active={view === "runs"} onClick={() => onView("runs")} />
          </>
        )}

        <Heading open={open}>Build</Heading>
        {can("agents:read") && (
          <Item
            icon={<BotIcon />}
            label="Agents"
            open={open}
            active={view === "agents" || view === "agent"}
            onClick={() => onView("agents")}
          />
        )}
        {can("knowledge:read") && (
          <Item
            icon={<BookIcon />}
            label="Knowledge"
            open={open}
            active={view === "knowledge"}
            onClick={() => onView("knowledge")}
          />
        )}
        {can("artifacts:read") && (
          <Item icon={<BoxIcon />} label="Storage" open={open} active={view === "storage"} onClick={() => onView("storage")} />
        )}
        {can("schedules:read") && (
          <Item
            icon={<ClockIcon />}
            label="Schedules"
            open={open}
            active={view === "schedules"}
            onClick={() => onView("schedules")}
          />
        )}

        <Heading open={open}>Observe</Heading>
        {can("runs:read") && (
          <Item
            icon={<ChartIcon />}
            label="Analytics"
            open={open}
            active={view === "analytics"}
            onClick={() => onView("analytics")}
          />
        )}
        {can("alerts:read") && (
          <Item
            icon={<BellIcon />}
            label="Alerts"
            open={open}
            active={view === "alerts"}
            badge={alertCount}
            onClick={() => onView("alerts")}
          />
        )}
      </nav>

      {projectsSlot}

      {open && can("runs:read") && (
        <div className="flex shrink-0 flex-col pt-3">
          {/* The label stays in reach while the list scrolls under it. */}
          <div className="sticky top-0 z-10 flex items-center justify-between gap-1 bg-[var(--oap-plane)] py-1 pr-2 pl-2">
            <p className="px-2 text-aui-xs text-aui-muted">Recents</p>
            <Popover
              narrow
              align="right"
              label={filters > 0 ? `Filter conversations, ${filters} on` : "Filter conversations"}
              className="relative inline-flex size-6 items-center justify-center rounded-aui text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-expanded:bg-aui-hover aria-expanded:text-aui-fg"
              button={
                <>
                  <FilterIcon size={14} />
                  {filters > 0 && <span aria-hidden className="absolute top-0.5 right-0.5 size-1.5 rounded-full bg-aui-accent" />}
                </>
              }
            >
              {() => (
                <div className="aui-scroll flex max-h-[60vh] flex-col overflow-y-auto">
                  <Choices
                    title="Show"
                    value={standing}
                    options={[
                      { value: "", label: "Every conversation" },
                      { value: "running", label: "Running now" },
                      { value: "failed", label: "With a failed reply" },
                    ]}
                    onChange={setStanding}
                  />
                  {answering.length > 1 && (
                    <Choices
                      title="Answered by"
                      value={agent}
                      options={[
                        { value: "", label: "Any agent" },
                        ...answering.map((a) => ({ value: a, label: a, mark: <AgentMark name={a} size={12} /> })),
                      ]}
                      onChange={setAgent}
                    />
                  )}
                  {filters > 0 && (
                    <button
                      type="button"
                      onClick={clear}
                      className="mt-1 rounded-aui border-[var(--oap-rule)] border-t px-2.5 py-2 text-left text-aui-sm text-aui-fg hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent"
                    >
                      Clear filters
                    </button>
                  )}
                </div>
              )}
            </Popover>
          </div>
          <ul className="px-2 pb-3" aria-label="Conversations" aria-busy={loading !== null}>
            {recents.length === 0 && loading === "first" && (
              <li className="px-2.5">
                <SkeletonRows count={5} height="1.25rem" />
              </li>
            )}
            {recents.length === 0 && loading !== "first" && stalled && (
              <li className="flex flex-col items-start gap-1.5 px-2.5 py-1.5 text-aui-xs text-aui-muted">
                Your conversations could not be loaded.
                <button
                  type="button"
                  onClick={retry}
                  className="oap-press inline-flex h-6 items-center rounded-full border border-[var(--oap-rule)] px-2.5 text-aui-fg hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent"
                >
                  Try Again
                </button>
              </li>
            )}
            {recents.length === 0 && loading !== "first" && !stalled && (
              <li className="px-2.5 py-1.5 text-aui-xs text-aui-muted">
                {filters > 0
                    ? "No conversation matches these filters."
                    : "Your conversations appear here."}
              </li>
            )}
            {recents.map((t) => (
              <li key={t.id} className="oap-enter">
                <button
                  type="button"
                  onClick={() => onSelect(t.id)}
                  aria-current={view === "chat" && t.id === selected ? "page" : undefined}
                  className="flex h-8 w-full items-center gap-2 rounded-aui px-2 text-left text-aui-sm text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-[current=page]:bg-aui-hover aria-[current=page]:text-aui-fg"
                >
                  {/* The orb of the agent that last answered: moving while it works, red when a reply failed. */}
                  <AgentMark
                    name={t.agents[t.agents.length - 1] ?? "?"}
                    size={12}
                    state={t.active > 0 ? "thinking" : t.failed > 0 ? "error" : "idle"}
                  />
                  <span className="min-w-0 flex-1 truncate">{t.title}</span>
                  {t.active > 0 && <span className="sr-only">, running</span>}
                  {t.active === 0 && t.failed > 0 && <span className="sr-only">, a reply failed</span>}
                </button>
              </li>
            ))}
            {/* Older conversations arrive a page at a time, when asked for. */}
            {(hasMore || loading === "more") && (
              <li className="flex justify-center pt-2">
                <button
                  type="button"
                  onClick={more}
                  disabled={loading === "more"}
                  className="inline-flex h-6 items-center rounded-full border border-[var(--oap-rule)] px-2.5 text-aui-xs text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent disabled:cursor-default"
                >
                  {loading === "more" ? <span className="aui-shimmer">Loading</span> : failed ? "Could not load. Try again" : "See more"}
                </button>
              </li>
            )}
          </ul>
        </div>
      )}
      </div>

      {!live && open && <p className="px-4 pb-2 text-aui-2xs text-aui-muted">Reconnecting. What you see may be out of date.</p>}
      <div className={`flex items-center gap-1 border-aui-border border-t p-1.5 ${open ? "" : "flex-col"}`}>
        <button
          type="button"
          onClick={() => onView("settings")}
          aria-current={view === "settings" ? "page" : undefined}
          aria-label={`Settings for ${name}`}
          title={open ? undefined : "Settings"}
          className={`flex min-w-0 items-center gap-2 rounded-aui p-1 text-left transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent aria-[current=page]:bg-aui-hover ${
            open ? "flex-1" : ""
          }`}
        >
          <Avatar name={name} prefs={prefs} size={26} />
          {open && (
            <span className="min-w-0 flex-1">
              <span className="block truncate text-aui-sm text-aui-fg">{name}</span>
              <span className="block truncate text-aui-2xs text-aui-muted">
                {dev ? "Dev mode, no sign-in" : (who.roles ?? []).join(", ") || who.tenant}
              </span>
            </span>
          )}
        </button>
        {!dev && (
          <button
            type="button"
            onClick={onSignOut}
            aria-label="Sign out"
            title="Sign out"
            className="inline-flex size-8 shrink-0 items-center justify-center rounded-aui text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent"
          >
            <LogOutIcon />
          </button>
        )}
      </div>
    </aside>
  );
}
