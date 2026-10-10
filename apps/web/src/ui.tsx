import { BanIcon, CheckIcon, ChevronDownIcon, ShieldIcon, XIcon } from "@agent-ui/render-web";
import { AgentOrb, type OrbSize, type OrbState } from "@agent-ui/web";
import { type CSSProperties, type ReactNode, useEffect, useId, useRef, useState } from "react";
import type { Run } from "./adapter.ts";
import { cause, elapsed, took } from "./format.ts";

export const field =
  "w-full rounded-aui border border-aui-border bg-aui-bg px-2.5 py-1.5 text-aui-sm text-aui-fg outline-none placeholder:text-aui-muted focus-visible:border-aui-accent focus-visible:ring-2 focus-visible:ring-aui-ring";

export const button =
  "inline-flex h-8 items-center justify-center gap-1.5 collegium-press rounded-aui border border-aui-border bg-aui-surface px-3 text-aui-sm text-aui-fg hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent disabled:cursor-not-allowed disabled:opacity-50";

export const primary =
  "inline-flex h-8 items-center justify-center gap-1.5 collegium-press rounded-aui bg-aui-primary px-3 text-aui-sm font-medium text-aui-primary-fg hover:opacity-90 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-aui-accent disabled:cursor-not-allowed disabled:opacity-50";

export function Label({ text, hint, children }: { text: string; hint?: string; children: ReactNode }) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-aui-xs font-medium text-aui-muted">{text}</span>
      {children}
      {hint && <span className="text-aui-2xs text-aui-muted">{hint}</span>}
    </label>
  );
}

export function Problem({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <p role="alert" className="rounded-aui border border-aui-bad/40 bg-aui-bad/10 px-3 py-2 text-aui-sm text-aui-fg">
      {message}
    </p>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col gap-1 py-10">
      <p className="collegium-display text-aui-lg font-medium text-aui-fg">{title}</p>
      {children && <p className="max-w-md text-aui-sm text-aui-muted">{children}</p>}
    </div>
  );
}

const statusTone: Record<Run["status"], string> = {
  accepted: "bg-aui-neutral",
  running: "bg-aui-info",
  parked: "bg-aui-warn",
  completed: "bg-[var(--collegium-ink)]",
  failed: "bg-aui-bad",
  // Stopped from outside is not a failure, so it is not red.
  stopped: "bg-[var(--collegium-ink-faint)]",
};

const statusWord: Record<Run["status"], string> = {
  accepted: "Queued",
  running: "Running",
  parked: "Waiting",
  completed: "Done",
  failed: "Failed",
  stopped: "Stopped",
};

/** Status as a dot plus a word, so it never depends on color alone. */
export function Status({ status }: { status: Run["status"] }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-aui-xs text-aui-muted">
      <span aria-hidden className={`size-1.5 rounded-full ${statusTone[status]}`} />
      {statusWord[status]}
    </span>
  );
}

export function Chip({ children }: { children: ReactNode }) {
  return (
    <span className="inline-flex h-5 items-center rounded-full border border-aui-border px-2 text-aui-2xs text-aui-muted">
      {children}
    </span>
  );
}

const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ["day", 86_400],
  ["hour", 3_600],
  ["minute", 60],
];
const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });

export function ago(iso: string): string {
  const seconds = Math.round((Date.parse(iso) - Date.now()) / 1000);
  for (const [unit, size] of units) {
    if (Math.abs(seconds) >= size) return relative.format(Math.round(seconds / size), unit);
  }
  return "just now";
}

// Formatting with no React in it lives in format.ts, so plain modules and their tests can use it too.
export { cause, elapsed, isEnded, took, usd } from "./format.ts";

/**
 * Every page sits in the same left-aligned column, so the title and the first
 * row of content start at the same place wherever a person goes.
 */
export function Page({
  title,
  lede,
  actions,
  children,
}: {
  title: string;
  lede: string;
  actions?: ReactNode;
  /** Kept for callers. Pages share one width now. */
  wide?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="aui-scroll min-h-0 flex-1 overflow-y-auto">
      {/* A container, so rows lay themselves out by the room the page has, not by the window. */}
      <div className="@container collegium-fade flex w-full max-w-[68rem] flex-col gap-6 px-5 pt-8 pb-12 md:px-10">
        <header className="flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
          <div className="min-w-0">
            <h1 className="collegium-display text-[1.875rem] leading-[1.15] font-semibold text-aui-fg">{title}</h1>
            <p className="max-w-[62ch] pt-1.5 text-aui-sm text-aui-muted">{lede}</p>
          </div>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </header>
        {children}
      </div>
    </div>
  );
}

export const card = "collegium-glass rounded-aui-lg p-4";

/** A list of rows on hairlines, with no box around it. */
export const rows = "divide-y divide-[var(--collegium-rule)] border-[var(--collegium-rule)] border-y";

/** Figures side by side on one rule. Wrap a row of `Stat`s in it. */
export const figures =
  "grid grid-cols-2 gap-x-8 gap-y-5 border-[var(--collegium-rule)] border-y py-5 sm:grid-cols-3 lg:grid-cols-5";

/** A figure with its label. The value is ink, never a status color. */
export function Stat({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className="flex min-w-0 flex-col">
      <span className="text-aui-xs text-aui-muted">{label}</span>
      <span className="collegium-display pt-0.5 text-[1.75rem] leading-9 font-semibold text-aui-fg">{value}</span>
      {note && <span className="text-aui-2xs text-aui-muted">{note}</span>}
    </div>
  );
}

/** The longest run in a set, as the scale a tape is drawn against. */
export function longest(runs: Run[]): number {
  return runs.reduce((max, r) => Math.max(max, elapsed(r)), 0);
}

/** What a tape says, for anyone who cannot see it. */
function tally(runs: Run[]): string {
  if (runs.length === 0) return "No runs yet";
  const count = (s: Run["status"]) => runs.filter((r) => r.status === s).length;
  const parts = [`${runs.length} ${runs.length === 1 ? "run" : "runs"}`];
  for (const [status, word] of [
    ["failed", "failed"],
    ["running", "running"],
    ["parked", "waiting"],
  ] as const) {
    if (count(status) > 0) parts.push(`${count(status)} ${word}`);
  }
  return parts.join(", ");
}

/**
 * The run tape: the most recent runs, oldest on the left, one tick each. A
 * tick's height is how long the run took, on a log scale so a slow run stands
 * out without flattening the rest. Pass `onOpen` to make each tick open its run.
 */
export function Tape({
  runs,
  slots = 40,
  scale,
  tall,
  onOpen,
  className = "",
}: {
  /** Any order. The newest `slots` are drawn. */
  runs: Run[];
  slots?: number;
  /** The duration, in ms, that fills the tape. Share one across tapes to compare them. */
  scale?: number;
  tall?: boolean;
  onOpen?: (id: string) => void;
  className?: string;
}) {
  const shown = [...runs].sort((a, b) => a.created_at.localeCompare(b.created_at)).slice(-slots);
  const top = Math.log1p(Math.max(scale ?? longest(shown), 1));
  const blanks = Array.from({ length: Math.max(slots - shown.length, 0) }, (_, i) => i);
  const style = tall ? ({ "--collegium-tape-h": "2.75rem" } as React.CSSProperties) : undefined;
  return (
    <div
      role={onOpen ? "group" : "img"}
      aria-label={`Recent runs. ${tally(shown)}.`}
      className={`collegium-tape ${className}`}
      style={style}
    >
      {blanks.map((i) => (
        <span key={i} aria-hidden className="collegium-tick" data-status="none" />
      ))}
      {shown.map((r) => {
        const t = { "--t": Math.min(Math.log1p(elapsed(r)) / top, 1).toFixed(3) } as React.CSSProperties;
        const label = `${r.agent}, ${statusWord[r.status].toLowerCase()}, ${took(elapsed(r))}, ${ago(r.created_at)}`;
        return onOpen ? (
          <button
            key={r.id}
            type="button"
            className="collegium-tick"
            data-status={r.status}
            style={t}
            title={label}
            aria-label={`Open run: ${label}`}
            onClick={() => onOpen(r.id)}
          />
        ) : (
          <span key={r.id} aria-hidden className="collegium-tick" data-status={r.status} style={t} title={label} />
        );
      })}
    </div>
  );
}

/** A stable hue for a name, so an agent keeps its color everywhere it appears. */
export function hueOf(name: string): number {
  let h = 0;
  for (const c of name) h = (h * 31 + c.charCodeAt(0)) % 360;
  return h;
}

/**
 * Gives everything inside it an agent's color. agent-ui draws its accent from
 * one hue, so setting that hue here recolors the orb, and any accent, for this
 * agent alone.
 */
export function AgentTint({ name, className, children }: { name: string; className?: string; children: ReactNode }) {
  return (
    <div data-accent="agent" className={className} style={{ "--aui-accent-h": hueOf(name) } as CSSProperties}>
      {children}
    </div>
  );
}

const orbSize = (px: number): OrbSize => (px <= 14 ? "xs" : px <= 22 ? "sm" : px <= 36 ? "md" : "lg");

/**
 * An agent's mark is its orb, in its own color. The same orb sits beside its
 * answers, where it moves with what the agent is doing, so the mark in a list
 * and the presence in a conversation are one thing. At rest it is still.
 */
export function AgentMark({ name, size = 18, state = "idle" }: { name: string; size?: number; state?: OrbState }) {
  return (
    <span data-accent="agent" className="inline-flex shrink-0" style={{ "--aui-accent-h": hueOf(name) } as CSSProperties}>
      <AgentOrb state={state} size={orbSize(size)} label="" />
    </span>
  );
}

/** A labelled on/off choice. */
export function Switch({
  label,
  hint,
  checked,
  disabled,
  onChange,
}: {
  label: string;
  hint?: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (next: boolean) => void;
}) {
  return (
    <label className="flex items-start justify-between gap-4">
      <span className="min-w-0">
        <span className="block text-aui-sm text-aui-fg">{label}</span>
        {hint && <span className="block text-aui-xs text-aui-muted">{hint}</span>}
      </span>
      <input
        type="checkbox"
        className="collegium-switch mt-0.5"
        checked={checked}
        disabled={disabled}
        onChange={(e) => onChange(e.target.checked)}
      />
    </label>
  );
}

/** One of a few choices, shown together. */
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: { id: T; label: string }[];
  onChange: (next: T) => void;
}) {
  return (
    <fieldset className="inline-flex rounded-aui border border-aui-border p-0.5">
      <legend className="sr-only">{label}</legend>
      {options.map((o) => (
        <button
          key={o.id}
          type="button"
          aria-pressed={value === o.id}
          onClick={() => onChange(o.id)}
          className="h-7 rounded-aui-sm px-2.5 text-aui-xs text-aui-muted transition-colors hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-pressed:bg-aui-hover aria-pressed:text-aui-fg"
        >
          {o.label}
        </button>
      ))}
    </fieldset>
  );
}

/** Tabs for the sections of one thing. */
export function Tabs<T extends string>({
  label,
  value,
  tabs,
  onChange,
}: {
  label: string;
  value: T;
  tabs: { id: T; label: string; count?: number }[];
  onChange: (next: T) => void;
}) {
  return (
    <div role="tablist" aria-label={label} className="flex gap-1 border-aui-border border-b">
      {tabs.map((t) => (
        <button
          key={t.id}
          type="button"
          role="tab"
          aria-selected={value === t.id}
          onClick={() => onChange(t.id)}
          className="-mb-px inline-flex h-9 items-center gap-1.5 border-transparent border-b-2 px-3 text-aui-sm text-aui-muted transition-colors hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-selected:border-aui-accent aria-selected:text-aui-fg"
        >
          {t.label}
          {t.count !== undefined && t.count > 0 && (
            <span className="rounded-full bg-aui-hover px-1.5 text-aui-2xs tabular-nums text-aui-muted">{t.count}</span>
          )}
        </button>
      ))}
    </div>
  );
}

/**
 * A button that opens a small panel. It closes on Escape, on a click outside,
 * and when focus leaves it, and returns focus to the button.
 */
export function Popover({
  label,
  button: trigger,
  className,
  align = "left",
  up = false,
  narrow = false,
  children,
}: {
  label: string;
  button: ReactNode;
  className?: string;
  align?: "left" | "right";
  up?: boolean;
  /** A slimmer panel, for a button in a tight column such as the sidebar. */
  narrow?: boolean;
  children: (close: () => void) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const present = usePresence(open);
  const root = useRef<HTMLDivElement>(null);
  const id = useId();

  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [open]);

  return (
    <div
      ref={root}
      className="relative"
      onKeyDown={(e) => {
        if (e.key === "Escape" && open) {
          e.stopPropagation();
          setOpen(false);
          root.current?.querySelector<HTMLButtonElement>("button")?.focus();
        }
      }}
    >
      <button
        type="button"
        aria-label={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={() => setOpen(!open)}
        className={className}
      >
        {trigger}
      </button>
      {present.mounted && (
        <div
          id={id}
          role="dialog"
          aria-label={label}
          data-leaving={present.leaving}
          style={{ "--collegium-pop-origin": `${up ? "bottom" : "top"} ${align}` } as CSSProperties}
          className={`collegium-glass-pop collegium-pop absolute z-40 max-w-[calc(100vw-2rem)] rounded-aui-lg p-1.5 ${narrow ? "w-56" : "w-80"} ${
            align === "right" ? "right-0" : "left-0"
          } ${up ? "bottom-full mb-2" : "top-full mt-2"}`}
        >
          {children(() => setOpen(false))}
        </div>
      )}
    </div>
  );
}

function SearchIcon({ size = 14 }: { size?: number }) {
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
    >
      <circle cx="11" cy="11" r="7" />
      <path d="m20 20-3.5-3.5" />
    </svg>
  );
}

/** A search box that filters what is on the page as a person types. */
export function SearchField({
  label,
  value,
  onChange,
  placeholder,
  hint,
  inputRef,
  className = "",
}: {
  label: string;
  value: string;
  onChange: (next: string) => void;
  placeholder?: string;
  /** A shortcut shown while the box is empty, such as ⌘K. */
  hint?: string;
  inputRef?: React.Ref<HTMLInputElement>;
  className?: string;
}) {
  return (
    <div
      className={`flex h-8 items-center gap-2 rounded-aui border border-[var(--collegium-rule)] bg-aui-bg px-2.5 text-aui-muted transition-colors focus-within:border-aui-accent focus-within:text-aui-fg ${className}`}
    >
      <SearchIcon />
      <input
        ref={inputRef}
        type="search"
        aria-label={label}
        value={value}
        placeholder={placeholder ?? label}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Escape" && value) {
            e.stopPropagation();
            onChange("");
          }
        }}
        className="min-w-0 flex-1 bg-transparent text-aui-sm text-aui-fg outline-none placeholder:text-aui-muted [&::-webkit-search-cancel-button]:hidden"
      />
      {value ? (
        <button
          type="button"
          aria-label="Clear search"
          onClick={() => onChange("")}
          className="-mr-1 inline-flex size-5 items-center justify-center rounded-aui-sm text-aui-muted hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent"
        >
          <XIcon size={12} />
        </button>
      ) : (
        hint && <kbd className="font-aui-sans text-aui-2xs text-aui-muted">{hint}</kbd>
      )}
    </div>
  );
}

/**
 * Keeps something on screen for as long as its leaving takes. `mounted` says
 * whether to render it, and `leaving` is true for the moment between being
 * closed and being gone: set it as data-leaving so the motion classes play out.
 * The timer is cleared if the thing reopens or the component goes away.
 */
export function usePresence(open: boolean, ms = 110): { mounted: boolean; leaving: boolean } {
  const [held, setHeld] = useState(open);
  useEffect(() => {
    if (open) {
      setHeld(true);
      return;
    }
    const timer = window.setTimeout(() => setHeld(false), ms);
    return () => window.clearTimeout(timer);
  }, [open, ms]);
  return { mounted: open || held, leaving: !open && held };
}

/** Renders its children while `when` is true and for `ms` after, telling them when they are leaving. */
export function Presence({ when, ms, children }: { when: boolean; ms?: number; children: (leaving: boolean) => ReactNode }) {
  const { mounted, leaving } = usePresence(when, ms);
  return mounted ? children(leaving) : null;
}

export type Flash = { text: string; bad: boolean };

/**
 * A brief confirmation that clears itself. `say("Copied")` shows for `ms` and
 * goes. `say(text, true)` is a failure: it stays until the next `say` or
 * `clear`, because a person needs time to read what went wrong. The timer is
 * cleared when the component goes away.
 */
export function useFlash(ms = 1600): { flash: Flash | null; say: (text: string, bad?: boolean) => void; clear: () => void } {
  const [flash, setFlash] = useState<Flash | null>(null);
  useEffect(() => {
    if (!flash || flash.bad) return;
    const timer = window.setTimeout(() => setFlash(null), ms);
    return () => window.clearTimeout(timer);
  }, [flash, ms]);
  return { flash, say: (text, bad = false) => setFlash({ text, bad }), clear: () => setFlash(null) };
}

/**
 * The note a `useFlash` shows, floating just above the control it belongs to.
 * It takes no room, so nothing beside the control moves, and the control keeps
 * its own label. Put it inside a `relative` wrapper. It is a live region, so
 * the confirmation is announced as well as seen.
 */
export function FlashNote({ flash, align = "center" }: { flash: Flash | null; align?: "center" | "left" | "right" }) {
  const present = usePresence(flash !== null);
  // Keep the words while the note leaves.
  const last = useRef<Flash | null>(flash);
  if (flash) last.current = flash;
  const shown = flash ?? last.current;
  const place = align === "center" ? "left-1/2 -translate-x-1/2" : align === "left" ? "left-0" : "right-0";
  return (
    <span role="status" aria-live="polite" className={`pointer-events-none absolute bottom-full z-50 mb-1.5 ${place}`}>
      {present.mounted && shown && (
        <span
          data-testid="flash-note"
          data-bad={shown.bad}
          data-leaving={present.leaving}
          className="collegium-note collegium-glass-pop flex items-center gap-1.5 whitespace-nowrap rounded-aui px-2 py-1 text-aui-xs text-aui-fg"
        >
          {/* A mark as well as the words, so a small notice beside a bare icon still reads as a result. */}
          {shown.bad ? <XIcon size={12} className="text-aui-bad" /> : <CheckIcon size={12} className="text-aui-ok" />}
          {shown.text}
        </span>
      )}
    </span>
  );
}

/**
 * Rows that stand in for a list while it loads, at the height the real rows
 * will have, so nothing jumps when they arrive. Says "Loading" to a screen
 * reader. The shimmer stops when motion is reduced.
 */
export function SkeletonRows({ count = 4, height = "2rem", className = "" }: { count?: number; height?: string; className?: string }) {
  return (
    <div role="status" data-testid="skeleton" className={`flex flex-col gap-2 py-2 ${className}`}>
      <span className="sr-only">Loading</span>
      {Array.from({ length: count }, (_, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: placeholders have no identity
        <span key={i} aria-hidden className="aui-skeleton block" style={{ height, width: `${92 - ((i * 17) % 40)}%` }} />
      ))}
    </div>
  );
}

export type Option<T extends string = string> = {
  value: T;
  label: string;
  /** A second line that says what choosing this does. */
  hint?: string;
  /** Drawn before the label, such as an agent's mark. */
  mark?: ReactNode;
};

/**
 * A menu of choices, used wherever a person picks one thing from a list. It
 * opens toward the side with more room, moves with the arrow keys, jumps to a
 * choice as its name is typed, and grows a filter when the list is long.
 */
export function Menu<T extends string>({
  label,
  value,
  options,
  onChange,
  disabled,
  quiet,
  shows,
  header,
  align = "left",
  className = "",
}: {
  label: string;
  value: T;
  /** A line above the choices that says what they act on. */
  header?: ReactNode;
  options: Option<T>[];
  onChange: (next: T) => void;
  disabled?: boolean;
  /** Text only, for a menu that sits in a line of other controls. */
  quiet?: boolean;
  /** What the button says, when it is not the chosen option's label. */
  shows?: ReactNode;
  align?: "left" | "right";
  className?: string;
}) {
  const [open, setOpen] = useState(false);
  const present = usePresence(open);
  const [up, setUp] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const root = useRef<HTMLDivElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const filter = useRef<HTMLInputElement>(null);
  const typed = useRef({ text: "", at: 0 });
  const id = useId();

  const searchable = options.length > 8;
  const q = query.trim().toLowerCase();
  const shown = q ? options.filter((o) => `${o.label} ${o.hint ?? ""}`.toLowerCase().includes(q)) : options;
  const chosen = options.find((o) => o.value === value);

  const show = () => {
    const box = root.current?.getBoundingClientRect();
    if (box) {
      const below = window.innerHeight - box.bottom;
      setUp(below < 300 && box.top > below);
    }
    setQuery("");
    setActive(Math.max(options.findIndex((o) => o.value === value), 0));
    setOpen(true);
  };
  const hide = (refocus = true) => {
    setOpen(false);
    if (refocus) root.current?.querySelector<HTMLButtonElement>("button")?.focus();
  };
  const pick = (o: Option<T> | undefined) => {
    if (!o) return;
    hide();
    if (o.value !== value || shows !== undefined) onChange(o.value);
  };

  useEffect(() => {
    if (!open) return;
    (searchable ? filter.current : list.current)?.focus();
    const away = (e: MouseEvent) => {
      if (root.current && !root.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [open, searchable]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: scroll follows the highlighted row
  useEffect(() => {
    if (open) list.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: "nearest" });
  }, [open, active]);

  const onKey = (e: React.KeyboardEvent) => {
    const last = shown.length - 1;
    if (e.key === "ArrowDown") setActive((a) => Math.min(a + 1, last));
    else if (e.key === "ArrowUp") setActive((a) => Math.max(a - 1, 0));
    else if (e.key === "Home") setActive(0);
    else if (e.key === "End") setActive(last);
    else if (e.key === "Enter" || (e.key === " " && !searchable)) pick(shown[active]);
    else if (e.key === "Escape") {
      e.stopPropagation();
      hide();
    } else if (e.key === "Tab") {
      hide(false);
      return;
    } else {
      // Typing a name jumps to it, as a native menu does.
      if (!searchable && e.key.length === 1 && !e.metaKey && !e.ctrlKey) {
        const now = Date.now();
        typed.current = { text: (now - typed.current.at > 600 ? "" : typed.current.text) + e.key.toLowerCase(), at: now };
        const hit = shown.findIndex((o) => o.label.toLowerCase().startsWith(typed.current.text));
        if (hit >= 0) setActive(hit);
      }
      return;
    }
    e.preventDefault();
  };

  return (
    <div ref={root} className={`relative ${quiet ? "inline-flex" : "flex"} ${className}`}>
      <button
        type="button"
        aria-label={`${label}: ${chosen?.label ?? "none"}`}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        disabled={disabled}
        onClick={() => (open ? hide() : show())}
        onKeyDown={(e) => {
          if (!open && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
            e.preventDefault();
            show();
          }
        }}
        className={
          quiet
            ? "inline-flex h-7 max-w-60 items-center gap-1 rounded-aui px-1.5 text-aui-sm text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent disabled:cursor-not-allowed disabled:opacity-50 aria-expanded:bg-aui-hover aria-expanded:text-aui-fg"
            : "flex h-8 w-full min-w-0 items-center gap-2 rounded-aui border border-aui-border bg-aui-bg px-2.5 text-left text-aui-sm text-aui-fg transition-colors hover:border-aui-ring focus-visible:border-aui-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-aui-ring disabled:cursor-not-allowed disabled:opacity-50 aria-expanded:border-aui-accent"
        }
      >
        {shows === undefined && chosen?.mark}
        <span className="min-w-0 flex-1 truncate">{shows ?? chosen?.label ?? "Choose"}</span>
        <ChevronDownIcon size={14} className={`text-aui-muted transition-transform motion-reduce:transition-none ${open ? "rotate-180" : ""}`} />
      </button>
      {present.mounted && (
        // biome-ignore lint/a11y/noStaticElementInteractions: keys are handled here for the filter and the list together
        <div
          onKeyDown={onKey}
          data-leaving={present.leaving}
          style={{ "--collegium-pop-origin": `${up ? "bottom" : "top"} ${align}` } as CSSProperties}
          className={`collegium-glass-pop collegium-pop absolute z-40 flex max-h-80 min-w-full max-w-[min(22rem,calc(100vw-2rem))] flex-col rounded-aui-lg p-1 ${
            quiet ? "w-max min-w-44" : ""
          } ${align === "right" ? "right-0" : "left-0"} ${up ? "bottom-full mb-1.5" : "top-full mt-1.5"}`}
        >
          {header && <div className="shrink-0 border-[var(--collegium-rule)] border-b px-2.5 pt-1.5 pb-2 text-aui-xs text-aui-muted">{header}</div>}
          {searchable && (
            <SearchField
              label={`Filter ${label.toLowerCase()}`}
              placeholder="Filter"
              value={query}
              inputRef={filter}
              onChange={(next) => {
                setQuery(next);
                setActive(0);
              }}
              className="mb-1 shrink-0"
            />
          )}
          <ul
            ref={list}
            id={id}
            role="listbox"
            aria-label={label}
            aria-activedescendant={shown[active] ? `${id}-${active}` : undefined}
            tabIndex={-1}
            className="aui-scroll min-h-0 overflow-y-auto outline-none"
          >
            {shown.length === 0 && <li className="px-2.5 py-2 text-aui-sm text-aui-muted">Nothing matches.</li>}
            {shown.map((o, i) => (
              // biome-ignore lint/a11y/useKeyWithClickEvents: the listbox handles keys for its options
              <li
                key={o.value}
                id={`${id}-${i}`}
                role="option"
                aria-selected={o.value === value}
                data-active={i === active}
                onMouseMove={() => setActive(i)}
                onClick={() => pick(o)}
                className="flex cursor-pointer items-center gap-2.5 rounded-aui px-2.5 py-1.5 text-aui-sm text-aui-fg data-[active=true]:bg-aui-hover"
              >
                {o.mark}
                <span className="min-w-0 flex-1">
                  <span className="block truncate">{o.label}</span>
                  {o.hint && <span className="block text-aui-xs text-aui-muted">{o.hint}</span>}
                </span>
                {o.value === value && shows === undefined && <CheckIcon size={14} className="text-aui-fg" />}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

/** The choices for a menu of agents, each with its mark. */
export function agentOptions(names: string[], none?: string): Option[] {
  const list: Option[] = names.map((n) => ({ value: n, label: n, mark: <AgentMark name={n} size={16} /> }));
  return none === undefined ? list : [{ value: "", label: none }, ...list];
}

export type StrokeState = "idle" | "working" | "waiting" | "failed" | "done";

/**
 * A surface known by its edge. With no `agent` the edge is the neutral rule,
 * which is right for anything a person or the system made. With an `agent` the
 * edge takes that agent's hue, and `state` says how the agent stands: working
 * breathes, waiting on a person is dashed and still, failed is red.
 */
export function Stroke({
  agent,
  state = "idle",
  edge = "start",
  className = "",
  testId = "stroke",
  children,
}: {
  /** The agent that made or owns what is inside. Leave out for people and the system. */
  agent?: string;
  state?: StrokeState;
  /** Where the line is drawn: the leading side, underneath, or all the way around. */
  edge?: "start" | "under" | "around";
  className?: string;
  testId?: string;
  children: ReactNode;
}) {
  return (
    <div
      data-testid={testId}
      data-agent={agent}
      data-state={agent ? state : undefined}
      data-edge={edge}
      className={`collegium-stroke ${className}`}
      style={agent ? ({ "--collegium-hue": hueOf(agent) } as CSSProperties) : undefined}
    >
      {children}
    </div>
  );
}

/** Which stroke state a run's status is. */
export function strokeOf(status: Run["status"]): StrokeState {
  // A stopped run is at rest: it is neither working nor a failure.
  return status === "failed" ? "failed" : status === "parked" ? "waiting" : status === "completed" ? "done" : status === "stopped" ? "idle" : "working";
}

/** Micro-USD as dollars, with zero shown as $0.00. For budgets, where zero is a real amount. */
export function dollars(micro: number): string {
  return new Intl.NumberFormat(undefined, { style: "currency", currency: "USD" }).format(micro / 1_000_000);
}

export type Period = "day" | "month";
export type UsageState = "none" | "under" | "warning" | "over";

/** Where spending stands against a limit. Warning starts at `warnAt`, a share of the limit. */
export function usageOf(spent: number, limit: number | null | undefined, warnAt = 0.8): UsageState {
  if (!limit || limit <= 0) return "none";
  return spent > limit ? "over" : spent >= limit * warnAt ? "warning" : "under";
}

const usageWord: Record<UsageState, string> = {
  none: "No limit",
  under: "Under limit",
  warning: "Near limit",
  over: "Over limit",
};

/** A formatter for a meter that counts things: `counted("runs")(12)` is "12 runs". */
export function counted(unit: string): (amount: number) => string {
  return (amount) => `${amount.toLocaleString()} ${unit}`;
}

/**
 * Use against a limit. The state is always written out, and going over is
 * drawn past the limit line, so nothing depends on color. Amounts are
 * micro-USD unless `format` says otherwise. Presentational: it fetches nothing.
 */
export function UsageMeter({
  label,
  spent,
  limit,
  warnAt = 0.8,
  projected,
  period,
  size = "row",
  labelled = false,
  format: show = dollars,
  testId = "usage-meter",
}: {
  /** What is being measured, such as a project's name. Read out, and shown at the header size. */
  label: string;
  spent: number;
  /** Leave out, or pass null or 0, when nothing caps the spending. */
  limit?: number | null;
  /** The share of the limit where the warning starts. */
  warnAt?: number;
  /** What the period is expected to end at, drawn as a lighter extension. */
  projected?: number;
  period?: Period;
  /** `row` fits a table cell. `header` leads a page. `mini` is the track alone, to sit inside another control. */
  size?: "row" | "header" | "mini";
  /** Show the label above a row-size meter. The header size always shows it. */
  labelled?: boolean;
  /** How an amount is written. Dollars from micro-USD unless given. Use `counted("runs")` for a plain count. */
  format?: (amount: number) => string;
  testId?: string;
}) {
  const state = usageOf(spent, limit, warnAt);
  const cap = limit && limit > 0 ? limit : 0;
  const ahead = projected !== undefined && projected > spent ? projected : 0;
  const scale = Math.max(cap, spent, ahead, 1);
  const pct = (v: number) => `${Math.min((100 * v) / scale, 100)}%`;
  const share = cap ? Math.round((100 * spent) / cap) : null;
  // Exactly at the limit is still the warning state, but "near" would be wrong.
  const word = cap > 0 && spent === cap ? "At limit" : usageWord[state];
  const each = period ? ` a ${period}` : "";
  const text = cap
    ? `${show(spent)} of ${show(cap)}${each}, ${share}%. ${state === "over" ? `Over limit by ${show(spent - cap)}` : word}.${
        ahead ? ` Heading for ${show(ahead)}.` : ""
      }`
    : `${show(spent)} used. No limit.`;
  const note =
    state === "over" ? `Over by ${show(spent - cap)}` : ahead && cap && ahead > cap ? `Heading for ${show(ahead)}` : word;
  const header = size === "header";

  if (size === "mini") {
    return (
      <span
        data-testid={testId}
        data-usage={state}
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={cap || Math.max(spent, 1)}
        aria-valuenow={Math.min(spent, cap || spent)}
        aria-valuetext={text}
        title={text}
        className="collegium-meter-track inline-block w-10 shrink-0"
        style={{ "--collegium-meter-h": "4px" } as CSSProperties}
      >
        {cap > 0 && <span className="collegium-meter-fill" style={{ width: pct(spent) }} />}
      </span>
    );
  }

  return (
    <div data-testid={testId} data-usage={state} className={`flex min-w-0 flex-col ${header ? "gap-2" : "gap-1"}`}>
      {header && (
        <div className="flex flex-wrap items-baseline justify-between gap-x-4">
          <span className="text-aui-xs text-aui-muted">{label}</span>
          <span className="text-aui-xs text-aui-muted">
            {cap ? `of ${show(cap)}${each}` : "No limit"}
          </span>
        </div>
      )}
      {!header && labelled && <span className="truncate text-aui-xs text-aui-muted">{label}</span>}
      <div className="flex items-baseline justify-between gap-3">
        <span className={`collegium-display min-w-0 truncate font-semibold text-aui-fg ${header ? "text-[1.75rem] leading-9" : "text-aui-sm"}`}>
          {show(spent)}
          {!header && cap > 0 && <span className="font-normal text-aui-muted"> of {show(cap)}</span>}
        </span>
        <span className={`shrink-0 text-aui-xs ${state === "over" || state === "warning" ? "font-medium text-aui-fg" : "text-aui-muted"}`}>
          {header && !cap ? "" : note}
        </span>
      </div>
      <div
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={cap || Math.max(spent, 1)}
        aria-valuenow={Math.min(spent, cap || spent)}
        aria-valuetext={text}
        className="collegium-meter-track"
        style={header ? ({ "--collegium-meter-h": "10px" } as CSSProperties) : undefined}
      >
        {ahead > 0 && <span data-testid={`${testId}-projected`} className="collegium-meter-projected" style={{ width: pct(ahead) }} />}
        {/* With no limit there is nothing to fill toward, so the track stays empty. */}
        {cap > 0 && <span className="collegium-meter-fill" style={{ width: pct(spent) }} />}
        {cap > 0 && warnAt > 0 && warnAt < 1 && <span aria-hidden className="collegium-meter-warn" style={{ left: pct(cap * warnAt) }} />}
        {cap > 0 && scale > cap && <span aria-hidden className="collegium-meter-limit" style={{ left: pct(cap) }} />}
      </div>
    </div>
  );
}

const periods: Option<Period>[] = [
  { value: "month", label: "a month" },
  { value: "day", label: "a day" },
];

/** Dollars typed by a person, as micro-USD. Null when empty, undefined when it is not an amount. */
export function parseDollars(text: string): number | null | undefined {
  const t = text.trim().replace(/^\$/, "").replace(/,/g, "");
  if (t === "") return null;
  if (!/^\d+(\.\d{0,2})?$|^\.\d{1,2}$/.test(t)) return undefined;
  return Math.round(Number(t) * 1_000_000);
}

/**
 * A spending limit: dollars, and the period they cover. The value is micro-USD,
 * or null for no limit of its own. When it is null and `inherits` is given, the
 * field says which limit applies instead.
 */
export function LimitField({
  label,
  value,
  period,
  onChange,
  inherits,
  disabled,
  testId = "limit-field",
}: {
  label: string;
  /** Micro-USD, or null when this scope sets no limit. */
  value: number | null;
  period: Period;
  onChange: (value: number | null, period: Period) => void;
  /** The limit that applies when this one is empty, and where it comes from. */
  inherits?: { scope: string; value: number; period: Period };
  disabled?: boolean;
  testId?: string;
}) {
  const shown = value === null ? "" : (value / 1_000_000).toFixed(2).replace(/\.00$/, "");
  const [text, setText] = useState(shown);
  const [bad, setBad] = useState(false);
  const id = useId();

  // Follow the value when it changes from outside, but not while it is being typed.
  const last = useRef(value);
  useEffect(() => {
    if (last.current !== value) {
      last.current = value;
      setText(shown);
      setBad(false);
    }
  }, [value, shown]);

  const type = (next: string) => {
    setText(next);
    const parsed = parseDollars(next);
    setBad(parsed === undefined);
    if (parsed !== undefined) {
      last.current = parsed;
      onChange(parsed, period);
    }
  };

  const hint = bad
    ? "Enter an amount in dollars, such as 250 or 49.50."
    : value !== null
      ? undefined
      : inherits
        ? `Inherits ${dollars(inherits.value)} a ${inherits.period} from ${inherits.scope}.`
        : "No limit.";

  return (
    <div data-testid={testId} data-empty={value === null} className="flex flex-col gap-1">
      <label htmlFor={id} className="text-aui-xs font-medium text-aui-muted">
        {label}
      </label>
      <div className="flex items-center gap-2">
        <div
          className={`flex h-8 min-w-0 flex-1 items-center gap-1 rounded-aui border bg-aui-bg px-2.5 transition-colors focus-within:border-aui-accent focus-within:ring-2 focus-within:ring-aui-ring ${
            bad ? "border-aui-bad" : "border-aui-border"
          } ${disabled ? "opacity-50" : ""}`}
        >
          <span aria-hidden className="text-aui-sm text-aui-muted">
            $
          </span>
          <input
            id={id}
            inputMode="decimal"
            autoComplete="off"
            disabled={disabled}
            value={text}
            placeholder={inherits ? (inherits.value / 1_000_000).toFixed(2).replace(/\.00$/, "") : "No limit"}
            aria-invalid={bad}
            aria-describedby={hint ? `${id}-hint` : undefined}
            onChange={(e) => type(e.target.value)}
            className="min-w-0 flex-1 bg-transparent text-aui-sm tabular-nums text-aui-fg outline-none placeholder:text-aui-muted disabled:cursor-not-allowed"
          />
        </div>
        <Menu label={`${label} period`} className="w-28 shrink-0" value={period} options={periods} disabled={disabled} onChange={(p) => onChange(value, p)} />
      </div>
      {hint && (
        <span id={`${id}-hint`} role={bad ? "alert" : undefined} className="text-aui-2xs text-aui-muted">
          {hint}
        </span>
      )}
    </div>
  );
}

export type PolicyAction = "block" | "approve";

const actionWord: Record<PolicyAction, string> = {
  block: "Blocks at the limit",
  approve: "Asks for approval",
};

/** What a policy covers and what it does when its limit is reached. */
export function PolicyBadge({
  scope,
  name,
  action,
  testId = "policy-badge",
}: {
  /** The kind of thing the policy covers, such as Workspace, Project or Agent. */
  scope: string;
  /** Which one, when the scope has many. */
  name?: string;
  action: PolicyAction;
  testId?: string;
}) {
  const Icon = action === "block" ? BanIcon : ShieldIcon;
  return (
    <span
      data-testid={testId}
      data-action={action}
      className="inline-flex h-6 max-w-full items-center gap-1.5 rounded-full border border-[var(--collegium-stroke)] pr-2.5 pl-2 text-aui-xs text-aui-muted"
    >
      <Icon size={12} />
      <span className="truncate">
        <span className="text-aui-fg">{name ? `${scope} ${name}` : scope}</span>, {actionWord[action].toLowerCase()}
      </span>
    </span>
  );
}
