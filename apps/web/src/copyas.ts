/**
 * "Copy as": every way an item can leave the console, in one place.
 *
 * An item is a run, an agent's call, one answer, or a tool call. Each kind
 * has formats, listed in `formats`. Copy, Download and Export all go through
 * the same serialisers, so what is pasted and what is saved cannot differ.
 * Adding a format is one entry here. No React in this file.
 */
import type { Run } from "./adapter.ts";
import type { Span } from "./api.ts";
import { cause, elapsed, took, usd } from "./format.ts";

export type RunData = { runs: Run[]; spans: Span[] };

/** A tool call as the console knows it. Arguments and results are present only when the platform reported them. */
export type ToolCall = {
  name: string;
  /** The run that made the call, and its agent. */
  run?: string;
  agent?: string;
  args?: unknown;
  result?: string;
  error?: string;
  start?: string;
  end?: string;
};

export type Item =
  | { kind: "run"; run: Run; data: RunData }
  /** A run another agent started. Same record as a run, different things worth copying. */
  | { kind: "call"; run: Run; data: RunData }
  | { kind: "answer"; run: Run }
  | { kind: "tool"; tool: ToolCall };

export type Format = {
  id: string;
  /** What the menu says. Sentence case: it names a thing, not a button. */
  label: string;
  hint?: string;
  mime: "text/markdown" | "text/plain" | "application/json";
  ext: "md" | "txt" | "json";
  text: () => string;
};

/** A run's status in specific words. */
export function statusWord(run: Run): string {
  switch (run.status) {
    case "accepted":
      return "Queued";
    case "running":
      return run.calls > 0 ? "Running, calling others" : "Running";
    case "parked":
      return run.reason ? `Waiting: ${run.reason}` : "Waiting on you";
    case "failed":
      return "Failed";
    case "stopped":
      return "Stopped";
    case "completed":
      return "Done";
  }
}

const SECRET = /secret|token|password|passwd|api[_-]?key|authorization|credential|cookie/i;

/** A copy of a value with anything under a secret-looking key replaced. Applied to every JSON that leaves. */
export function redact<T>(value: T): T {
  if (Array.isArray(value)) return value.map(redact) as T;
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([k, v]) => [k, SECRET.test(k) && v !== null && v !== undefined ? "[redacted]" : redact(v)]),
    ) as T;
  }
  return value;
}

const json = (value: unknown) => `${JSON.stringify(redact(value), null, 2)}\n`;

/** Markdown with its marks taken off, for pasting where they would show as symbols. */
export function plain(markdown: string): string {
  return markdown
    .replace(/^#{1,6} /gm, "")
    .replace(/^> ?/gm, "")
    .replace(/\*\*(.+?)\*\*/g, "$1")
    .replace(/`([^`]+)`/g, "$1");
}

/** Which variant produced a run: the agent, its revision, and how it was called. */
export function variantOf(run: Run): string {
  return `${run.agent}, revision ${run.rev}${run.mode ? `, called as ${run.mode}` : ""}`;
}

function runRecord(r: Run) {
  return {
    id: r.id,
    parent: r.parent ?? null,
    depth: r.depth,
    mode: r.mode ?? null,
    agent: r.agent,
    revision: r.rev,
    origin: r.origin,
    project: r.project ?? null,
    status: r.status,
    request: r.text,
    answer: r.answer ?? null,
    error: r.error ?? null,
    waiting_for: r.reason ?? null,
    lease_micro_usd: r.lease,
    granted_micro_usd: r.granted,
    calls: r.calls,
    started_at: r.created_at,
    updated_at: r.updated_at,
    duration_ms: elapsed(r),
  };
}

/** Everything about a run and what it called, as one object a person can keep or hand on. */
export function runManifest(run: Run, data: RunData) {
  const runs = data.runs.length > 0 ? data.runs : [run];
  return {
    manifest: 1,
    exported_at: new Date().toISOString(),
    run: run.id,
    thread: run.thread,
    agents: [...new Map(runs.map((r) => [r.agent, { name: r.agent, revision: r.rev }])).values()],
    runs: runs.map(runRecord),
    spans: data.spans,
  };
}

const quote = (text: string) =>
  text
    .trim()
    .split("\n")
    .map((l) => `> ${l}`)
    .join("\n");

const toolsOf = (data: RunData, id: string) => data.spans.filter((s) => s.kind === "tool" && s.run_id === id);

/** A run and what it called as Markdown a person can read, paste or file. */
export function runMarkdown(run: Run, data: RunData): string {
  const runs = data.runs.length > 0 ? data.runs : [run];
  const out: string[] = [];
  const section = (r: Run, level: number) => {
    const who = r.id === run.id ? r.agent : `${r.agent}${r.mode ? ` (${r.mode})` : ""}`;
    out.push(`${"#".repeat(level)} ${who}`, "");
    out.push(`- Status: ${statusWord(r)}`, `- Took: ${took(elapsed(r))}`, `- Agent revision: ${r.rev}`, `- Run: \`${r.id}\``);
    if (r.lease > 0) out.push(`- Budget: ${usd(r.granted)} of ${usd(r.lease)} handed to its calls`);
    out.push("", "**Asked**", "", quote(r.text || "No request text"), "");
    if (r.answer) out.push("**Answered**", "", r.answer.trim(), "");
    if (r.status === "failed") out.push("**Failed**", "", cause(r.error) || "The run failed without saying why.", "");
    if (r.status === "stopped") out.push("**Stopped**", "", r.cause || "The run was stopped before it finished.", "");
    const tools = toolsOf(data, r.id);
    if (tools.length > 0) {
      out.push("**Tools**", "");
      for (const s of tools) {
        const how = s.error ? `failed: ${cause(s.error)}` : s.end ? took(Date.parse(s.end) - Date.parse(s.start)) : "still running";
        out.push(`- \`${s.name}\`, ${how}`);
      }
      out.push("");
    }
    for (const child of runs.filter((c) => c.parent === r.id).sort((a, b) => a.created_at.localeCompare(b.created_at))) {
      section(child, Math.min(level + 1, 6));
    }
  };
  section(run, 1);
  return `${out.join("\n").trimEnd()}\n`;
}

/** A tool span from a trace as a tool call item. */
export function toolOf(span: Span): ToolCall {
  return { name: span.name, run: span.run_id, agent: span.agent, start: span.start, end: span.end, error: span.error };
}

/** The call itself: its name, and its arguments when they are known. */
export function toolCallText(t: ToolCall): string {
  return t.args === undefined ? `${t.name}()` : `${t.name}(${JSON.stringify(redact(t.args), null, 2)})`;
}

/** What came back: the result, the failure, or that nothing has yet. */
export function toolResultText(t: ToolCall): string {
  if (t.error) return `Failed: ${cause(t.error)}`;
  if (t.result !== undefined && t.result !== "") return t.result;
  return t.end ? "It returned nothing." : "No result yet. The call is still running.";
}

export function toolMarkdown(t: ToolCall): string {
  const out = [`**Tool** \`${t.name}\`${t.agent ? `, called by ${t.agent}` : ""}`, ""];
  if (t.start && t.end) out.push(`- Took: ${took(Date.parse(t.end) - Date.parse(t.start))}`);
  out.push(`- Status: ${t.error ? "Failed" : t.end || t.result !== undefined ? "Done" : "Running"}`, "");
  if (t.args !== undefined) out.push("**Arguments**", "", "```json", JSON.stringify(redact(t.args), null, 2), "```", "");
  out.push("**Result**", "", toolResultText(t));
  return `${out.join("\n").trimEnd()}\n`;
}

const md = (id: string, label: string, text: () => string, hint?: string): Format => ({ id, label, hint, mime: "text/markdown", ext: "md", text });
const txt = (id: string, label: string, text: () => string, hint?: string): Format => ({ id, label, hint, mime: "text/plain", ext: "txt", text });
const js = (id: string, label: string, value: () => unknown, hint?: string): Format => ({
  id,
  label,
  hint,
  mime: "application/json",
  ext: "json",
  text: () => json(value()),
});

/**
 * The formats an item can be copied as, in the order the menu shows them.
 * A format is listed only when the item has the data for it: a tool call
 * with no arguments reported has no "arguments" entry.
 */
export function formats(item: Item): Format[] {
  switch (item.kind) {
    case "run": {
      const { run, data } = item;
      return [
        md("markdown", "Markdown", () => runMarkdown(run, data), "The request, the answer and every call."),
        txt("text", "Plain text", () => plain(runMarkdown(run, data))),
        js("json", "JSON manifest", () => runManifest(run, data), "Runs, agents and revisions, tools, timings and statuses."),
      ];
    }
    case "call": {
      const { run, data } = item;
      return [
        txt("asked", "What it was asked", () => `${(run.text || "").trim()}\n`),
        ...(run.answer ? [md("answered", "What it answered", () => `${run.answer?.trim()}\n`)] : []),
        ...(run.status === "failed" ? [txt("failure", "Why it failed", () => `${cause(run.error) || "The run failed without saying why."}\n`)] : []),
        md("markdown", "Its whole conversation", () => runMarkdown(run, data), `As Markdown. ${variantOf(run)}.`),
        txt("text", "Its whole conversation, plain text", () => plain(runMarkdown(run, data))),
        js("json", "JSON manifest", () => runManifest(run, data)),
      ];
    }
    case "answer": {
      const { run } = item;
      const answer = (run.answer ?? "").trim();
      return [
        ...(answer ? [md("markdown", "Markdown", () => `${answer}\n`), txt("text", "Plain text", () => `${plain(answer)}\n`)] : []),
        js("json", "JSON", () => runRecord(run), `The run's record. ${variantOf(run)}.`),
      ];
    }
    case "tool": {
      const t = item.tool;
      const has = t.result !== undefined || t.error !== undefined || t.end !== undefined;
      return [
        txt("call", "The call", () => `${toolCallText(t)}\n`, t.args === undefined ? "Its name. The platform did not report arguments." : "Its name and arguments."),
        ...(has ? [txt("result", "The result", () => `${toolResultText(t)}\n`)] : []),
        md("both", "Call and result", () => toolMarkdown(t), "As Markdown."),
        ...(t.args !== undefined ? [js("args", "Arguments as JSON", () => t.args)] : []),
        js("json", "JSON", () => t, "The call as the console holds it."),
      ];
    }
  }
}

/** A file name for an item in a format: run-r2.md, tool-git_tag.json. */
export function fileName(item: Item, format: Format): string {
  const safe = (s: string) => s.replace(/[^A-Za-z0-9_.-]+/g, "-").slice(0, 60);
  const stem = item.kind === "tool" ? `tool-${safe(item.tool.name)}` : `${item.kind === "answer" ? "answer" : "run"}-${safe(item.run.id)}`;
  return format.id === "markdown" || format.id === "json" || format.id === "text" ? `${stem}.${format.ext}` : `${stem}-${format.id}.${format.ext}`;
}

/** Past this size a copy becomes a download: a clipboard this full is rarely what was wanted. */
export const COPY_LIMIT = 200_000;
