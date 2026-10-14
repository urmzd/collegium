/**
 * The console's inbox transport. The model, the wire mapping and the routes are
 * shared with every other surface in `@collegium/adapter`. What is the
 * console's own is how it calls: same origin, with the session cookie.
 */
import { type Http, MailError, mailTransport, type Transport } from "@collegium/adapter";

export * from "@collegium/adapter/mail";

const http: Http = async <T>(method: string, path: string, json?: unknown): Promise<T> => {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: json === undefined ? undefined : { "Content-Type": "application/json" },
    body: json === undefined ? undefined : JSON.stringify(json),
  });
  if (res.status === 204) return undefined as T;
  const data = (await res.json().catch(() => ({}))) as { error?: string };
  if (!res.ok) throw new MailError(res.status, data.error ?? `Request failed (${res.status})`);
  return data as T;
};

export const transport: Transport = mailTransport(http);
