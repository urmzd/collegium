/**
 * Everything this app asks of collegium, and a hand-written client for it.
 *
 * The rest of the app depends on the `Collegium` interface only. `httpCollegium`
 * is a stand-in for the client generated from the platform's OpenAPI document:
 * when that client lands, this file is the one that changes.
 */
import {
  type BusEvent,
  type Http,
  mailTransport,
  type Run,
  type Transport,
} from "@collegium/adapter";

export type { BusEvent, Run };

/** Who the platform says is calling, as `GET /api/me` answers. */
export interface Me {
  principal: { subject: string; tenant: string; kind: "user" | "machine"; name?: string };
  permissions: string[];
  first_admin?: boolean;
}

export interface Agent {
  name: string;
  description?: string;
}

/** An agent revision, as `GET /api/agents` lists them. Only the fields this app reads. */
export interface Revision {
  spec: Agent;
  rev?: number;
}

/** The part of a client record this app reads: who answers, and who may. */
export interface ClientRecord {
  id: string;
  surface: string;
  enabled: boolean;
  default_agent?: string;
  /** Agents the client may route to besides its default. `*` means any. */
  agents?: string[];
  identity?: { team?: string; app_id?: string };
}

/**
 * The body of `POST /api/runs`, and nothing more: the route refuses fields it
 * does not know, so who asked in Slack, the client and the thread cannot be
 * sent. See the README for what the API would need.
 */
export interface RunRequest {
  /** Who should answer. Empty lets the platform pick. */
  agent?: string;
  text: string;
  /** The turn this one follows. Left out, it starts a conversation. */
  after?: string;
  project?: string;
}

/** Every call takes an optional signal, as the generated client's methods do. */
export interface CallOptions {
  signal?: AbortSignal;
}

/** The inbox changed. It names a message and nothing more. */
export interface InboxNotice {
  id: string;
  kind: string;
  state: string;
  updated_at: string;
}

export type StreamItem =
  | { type: "event"; event: BusEvent }
  | { type: "inbox"; notice: InboxNotice };

/** A limit that turned a run away, as the platform reports it. */
export interface Refusal {
  limit?: string;
  remaining?: number;
  resets_at?: string;
  policy?: { scope?: { kind: string; name?: string }; version?: number; period?: string };
}

export class CollegiumError extends Error {
  constructor(
    readonly status: number,
    message: string,
    /** Set when a limit refused the run (429 or 402). */
    readonly refusal?: Refusal,
  ) {
    super(message);
  }
}

/**
 * What this app asks of collegium. The names and shapes follow the generated
 * `@collegium/client` (`createClient`), so swapping it in is mechanical. Where
 * this interface answers null for a 404, the generated client throws: the
 * swap wraps those three calls.
 */
export interface Collegium {
  getMe(options?: CallOptions): Promise<Me>;
  listAgents(options?: CallOptions): Promise<{ agents: Revision[]; builtin?: string[] }>;
  /** One client record, or null when it does not exist for this caller. */
  getClient(id: string, options?: CallOptions): Promise<ClientRecord | null>;
  /** Start a conversation or continue one. The key goes only in the `Idempotency-Key` header. */
  createRun(body: RunRequest, idempotencyKey?: string, options?: CallOptions): Promise<Run>;
  /** A run's record, or null when it does not exist for this caller. */
  getRun(id: string, options?: CallOptions): Promise<Run | null>;
  /** Every turn of a conversation, or null when there is no such conversation. */
  getThread(id: string, options?: CallOptions): Promise<{ turns: Run[] } | null>;
  stopRun(id: string, options?: CallOptions): Promise<Run>;
  /**
   * A run's events as they happen, from its first. The stream also carries
   * inbox notices for the whole workspace. It ends when the signal aborts.
   */
  runEvents(
    run: { agent: string; id: string },
    options: CallOptions & { from?: number },
  ): AsyncIterable<StreamItem>;
  /** Inbox notices alone, for as long as the signal allows. */
  notices(options: CallOptions): AsyncIterable<InboxNotice>;
  /**
   * The inbox, through the shared adapter's transport: `listInbox`,
   * `getInboxMessage` and `resolveInboxPart` (plus read, dismiss and snooze)
   * in the generated client.
   */
  inbox: Transport;
}

/** Where a bearer token comes from. Called before every request, so it may refresh. */
export type TokenSource = (opts?: { fresh?: boolean }) => Promise<string | undefined>;

/** A token that never changes, or none at all for a loopback server in dev auth. */
export const staticToken =
  (token?: string): TokenSource =>
  async () =>
    token || undefined;

/**
 * An OAuth client-credentials grant, which is how a machine gets a collegium
 * token from the identity provider. The token is kept until a minute before it
 * expires, and asked for again when the platform refuses it.
 */
export function clientCredentials(cfg: {
  tokenUrl: string;
  clientId: string;
  clientSecret: string;
  scopes: string[];
  audience?: string;
  fetch?: typeof fetch;
  now?: () => number;
}): TokenSource {
  const now = cfg.now ?? Date.now;
  let held: { token: string; until: number } | undefined;
  return async ({ fresh } = {}) => {
    if (held && !fresh && now() < held.until) return held.token;
    const body = new URLSearchParams({ grant_type: "client_credentials" });
    if (cfg.scopes.length > 0) body.set("scope", cfg.scopes.join(" "));
    if (cfg.audience) body.set("audience", cfg.audience);
    const basic = Buffer.from(`${cfg.clientId}:${cfg.clientSecret}`).toString("base64");
    const res = await (cfg.fetch ?? fetch)(cfg.tokenUrl, {
      method: "POST",
      headers: {
        authorization: `Basic ${basic}`,
        "content-type": "application/x-www-form-urlencoded",
      },
      body,
    });
    const data = (await res.json().catch(() => ({}))) as {
      access_token?: string;
      expires_in?: number;
    };
    if (!res.ok || !data.access_token) {
      // The provider's own words may carry the request back, so they are not repeated.
      throw new CollegiumError(res.status, "the identity provider did not issue a token");
    }
    held = { token: data.access_token, until: now() + ((data.expires_in ?? 300) - 60) * 1000 };
    return held.token;
  };
}

const enc = encodeURIComponent;

/**
 * Split a server-sent event stream into frames. A frame is the lines up to a
 * blank one: `event:` names it, `data:` lines are its body, and a line that
 * starts with a colon is a comment the server sends to keep the stream open.
 */
export async function* frames(
  body: ReadableStream<Uint8Array>,
): AsyncGenerator<{ event: string; data: string }> {
  const decoder = new TextDecoder();
  let buffer = "";
  const reader = body.getReader();
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) return;
      buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, "\n");
      for (let end = buffer.indexOf("\n\n"); end !== -1; end = buffer.indexOf("\n\n")) {
        const frame = buffer.slice(0, end);
        buffer = buffer.slice(end + 2);
        let event = "message";
        const data: string[] = [];
        for (const line of frame.split("\n")) {
          if (line.startsWith("event:")) event = line.slice(6).trim();
          else if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
        }
        if (data.length > 0) yield { event, data: data.join("\n") };
      }
    }
  } finally {
    reader.cancel().catch(() => {});
  }
}

const sleep = (ms: number, signal: AbortSignal) =>
  new Promise<void>((resolve) => {
    if (signal.aborted) return resolve();
    const done = () => {
      clearTimeout(timer);
      signal.removeEventListener("abort", done);
      resolve();
    };
    const timer = setTimeout(done, ms);
    signal.addEventListener("abort", done);
  });

/**
 * The stand-in client, over fetch, against the routes in `internal/httpapi`.
 *
 * | Call | Route |
 * |------|-------|
 * | getMe | `GET /api/me` |
 * | listAgents | `GET /api/agents` |
 * | getClient | `GET /api/clients/{id}` |
 * | createRun | `POST /api/runs` with an `Idempotency-Key` header |
 * | getRun | `GET /api/runs/{id}` |
 * | getThread | `GET /api/threads/{id}` |
 * | runEvents, notices | `GET /api/events?topic=` as server-sent events, resumed with `from` |
 * | stopRun | `POST /api/runs/{id}/stop` |
 * | inbox | `GET /api/inbox`, `GET /api/inbox/{id}`, `POST /api/inbox/{id}/read`, `/dismiss`, `/snooze`, `/parts/{part}/resolve` |
 */
export function httpCollegium(cfg: {
  baseUrl: string;
  token: TokenSource;
  fetch?: typeof fetch;
  /** How long to wait before reopening an event stream that ended. */
  reconnectMs?: number;
}): Collegium {
  const base = cfg.baseUrl.replace(/\/+$/, "");
  const call = cfg.fetch ?? fetch;

  const send = async (
    method: string,
    path: string,
    init: { json?: unknown; headers?: Record<string, string>; signal?: AbortSignal } = {},
  ): Promise<Response> => {
    const once = async (fresh: boolean) => {
      const token = await cfg.token({ fresh });
      return call(`${base}${path}`, {
        method,
        signal: init.signal,
        headers: {
          ...(token ? { authorization: `Bearer ${token}` } : {}),
          ...(init.json === undefined ? {} : { "content-type": "application/json" }),
          ...init.headers,
        },
        body: init.json === undefined ? undefined : JSON.stringify(init.json),
      });
    };
    const res = await once(false);
    // A token that expired between the check and the call is asked for once more.
    return res.status === 401 ? once(true) : res;
  };

  const http = async <T>(
    method: string,
    path: string,
    json?: unknown,
    headers?: Record<string, string>,
  ): Promise<T> => {
    const res = await send(method, path, { json, headers });
    if (res.status === 204) return undefined as T;
    const data = (await res.json().catch(() => ({}))) as { error?: string } & Refusal;
    if (!res.ok) {
      const refused = res.status === 429 || res.status === 402;
      throw new CollegiumError(
        res.status,
        data.error ?? `Request failed (${res.status})`,
        refused
          ? {
              limit: data.limit,
              remaining: data.remaining,
              resets_at: data.resets_at,
              policy: data.policy,
            }
          : undefined,
      );
    }
    return data as T;
  };

  /** Null for what does not exist to this caller, which is also what another tenant's object answers. */
  const orNull = async <T>(work: Promise<T>): Promise<T | null> => {
    try {
      return await work;
    } catch (e) {
      if (e instanceof CollegiumError && e.status === 404) return null;
      throw e;
    }
  };

  /**
   * One topic's stream, reopened whenever it ends. Every open replays what the
   * bus still holds, so run events are told apart by their sequence number and
   * each is handed on once.
   */
  async function* stream(
    topic: string,
    signal: AbortSignal,
    from?: number,
  ): AsyncGenerator<StreamItem> {
    const seen = new Map<string, number>();
    while (!signal.aborted) {
      try {
        const query = `topic=${enc(topic)}${from === undefined ? "" : `&from=${from}`}`;
        const res = await send("GET", `/api/events?${query}`, {
          signal,
          headers: { accept: "text/event-stream" },
        });
        if (!res.ok || !res.body) {
          throw new CollegiumError(res.status, `the event stream answered ${res.status}`);
        }
        for await (const frame of frames(res.body)) {
          let parsed: unknown;
          try {
            parsed = JSON.parse(frame.data);
          } catch {
            // A frame that is not JSON is ignored rather than fatal.
            continue;
          }
          if (frame.event === "inbox") {
            yield { type: "inbox", notice: parsed as InboxNotice };
            continue;
          }
          const event = parsed as BusEvent;
          if ((seen.get(event.run_id) ?? -1) >= event.seq) continue;
          seen.set(event.run_id, event.seq);
          yield { type: "event", event };
        }
      } catch (e) {
        if (signal.aborted) return;
        // Not found and not allowed do not get better by asking again.
        if (e instanceof CollegiumError && [400, 401, 403, 404].includes(e.status)) throw e;
      }
      await sleep(cfg.reconnectMs ?? 1000, signal);
    }
  }

  const never = new AbortController().signal;
  return {
    getMe: () => http<Me>("GET", "/api/me"),
    listAgents: () => http<{ agents: Revision[]; builtin?: string[] }>("GET", "/api/agents"),
    getClient: (id) => orNull(http<ClientRecord>("GET", `/api/clients/${enc(id)}`)),
    createRun: (body, key) =>
      http<Run>("POST", "/api/runs", body, key ? { "Idempotency-Key": key } : undefined),
    getRun: (id) => orNull(http<Run>("GET", `/api/runs/${enc(id)}`)),
    getThread: (id) => orNull(http<{ turns: Run[] }>("GET", `/api/threads/${enc(id)}`)),
    stopRun: (id) => http<Run>("POST", `/api/runs/${enc(id)}/stop`),
    runEvents: (run, options) =>
      stream(`agent:${run.agent}:${run.id}`, options.signal ?? never, options.from),
    notices: async function* (options) {
      // No agent can be named this, so the topic carries no run and the stream is the inbox's alone.
      for await (const item of stream("agent:_inbox", options.signal ?? never)) {
        if (item.type === "inbox") yield item.notice;
      }
    },
    inbox: mailTransport(http as Http),
  };
}
