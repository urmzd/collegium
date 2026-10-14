// Hand-written. Everything under ./generated is written by oag from
// api/openapi.yaml and must not be edited; this file is the thin layer over it
// for the things the generator does not do. Each is listed in docs/api.md.
import {
  ApiClient,
  ApiError,
  type ApiResponse,
  type RequestOptions,
  type RetryConfig,
} from "./generated/client.js";
import { SSEError, type SSEOptions } from "./generated/sse.js";
import type {
  ErrorBody,
  InboxNotice,
  ManifestEvent,
  Refusal,
  Run,
  RunEvent,
} from "./generated/types.js";

/** A bearer token, or a function that returns the current one. */
export type TokenSource = string | (() => string | Promise<string>);

/** How to reach a collegium server and who to be when asking. */
export interface CollegiumOptions {
  /**
   * Where the server is, such as `https://collegium.example.com`. Use `""` in
   * a browser page the server itself serves, so requests stay same-origin.
   */
  baseUrl: string;
  /**
   * A machine's access token, sent as `Authorization: Bearer`. Pass a function
   * to fetch or refresh it: it is called before every request.
   */
  token?: TokenSource;
  /**
   * Set to `"include"` or `"same-origin"` to authenticate with the browser's
   * session cookie instead of a token. Node's fetch has no cookie jar, so this
   * only means something in a browser.
   */
  credentials?: RequestCredentials;
  /** Headers sent with every request. */
  headers?: Record<string, string>;
  /** The fetch to use. Defaults to the global one. */
  fetch?: typeof globalThis.fetch;
  /**
   * Retry with backoff. Off by default, unlike the generated client: a
   * retried POST without an idempotency key can start a second run, and a
   * limit's refusal (429) is an answer to show, not a fault to wait out.
   */
  retry?: RetryConfig | false;
  /** Give up on a request after this many milliseconds. Not applied to streams. */
  timeout?: number;
}

type Kind = RunEvent["kind"];

/**
 * A run event with its payload decoded. The wire form carries the payload as
 * base64 JSON. This is the same union with `body` in its place, typed by kind
 * from the generated `ManifestEvent`, which is the decoded shape the server
 * itself sends in a run manifest.
 */
export type DecodedRunEvent = {
  [K in Kind]: Omit<Extract<RunEvent, { kind: K }>, "payload"> & {
    body?: Extract<ManifestEvent, { kind: K }>["body"];
  };
}[Kind];

/** How `runEvents` reconnects when the stream drops before the run is over. */
export interface ResumeOptions {
  /** Reconnections in a row without an event before giving up. Default 5. */
  maxRetries?: number;
  /** The first wait, doubled each time. Default 500 ms. */
  initialDelayMs?: number;
  /** The longest wait. Default 10 s. */
  maxDelayMs?: number;
}

const DEFAULT_RESUME: Required<ResumeOptions> = { maxRetries: 5, initialDelayMs: 500, maxDelayMs: 10_000 };

/**
 * `runEvents` could not follow a run to an event that ends it. `reason` says
 * why: `stopped` (the run was stopped, which publishes no ending event),
 * `ended` (its record says it is over and no ending event was found), or
 * `gave-up` (the stream kept dropping). `run` is the last record read.
 */
export class StreamEndedError extends Error {
  readonly reason: "stopped" | "ended" | "gave-up";
  readonly run: Run | undefined;
  constructor(reason: "stopped" | "ended" | "gave-up", message: string, run: Run | undefined, cause?: unknown) {
    super(message, { cause });
    this.name = "StreamEndedError";
    this.reason = reason;
    this.run = run;
  }
}

/** Whether a stream error is the connection going away, worth reconnecting after. */
function droppedStream(error: unknown): boolean {
  if (error instanceof SSEError || error instanceof ApiError) {
    const status = error.statusCode ?? 0;
    return status === 0 || status === 408 || status === 429 || status >= 500;
  }
  return !(error instanceof DOMException && (error.name === "AbortError" || error.name === "TimeoutError"));
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) return reject(signal.reason);
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(signal?.reason);
    };
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

/** One message from the event stream, told apart by `type`. */
export type StreamItem =
  | { type: "event"; event: DecodedRunEvent }
  | { type: "inbox"; notice: InboxNotice };

/** What a failed request said, whichever generated error class carried it. */
export interface ApiFailure {
  /** The HTTP status, or 0 when there was no response to read one from. */
  status: number;
  /** The server's own message when it sent one. */
  message: string;
  /** The response body: `{error}`, or a `Refusal` when a limit refused a run. */
  body: ErrorBody | Refusal | undefined;
  /** Set when a limit refused the run (429 or 402). */
  refusal?: Refusal;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null;
}

/** Whether an error body is a limit's refusal, with the limit and policy that refused. */
export function isRefusal(body: unknown): body is Refusal {
  return isRecord(body) && typeof body.error === "string" && typeof body.limit === "string" && typeof body.remaining === "number";
}

/**
 * Read the status and body out of an error thrown by this client. Returns
 * undefined for anything else, such as a network failure or an abort.
 */
export function failureOf(error: unknown): ApiFailure | undefined {
  if (!(error instanceof ApiError) && !(error instanceof SSEError)) return undefined;
  const body = isRecord(error.body) && typeof error.body.error === "string" ? (error.body as unknown as ErrorBody | Refusal) : undefined;
  return {
    status: error.statusCode ?? 0,
    message: body?.error ?? error.message,
    body,
    refusal: isRefusal(body) ? body : undefined,
  };
}

/** The topic for every agent, one agent, or one run of an agent. */
export function topicFor(agent?: string, run?: string): string {
  if (!agent) return "agent";
  return run ? `agent:${agent}:${run}` : `agent:${agent}`;
}

function decodeBase64(value: string): string {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return new TextDecoder().decode(bytes);
}

/**
 * Decode an event's payload. A payload that is absent, or is not JSON, leaves
 * `body` undefined rather than throwing: kinds are an open set, and a reader
 * should pass over what it does not know.
 */
export function decodeEvent(event: RunEvent): DecodedRunEvent {
  const { payload, ...rest } = event;
  let body: unknown;
  if (payload) {
    try {
      body = JSON.parse(decodeBase64(payload));
    } catch {
      body = undefined;
    }
  }
  return { ...rest, body } as DecodedRunEvent;
}

/**
 * Whether an event ends its run on the stream: it completed, or it failed and
 * will not be retried. A stopped run publishes no such event, so the run's
 * record (`getRun`) stays the authority on whether a run is over.
 */
export function endsRun(event: DecodedRunEvent): boolean {
  if (event.kind === "lifecycle.completed") return true;
  return event.kind === "lifecycle.failed" && event.body?.terminal === true;
}

/**
 * The collegium API client: every generated operation, plus the event stream
 * as typed items and downloads as real bytes.
 */
export class Collegium extends ApiClient {
  readonly #baseUrl: string;
  readonly #fetch: typeof globalThis.fetch;
  readonly #prepare: (req: { url: string; init: RequestInit }) => Promise<{ url: string; init: RequestInit }>;
  readonly #headers: Record<string, string>;
  readonly #timeout: number | undefined;

  constructor(options: CollegiumOptions) {
    const fetchFn = options.fetch ?? globalThis.fetch.bind(globalThis);
    const prepare = async (req: { url: string; init: RequestInit }) => {
      const headers = new Headers(req.init.headers);
      if (options.token !== undefined && !headers.has("Authorization")) {
        const token = typeof options.token === "function" ? await options.token() : options.token;
        headers.set("Authorization", `Bearer ${token}`);
      }
      const init: RequestInit = { ...req.init, headers: Object.fromEntries(headers) };
      if (options.credentials !== undefined) init.credentials = options.credentials;
      return { url: req.url, init };
    };
    super({
      baseUrl: options.baseUrl,
      headers: options.headers,
      fetch: fetchFn,
      requestInterceptor: prepare,
      retry: options.retry ?? false,
      timeout: options.timeout,
    });
    this.#baseUrl = options.baseUrl.replace(/\/$/, "");
    this.#fetch = fetchFn;
    this.#prepare = prepare;
    this.#headers = options.headers ?? {};
    this.#timeout = options.timeout;
  }

  /**
   * The event stream as typed items. A message with no name is a run event,
   * decoded. A message named `inbox` is a notice that an inbox message
   * changed.
   *
   * Leaving the loop in any way (`break`, `return`, a throw, the stream
   * ending) closes the request, and so does aborting `options.signal`.
   *
   * The server ends the stream when the reader falls behind, and this returns
   * when it does. `runEvents` resumes on its own; a reader of a wider topic
   * calls again.
   */
  async *events(params: { topic?: string; from?: number } = {}, options?: SSEOptions): AsyncGenerator<StreamItem, void, undefined> {
    const own = new AbortController();
    const signal = options?.signal ? AbortSignal.any([options.signal, own.signal]) : own.signal;
    try {
      for await (const message of this.streamEvents(params.topic, params.from, { retry: false, ...options, signal })) {
        if (message.event === "inbox") {
          yield { type: "inbox", notice: message.data as InboxNotice };
        } else if (message.event === undefined || message.event === "message") {
          yield { type: "event", event: decodeEvent(message.data as RunEvent) };
        }
      }
    } finally {
      // The generated reader only releases its lock, so leaving the loop left
      // the connection and the server's subscription open (urmzd/oag#35).
      // Aborting is what closes them. Remove once oag closes the body itself.
      own.abort();
    }
  }

  /**
   * One run's events, decoded, ending after the event that completes it or
   * fails it for good. It never ends quietly before that.
   *
   * When the stream drops (the server ends a reader that fell behind, a
   * restart, a proxy's timeout), it reads the run's record and, while the
   * run is not over, reconnects after the last `seq` it yielded, with
   * backoff. It throws `StreamEndedError` when it cannot resume, or when the
   * run's record says it ended with no event to say so, which is what a
   * stopped run does. An error the server answers with (403, 404) is thrown
   * as it is.
   *
   * The generated stream treats a dropped connection as a normal end
   * (urmzd/oag#35), which is why the resuming lives here.
   */
  async *runEvents(
    run: { agent: string; id: string },
    params: { from?: number; resume?: ResumeOptions } = {},
    options?: SSEOptions,
  ): AsyncGenerator<DecodedRunEvent, void, undefined> {
    const resume = { ...DEFAULT_RESUME, ...params.resume };
    let from = params.from;
    let failures = 0;
    let draining = false;
    let cause: unknown;
    let record: Run | undefined;
    for (;;) {
      try {
        for await (const item of this.events({ topic: topicFor(run.agent, run.id), from }, options)) {
          if (item.type !== "event" || item.event.run_id !== run.id) continue;
          if (from !== undefined && item.event.seq <= from) continue;
          from = item.event.seq;
          failures = 0;
          yield item.event;
          if (endsRun(item.event)) return;
        }
        cause = undefined;
      } catch (error) {
        if (options?.signal?.aborted || !droppedStream(error)) throw error;
        cause = error;
      }
      if (draining) {
        // The record said the run was over and a last read found no event
        // to end it.
        throw new StreamEndedError("ended", `run ${run.id} ended without an event that says so`, record, cause);
      }
      try {
        record = await this.getRun(run.id, { signal: options?.signal });
      } catch (error) {
        // A server that is restarting cannot answer either. That is one more
        // failed attempt, not the end.
        if (options?.signal?.aborted || !droppedStream(error)) throw error;
        cause = error;
        record = undefined;
      }
      if (record?.status === "stopped") {
        throw new StreamEndedError("stopped", `run ${run.id} was stopped`, record, cause);
      }
      if (record?.status === "completed" || record?.status === "failed") {
        // It ended while the stream was down. Its last events are read once
        // more, without waiting.
        draining = true;
        continue;
      }
      if (++failures > resume.maxRetries) {
        throw new StreamEndedError("gave-up", `the stream for run ${run.id} could not be resumed`, record, cause);
      }
      const delay = Math.min(resume.initialDelayMs * 2 ** (failures - 1), resume.maxDelayMs);
      await sleep(delay, options?.signal);
    }
  }

  // The generated client reads every response as text, which corrupts a
  // file. These two operations answer with bytes, so they are redone here
  // with the same signatures.

  override async getArtifactContent(id: string, inline?: "1", options?: RequestOptions): Promise<Blob> {
    return (await this.getArtifactContentRaw(id, inline, options)).data;
  }

  override getArtifactContentRaw(id: string, inline?: "1", options?: RequestOptions): Promise<ApiResponse<Blob>> {
    const query = inline === undefined ? "" : `?inline=${encodeURIComponent(inline)}`;
    return this.#bytes(`/api/artifacts/${encodeURIComponent(id)}/content${query}`, options);
  }

  override async getPreferencesPhoto(options?: RequestOptions): Promise<Blob> {
    return (await this.getPreferencesPhotoRaw(options)).data;
  }

  override getPreferencesPhotoRaw(options?: RequestOptions): Promise<ApiResponse<Blob>> {
    return this.#bytes("/api/preferences/photo", options);
  }

  async #bytes(path: string, options?: RequestOptions): Promise<ApiResponse<Blob>> {
    const timeout = options?.timeout ?? this.#timeout;
    const signals = [options?.signal, timeout === undefined ? undefined : AbortSignal.timeout(timeout)].filter(
      (s): s is AbortSignal => s !== undefined,
    );
    const req = await this.#prepare({
      url: `${this.#baseUrl}${path}`,
      init: {
        method: "GET",
        headers: { ...this.#headers, ...options?.headers },
        signal: signals.length > 1 ? AbortSignal.any(signals) : signals[0],
      },
    });
    const response = await this.#fetch(req.url, req.init);
    if (!response.ok) {
      const text = await response.text();
      let body: unknown = text;
      try {
        body = JSON.parse(text);
      } catch {
        // A proxy's error page is not JSON. The text is kept as it is.
      }
      throw new ApiError(`API request failed: ${response.status} ${response.statusText}`, response.status, response.statusText, body);
    }
    return { ok: true, status: response.status, statusText: response.statusText, headers: response.headers, data: await response.blob() };
  }
}

/** Make a client. The same as `new Collegium(options)`. */
export function createClient(options: CollegiumOptions): Collegium {
  return new Collegium(options);
}
