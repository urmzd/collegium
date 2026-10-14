/**
 * Everything this app asks of Slack: Web API calls, and a Socket Mode
 * connection that delivers events without a public URL.
 *
 * The Web API client follows the one in vultus's Slack sandbox: JSON bodies,
 * typed errors, and waiting out a 429 for as long as Slack says.
 */

export class SlackError extends Error {
  constructor(
    readonly method: string,
    readonly code: string,
    readonly detail?: unknown,
  ) {
    super(`${method}: ${code}${detail ? ` ${JSON.stringify(detail)}` : ""}`);
  }
}

export interface SlackApi {
  call<T = Record<string, unknown>>(method: string, args?: Record<string, unknown>): Promise<T>;
  /** Answer a slash command or a shortcut through the address Slack gave for it. */
  respond(url: string, body: Record<string, unknown>): Promise<void>;
}

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** Only Slack's own hooks are ever posted to, whatever a payload says. */
function slackHook(url: string): boolean {
  try {
    const u = new URL(url);
    return u.protocol === "https:" && u.hostname === "hooks.slack.com";
  } catch {
    return false;
  }
}

export function slackClient(
  token: string,
  opts: { fetch?: typeof fetch; retries?: number } = {},
): SlackApi {
  const call = opts.fetch ?? fetch;
  return {
    async call(method, args = {}) {
      for (let attempt = 0; ; attempt++) {
        const res = await call(`https://slack.com/api/${method}`, {
          method: "POST",
          headers: {
            "content-type": "application/json; charset=utf-8",
            authorization: `Bearer ${token}`,
          },
          body: JSON.stringify(args),
        });
        if (res.status === 429 && attempt < (opts.retries ?? 3)) {
          await wait(Number(res.headers.get("retry-after") ?? "1") * 1000);
          continue;
        }
        const body = (await res.json()) as { ok: boolean; error?: string } & Record<
          string,
          unknown
        >;
        if (!body.ok) {
          throw new SlackError(
            method,
            body.error ?? `http_${res.status}`,
            body.response_metadata ?? body.errors,
          );
        }
        return body as never;
      }
    },
    async respond(url, body) {
      if (!slackHook(url)) return;
      await call(url, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(body),
      });
    },
  };
}

/** One delivery from Slack over Socket Mode, already acknowledged. */
export interface Envelope {
  type: string;
  envelope_id?: string;
  payload?: Record<string, unknown>;
  retry_attempt?: number;
  retry_reason?: string;
}

/**
 * Hold a Socket Mode connection open and hand every delivery to `handle`.
 * Each is acknowledged before it is handled, because Slack sends again
 * anything not acknowledged within three seconds. A closed socket is reopened.
 */
export async function socketMode(
  app: SlackApi,
  handle: (envelope: Envelope) => Promise<void>,
  opts: { log?: (line: string) => void; signal?: AbortSignal } = {},
): Promise<void> {
  const log = opts.log ?? (() => {});
  const connect = async () => {
    if (opts.signal?.aborted) return;
    const { url } = await app.call<{ url: string }>("apps.connections.open");
    const ws = new WebSocket(url);
    opts.signal?.addEventListener("abort", () => ws.close(), { once: true });
    ws.onopen = () => log("connected to Slack");
    ws.onmessage = (message) => {
      let envelope: Envelope;
      try {
        envelope = JSON.parse(String(message.data)) as Envelope;
      } catch {
        return;
      }
      if (envelope.envelope_id) ws.send(JSON.stringify({ envelope_id: envelope.envelope_id }));
      if (envelope.type === "disconnect") return ws.close();
      handle(envelope).catch((e: Error) => log(`handler failed: ${e.message}`));
    };
    ws.onclose = () => {
      if (opts.signal?.aborted) return;
      log("socket closed, reconnecting");
      again();
    };
  };
  /** Keep trying: Slack being unreachable for a while must not end the app. */
  const again = () => {
    setTimeout(() => {
      connect().catch((e: Error) => {
        log(`reconnect failed: ${e.message}`);
        again();
      });
    }, 1000);
  };
  await connect();
}
