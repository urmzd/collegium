import type { ReactNode } from "react";
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
          <div className="min-w-0">
            <h1 className="text-aui-lg font-medium text-aui-fg">{title}</h1>
            <p className="max-w-2xl text-aui-sm text-aui-muted">{lede}</p>
          </div>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </div>
        {children}
      </div>
    </div>
  );
}

export const card = "rounded-aui-lg border border-aui-border bg-aui-surface p-4";

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
