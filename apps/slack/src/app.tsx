import "./react.ts";
import { createHash } from "node:crypto";
import {
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
import { decodeSlackAction, LIMITS, reactionChanges, renderToSlack } from "@vultus/render-slack";
import { choose, type Routing } from "./agents.ts";
import {
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
  /** Stop was pressed. Nothing more of the run is rendered. */
  stopped: boolean;
  wake(item: Item): void;
}

interface Thread {
  queue: QueueState;
  jobs: Map<string, Job>;
  live?: Live;
}

const terminal = (s: Run["status"]) => s === "completed" || s === "failed" || s === "stopped";

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
function forSlack(message: PlatformMessage): PlatformMessage {
  return {
    ...message,
    parts: message.parts.map((p) => (p.type === "budget" ? { ...p, allowed: [] } : p)),
  };
}

const short = (s: string) => createHash("sha256").update(s).digest("hex").slice(0, 20);

export class App {
  private readonly threads = new Map<string, Thread>();
  /** Slack deliveries already taken, so a redelivery in this process does nothing. */
  private readonly seen = new Recent<true>(10_000);
  /** Who asked for each run, by Slack user id. */
  private readonly requesters = new Recent<string>(10_000);
  /** Inbox messages posted to a thread, so a change edits the post instead of adding one. */
  private readonly mail = new Recent<{ channel: string; ts: string }>(2_000);
  private readonly work = new Set<Promise<unknown>>();
  private native = true;
  private agentList: { at: number; agents: Agent[] } | null = null;
  private clientRecord: { at: number; client: ClientRecord | null } | null = null;
  private readonly flushMs: number;
  private readonly pollMs: number;

  constructor(
    private readonly slack: SlackApi,
    private readonly collegium: Collegium,
    private readonly opts: AppOptions,
  ) {
    this.flushMs = opts.flushMs ?? 700;
    this.pollMs = opts.pollMs ?? 5_000;
  }

  private log(line: string) {
    this.opts.log?.(line);
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
      // A client that is gone, or is for another surface or team, routes to nothing.
      const ours =
        found &&
        found.surface === "slack" &&
        (!found.identity?.team || found.identity.team === this.opts.team);
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
    const turns = (await this.collegium.getThread(runId(this.opts.subject, first)))?.turns;
    // The first run in a thread is keyed by the thread, which is what names the conversation.
    if (!turns || turns.length === 0) return { key: first };
    if (job.key === first) return "duplicate";
    if (turns.some((t) => t.id === runId(this.opts.subject, job.key))) return "duplicate";
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
      if (!job.again) {
        const choice = choose(job.text, await this.agents(), await this.routing());
        if (choice.agent === null) {
          return this.refuse(job, presenter, "", {
            kind: "forbidden",
            message: "No agent answers here yet. Name one this app may reach, as @name.",
          });
        }
        agent = choice.agent;
        text = choice.text;
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
      run = await this.collegium.createRun(
        { agent, text, ...(placed.after ? { after: placed.after } : {}) },
        placed.key,
      );
    } catch (e) {
      if (!(e instanceof CollegiumError)) throw e;
      const refusal = e.refusal;
      const wait = refusal?.resets_at
        ? Math.max(0, Math.round((Date.parse(refusal.resets_at) - Date.now()) / 1000))
        : undefined;
      return this.refuse(job, presenter, agent, {
        kind:
          e.status === 429
            ? "rate_limited"
            : e.status === 402 || e.status === 403
              ? "forbidden"
              : e.status >= 500
                ? "unavailable"
                : "internal",
        message: e.message,
        httpStatus: e.status,
        ...(wait ? { retryAfterSeconds: wait } : {}),
      });
    }

    live.run = run;
    if (live.stopped) {
      // Stop was pressed while the run was being started, so it is told now.
      const told = await this.collegium.stopRun(run.id).catch(() => undefined);
      items.push({ type: "stopped", run: told });
    }
    this.requesters.set(run.id, job.user);
    this.log(`run ${run.id} for ${job.user} in ${job.session.channel}, answered by ${run.agent}`);
    const following = follow(run, { href: this.href });
    const abort = new AbortController();
    const pump = this.pump(run, following, presenter, live, items, abort.signal);
    let turn: Turn;
    try {
      turn = await pump;
    } finally {
      abort.abort();
    }
    await presenter.finish(turn);
    this.native = this.native && presenter.streaming;
    this.log(`run ${run.id} ended ${turn.status}`);
    return turn.status === "done" ? "done" : turn.status === "stopped" ? "stopped" : "failed";
  }

  /**
   * Fold a run's events into its turn and keep Slack in step, until the turn
   * ends. The record is read beside the stream, because the ledger decides
   * whether a run is over: a stop from the console publishes no final event.
   */
  private async pump(
    run: Run,
    following: Follower,
    presenter: Presenter,
    live: Live,
    items: Mailbox<Item>,
    signal: AbortSignal,
  ): Promise<Turn> {
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
    for (;;) {
      const item = await items.next();
      if (item.type === "stopped") {
        if (item.run && terminal(item.run.status)) following.settle(item.run);
        // Collegium did not confirm: the turn stops here, and says it may still finish elsewhere.
        if (!ended(following.turn)) {
          following.push({ type: "turn.stopped" }, { type: "stop.updated", stop: "failed" });
        }
        return following.turn;
      }
      // Stop was pressed: nothing more of this run is rendered while collegium is told.
      if (live.stopped) continue;
      if (item.type === "event" && item.event.run_id === run.id) {
        const before = following.turn;
        dirty = following.apply(item.event) !== before || dirty;
      } else if (item.type === "record" && terminal(item.run.status)) {
        // A stopped run publishes nothing more. An ended one is given one more read
        // for its final event to arrive, so the streamed answer is not cut short.
        over += 1;
        if (item.run.status === "stopped" || over > 1) following.settle(item.run);
      } else if (item.type === "broken") {
        this.log(`event stream for ${run.id} ended: ${item.error.message}`);
        await read();
      }
      if (ended(following.turn)) return following.turn;
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
    const conversation = runId(this.opts.subject, threadKey(session));
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
    // Runs this process is not following: confirm each in the thread.
    for (const run of stopped) {
      if (run.id === following?.id || run.status !== "stopped") continue;
      const turn = follow(run, { href: this.href }).settle(run);
      const view = renderToSlack(<TurnView turn={turn} />);
      await this.slack
        .call("chat.postMessage", {
          channel: session.channel,
          thread_ts: session.threadTs,
          text: view.text.slice(0, LIMITS.fallbackText),
          blocks: view.blocks,
        })
        .catch((e: Error) => this.log(`stop not confirmed in the thread: ${e.message}`));
    }
    if (!live) {
      await this.slack
        .call("agents.sessions.setStatus", {
          channel_id: session.channel,
          thread_ts: session.threadTs,
          status: "active",
        })
        .catch((e: Error) => this.log(`session status skipped: ${e.message}`));
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
    if (ui.id === ActionId.retry || ui.id === ActionId.resume) {
      const run = await this.collegium.getRun(ui.payload.turnId);
      if (!run || !this.again(run, p.user.id, p.message?.ts)) {
        await this.whisper(p, defaultCopy.cantRetry);
      }
    }
    // Opening a called run, reporting and reconnecting have nowhere to go from Slack yet.
  }

  /** Ask a turn again, in the thread it was asked in. False when that thread is not this app's. */
  private again(run: Run, user: string, clickedTs?: string): boolean {
    const session = sessionOf(this.opts.subject, run.thread);
    if (!session || session.team !== this.opts.team) return false;
    const { agent, text, after } = retryOf(run);
    this.enqueue({
      session,
      text,
      triggerTs: clickedTs ?? session.threadTs,
      user,
      // One retry per run: a second click joins the first.
      key: `slack-again-${short(run.id)}`,
      // The first turn of a conversation is asked again after itself, so the thread stays one conversation.
      again: { agent, after: after ?? run.id },
    });
    return true;
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

  // The inbox

  /** Whether a Slack person may settle an inbox message here: they asked for the run, or they are an operator. */
  private maySettle(user: string, message: PlatformMessage): boolean {
    if ((this.opts.operators ?? []).includes(user)) return true;
    return message.about.some((s) => s.kind === "run" && this.requesters.get(s.id) === user);
  }

  private async onMail(p: ActionPayload, ui: UiAction, message: PlatformMessage) {
    if (!this.maySettle(p.user.id, message)) return this.whisper(p, defaultCopy.notYours);
    // A failed turn is asked again here, where its answer can be shown, when the thread is this app's.
    const part = message.parts.find((x) => partMatches(x, ui.payload.target));
    let request = toRequest(message, ui);
    if (ui.id === ActionId.retry && part?.type === "failure" && part.failure.reference) {
      const run = await this.collegium.getRun(part.failure.reference);
      if (run && this.again(run, p.user.id, p.message?.ts)) {
        request = { call: "resolve", partId: part.id, action: "acknowledge" };
      }
    }
    if (!request) return;
    let updated: PlatformMessage;
    try {
      updated = await this.collegium.inbox.send(message, request);
    } catch (e) {
      if (!(e instanceof CollegiumError)) throw e;
      return this.whisper(p, e.message);
    }
    const channel = p.channel?.id ?? p.container?.channel_id;
    if (p.container?.type === "view" || p.view?.type === "home") {
      await this.publishHome(p.user.id);
    } else if (channel && p.message?.ts) {
      await this.showMail(updated, { channel, ts: p.message.ts });
    }
  }

  private async showMail(message: PlatformMessage, at: { channel: string; ts: string }) {
    const view = renderToSlack(<MessageView message={forSlack(message)} />);
    await this.slack.call("chat.update", {
      channel: at.channel,
      ts: at.ts,
      text: view.text.slice(0, LIMITS.fallbackText),
      blocks: view.blocks,
    });
  }

  /** An inbox message changed. One about a run in this app's thread is shown there, once, and kept current. */
  private async onNotice(notice: InboxNotice): Promise<void> {
    const message = await this.collegium.inbox.get(notice.id).catch(() => null);
    if (!message) return;
    const posted = this.mail.get(message.id);
    if (posted) return this.showMail(message, posted);
    if (messageState(message) !== "open") return;
    // A failed turn already says so in its own message, with Retry.
    if (message.kind === "failure" || message.parts.some((p) => p.type === "failure")) return;
    const about = message.about.find((s) => s.kind === "run");
    const run = about ? await this.collegium.getRun(about.id) : null;
    const session = run ? sessionOf(this.opts.subject, run.thread) : null;
    if (!session || session.team !== this.opts.team) return;
    const view = renderToSlack(<MessageView message={forSlack(message)} />);
    const sent = await this.slack.call<{ ts: string }>("chat.postMessage", {
      channel: session.channel,
      thread_ts: session.threadTs,
      text: view.text.slice(0, LIMITS.fallbackText),
      blocks: view.blocks,
    });
    this.mail.set(message.id, { channel: session.channel, ts: sent.ts });
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
        text: `<@${where.user}>: ${text}`,
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
