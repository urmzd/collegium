import "./react.ts";
import { createHash } from "node:crypto";
import {
  answerOfAction,
  answers,
  ended,
  type Follower,
  follow,
  type PlatformMessage,
  retryOf,
  toRequest,
} from "@collegium/adapter";
import { defaultCopy, MailboxView, MessageView, TurnView } from "@vultus/components";
import {
  ActionId,
  emptyQueue,
  emptyTurn,
  type Failure,
  feedbackEvent,
  messageState,
  partMatches,
  type QueueState,
  queueReduce,
  reduce,
  type Turn,
  type UiAction,
} from "@vultus/core";
import {
  decodeSlackAction,
  LIMITS,
  reactionChanges,
  renderToSlack,
  type SerializeResult,
} from "@vultus/render-slack";
import { choose, mayRoute, type Routing } from "./agents.ts";
import {
  type ActAs,
  type Agent,
  type ClientRecord,
  type Collegium,
  CollegiumError,
  type InboxNotice,
  type Run,
  type StreamItem,
} from "./collegium.ts";
import { askForm, ENTRY, words } from "./host.ts";
import { eventKey, runId, type Session, sessionName, sessionOf, threadKey } from "./keys.ts";
import { Presenter } from "./present.tsx";
import { type Envelope, type SlackApi, SlackError } from "./slack.ts";

/**
 * The Slack app: Slack deliveries in, collegium calls and Slack calls out.
 *
 * It holds no rendering of its own. A run's events go through
 * `@collegium/adapter` into one vultus turn, and that turn goes through
 * `@vultus/render-slack`. It holds no record of its own either: which
 * conversation a thread is, and whether a message was already answered, are
 * asked of collegium. What it keeps in memory is listed in the README, with
 * what a restart costs.
 */

export interface AppOptions {
  /** The one Slack team this instance serves. */
  team: string;
  /** This app's own bot user, whose messages never start a run. */
  botUser: string;
  /** Who collegium says this app is. Run ids are made from it. */
  subject: string;
  /** The workspace collegium says this app is in. Run ids are made from it too. */
  tenant: string;
  operators?: string[];
  /** The client record this app is, when it is one. */
  clientId?: string;
  defaultAgent?: string;
  /** Where people reach the console, for links to files. */
  publicUrl?: string;
  /** The least time between two writes of one turn to Slack. */
  flushMs?: number;
  /** How often a run's record is read while it is being followed. */
  pollMs?: number;
  /** How long a run may go without an event or a change to its record before it is followed slowly. */
  stallMs?: number;
  /** The longest wait between two reads of a quiet run's record. */
  slowPollMs?: number;
  /**
   * How long the stop button waits for a run this process is not following to
   * end before it settles the session anyway. The runtime gives a turn ten
   * seconds to wrap up by default.
   */
  stopWaitMs?: number;
  log?: (line: string) => void;
}

interface Job {
  session: Session;
  text: string;
  /** The message that asked. Lifecycle reactions go on it. */
  triggerTs: string;
  user: string;
  /** Names the Slack event, so the same event is one run. */
  key: string;
  /** Set when a turn is asked again: who answers and where it continues are already known. */
  again?: { agent: string; after?: string };
  /**
   * Who the run is for, when that is not the person who sent this. A turn
   * asked again stays its first asker's; null when nobody is known.
   */
  requester?: string | null;
}

type Item =
  | StreamItem
  | { type: "record"; run: Run }
  | { type: "stopped"; run?: Run }
  | { type: "broken"; error: Error }
  | { type: "tick" };

/** A run being followed right now. */
interface Live {
  run?: Run;
  /** Stop was pressed. The run is followed until collegium says how it ended. */
  stopped: boolean;
  wake(item: Item): void;
}

interface Thread {
  queue: QueueState;
  jobs: Map<string, Job>;
  live?: Live;
}

const terminal = (s: Run["status"]) => s === "completed" || s === "failed" || s === "stopped";

/** A turn waiting on a person's answer to a question. */
const waitsOnPerson = (turn: Turn) =>
  turn.status === "waiting" &&
  turn.interrupts.some((i) =>
    i.type === "approval" ? i.decision.kind === "pending" : i.answer.kind === "pending",
  );

/** A small wait queue: what the stream, the poll and the stop button put in, the pump takes out. */
class Mailbox<T> {
  private readonly items: T[] = [];
  private waiter: ((item: T) => void) | null = null;
  push(item: T) {
    if (this.waiter) {
      const w = this.waiter;
      this.waiter = null;
      w(item);
    } else this.items.push(item);
  }
  get empty() {
    return this.items.length === 0;
  }
  next(): Promise<T> {
    const item = this.items.shift();
    if (item !== undefined) return Promise.resolve(item);
    return new Promise((resolve) => {
      this.waiter = resolve;
    });
  }
}

/** Remembers the most recent `max` entries and forgets the oldest. */
class Recent<V> extends Map<string, V> {
  constructor(private readonly max: number) {
    super();
  }
  override set(key: string, value: V) {
    super.delete(key);
    super.set(key, value);
    if (this.size > this.max) super.delete(this.keys().next().value as string);
    return this;
  }
}

/**
 * An inbox message as Slack can show it. vultus's shared budget part offers
 * only fixed amounts to raise a limit to, and the platform names none, so on
 * Slack a reached limit is something to read: it is raised in the console.
 */
// vultus: MessagePartView's budget part needs a raise control that takes an amount (not only
// fixed `raiseTo` buttons) and an Acknowledge button, and render-slack must drop an empty actions
// block. Until then a limit is read-only here.
/**
 * Text a person asked with, made safe to repeat as the app. The app posts it
 * as itself, so a `<!channel>`, `<!here>`, `<!everyone>` or `<!subteam^…>` it
 * passed through would notify everyone with the app's rights, and a
 * `<url|label>` would show a link under a label the person chose. Only a
 * person (`<@U…>`) or channel (`<#C…>`) reference is kept; everything else is
 * shown as typed. Slack sends command text entity-encoded and form values raw,
 * so the text is decoded first and then encoded once.
 */
export function echoed(text: string): string {
  const raw = text.replace(/&lt;|&gt;|&amp;/g, (e) =>
    e === "&lt;" ? "<" : e === "&gt;" ? ">" : "&",
  );
  const kept = /<@[UW][A-Z0-9]+(?:\|[^<>]*)?>|<#C[A-Z0-9]+(?:\|[^<>]*)?>/g;
  const encode = (s: string) =>
    s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  let out = "";
  let at = 0;
  for (const m of raw.matchAll(kept)) {
    out += encode(raw.slice(at, m.index)) + m[0];
    at = (m.index ?? 0) + m[0].length;
  }
  return out + encode(raw.slice(at));
}

export function forSlack(message: PlatformMessage): PlatformMessage {
  return {
    ...message,
    parts: message.parts.map((p) => (p.type === "budget" ? { ...p, allowed: [] } : p)),
  };
}

/**
 * A refusal or an error from collegium, as vultus's failure copy. The
 * platform's own words go to the log, never to Slack: they can name internals.
 */
function publicFailure(e: unknown): Failure {
  const status = e instanceof CollegiumError ? e.status : 0;
  if (status === 429 || status === 402) {
    const resets = e instanceof CollegiumError ? e.refusal?.resets_at : undefined;
    const wait = resets ? Math.max(0, Math.round((Date.parse(resets) - Date.now()) / 1000)) : 0;
    return {
      kind: "rate_limited",
      message: defaultCopy.budgetTitle,
      httpStatus: status,
      ...(wait ? { retryAfterSeconds: wait } : {}),
    };
  }
  // vultus: failure copy has titles only (copy.failure[kind]); a body per kind, such as
  // copy.failureBody[kind], would stop the title being repeated as the message here.
  if (status === 401 || status === 403) {
    return { kind: "forbidden", message: defaultCopy.failure.forbidden(), httpStatus: status };
  }
  if (status === 0 || status >= 500) {
    return {
      kind: "unavailable",
      service: "collegium",
      message: defaultCopy.failure.unavailable("collegium"),
      ...(status ? { httpStatus: status } : {}),
    };
  }
  return { kind: "internal", message: defaultCopy.failure.internal(), httpStatus: status };
}

/** Nobody this app may reach can answer. */
// vultus: needs copy for this, such as copy.noAgent(how to name one).
const NO_AGENT: Failure = {
  kind: "forbidden",
  message: "No agent answers here yet. Name one this app may reach, as @name.",
};

/** A retry by someone who did not ask, and is not an operator. */
// vultus: needs copy for this, such as copy.notYoursRetry; copy.notYours speaks of approving.
const RETRY_NOT_YOURS = "Only the person who asked can retry this.";

/** Wait, or stop waiting when the signal aborts. */
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

const short = (s: string) => createHash("sha256").update(s).digest("hex").slice(0, 20);

export class App {
  private readonly threads = new Map<string, Thread>();
  /** Slack deliveries already taken, so a redelivery in this process does nothing. */
  private readonly seen = new Recent<true>(10_000);
  /** Who asked for each run, by Slack user id. */
  private readonly requesters = new Recent<string>(10_000);
  /** Inbox messages posted to a thread, so a change edits the post instead of adding one. */
  private readonly mail = new Recent<{ channel: string; ts: string; threadTs?: string }>(2_000);
  private readonly work = new Set<Promise<unknown>>();
  private native = true;
  private agentList: { at: number; agents: Agent[] } | null = null;
  private clientRecord: { at: number; client: ClientRecord | null } | null = null;
  private readonly flushMs: number;
  private readonly pollMs: number;
  private readonly stallMs: number;
  private readonly slowPollMs: number;
  private readonly stopWaitMs: number;
  /** Ends every slow follow on shutdown. */
  private readonly closing = new AbortController();
  /**
   * Runs whose end has been posted in their thread, by the slow follow or by
   * the stop button. Whichever posts first claims it, so an end is posted once.
   */
  private readonly confirmed = new Recent<true>(10_000);
  /** Runs followed slowly right now, and how to make each one look again at once. */
  private readonly slow = new Map<string, () => void>();

  constructor(
    private readonly slack: SlackApi,
    private readonly collegium: Collegium,
    private readonly opts: AppOptions,
  ) {
    this.flushMs = opts.flushMs ?? 700;
    this.pollMs = opts.pollMs ?? 5_000;
    this.stallMs = opts.stallMs ?? 15 * 60_000;
    this.slowPollMs = opts.slowPollMs ?? 3 * 60_000;
    this.stopWaitMs = opts.stopWaitMs ?? 30_000;
  }

  private log(line: string) {
    this.opts.log?.(line);
  }

  /** Stop following quiet runs. Turns in flight still finish. */
  close(): void {
    this.closing.abort();
  }

  /** Resolves when everything started so far has finished. Tests wait on it. */
  async idle(): Promise<void> {
    while (this.work.size > 0) await Promise.allSettled([...this.work]);
  }

  private track<T>(work: Promise<T>): Promise<T> {
    this.work.add(work);
    const done = () => this.work.delete(work);
    work.then(done, done);
    return work;
  }

  /** One delivery from Slack. Anything from another team is dropped before it is read. */
  async handle(envelope: Envelope): Promise<void> {
    const p = envelope.payload ?? {};
    const team = teamOf(envelope.type, p);
    if (team !== this.opts.team) {
      if (envelope.type !== "hello") this.log(`ignored a ${envelope.type} for another team`);
      return;
    }
    const work =
      envelope.type === "events_api"
        ? this.onEvent(p.event as SlackEvent)
        : envelope.type === "interactive" && p.type === "block_actions"
          ? this.onAction(p as unknown as ActionPayload)
          : envelope.type === "interactive"
            ? this.onInteractive(p as unknown as InteractivePayload)
            : envelope.type === "slash_commands"
              ? this.onCommand(p as unknown as CommandPayload)
              : Promise.resolve();
    await this.track(work);
  }

  /** Follow the inbox for as long as the signal allows, and show what concerns this app's threads. */
  async watchInbox(signal: AbortSignal): Promise<void> {
    for await (const notice of this.collegium.notices({ signal })) {
      await this.track(
        this.onNotice(notice).catch((e: Error) => this.log(`inbox notice failed: ${e.message}`)),
      );
    }
  }

  // Routing

  private async agents(): Promise<Agent[]> {
    if (this.agentList && Date.now() - this.agentList.at < 30_000) return this.agentList.agents;
    const agents = (await this.collegium.listAgents()).agents.map((r) => r.spec);
    this.agentList = { at: Date.now(), agents };
    return agents;
  }

  /** The client record is read again every half minute, so a change in the console takes effect. */
  private async routing(): Promise<Routing> {
    if (!this.opts.clientId) return { defaultAgent: this.opts.defaultAgent };
    if (!this.clientRecord || Date.now() - this.clientRecord.at >= 30_000) {
      const found = await this.collegium.getClient(this.opts.clientId);
      // A client that is gone, is for another surface, or does not name this team routes to nothing.
      const ours = found && found.surface === "slack" && found.identity?.team === this.opts.team;
      this.clientRecord = {
        at: Date.now(),
        client: ours
          ? found
          : { id: this.opts.clientId, surface: "slack", enabled: false, agents: [] },
      };
    }
    return { client: this.clientRecord.client, defaultAgent: this.opts.defaultAgent };
  }

  private href = (path: string) =>
    this.opts.publicUrl ? `${this.opts.publicUrl}${path}` : undefined;

  // Events

  private async onEvent(event: SlackEvent): Promise<void> {
    if (!event) return;
    if (event.type === "app_home_opened") {
      if (event.tab === "home" && event.user) await this.publishHome(event.user);
      return;
    }
    if (event.type === "agent_session_stopped") {
      const channel = event.channel ?? event.channel_id;
      if (!channel || !event.thread_ts) return;
      await this.stop({ team: this.opts.team, channel, threadTs: event.thread_ts });
      return;
    }
    const asked =
      event.type === "app_mention" || (event.type === "message" && event.channel_type === "im");
    // Loop prevention: a bot's message, this app's own, an edit or a join never starts a run.
    if (!asked || event.subtype || event.bot_id || !event.user) return;
    if (event.user === this.opts.botUser || !event.channel || !event.ts) return;
    const session: Session = {
      team: this.opts.team,
      channel: event.channel,
      threadTs: event.thread_ts ?? event.ts,
    };
    const text = (event.text ?? "").replaceAll(`<@${this.opts.botUser}>`, " ").trim();
    if (!text) return;
    this.enqueue({
      session,
      text,
      triggerTs: event.ts,
      user: event.user,
      key: eventKey(session, event.ts),
    });
  }

  // The queue: one turn at a time per thread, as in the console's composer.

  private thread(session: Session): Thread {
    const name = sessionName(session);
    let thread = this.threads.get(name);
    if (!thread) {
      thread = { queue: emptyQueue("auto"), jobs: new Map() };
      this.threads.set(name, thread);
    }
    return thread;
  }

  private enqueue(job: Job) {
    if (this.seen.has(job.key)) return;
    this.seen.set(job.key, true);
    const thread = this.thread(job.session);
    thread.jobs.set(job.key, job);
    const [queue, effect] = queueReduce(thread.queue, {
      type: "send",
      message: { id: job.key, text: job.text, queuedAt: new Date().toISOString() },
    });
    thread.queue = queue;
    if (effect.type === "start") {
      this.track(this.drive(thread, job, false));
      return;
    }
    // It waits behind the turn in flight. The reaction says it was seen.
    this.track(this.react(job, null, "queued"));
  }

  private async react(job: Job, prev: Turn["status"] | null, next: Turn["status"]) {
    const { add, remove } = reactionChanges(prev, next);
    const at = { channel: job.session.channel, timestamp: job.triggerTs };
    for (const name of remove)
      await this.slack.call("reactions.remove", { ...at, name }).catch(() => {});
    for (const name of add) await this.slack.call("reactions.add", { ...at, name }).catch(() => {});
  }

  /** Answer one message, then whatever the queue releases next. */
  private async drive(thread: Thread, first: Job, waited: boolean): Promise<void> {
    let job: Job | undefined = first;
    let queued = waited;
    while (job) {
      let outcome: "done" | "failed" | "stopped" = "failed";
      try {
        outcome = await this.answer(thread, job, queued);
      } catch (e) {
        this.log(`turn failed: ${(e as Error).message}`);
      }
      thread.live = undefined;
      thread.jobs.delete(job.key);
      let [queue, effect] = queueReduce(thread.queue, { type: "turnEnded", outcome });
      if (queue.status === "held" && queue.heldReason === "stopped") {
        // Stop means stop: what was waiting behind the stopped turn is not started.
        for (const m of queue.pending) {
          const waiting = thread.jobs.get(m.id);
          if (waiting) await this.react(waiting, "queued", "stopped");
          thread.jobs.delete(m.id);
        }
        [queue, effect] = queueReduce(queue, { type: "clear" });
      } else if (queue.status === "held") {
        // Slack has no control to release a held queue, and each message is its own question.
        [queue, effect] = queueReduce(queue, { type: "resume" });
      }
      thread.queue = queue;
      job = effect.type === "start" ? thread.jobs.get(effect.message.id) : undefined;
      queued = true;
    }
    if (thread.queue.status === "idle" && thread.jobs.size === 0) {
      this.threads.delete(sessionName(first.session));
    }
  }

  /**
   * Where a message sits in its conversation. The conversation's id follows
   * from the thread, so collegium is asked rather than a local record.
   */
  private async place(job: Job): Promise<{ key: string; after?: string } | "duplicate"> {
    const first = threadKey(job.session);
    const turns = (
      await this.collegium.getThread(runId(this.opts.tenant, this.opts.subject, first))
    )?.turns;
    // The first run in a thread is keyed by the thread, which is what names the conversation.
    if (!turns || turns.length === 0) return { key: first };
    if (job.key === first) return "duplicate";
    if (turns.some((t) => t.id === runId(this.opts.tenant, this.opts.subject, job.key)))
      return "duplicate";
    if (job.again) return { key: job.key, after: job.again.after };
    const latest = turns
      .filter((t) => !t.parent)
      .sort((a, b) => a.created_at.localeCompare(b.created_at))
      .at(-1);
    return { key: job.key, after: latest?.id };
  }

  private target(job: Job) {
    return {
      channel: job.session.channel,
      threadTs: job.session.threadTs,
      triggerTs: job.triggerTs,
      recipientUser: job.user,
      recipientTeam: job.session.team,
    };
  }

  /** A turn that ended before any run existed: nothing to retry, only something to read. */
  private async refuse(job: Job, presenter: Presenter, agent: string, failure: Failure) {
    const turn = reduce(emptyTurn(job.key, { name: agent || "collegium" }), {
      type: "turn.failed",
      failure: { ...failure, retryable: false },
    });
    await presenter.finish(turn);
    this.native = this.native && presenter.streaming;
    return "failed" as const;
  }

  private async answer(thread: Thread, job: Job, queued: boolean) {
    const items = new Mailbox<Item>();
    const live: Live = { stopped: false, wake: (item) => items.push(item) };
    thread.live = live;
    const presenter = new Presenter(this.slack, this.target(job), {
      native: this.native,
      log: (line) => this.log(line),
      ...(queued ? { shown: "queued" as const } : {}),
    });

    let placed: Awaited<ReturnType<App["place"]>>;
    let agent = job.again?.agent ?? "";
    let text = job.text;
    let run: Run;
    try {
      placed = await this.place(job);
      // Collegium already has a run for this Slack event: it was answered before a restart.
      if (placed === "duplicate") return "done" as const;
      const routing = await this.routing();
      if (!job.again) {
        const choice = choose(job.text, await this.agents(), routing);
        if (choice.agent === null) return this.refuse(job, presenter, "", NO_AGENT);
        agent = choice.agent;
        text = choice.text;
      } else if (!mayRoute(routing, agent)) {
        // Asked again after the client was disabled or the agent left its list.
        return this.refuse(job, presenter, agent, NO_AGENT);
      }
      // Stop was pressed before there was a run to stop: say so and settle the session.
      if (live.stopped) {
        await presenter.finish(
          reduce(emptyTurn(job.key, { name: agent || "collegium" }), { type: "turn.stopped" }),
        );
        return "stopped" as const;
      }
      // Who asked, the client and the thread are known here and have nowhere to go:
      // the route takes none of them. See the README.
      // A person the client maps asks as themselves, so they do not approve
      // their own run's calls. Anyone else asks through the app.
      const asking = await this.actAs(job.requester ?? job.user);
      run = await this.collegium.createRun(
        {
          agent,
          text,
          ...(placed.after ? { after: placed.after } : {}),
          ...(asking ? { act_as: asking } : {}),
        },
        placed.key,
      );
    } catch (e) {
      this.log(`collegium refused the turn: ${(e as Error).message}`);
      return this.refuse(job, presenter, agent, publicFailure(e));
    }

    live.run = run;
    if (live.stopped) {
      // Stop was pressed while the run was being started, so it is told now.
      const told = await this.collegium.stopRun(run.id).catch(() => undefined);
      items.push({ type: "stopped", run: told });
    }
    const owner = job.requester === undefined ? job.user : job.requester;
    if (owner) this.requesters.set(run.id, owner);
    this.log(`run ${run.id} for ${job.user} in ${job.session.channel}, answered by ${run.agent}`);
    const following = follow(run, { href: this.href });
    const abort = new AbortController();
    const pump = this.pump(run, following, presenter, items, abort.signal);
    let turn: Turn;
    let stalled: boolean;
    try {
      ({ turn, stalled } = await pump);
    } finally {
      abort.abort();
    }
    // A quiet run's stream is closed as it stands: still working, so the session
    // stays processing and Stop stays available. Its end is posted when it comes.
    await presenter.finish(turn);
    this.native = this.native && presenter.streaming;
    if (stalled) {
      this.track(this.later(run, following, presenter));
      // The thread is not held for it. A later message follows it in the conversation,
      // as collegium orders any turn sent after one that is still running.
      return "done" as const;
    }
    this.log(`run ${run.id} ended ${turn.status}`);
    return turn.status === "done" ? "done" : turn.status === "stopped" ? "stopped" : "failed";
  }

  /**
   * Follow a quiet run by its record, less and less often, until it ends, then
   * post its end in the thread. A stop pressed meanwhile confirms it instead.
   */
  private async later(run: Run, following: Follower, presenter: Presenter): Promise<void> {
    let wait = this.pollMs;
    try {
      while (!this.closing.signal.aborted) {
        const nudged = await this.nap(run.id, wait);
        // A stop was pressed: the run will end soon, so it is read often again.
        wait = nudged ? Math.min(this.pollMs, 500) : Math.min(wait * 2, this.slowPollMs);
        if (this.closing.signal.aborted) return;
        const record = await this.collegium.getRun(run.id).catch(() => null);
        if (!record || !terminal(record.status)) continue;
        if (!this.claim(run.id)) return;
        following.settle(record);
        await presenter.conclude(following.turn);
        this.log(`run ${run.id} ended ${following.turn.status}, after it went quiet`);
        return;
      }
    } finally {
      this.slow.delete(run.id);
    }
  }

  /** Wait, until the time is up, the app closes, or the run is nudged. Reports whether it was nudged. */
  private nap(id: string, ms: number): Promise<boolean> {
    return new Promise((resolve) => {
      const ctl = new AbortController();
      const stopWaiting = () => ctl.abort();
      this.closing.signal.addEventListener("abort", stopWaiting, { once: true });
      let nudged = false;
      this.slow.set(id, () => {
        nudged = true;
        ctl.abort();
      });
      sleep(ms, ctl.signal).then(() => {
        this.closing.signal.removeEventListener("abort", stopWaiting);
        resolve(nudged);
      });
    });
  }

  /** Claim the posting of a run's end. False when it has been posted already. */
  private claim(id: string): boolean {
    if (this.confirmed.has(id)) return false;
    this.confirmed.set(id, true);
    return true;
  }

  /**
   * Fold a run's events into its turn and keep Slack in step, until the turn
   * ends. The record is read beside the stream, because the ledger decides
   * whether a run is over, and a stream can miss the event that says so.
   */
  private async pump(
    run: Run,
    following: Follower,
    presenter: Presenter,
    items: Mailbox<Item>,
    signal: AbortSignal,
  ): Promise<{ turn: Turn; stalled: boolean }> {
    (async () => {
      try {
        for await (const item of this.collegium.runEvents(run, { signal })) items.push(item);
      } catch (e) {
        if (!signal.aborted) items.push({ type: "broken", error: e as Error });
      }
    })();
    const read = async () => {
      const record = await this.collegium.getRun(run.id).catch(() => null);
      if (record && !signal.aborted) items.push({ type: "record", run: record });
    };
    const poll = setInterval(read, this.pollMs);
    let tick: ReturnType<typeof setTimeout> | undefined;
    signal.addEventListener("abort", () => {
      clearInterval(poll);
      clearTimeout(tick);
    });

    // The session shows work has begun before the first event arrives.
    await presenter.update(following.turn);
    let dirty = false;
    let last = 0;
    let over = 0;
    let progress = Date.now();
    let seen = run.updated_at;
    for (;;) {
      const item = await items.next();
      if (item.type === "stopped") {
        if (item.run && terminal(item.run.status)) {
          following.settle(item.run);
          return { turn: following.turn, stalled: false };
        }
        if (!item.run) {
          // Collegium did not take the stop: the turn stops here, and says it may still finish elsewhere.
          if (!ended(following.turn)) {
            following.push({ type: "turn.stopped" }, { type: "stop.updated", stop: "failed" });
          }
          return { turn: following.turn, stalled: false };
        }
        // Collegium took it and the run is stopping. It is followed on until
        // lifecycle.stopped, or its record, says how it ended: stopped with
        // what it had said, or completed if it finished first.
        following.push({ type: "stop.updated", stop: "acknowledged" });
        dirty = true;
      }
      if (item.type === "record" && item.run.updated_at !== seen) {
        seen = item.run.updated_at;
        progress = Date.now();
      }
      if (item.type === "event" && item.event.run_id === run.id) {
        progress = Date.now();
        const before = following.turn;
        dirty = following.apply(item.event) !== before || dirty;
        // A question shows with its buttons at once, and its card settles
        // where it was shown when it is answered, whoever answered it.
        if (waitsOnPerson(following.turn)) {
          await presenter.pause(following.turn);
          dirty = false;
        } else if (following.turn.interrupts !== before.interrupts) {
          await presenter.settleQuestion(following.turn);
        }
      } else if (item.type === "record" && terminal(item.run.status)) {
        // An ended run is given one more read for its final event to arrive, so
        // the streamed answer is not cut short. The record carries the answer too.
        over += 1;
        if (over > 1) following.settle(item.run);
      } else if (item.type === "record" && Date.now() - progress > this.stallMs) {
        // Nothing has been heard for a long time. That is not a failure: a long tool
        // call, or a run waiting on other work, says nothing until it moves. The
        // stream is closed and the run is followed slowly from here.
        this.log(`run ${run.id} quiet for ${this.stallMs}ms, following it slowly`);
        return { turn: following.turn, stalled: true };
      } else if (item.type === "broken") {
        this.log(`event stream for ${run.id} ended: ${item.error.message}`);
        await read();
      }
      if (ended(following.turn)) return { turn: following.turn, stalled: false };
      if (!dirty || !items.empty) continue;
      const wait = this.flushMs - (Date.now() - last);
      if (wait > 0) {
        // Slack prefers fewer, larger appends: what arrives meanwhile goes out together.
        clearTimeout(tick);
        tick = setTimeout(() => items.push({ type: "tick" }), wait);
        continue;
      }
      await presenter.update(following.turn);
      dirty = false;
      last = Date.now();
    }
  }

  // Stop

  /**
   * The stop button, in the order Slack asks for: stop the work, confirm in
   * the thread, then move the session off processing. Slack changes nothing by
   * itself. Which runs to stop is asked of collegium as well as remembered, so
   * the button works after a restart.
   */
  private async stop(session: Session): Promise<void> {
    const thread = this.threads.get(sessionName(session));
    const live = thread?.live;
    // A turn whose run is still being started stops itself once it has one.
    const following = live?.run;
    if (live) live.stopped = true;
    const ids = new Set<string>();
    if (following) ids.add(following.id);
    const conversation = runId(this.opts.tenant, this.opts.subject, threadKey(session));
    const turns = (await this.collegium.getThread(conversation).catch(() => null))?.turns;
    for (const t of turns ?? []) if (!t.parent && !terminal(t.status)) ids.add(t.id);

    const stopped: Run[] = [];
    for (const id of ids) {
      try {
        stopped.push(await this.collegium.stopRun(id));
      } catch (e) {
        this.log(`stop of ${id} failed: ${(e as Error).message}`);
      }
    }
    if (live && following) {
      // The turn in flight confirms in its own message and settles the session.
      live.wake({ type: "stopped", run: stopped.find((r) => r.id === following.id) });
    }
    // Runs this process is not following live. One it follows slowly is
    // looked at again at once, and its slow follow posts its end, stopped or
    // not. Any other is read until it ends and its end is posted here, in
    // Slack's order: the end, then the status. One that has not ended by
    // stopWaitMs is left to a slow follow, and the session stays working
    // until it has.
    const deadline = Date.now() + this.stopWaitMs;
    const ends = await Promise.all(
      stopped
        .filter((run) => run.id !== following?.id)
        .map((run) => {
          const nudge = this.slow.get(run.id);
          if (nudge) {
            nudge();
            return Promise.resolve(false);
          }
          return this.endOf(session, run, deadline);
        }),
    );
    if (!live && ends.every(Boolean)) await this.settleSession(session);
  }

  /** Set a session's status to active: nothing is working in it any more. */
  private async settleSession(session: Session): Promise<void> {
    await this.slack
      .call("agents.sessions.setStatus", {
        channel_id: session.channel,
        thread_ts: session.threadTs,
        status: "active",
      })
      .catch((e: Error) => this.log(`session status skipped: ${e.message}`));
  }

  /**
   * Post a run's end in its thread once: what it had said, marked stopped, or
   * its answer when it finished before the stop reached it.
   */
  private async postEnd(session: Session, run: Run): Promise<void> {
    if (!this.claim(run.id)) return;
    if (run.status !== "stopped")
      this.log(`run ${run.id} ended ${run.status} before the stop reached it`);
    const turn = follow(run, { href: this.href }).settle(run);
    await this.postView(session, renderToSlack(<TurnView turn={turn} />)).catch((e: Error) =>
      this.log(`the end of run ${run.id} was not posted: ${e.message}`),
    );
  }

  /**
   * Read a run that was asked to stop until it ends, and post its end. Reports
   * whether it ended by the deadline. One that has not is followed on slowly,
   * and its end is posted, and the session settled, when it comes.
   */
  private async endOf(session: Session, run: Run, deadline: number): Promise<boolean> {
    // A stop takes seconds, not minutes, so it is read more often than a quiet run.
    let wait = Math.min(this.pollMs, 500);
    let record: Run | null = run;
    while (
      !(record && terminal(record.status)) &&
      !this.closing.signal.aborted &&
      Date.now() < deadline
    ) {
      await sleep(Math.min(wait, Math.max(deadline - Date.now(), 0)), this.closing.signal);
      wait = Math.min(wait * 2, 5_000);
      record = await this.collegium.getRun(run.id).catch(() => null);
    }
    if (record && terminal(record.status)) {
      await this.postEnd(session, record);
      return true;
    }
    if (this.closing.signal.aborted) return false;
    this.log(
      `run ${run.id} was asked to stop and had not ended after ${this.stopWaitMs}ms; following it slowly`,
    );
    this.track(this.endLater(session, run));
    return false;
  }

  /** Follow a stopping run by its record, less and less often, then post its end and settle the session. */
  private async endLater(session: Session, run: Run): Promise<void> {
    let wait = Math.min(this.pollMs, 500);
    while (!this.closing.signal.aborted) {
      await sleep(wait, this.closing.signal);
      wait = Math.min(wait * 2, this.slowPollMs);
      if (this.closing.signal.aborted) return;
      const record = await this.collegium.getRun(run.id).catch(() => null);
      if (!record || !terminal(record.status)) continue;
      await this.postEnd(session, record);
      if (!this.threads.get(sessionName(session))?.live) await this.settleSession(session);
      return;
    }
  }

  // Clicks

  private async whisper(p: ActionPayload, text: string) {
    const channel = p.channel?.id ?? p.container?.channel_id;
    if (!channel) return;
    await this.slack
      .call("chat.postEphemeral", {
        channel,
        user: p.user.id,
        // Reply where the button was clicked, not in the channel above the thread.
        ...(p.message?.thread_ts ? { thread_ts: p.message.thread_ts } : {}),
        text,
      })
      .catch(() => {});
  }

  private async onAction(p: ActionPayload): Promise<void> {
    const action = p.actions?.[0];
    const ui = action ? decodeSlackAction(action, p.state) : null;
    // Ticking a checkbox is not a decision. The buttons read the ticks.
    if (!ui) return;
    const dedupe = `action-${p.trigger_id ?? ""}`;
    if (p.trigger_id && this.seen.has(dedupe)) return;
    this.seen.set(dedupe, true);

    if (ui.id === ActionId.feedback) return this.onFeedback(ui, p.user.id);
    // A click on an inbox message names the message. Anything else names a run.
    const message = await this.collegium.inbox.get(ui.payload.turnId).catch(() => null);
    if (message) return this.onMail(p, ui, message);
    if (answers(ui)) return this.onAnswer(p, ui);
    if (ui.id === ActionId.retry || ui.id === ActionId.resume) {
      const run = await this.collegium.getRun(ui.payload.turnId);
      const outcome = run ? await this.again(run, p.user.id, p.message?.ts) : "elsewhere";
      if (outcome !== "started") await this.refuseRetry(p, outcome);
    }
    // Opening a called run, reporting and reconnecting have nowhere to go from Slack yet.
  }

  /** Say why a turn was not asked again. */
  private refuseRetry(p: ActionPayload, outcome: "not-yours" | "elsewhere" | "not-routed") {
    return this.whisper(p, outcome === "not-yours" ? RETRY_NOT_YOURS : defaultCopy.cantRetry);
  }

  /**
   * Ask a turn again, in the thread it was asked in. Only the person who asked
   * for it, or an operator, may: a retry is a new run on their behalf. When
   * who asked is not known (after a restart) only an operator may. The new run
   * stays its first asker's, and the routing rules are checked again.
   */
  private async again(
    run: Run,
    user: string,
    clickedTs?: string,
  ): Promise<"started" | "not-yours" | "elsewhere" | "not-routed"> {
    const session = await this.sessionOfRun(run);
    if (!session || session.team !== this.opts.team) return "elsewhere";
    const owner = this.requesters.get(run.id);
    const operator = (this.opts.operators ?? []).includes(user);
    if (!operator && (!owner || owner !== user)) return "not-yours";
    if (!mayRoute(await this.routing(), run.agent)) return "not-routed";
    const { agent, text, after } = retryOf(run);
    this.enqueue({
      session,
      text,
      triggerTs: clickedTs ?? session.threadTs,
      user,
      requester: owner ?? null,
      // One retry per run: a second click joins the first.
      key: `slack-again-${short(run.id)}`,
      // The first turn of a conversation is asked again after itself, so the thread stays one conversation.
      again: { agent, after: after ?? run.id },
    });
    return "started";
  }

  /**
   * A rating has nowhere to be stored yet: collegium has no route for one, and
   * Slack's rating control shows no selection to redraw. It is written to the
   * log, so it is not lost while that is built.
   */
  private onFeedback(ui: UiAction, user: string) {
    const event = feedbackEvent(ui);
    if (event?.type === "feedback") {
      this.log(`feedback on ${ui.payload.turnId} by ${user}: ${event.rating ?? "cleared"}`);
    }
  }

  // Questions a run waits on

  /**
   * Who a Slack person is in the workspace, as this app's client maps them,
   * or null. Only the client this app is maps people, and only for the
   * credential it names, which must be this app's own.
   */
  private async actAs(user: string): Promise<ActAs | null> {
    const routing = await this.routing().catch(() => null);
    const client = routing?.client;
    if (!client || !this.opts.clientId || !client.enabled) return null;
    const mapped =
      client.slack?.machine === this.opts.subject && Boolean(client.slack?.people?.[user]);
    return mapped ? { client: this.opts.clientId, user } : null;
  }

  /**
   * Answer a question shown on a run, for the person who clicked. Collegium
   * decides whether they may: it is asked as them, through the client that
   * maps them, and a person it does not map is never asked for at all. The
   * card settles from the run's own events.
   */
  private async onAnswer(p: ActionPayload, ui: UiAction): Promise<void> {
    const answer = answerOfAction(ui);
    if (!answer) {
      // vultus: a modal that collects a reply to a question (as feedbackModal does for a
      // rating), for Something Else and Reply. Until then a reply is typed in the console.
      if (ui.id === ActionId.clarifyOther) return this.whisper(p, words.replyInConsole);
      return;
    }
    const as = await this.actAs(p.user.id);
    if (!as) {
      this.log(`an answer from ${p.user.id} was not sent: this client does not map them to anyone`);
      return this.whisper(p, defaultCopy.noPermission);
    }
    try {
      await this.collegium.answerQuestion(answer, as);
      this.log(`question ${answer.question} on ${answer.run} answered for ${p.user.id}`);
    } catch (e) {
      this.log(
        `question ${answer.question} on ${answer.run} was not answered: ${(e as Error).message}`,
      );
      const status = e instanceof CollegiumError ? e.status : 0;
      return this.whisper(
        p,
        status === 403 ? defaultCopy.noPermission : defaultCopy.failure.internal(),
      );
    }
  }

  // The inbox

  /** Whether a Slack person may settle an inbox message here: they asked for the run, or they are an operator. */
  private maySettle(user: string, message: PlatformMessage): boolean {
    if ((this.opts.operators ?? []).includes(user)) return true;
    return message.about.some((s) => s.kind === "run" && this.requesters.get(s.id) === user);
  }

  private async onMail(p: ActionPayload, ui: UiAction, message: PlatformMessage) {
    const part = message.parts.find((x) => partMatches(x, ui.payload.target));
    const question = part?.type === "approval" || part?.type === "choice" || part?.type === "reply";
    // Who may answer a question is collegium's to decide, for the person as it knows them.
    const as = question ? await this.actAs(p.user.id) : null;
    if (question && !as) return this.whisper(p, defaultCopy.noPermission);
    if (!question && !this.maySettle(p.user.id, message)) {
      return this.whisper(p, defaultCopy.notYours);
    }
    if (part?.type === "reply" && ui.id === ActionId.reply && !ui.payload.text) {
      return this.whisper(p, words.replyInConsole);
    }
    // A failed turn is asked again here, where its answer can be shown, when the thread is this app's.
    let request = toRequest(message, ui);
    if (request?.call === "resolve" && as)
      request = { ...request, values: { ...request.values, act_as: as } };
    if (ui.id === ActionId.retry) {
      // Only a failed run can be asked again. A Retry on anything else is refused.
      if (part?.type !== "failure") return this.refuseRetry(p, "elsewhere");
      // A retry is always this app's, so its answer shows where it was asked and the
      // client's agents are checked. Collegium is never asked to retry on its own.
      const run = part.failure.reference
        ? await this.collegium.getRun(part.failure.reference)
        : null;
      const outcome = run ? await this.again(run, p.user.id, p.message?.ts) : "elsewhere";
      if (outcome !== "started") return this.refuseRetry(p, outcome);
      request = { call: "resolve", partId: part.id, action: "acknowledge" };
    }
    if (!request) return;
    let updated: PlatformMessage;
    try {
      updated = await this.collegium.inbox.send(message, request);
    } catch (e) {
      // The platform's words stay in the log. The person is told in vultus's.
      this.log(`inbox ${message.id} was not settled: ${(e as Error).message}`);
      const status = e instanceof CollegiumError ? e.status : 0;
      return this.whisper(
        p,
        status === 403 ? defaultCopy.noPermission : defaultCopy.failure.internal(),
      );
    }
    const channel = p.channel?.id ?? p.container?.channel_id;
    if (p.container?.type === "view" || p.view?.type === "home") {
      await this.publishHome(p.user.id);
    } else if (channel && p.message?.ts) {
      await this.showMail(updated, {
        channel,
        ts: p.message.ts,
        ...(p.message.thread_ts ? { threadTs: p.message.thread_ts } : {}),
      });
    }
  }

  /** Post a rendered view in a thread, every page of it. Returns the first message. */
  private async postView(
    at: { channel: string; threadTs: string },
    view: SerializeResult,
  ): Promise<string> {
    let first = "";
    for (const [i, blocks] of view.pages.entries()) {
      const sent = await this.slack.call<{ ts: string }>("chat.postMessage", {
        channel: at.channel,
        thread_ts: at.threadTs,
        // vultus: needs a fallback-text helper for continued pages, as for turns.
        text: (i === 0 ? view.text : `(${i + 1}/${view.pages.length}) ${view.text}`).slice(
          0,
          LIMITS.fallbackText,
        ),
        blocks,
      });
      first ||= sent.ts;
    }
    return first;
  }

  /** Edit a posted view. Pages beyond the first follow it in the thread. */
  private async showMail(
    message: PlatformMessage,
    at: { channel: string; ts: string; threadTs?: string },
  ) {
    const view = renderToSlack(<MessageView message={forSlack(message)} />);
    const [first = [], ...rest] = view.pages;
    await this.slack.call("chat.update", {
      channel: at.channel,
      ts: at.ts,
      text: view.text.slice(0, LIMITS.fallbackText),
      blocks: first,
    });
    if (rest.length > 0 && at.threadTs) {
      await this.postView(
        { channel: at.channel, threadTs: at.threadTs },
        { ...view, pages: rest, blocks: rest[0] ?? [] },
      );
    }
  }

  /** The Slack thread a run's conversation lives in, read from its first run's trigger id. */
  private async sessionOfRun(run: Run): Promise<Session | null> {
    const first =
      run.id === run.thread ? run : await this.collegium.getRun(run.thread).catch(() => null);
    return first?.tag_id ? sessionOf(this.opts.subject, first.tag_id) : null;
  }

  /** An inbox message changed. One about a run in this app's thread is shown there, once, and kept current. */
  private async onNotice(notice: InboxNotice): Promise<void> {
    const message = await this.collegium.inbox.get(notice.id).catch(() => null);
    if (!message) return;
    const posted = this.mail.get(message.id);
    if (posted) return this.showMail(message, posted);
    if (messageState(message) !== "open") return;
    // A failed turn already says so in its own message, with Retry, and a
    // question is shown on the run it holds up.
    if (message.kind === "failure" || message.parts.some((p) => p.type === "failure")) return;
    if (message.kind === "approval" || message.kind === "clarification") return;
    const about = message.about.find((s) => s.kind === "run");
    const run = about ? await this.collegium.getRun(about.id) : null;
    const session = run ? await this.sessionOfRun(run) : null;
    if (!session || session.team !== this.opts.team) return;
    const ts = await this.postView(
      session,
      renderToSlack(<MessageView message={forSlack(message)} />),
    );
    this.mail.set(message.id, { channel: session.channel, ts, threadTs: session.threadTs });
  }

  /** The Home tab: what is waiting on this person. An operator sees the whole inbox the app may read. */
  private async publishHome(user: string): Promise<void> {
    const page = await this.collegium.inbox.list({ limit: 30, state: "open" });
    const operator = (this.opts.operators ?? []).includes(user);
    const messages = operator
      ? page.messages
      : page.messages.filter((m) => this.maySettle(user, m));
    const view = renderToSlack(<MailboxView messages={messages.map(forSlack)} />, {
      placement: "home",
    });
    await this.slack.call("views.publish", {
      user_id: user,
      view: { type: "home", blocks: view.blocks },
    });
  }

  // Shortcuts and the command

  /**
   * Start a turn somewhere that has no message to answer yet: say who asked
   * what, then answer in that message's thread.
   */
  private async start(
    where: { channel: string; user: string },
    text: string,
    responseUrl?: string,
  ): Promise<void> {
    let root: { ts: string; channel: string };
    try {
      root = await this.slack.call("chat.postMessage", {
        channel: where.channel,
        // vultus: needs a component for the asking message itself, such as RequestView({by, text}),
        // which the web shows as the user's bubble. Until then it is the person and their words.
        text: `<@${where.user}>: ${echoed(text)}`,
      });
    } catch (e) {
      const away = e instanceof SlackError && /not_in_channel|channel_not_found/.test(e.code);
      if (!away || !responseUrl) throw e;
      // The app can only post where it is a member. Say so instead of failing silently.
      await this.slack.respond(responseUrl, {
        response_type: "ephemeral",
        text: words.notInChannel,
      });
      return;
    }
    const session: Session = { team: this.opts.team, channel: root.channel, threadTs: root.ts };
    this.enqueue({ session, text, triggerTs: root.ts, user: where.user, key: threadKey(session) });
  }

  private async onCommand(c: CommandPayload): Promise<void> {
    if (c.command !== ENTRY.command) return;
    if (c.trigger_id) {
      if (this.seen.has(`command-${c.trigger_id}`)) return;
      this.seen.set(`command-${c.trigger_id}`, true);
    }
    const text = (c.text ?? "").trim();
    if (!text || text === "help") {
      // Who can be asked, in the platform's own words.
      // vultus: needs an AgentList component (name, description), as the console's agent menu shows.
      const routing = await this.routing();
      const lines = (await this.agents())
        .filter((a) => choose(`@${a.name}`, [a], routing).how === "named")
        .map((a) => (a.description ? `\`@${a.name}\`: ${a.description}` : `\`@${a.name}\``));
      if (lines.length > 0) {
        await this.slack.respond(c.response_url, {
          response_type: "ephemeral",
          text: lines.join("\n"),
        });
      }
      return;
    }
    await this.start({ channel: c.channel_id, user: c.user_id }, text, c.response_url);
  }

  private async onInteractive(p: InteractivePayload): Promise<void> {
    if (p.trigger_id) {
      const once = `${p.type}-${p.trigger_id}`;
      if (this.seen.has(once)) return;
      this.seen.set(once, true);
    }
    if (p.type === "shortcut" && p.callback_id === ENTRY.ask) {
      await this.slack.call("views.open", { trigger_id: p.trigger_id, view: askForm() });
      return;
    }
    if (p.type === "message_action" && p.callback_id === ENTRY.askAbout) {
      const { channel, message } = p;
      const text = (message?.text ?? "").trim();
      if (!channel || !message || !text) return;
      // The message is the question, so the answer belongs in its thread.
      const session: Session = {
        team: this.opts.team,
        channel: channel.id,
        threadTs: message.thread_ts ?? message.ts,
      };
      this.enqueue({
        session,
        text,
        triggerTs: message.ts,
        user: p.user.id,
        key: eventKey(session, message.ts, p.trigger_id),
      });
      return;
    }
    if (p.type !== "view_submission" || p.view?.callback_id !== ENTRY.form) return;
    const text = p.view.state?.values?.text?.value?.value?.trim() ?? "";
    if (!text) return;
    // A global shortcut has no channel: answer in the app's own messages with this person.
    await this.start({ channel: p.user.id, user: p.user.id }, text);
  }
}

/** The team a delivery says it is from. Each kind of delivery carries it somewhere else. */
function teamOf(type: string, p: Record<string, unknown>): string | undefined {
  const at = (v: unknown, k: string) =>
    v && typeof v === "object" ? (v as Record<string, unknown>)[k] : undefined;
  if (type === "events_api" || type === "slash_commands") return p.team_id as string | undefined;
  if (type === "interactive") {
    return (at(p.team, "id") ?? at(p.user, "team_id")) as string | undefined;
  }
  return undefined;
}

interface SlackEvent {
  type: string;
  subtype?: string;
  channel?: string;
  channel_id?: string;
  channel_type?: string;
  user?: string;
  bot_id?: string;
  text?: string;
  ts?: string;
  thread_ts?: string;
  tab?: string;
}

interface ActionPayload {
  user: { id: string };
  channel?: { id: string };
  container?: { type?: string; channel_id?: string };
  view?: { type?: string };
  trigger_id?: string;
  message?: { ts: string; thread_ts?: string };
  actions?: { action_id: string; value?: string; selected_option?: { value: string } }[];
  state?: Parameters<typeof decodeSlackAction>[1];
}

interface InteractivePayload {
  type: string;
  callback_id?: string;
  trigger_id: string;
  user: { id: string };
  channel?: { id: string };
  message?: { ts: string; thread_ts?: string; text?: string };
  view?: {
    callback_id: string;
    state?: { values?: Record<string, Record<string, { value?: string }>> };
  };
}

interface CommandPayload {
  command: string;
  text?: string;
  user_id: string;
  channel_id: string;
  trigger_id?: string;
  response_url: string;
}
