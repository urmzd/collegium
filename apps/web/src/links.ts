/**
 * Addresses into the console. The console is one page that reads its
 * address: `?view=` names the screen, and `message` or `alert` the one item
 * open on it. Every link to an item is built here, so a link and what the
 * console opens for it cannot disagree.
 */
import type { Alert } from "./api.ts";

/** A console address: `/?view=...` on this console, or under `base` (its public URL) when given. */
export function consoleUrl(view: string, extra: Record<string, string> = {}, base?: string): string {
  const path = `/?${new URLSearchParams({ view, ...extra })}`;
  return base ? `${base.replace(/\/+$/, "")}${path}` : path;
}

/** One inbox message: the inbox, with that message open. */
export const inboxItemUrl = (id: string, base?: string) => consoleUrl("inbox", { message: id }, base);

/** One alert: the Alerts page, with that alert open. */
export const alertUrl = (id: string, base?: string) => consoleUrl("alerts", { alert: id }, base);

/**
 * The alert a link names, from the alerts this person was given. The list is
 * the tenant's own, so an id from anywhere else finds nothing: null means not
 * found, and the page says so rather than showing something else.
 */
export function alertNamed(alerts: Alert[], id: string | null): Alert | null {
  if (!id) return null;
  return alerts.find((a) => a.id === id) ?? null;
}
