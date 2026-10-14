import "./react.ts";
import { TurnView } from "@vultus/components";
import type { Turn, TurnStatus } from "@vultus/core";
import {
  type Block,
  emptyBudget,
  LIMITS,
  lifecycle,
  reactionChanges,
  renderToSlack,
  type StreamChunk,
  streamHeader,
  streamPages,
  streamStart,
  streamStopTitle,
  streamTrailer,
  streamUpdate,
} from "@vultus/render-slack";
import { type SlackApi, SlackError } from "./slack.ts";

/**
 * One turn on Slack, kept in step with the turn's state as it changes.
 *
 * This is the host loop from vultus's Slack sandbox (`apps/slack-sandbox`,
 * `run.tsx`), driven by a live turn instead of a recorded one: a reaction on
 * the message that asked, the session's status, `chat.startStream`, ordered
 * `chat.appendStream` calls, and `chat.stopStream` with the final blocks. A
 * turn that outgrows one message continues in the next, in the same thread.
 * Every block and every chunk comes from `@vultus/render-slack`.
 *
 * Where native streaming is not available the turn is posted once and edited
 * in place, from the same components.
 */

export interface Target {
  channel: string;
  /** The thread the turn answers in. */
  threadTs: string;
  /** The message that asked. Lifecycle reactions go on it. */
  triggerTs: string;
  recipientUser: string;
  recipientTeam: string;
}

export interface Presented {
  /** The message that holds the final blocks, for a later `chat.update`. */
  ts: string;
  /** Every message the turn used, in order. */
  messages: string[];
}

/** Codes that mean the place itself is unusable, so falling back to a plain post would fail too. */
const NO_PLACE =
  /^(not_in_channel|channel_not_found|is_archived|invalid_auth|not_authed|account_inactive|token_revoked|token_expired|missing_scope|ratelimited)$/;

export class Presenter {
  private sent: Turn | null = null;
  private ts = "";
  private readonly messages: string[] = [];
  private page = 1;
  private budget = emptyBudget();
  /** The status already shown as a reaction and a session status. */
  private shown: TurnStatus | null = null;
  /** Native streaming until Slack refuses it, then post and update. */
  private native: boolean;
  private closed = false;
  /** A write failed mid-turn. Nothing more is written until the turn ends. */
  private degraded = false;
  /** The streamed message was already closed. */
  private stopped = false;
  /** The session status Slack was last told. */
  private session: string | null = null;
  /** A session in a code channel is the channel itself, with no thread. */
  private threadless = false;

  constructor(
    private readonly slack: SlackApi,
    private readonly target: Target,
    private readonly opts: {
      native?: boolean;
      log?: (line: string) => void;
      /** A status the message that asked already shows, such as the reaction for a queued message. */
      shown?: TurnStatus;
    } = {},
  ) {
    this.native = opts.native ?? true;
    this.shown = opts.shown ?? null;
  }

  private log(line: string) {
    this.opts.log?.(line);
  }

  /** Whether native streaming was still in use, so the caller can remember a refusal. */
  get streaming(): boolean {
    return this.native;
  }

  private async react(next: TurnStatus) {
    const { add, remove } = reactionChanges(this.shown, next);
    const at = { channel: this.target.channel, timestamp: this.target.triggerTs };
    for (const name of remove) {
      await this.slack.call("reactions.remove", { ...at, name }).catch(() => {});
    }
    for (const name of add) {
      await this.slack.call("reactions.add", { ...at, name }).catch(() => {});
    }
  }

  private async status(turn: Turn) {
    const { sessionStatus } = lifecycle(turn);
    // Queued and working are both "processing" to Slack: it is told once.
    if (sessionStatus === this.session) return;
    this.session = sessionStatus;
    const args = { channel_id: this.target.channel, status: sessionStatus };
    try {
      await this.slack.call(
        "agents.sessions.setStatus",
        this.threadless ? args : { ...args, thread_ts: this.target.threadTs },
      );
    } catch (e) {
      if (e instanceof SlackError && e.code === "thread_ts_not_allowed" && !this.threadless) {
        this.threadless = true;
        await this.slack
          .call("agents.sessions.setStatus", args)
          .catch((again: Error) => this.log(`session status skipped: ${again.message}`));
        return;
      }
      // A refusal here never costs the answer: it is already in the thread.
      this.log(`session status skipped: ${(e as Error).message}`);
    }
  }

  /** Show a status change once: the reaction, then the session. */
  private async sync(turn: Turn) {
    if (this.shown === turn.status) return;
    await this.react(turn.status);
    await this.status(turn);
    this.shown = turn.status;
  }

  private async open(turn: Turn) {
    const args = {
      channel: this.target.channel,
      thread_ts: this.target.threadTs,
      recipient_user_id: this.target.recipientUser,
      recipient_team_id: this.target.recipientTeam,
      ...streamStart(turn),
    };
    const header = streamHeader(turn, { page: this.page });
    let started: { ts: string };
    try {
      started = await this.slack.call("chat.startStream", {
        ...args,
        ...(header.length > 0 ? { chunks: header } : {}),
      });
    } catch (e) {
      const rejected = e instanceof SlackError && /^invalid_(chunks|blocks)$/.test(e.code);
      if (!rejected || header.length === 0) throw e;
      // The workspace does not take block chunks yet: the continuation opens without its header.
      this.log(`continuation header left out: ${e.message}`);
      started = await this.slack.call("chat.startStream", args);
    }
    this.ts = started.ts;
    this.messages.push(this.ts);
  }

  /** Close the current message and continue in a new one. */
  private async turnPage(turn: Turn, trailer: boolean) {
    const at = { channel: this.target.channel, ts: this.ts };
    if (trailer) {
      await this.slack
        .call("chat.appendStream", { ...at, chunks: [streamTrailer()] })
        .catch(() => {});
    }
    await this.slack.call("chat.stopStream", at);
    this.page += 1;
    await this.open(turn);
  }

  private async append(turn: Turn, chunks: StreamChunk[]) {
    const plan = streamPages(this.budget, chunks);
    for (const [i, part] of plan.pages.entries()) {
      if (i > 0) await this.turnPage(turn, true);
      if (part.length === 0) continue;
      if (!this.ts) await this.open(turn);
      try {
        // Each append is awaited in order, so no task finishes after the stream stops.
        await this.slack.call("chat.appendStream", {
          channel: this.target.channel,
          ts: this.ts,
          chunks: part,
        });
      } catch (e) {
        if (!(e instanceof SlackError) || e.code !== "msg_too_long") throw e;
        // The estimate was off and the message is full: it cannot take a trailer either.
        await this.turnPage(turn, false);
        await this.slack.call("chat.appendStream", {
          channel: this.target.channel,
          ts: this.ts,
          chunks: part,
        });
        plan.budget = streamPages(emptyBudget(), part).budget;
      }
    }
    this.budget = plan.budget;
  }

  private async post(blocks: Block[], text: string) {
    const posted = await this.slack.call<{ ts: string }>("chat.postMessage", {
      channel: this.target.channel,
      thread_ts: this.target.threadTs,
      text: text.slice(0, LIMITS.fallbackText),
      blocks,
    });
    this.ts = posted.ts;
    this.messages.push(this.ts);
  }

  private async edit(blocks: Block[], text: string) {
    await this.slack.call("chat.update", {
      channel: this.target.channel,
      ts: this.ts,
      text: text.slice(0, LIMITS.fallbackText),
      blocks,
    });
  }

  /** Streaming is not available here: carry on by posting and editing. Nothing was streamed yet. */
  private refused(e: unknown): boolean {
    if (!(e instanceof SlackError) || NO_PLACE.test(e.code)) return false;
    if (this.ts) return false;
    this.log(`native streaming is not available (${e.code}), posting and updating instead`);
    this.native = false;
    return true;
  }

  /**
   * Bring Slack up to a turn that is still going. A write Slack refuses does
   * not end the turn: the presenter stops writing as it goes, and `finish`
   * still ends it.
   */
  async update(turn: Turn): Promise<void> {
    if (this.closed || this.degraded) return;
    await this.sync(turn);
    try {
      if (this.native) {
        try {
          if (!this.ts) await this.open(turn);
          const { chunks } = streamUpdate(this.sent, turn);
          if (chunks.length > 0) await this.append(turn, chunks);
          this.sent = turn;
          return;
        } catch (e) {
          if (!this.refused(e)) throw e;
        }
      }
      const view = renderToSlack(<TurnView turn={turn} />);
      const [first = []] = view.pages;
      if (first.length === 0) return;
      if (!this.ts) await this.post(first, view.text);
      else await this.edit(first, view.text);
      this.sent = turn;
    } catch (e) {
      // The rest is written once, whole, when the turn ends.
      this.degraded = true;
      this.log(`writing the turn failed, it will be posted when it ends: ${(e as Error).message}`);
    }
  }

  /**
   * End the turn on Slack with its final layout, and settle the reaction and
   * the session. It always gets as far as the session: when the message being
   * written cannot take the end of the turn, the stream is closed and the
   * whole turn is posted again as a new message.
   */
  async finish(turn: Turn): Promise<Presented> {
    if (this.closed) return { ts: this.ts, messages: this.messages };
    this.closed = true;
    try {
      if (this.degraded) throw new Error("an earlier write failed");
      if (this.native && this.ts) {
        // Flush what was not yet appended, then stop with the final layout.
        const { chunks, rewritten } = streamUpdate(this.sent, turn);
        // Words Slack already has were changed: a stream cannot take that back.
        if (rewritten) throw new Error("the answer was rewritten after it streamed");
        if (chunks.length > 0) await this.append(turn, chunks);
        const title = streamStopTitle(turn);
        const final = renderToSlack(<TurnView turn={turn} streamed />);
        const [first = [], ...rest] = final.pages;
        await this.slack.call("chat.stopStream", {
          channel: this.target.channel,
          ts: this.ts,
          ...(title ? { chunks: [{ type: "plan_update", title }] } : {}),
          blocks: first,
        });
        this.stopped = true;
        await this.overflow(rest, final.text, final.pages.length);
        for (const w of final.warnings) this.log(`render warning: ${w}`);
      } else {
        // Nothing was streamed, so the message carries the whole turn.
        const final = renderToSlack(<TurnView turn={turn} />);
        const [first = [], ...rest] = final.pages;
        if (!this.ts) await this.post(first, final.text);
        else await this.edit(first, final.text);
        await this.overflow(rest, final.text, final.pages.length);
        for (const w of final.warnings) this.log(`render warning: ${w}`);
      }
    } catch (e) {
      this.log(`ending the turn as streamed failed: ${(e as Error).message}`);
      const final = renderToSlack(<TurnView turn={turn} />);
      const [first = [], ...rest] = final.pages;
      // The streamed message is closed with the whole turn as its final layout, so the
      // answer and its controls stay in one message.
      let closed = false;
      if (this.native && this.ts && !this.stopped) {
        closed = await this.slack
          .call("chat.stopStream", { channel: this.target.channel, ts: this.ts, blocks: first })
          .then(
            () => true,
            () => false,
          );
        if (!closed) {
          await this.slack
            .call("chat.stopStream", { channel: this.target.channel, ts: this.ts })
            .catch(() => {});
        }
      }
      try {
        // Only a message that could not take the turn at all is followed by a new one.
        if (!closed && !this.stopped) await this.post(first, final.text);
        await this.overflow(rest, final.text, final.pages.length);
      } catch (again) {
        this.log(`the turn could not be posted: ${(again as Error).message}`);
      }
    }
    // Reactions and the session status are each tried, whatever happened above.
    await this.sync(turn);
    return { ts: this.ts, messages: this.messages };
  }

  /**
   * The end of a turn whose message was already closed while it was still
   * working: the whole turn is posted as a new message in the thread, and the
   * reaction and the session settle.
   */
  async conclude(turn: Turn): Promise<void> {
    const final = renderToSlack(<TurnView turn={turn} />);
    try {
      const [first = [], ...rest] = final.pages;
      await this.post(first, final.text);
      await this.overflow(rest, final.text, final.pages.length);
    } catch (e) {
      this.log(`the end of the turn could not be posted: ${(e as Error).message}`);
    }
    await this.sync(turn);
  }

  /** Final blocks that outgrew one message follow it. Each needs its own fallback text. */
  private async overflow(rest: Block[][], text: string, total: number) {
    for (const [i, blocks] of rest.entries()) {
      // vultus: needs a fallback-text helper for continued pages, such as copy.continued(page, of, text).
      await this.post(blocks, `(${i + 2}/${total}) ${text}`);
    }
  }
}
