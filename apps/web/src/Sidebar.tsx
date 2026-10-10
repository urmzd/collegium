import { BotIcon, ClockIcon, MessageIcon } from "@agent-ui/render-web";
import type { ReactNode, SVGProps } from "react";
import type { Me, Preferences, ThreadSummary } from "./api.ts";
import { Avatar } from "./Settings.tsx";
import { AgentMark } from "./ui.tsx";

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
export const PanelLeftIcon = PanelIcon;
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
  projects,
  project,
  onProject,
  inboxCount,
  alertCount,
  onSignOut,
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
  projects: string[];
  /** The project the recents are narrowed to. Empty shows every conversation. */
  project: string;
  onProject: (name: string) => void;
  inboxCount: number;
  alertCount: number;
  onSignOut: () => void;
}) {
  const who = me.principal;
  const dev = who.subject === "dev";
  const name = prefs?.display_name || who.name || who.email || who.subject;
  // Recents is what people asked for. Scheduled and eval runs live on their own
  // pages, so a timer cannot push a person's work off the list.
  const recents = threads
    .filter((t) => t.origin === "api" && (!project || t.project === project))
    .slice(0, 40);

  return (
    <aside
      aria-label="Navigation"
      className={`oap-glass-bar flex shrink-0 flex-col border-aui-border border-r transition-[width] duration-200 motion-reduce:transition-none ${
        open ? "w-64" : "w-12"
      }`}
    >
      <div className={`flex h-12 items-center ${open ? "justify-between pr-2 pl-3.5" : "justify-center"}`}>
        {open && <span className="oap-wordmark truncate text-aui-sm font-semibold">Open Agent Platform</span>}
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

      <nav aria-label="Sections" className={`flex flex-col gap-0.5 ${open ? "px-2" : "items-center"}`}>
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

      {open && can("runs:read") ? (
        <div className="flex min-h-0 flex-1 flex-col pt-4">
          <div className="flex items-center justify-between gap-2 px-4 pb-1">
            <p className="text-aui-xs text-aui-muted">Recents</p>
            {projects.length > 0 && (
              <select
                aria-label="Show conversations in"
                value={project}
                onChange={(e) => onProject(e.target.value)}
                className="h-6 max-w-28 rounded-aui border border-aui-border bg-transparent px-1 text-aui-2xs text-aui-muted outline-none focus-visible:border-aui-accent"
              >
                <option value="">All projects</option>
                {projects.map((p) => (
                  <option key={p}>{p}</option>
                ))}
              </select>
            )}
          </div>
          <ul className="aui-scroll min-h-0 flex-1 overflow-y-auto px-2">
            {recents.length === 0 && (
              <li className="px-2.5 py-1.5 text-aui-xs text-aui-muted">
                {project ? "Nothing filed here yet." : "Your conversations appear here."}
              </li>
            )}
            {recents.map((t) => (
              <li key={t.id}>
                <button
                  type="button"
                  onClick={() => onSelect(t.id)}
                  aria-current={view === "chat" && t.id === selected ? "page" : undefined}
                  className="flex h-8 w-full items-center gap-2 rounded-aui px-2 text-left text-aui-sm text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-[current=page]:bg-aui-hover aria-[current=page]:text-aui-fg"
                >
                  <AgentMark name={t.agents[t.agents.length - 1] ?? "?"} size={16} />
                  <span className="min-w-0 flex-1 truncate">{t.title}</span>
                  {t.active > 0 ? (
                    <span className="shrink-0 text-aui-2xs">Running</span>
                  ) : t.failed > 0 ? (
                    <span className="shrink-0 text-aui-2xs">Failed</span>
                  ) : null}
                </button>
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <div className="flex-1" />
      )}

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
