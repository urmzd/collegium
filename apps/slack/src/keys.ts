import { createHash } from "node:crypto";

/**
 * How a Slack thread names a collegium conversation, and how one Slack event
 * names one run. Both are derived, never stored, so a restart loses neither.
 *
 * The platform makes a run's id from the workspace, the caller and the
 * idempotency key it was started with, and a conversation's id is the id of
 * its first run. So the
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

/**
 * The trigger id the platform gives a run this caller starts with a key: the
 * run's `tag_id`, `api-<length of subject in bytes>-<subject>-<key>`. The
 * length makes it unambiguous: subject "a" with key "b-c" is not subject "a-b"
 * with key "c".
 */
export function tagId(subject: string, key: string): string {
  return `api-${Buffer.byteLength(subject, "utf8")}-${subject}-${key}`;
}

/**
 * The id the platform gives a run this caller starts with a key. The runtime
 * derives it from the workspace and the trigger id together (mandatum's
 * runtime.RunID): a sha256 over each, length-prefixed, cut to 16 bytes.
 */
export function runId(tenant: string, subject: string, key: string): string {
  const h = createHash("sha256");
  for (const field of [tenant, tagId(subject, key)]) {
    const bytes = Buffer.from(field, "utf8");
    h.update(uvarint(bytes.length));
    h.update(bytes);
  }
  return `r${h.digest("hex").slice(0, 32)}`;
}

function uvarint(n: number): Buffer {
  const out: number[] = [];
  while (n >= 0x80) {
    out.push((n & 0x7f) | 0x80);
    n = Math.floor(n / 128);
  }
  out.push(n);
  return Buffer.from(out);
}

/**
 * The Slack thread a conversation was named from, given the `tag_id` of its
 * first run, or null when this app did not name it. A run id cannot be turned
 * back into a thread, so this reads the trigger id the run carries.
 */
export function sessionOf(subject: string, firstTagId: string): Session | null {
  const prefix = `${tagId(subject, "")}slack-`;
  if (!firstTagId.startsWith(prefix)) return null;
  const m = /^([A-Za-z0-9]+)-([A-Za-z0-9]+)-(\d+)-(\d+)$/.exec(firstTagId.slice(prefix.length));
  if (!m) return null;
  return { team: m[1] as string, channel: m[2] as string, threadTs: `${m[3]}.${m[4]}` };
}

/** Names one session inside this process. */
export const sessionName = (s: Session) => `${s.team}/${s.channel}/${s.threadTs}`;
