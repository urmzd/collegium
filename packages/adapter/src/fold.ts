import { emptyTurn, reduce, type Turn, type TurnEvent } from "@vultus/core";
import { adapter, type BusEvent, type Run, turnOf } from "./run.ts";

/**
 * A run as one vultus turn, kept current. This is the state every surface
 * renders from: the console hands it to its web components and the Slack app
 * hands the same object to the Block Kit renderer.
 */
export interface Follower {
  /** The turn as it stands. */
  readonly turn: Turn;
  /** Fold one of the run's events in. */
  apply(e: BusEvent): Turn;
  /** Fold vultus events in directly, for what a surface itself knows, such as a stop it asked for. */
  push(...events: TurnEvent[]): Turn;
  /**
   * Bring the turn in line with the run's record. The ledger decides whether a
   * run is over, and events only report attempts, so a run that ended without
   * a final event (a stop, a reconciled failure) is ended here.
   */
  settle(run: Run): Turn;
}

type Options = Parameters<typeof adapter>[0];

/** Follow one run from its first event. */
export function follow(run: Pick<Run, "id" | "agent">, opts: Options = {}): Follower {
  const to = adapter(opts);
  let turn = emptyTurn(run.id, { name: run.agent });
  const push = (...events: TurnEvent[]) => {
    turn = events.reduce(reduce, turn);
    return turn;
  };
  return {
    get turn() {
      return turn;
    },
    apply: (e) => push(...to(e)),
    push,
    settle: (record) => {
      // The record's answer is the whole answer. When the streamed text is not
      // where it begins, the stream missed or changed something: the record wins.
      if (record.status === "completed" && record.answer && !ended(turn)) {
        if (!record.answer.startsWith(turn.text)) turn = { ...turn, text: "" };
      }
      return push(...settled(turn, record));
    },
  };
}

/** A turn rebuilt from the events the journal still holds. Undefined when it holds none. */
export function replay(run: Pick<Run, "id" | "agent">, events: BusEvent[], opts: Options = {}): Turn | undefined {
  if (events.length === 0) return undefined;
  const f = follow(run, opts);
  for (const e of events) f.apply(e);
  return f.turn;
}

/** Whether a turn has reached an end no event will move it from. */
export function ended(turn: Turn): boolean {
  return turn.status === "done" || turn.status === "failed" || turn.status === "stopped";
}

/** The events that take a turn to where its run's record says it ended. None while the run is still going, or once the turn agrees. */
export function settled(turn: Turn, run: Run): TurnEvent[] {
  if (ended(turn)) return [];
  // What the stream did not deliver is the rest of the record's answer: the
  // whole answer of a completed run, the partial answer of a stopped one.
  const rest: TurnEvent[] =
    run.answer?.startsWith(turn.text) && run.answer.length > turn.text.length
      ? [{ type: "text.delta", text: run.answer.slice(turn.text.length) }]
      : [];
  switch (run.status) {
    case "completed":
      return [...rest, { type: "turn.completed" }];
    case "failed": {
      const failure = turnOf(run).failure;
      return failure ? [{ type: "turn.failed", failure }] : [];
    }
    case "stopped":
      // The platform has confirmed the stop: the run can start nothing more,
      // and what it had said is its partial answer.
      return [...rest, { type: "turn.stopped" }, { type: "stop.updated", stop: "confirmed" }];
    default:
      return [];
  }
}

/** What asking a turn again means to the platform: the same words, to the same agent, from the same point in the conversation. */
export function retryOf(run: Run): { agent: string; text: string; after?: string } {
  return { agent: run.agent, text: run.text, ...(run.prev ? { after: run.prev } : {}) };
}
