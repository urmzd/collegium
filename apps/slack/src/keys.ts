/**
 * How a Slack thread names a collegium conversation, and how one Slack event
 * names one run. Both are derived, never stored, so a restart loses neither.
 *
 * The platform makes a run's id from the caller and the idempotency key it was
 * started with, and a conversation's id is the id of its first run. So the
 * first run this app starts in a Slack thread is keyed by the thread itself,
 * which makes the conversation's id something the thread alone determines.
 * Later messages are keyed by their own timestamp.
 */

/** A thread in a channel: where a conversation lives on Slack. */
export interface Session {
  team: string;
  channel: string;
  threadTs: string;
}

/** Run ids become bus topic segments, which hold letters, digits, `_` and `-` only. */
const safe = (s: string) => s.replace(/[^A-Za-z0-9_]/g, "-");

/** The idempotency key of the first run in a Slack thread. */
export function threadKey(s: Session): string {
  return `slack-${safe(s.team)}-${safe(s.channel)}-${safe(s.threadTs)}`;
}

/**
 * The idempotency key of the run a Slack event asks for. A message is named by
 * its timestamp, which Slack repeats on every redelivery. A shortcut or a
 * command has no message of its own, so `extra` carries its trigger id.
 */
export function eventKey(s: Session, ts: string, extra?: string): string {
  const key = `slack-${safe(s.team)}-${safe(s.channel)}-${safe(ts)}`;
  return extra ? `${key}-${safe(extra)}` : key;
}

/** The id the platform gives a run this caller starts with a key. */
export function runId(subject: string, key: string): string {
  return `api-${subject}-${key}`;
}

/** The Slack thread a conversation id was made from, or null when this app did not make it. */
export function sessionOf(subject: string, conversation: string): Session | null {
  const prefix = `api-${subject}-slack-`;
  if (!conversation.startsWith(prefix)) return null;
  const m = /^([A-Za-z0-9]+)-([A-Za-z0-9]+)-(\d+)-(\d+)$/.exec(conversation.slice(prefix.length));
  if (!m) return null;
  return { team: m[1] as string, channel: m[2] as string, threadTs: `${m[3]}.${m[4]}` };
}

/** Names one session inside this process. */
export const sessionName = (s: Session) => `${s.team}/${s.channel}/${s.threadTs}`;
