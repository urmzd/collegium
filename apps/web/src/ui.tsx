import { type ReactNode, useEffect, useId, useRef, useState } from "react";
import type { Run } from "./adapter.ts";

export const field =
  "w-full rounded-aui border border-aui-border bg-aui-bg px-2.5 py-1.5 text-aui-sm text-aui-fg outline-none placeholder:text-aui-muted focus-visible:border-aui-accent focus-visible:ring-2 focus-visible:ring-aui-ring";

export const button =
  "inline-flex h-8 items-center justify-center gap-1.5 rounded-aui border border-aui-border bg-aui-surface px-3 text-aui-sm text-aui-fg transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent disabled:cursor-not-allowed disabled:opacity-50";

export const primary =
  "inline-flex h-8 items-center justify-center gap-1.5 rounded-aui bg-aui-primary px-3 text-aui-sm font-medium text-aui-primary-fg transition-opacity hover:opacity-90 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-aui-accent disabled:cursor-not-allowed disabled:opacity-50";

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
    <div className="flex flex-col items-center justify-center gap-1 px-6 py-16 text-center">
      <p className="text-aui-sm font-medium text-aui-fg">{title}</p>
      {children && <p className="max-w-sm text-aui-sm text-aui-muted">{children}</p>}
    </div>
  );
}

const statusTone: Record<Run["status"], string> = {
  accepted: "bg-aui-neutral",
  running: "bg-aui-info",
  parked: "bg-aui-warn",
  completed: "bg-aui-ok",
  failed: "bg-aui-bad",
};

const statusWord: Record<Run["status"], string> = {
  accepted: "Queued",
  running: "Running",
  parked: "Waiting",
  completed: "Done",
  failed: "Failed",
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

/** Micro-USD as dollars. Zero is an unmetered run, not a free one. */
export function usd(micro: number): string {
  if (micro === 0) return "Unmetered";
  return new Intl.NumberFormat(undefined, { style: "currency", currency: "USD" }).format(micro / 1_000_000);
}

/** A duration a person can read at a glance: 840ms, 4.2s, 3m 05s. */
export function took(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 1 : 0)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms % 60_000) / 1000);
  return `${m}m ${String(s).padStart(2, "0")}s`;
}

/** The last line of an error. Runtime errors nest, and the cause is at the end. */
export function cause(error: string | undefined): string {
  const lines = (error ?? "").split("\n").filter((l) => l.trim());
  return lines[lines.length - 1]?.trim() ?? "";
}

export function Page({
  title,
  lede,
  actions,
  wide,
  children,
}: {
  title: string;
  lede: string;
  actions?: ReactNode;
  wide?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="aui-scroll min-h-0 flex-1 overflow-y-auto">
      <div className={`mx-auto flex w-full flex-col gap-4 px-4 py-6 ${wide ? "max-w-5xl" : "max-w-3xl"}`}>
        <div className="flex flex-wrap items-start justify-between gap-3">
          {/* The top bar carries the page's title. This is the line under it. */}
          <p className="min-w-0 max-w-2xl text-aui-sm text-aui-muted" data-page={title}>
            {lede}
          </p>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </div>
        {children}
      </div>
    </div>
  );
}

export const card = "oap-glass rounded-aui-lg p-4";

/** A figure with its label. The value is ink, never a status color. */
export function Stat({ label, value, note }: { label: string; value: string; note?: string }) {
  return (
    <div className={`${card} flex flex-col gap-1`}>
      <span className="text-aui-xs text-aui-muted">{label}</span>
      <span className="text-aui-xl font-semibold text-aui-fg">{value}</span>
      {note && <span className="text-aui-2xs text-aui-muted">{note}</span>}
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
 * An agent's mark: its initial on a tile tinted with its own hue. The letter is
 * ink, so the mark reads without the color, and the color makes one agent
 * recognizable among many.
 */
export function AgentMark({ name, size = 20 }: { name: string; size?: number }) {
  const hue = hueOf(name);
  return (
    <span
      aria-hidden
      className="inline-flex shrink-0 items-center justify-center rounded-[28%] font-semibold text-aui-fg"
      style={{
        width: size,
        height: size,
        fontSize: Math.round(size * 0.52),
        background: `oklch(0.66 0.15 ${hue} / 26%)`,
        boxShadow: `inset 0 0 0 1px oklch(0.66 0.15 ${hue} / 55%)`,
      }}
    >
      {(name.trim()[0] ?? "?").toUpperCase()}
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
        className="oap-switch mt-0.5"
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
  children,
}: {
  label: string;
  button: ReactNode;
  className?: string;
  align?: "left" | "right";
  up?: boolean;
  children: (close: () => void) => ReactNode;
}) {
  const [open, setOpen] = useState(false);
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
      {open && (
        <div
          id={id}
          role="dialog"
          aria-label={label}
          className={`oap-glass-pop aui-pop-in absolute z-40 w-80 max-w-[calc(100vw-2rem)] rounded-aui-lg p-1.5 ${
            align === "right" ? "right-0" : "left-0"
          } ${up ? "bottom-full mb-2" : "top-full mt-2"}`}
        >
          {children(() => setOpen(false))}
        </div>
      )}
    </div>
  );
}
