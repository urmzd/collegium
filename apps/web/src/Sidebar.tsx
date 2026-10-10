import { BotIcon, ClockIcon, MessageIcon } from "@agent-ui/render-web";
import type { ReactNode, SVGProps } from "react";
import type { Run } from "./adapter.ts";
import type { Me } from "./api.ts";

export type View =
  | "new"
  | "run"
  | "inbox"
  | "runs"
  | "agents"
  | "knowledge"
  | "schedules"
  | "analytics"
  | "alerts";

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
const LogOutIcon = glyph(
  <>
    <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
    <path d="m16 17 5-5-5-5M21 12H9" />
  </>,
);

const dot: Record<Run["status"], string> = {
  accepted: "bg-aui-neutral",
  running: "bg-aui-info",
  parked: "bg-aui-warn",
  completed: "bg-aui-ok",
  failed: "bg-aui-bad",
};
const word: Record<Run["status"], string> = {
  accepted: "queued",
  running: "running",
  parked: "waiting",
  completed: "done",
  failed: "failed",
};

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
      className={`relative flex h-8 items-center gap-2.5 rounded-aui text-aui-sm text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-[current=page]:bg-aui-surface aria-[current=page]:text-aui-fg ${
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
    <p className="px-2.5 pt-4 pb-1 text-aui-2xs font-medium uppercase tracking-wide text-aui-muted">{children}</p>
  ) : (
    <span aria-hidden className="my-2 h-px w-5 bg-aui-border" />
  );
}

/**
 * The console's navigation. Open, it shows labels and recent runs. Closed, it
 * is a rail of icons. The choice is the caller's to persist.
 */
export function Sidebar({
  open,
  onToggle,
  view,
  onView,
  me,
  can,
  runs,
  selected,
  onSelect,
  inboxCount,
  alertCount,
  live,
  onSignOut,
}: {
  open: boolean;
  onToggle: () => void;
  view: View;
  onView: (v: View) => void;
  me: Me;
  can: (permission: string) => boolean;
  runs: Run[];
  selected: string | null;
  onSelect: (id: string) => void;
  inboxCount: number;
  alertCount: number;
  live: boolean;
  onSignOut: () => void;
}) {
  const who = me.principal;
  const dev = who.subject === "dev";
  const name = who.name || who.email || who.subject;
  // Recents is what people asked for. Scheduled runs live under Schedules and
  // every run is under Runs, so a timer cannot push a person's work off the list.
  const recents = runs.filter((r) => !r.schedule && r.origin !== "cron").slice(0, 30);

  return (
    <aside
      aria-label="Navigation"
      className={`flex shrink-0 flex-col border-aui-border border-r bg-aui-bg transition-[width] duration-200 motion-reduce:transition-none ${
        open ? "w-64" : "w-12"
      }`}
    >
      <div className={`flex h-12 items-center ${open ? "justify-between pr-2 pl-3.5" : "justify-center"}`}>
        {open && <span className="truncate text-aui-sm font-medium text-aui-fg">Open Agent Platform</span>}
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
          <Item icon={<PlusIcon />} label="New Run" open={open} active={view === "new"} onClick={() => onView("new")} />
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
          <Item icon={<BotIcon />} label="Agents" open={open} active={view === "agents"} onClick={() => onView("agents")} />
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
          <p className="px-4 pb-1 text-aui-2xs font-medium uppercase tracking-wide text-aui-muted">Recents</p>
          <ul className="aui-scroll min-h-0 flex-1 overflow-y-auto px-2">
            {recents.length === 0 && <li className="px-2.5 py-1.5 text-aui-xs text-aui-muted">Nothing you started yet.</li>}
            {recents.map((r) => (
              <li key={r.id}>
                <button
                  type="button"
                  onClick={() => onSelect(r.id)}
                  aria-current={view === "run" && r.id === selected ? "page" : undefined}
                  className="flex h-8 w-full items-center gap-2 rounded-aui px-2.5 text-left text-aui-sm text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-[current=page]:bg-aui-surface aria-[current=page]:text-aui-fg"
                >
                  <span aria-hidden className={`size-1.5 shrink-0 rounded-full ${dot[r.status]}`} />
                  <span className="min-w-0 flex-1 truncate">{r.text || r.agent}</span>
                  <span className="sr-only">, {word[r.status]}</span>
                  <span className="shrink-0 text-aui-2xs text-aui-muted">{r.agent}</span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      ) : (
        <div className="flex-1" />
      )}

      <div className={`flex items-center gap-2 border-aui-border border-t py-2 ${open ? "px-3" : "flex-col px-0"}`}>
        <span
          role="img"
          aria-label={live ? "Live" : "Reconnecting"}
          title={live ? "Live" : "Reconnecting"}
          className={`size-1.5 shrink-0 rounded-full ${live ? "bg-aui-ok" : "bg-aui-warn"}`}
        />
        {open && (
          <span className="min-w-0 flex-1">
            <span className="block truncate text-aui-sm text-aui-fg">{name}</span>
            <span className="block truncate text-aui-2xs text-aui-muted">
              {who.tenant}
              {who.roles?.length ? ` · ${who.roles.join(", ")}` : ""}
              {dev ? " · dev auth" : ""}
            </span>
          </span>
        )}
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
