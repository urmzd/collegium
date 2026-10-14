import { ActionId, type Approval, type Clarification, type Interrupt, type UiAction } from "@vultus/core";

/**
 * A question a run is parked on, as one shape for every surface: on the run
 * it is an interrupt (vultus's approval or clarification card), in the inbox
 * it is the same card inside a message part. Both are built here, so a
 * question looks and answers the same in the console and in Slack, whether
 * it is read on the run or in the inbox.
 */

/** A question as the platform sends it: `payload.Question` on a run, `question` on an inbox part. */
export interface WireQuestion {
  id: string;
  kind: string;
  agent?: string;
  prompt: string;
  options?: readonly string[];
  tool?: { name: string; call_id?: string; arguments?: unknown; schema?: unknown };
  asked_of?: string;
  expires_at?: string;
}

/** How a question was settled: a person's answer, or what happened instead. */
export interface Settled {
  /** approve, deny, choose, reply, expired, stopped or closed. */
  action: string;
  by?: string;
  at: string;
  /** The option chosen, the words replied, a note on a decision, or why nobody answered. */
  note?: string;
}

/** The runtime's answer (`lifecycle.resumed`) as a settlement. */
export function settledOf(answer: {
  decision?: string;
  choice?: string;
  text?: string;
  note?: string;
  by?: string;
  at?: string;
  expired?: boolean;
}): Settled {
  const at = answer.at ?? "";
  if (answer.expired) {
    // The same words the platform settles the inbox message with.
    const went = answer.choice || answer.text;
    const note =
      answer.decision === "deny"
        ? "Nobody answered in time, so it was denied"
        : went
          ? `Nobody answered in time, so it went with ${went}`
          : "Nobody answered in time";
    return { action: "expired", at, note };
  }
  if (answer.decision === "approve" || answer.decision === "deny") {
    return { action: answer.decision, by: answer.by, at, ...(answer.note ? { note: answer.note } : {}) };
  }
  if (answer.choice) return { action: "choose", by: answer.by, at, note: answer.choice };
  return { action: "reply", by: answer.by, at, note: answer.text ?? "" };
}

/** An inbox part's outcome as a settlement. */
export function settledFromOutcome(outcome: { action?: unknown; by?: unknown; at?: unknown; note?: unknown }): Settled | undefined {
  const action = typeof outcome.action === "string" ? outcome.action : "";
  if (!action) return undefined;
  const text = (v: unknown) => (typeof v === "string" ? v : "");
  return { action, by: text(outcome.by) || undefined, at: text(outcome.at), ...(text(outcome.note) ? { note: text(outcome.note) } : {}) };
}

const NOBODY = "Nobody";

function decisionOf(s: Settled | undefined): Approval["decision"] {
  if (!s) return { kind: "pending" };
  switch (s.action) {
    case "approve":
      return { kind: "approved", by: s.by ?? "", at: s.at, ...(s.note ? { reason: s.note } : {}) };
    case "deny":
      return { kind: "denied", by: s.by ?? "", at: s.at, ...(s.note ? { reason: s.note } : {}) };
    case "expired":
      return { kind: "expired", at: s.at };
    default:
      // Stopped, or the run ended without an answer.
      return { kind: "cancelled", at: s.at };
  }
}

// vultus: Clarification.answer has no state for "nobody answered in time" or
// "the run was stopped", so both read as declined, with the reason in words.
function answerOf(s: Settled | undefined, options: readonly string[]): Clarification["answer"] {
  if (!s) return { kind: "pending" };
  switch (s.action) {
    case "choose":
      return { kind: "answered", by: s.by ?? "", at: s.at, optionIds: options.includes(s.note ?? "") ? [s.note ?? ""] : [], ...(options.includes(s.note ?? "") ? {} : { other: s.note ?? "" }) };
    case "reply":
      return { kind: "answered", by: s.by ?? "", at: s.at, optionIds: [], other: s.note ?? "" };
    case "expired":
      return { kind: "declined", by: NOBODY, at: s.at, reason: s.note ?? "Nobody answered in time" };
    default:
      return { kind: "declined", by: s.by && s.by !== "system" ? s.by : NOBODY, at: s.at, ...(s.note ? { reason: s.note } : {}) };
  }
}

/** The tool call's arguments, when they are an object. */
function argsOf(q: WireQuestion): Record<string, unknown> | undefined {
  const a = q.tool?.arguments;
  return a && typeof a === "object" && !Array.isArray(a) ? (a as Record<string, unknown>) : undefined;
}

/**
 * The question as vultus's interrupt. An approval is an approval card with the
 * call's arguments; a choice is a clarification with its options; a reply is
 * a clarification that takes words. A question a person does not answer (a
 * delegation) is null.
 */
export function interruptOf(q: WireQuestion, settled?: Settled): Interrupt | null {
  const options = q.options ?? [];
  switch (q.kind) {
    case "approval": {
      const args = argsOf(q);
      return {
        type: "approval",
        id: q.id,
        summary: q.prompt,
        tool: q.tool?.name ?? "",
        ...(args ? { args } : {}),
        ...(q.expires_at ? { expiresAt: q.expires_at } : {}),
        // vultus: an editor for a tool's arguments from its schema, on web and in a Slack modal,
        // before `editable` can be offered. The API takes edited arguments already.
        decision: decisionOf(settled),
      };
    }
    case "choice":
    case "reply":
      return {
        type: "clarification",
        id: q.id,
        question: q.prompt,
        options: options.map((o) => ({ id: o, label: o })),
        ...(q.kind === "reply" ? { allowOther: true } : {}),
        answer: answerOf(settled, options),
      };
    default:
      return null;
  }
}

/** What answering a question asks of the platform: the run, the question, and the body of `answerQuestion`. */
export interface AnswerRequest {
  run: string;
  question: string;
  body: { action: "approve" | "deny" | "choose" | "reply"; choice?: string; text?: string; note?: string };
}

/**
 * A click on a question shown on a run, as an answer. `turnId` is the run and
 * `target` the question. Null for any other action, or a reply with no words.
 */
export function answerOfAction(action: UiAction): AnswerRequest | null {
  const { id, payload } = action;
  const at = { run: payload.turnId, question: payload.target };
  if (!at.run || !at.question) return null;
  switch (id) {
    case ActionId.approve:
      return { ...at, body: { action: "approve" } };
    case ActionId.deny:
      return { ...at, body: { action: "deny" } };
    case ActionId.clarify:
      return payload.option ? { ...at, body: { action: "choose", choice: payload.option } } : null;
    case ActionId.clarifyOther:
      return payload.text?.trim() ? { ...at, body: { action: "reply", text: payload.text.trim() } } : null;
    default:
      return null;
  }
}

/** True when an action answers a question: something only a person on the run's side can do. */
export const answers = (action: UiAction) =>
  action.id === ActionId.approve || action.id === ActionId.deny || action.id === ActionId.clarify || action.id === ActionId.clarifyOther;

/** The route an answer is sent to. */
export const answerPath = (r: AnswerRequest) =>
  `/api/runs/${encodeURIComponent(r.run)}/questions/${encodeURIComponent(r.question)}/answer`;
