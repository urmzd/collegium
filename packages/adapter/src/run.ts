import type { ArtifactKind, Interrupt, Task, Turn, TurnEvent } from "@vultus/core";
import { interruptOf, settledOf, type WireQuestion } from "./question.ts";

/**
 * The one place mandatum's wire format meets vultus's event model, for every
 * surface: the console and the Slack app both read runs through this file, so
 * the same run is the same turn in both. Everything above it speaks TurnEvent,
 * and everything below it speaks envelopes.
 */

/** An mandatum envelope.Event as the platform's event stream sends it. */
export interface BusEvent {
  seq: number;
  topic: string;
  run_id: string;
  tenant: string;
  agent: string;
  rev: number;
  origin: string;
  kind: string;
  /** Base64 of the kind-specific JSON body. */
  payload?: string;
  at: string;
}

export type ResponseFormat =
  | { type: "text"; max_chars?: number }
  | { type: "summary"; max_words?: number }
  | { type: "json_schema"; schema: Record<string, unknown> | boolean };

export type InputPart =
  | { type: "text"; text: string }
  | { type: "conversation_range"; thread: string; from: string; through: string }
  | { type: "artifact"; id: string; version: number }
  | { type: "run_result"; run: string };

export interface AgentCallInput {
  messages: { role: "user" | "assistant"; content: InputPart[] }[];
  response_format: ResponseFormat;
}

export interface PreparedCallInput {
  input: AgentCallInput;
  messages: { role: "user" | "assistant"; text: string }[];
}


/** Money in integer millionths of a currency, from saige's price catalog. */
export interface Cost {
  micros: number;
  currency: string;
  as_of?: string;
}

/** What some model calls used. `cost` is a lower bound when `unpriced_calls` is set. */
export interface Usage {
  model_calls?: number;
  requests?: number;
  prompt_tokens?: number;
  cached_prompt_tokens?: number;
  cache_write_tokens?: number;
  completion_tokens?: number;
  cost?: Cost;
  other_costs?: Cost[];
  unpriced_calls?: number;
}

/** What a run used: its own calls, the attached runs it rolled up, and how full its context was. */
export interface RunUsage {
  own: Usage;
  children?: Usage;
  total: Usage;
  context?: { model?: string; window?: number; tokens?: number };
}

/** A run as the platform's index reports it. */
export interface Run {
  id: string;
  tenant: string;
  agent: string;
  rev: number;
  origin: string;
  text: string;
  input?: PreparedCallInput;
  actor?: { id: string; display?: string; bot?: boolean };
  root: string;
  parent?: string;
  depth: number;
  mode?: "subagent" | "handoff" | "delegate";
  /** The schedule that raised the run, if one did. */
  schedule?: string;
  /** The conversation the run belongs to. */
  thread: string;
  /** The turn this one follows. Runs sharing a prev are branches. */
  prev?: string;
  /** Why the agent was chosen, when the platform picked it. */
  picked?: string;
  project?: string;
  lease: number;
  /** Micro-USD leased to calls that have not ended. */
  granted: number;
  /** Micro-USD priced in US dollars: the run's own calls and the calls it made that have ended. */
  spent?: number;
  /** What the run used, as the runtime counts it. Absent until its first model call. */
  usage?: RunUsage;
  calls: number;
  /** Stopped is ended from outside: by a person, or because the run it was attached to ended. */
  status: "accepted" | "running" | "parked" | "completed" | "failed" | "stopped";
  /** Set on a run another run called. Attached runs end with their caller, delegated ones do not. */
  lifecycle?: "attached" | "delegated";
  /** Why a stopped run was stopped. */
  cause?: string;
  /** What a parked run is waiting on a person for. */
  question?: WireQuestion;
  /** The trigger id the run was accepted with. Its `id` is derived from it and the workspace. */
  tag_id?: string;
  /** Asked to stop and not ended yet. It ends stopped, or completed or failed if it finished first. */
  stopping?: boolean;
  /** When the run was first asked to stop, while it is stopping. */
  stop_requested_at?: string;
  /** The run has its answer and is waiting for runs attached to it. */
  finishing?: boolean;
  answer?: string;
  error?: string;
  reason?: string;
  created_at: string;
  updated_at: string;
}

/** Decode an event body. Bodies are UTF-8 JSON, base64-encoded by Go. */
export function body<T>(e: BusEvent): Partial<T> {
  if (!e.payload) return {};
  try {
    const bytes = Uint8Array.from(atob(e.payload), (c) => c.charCodeAt(0));
    return JSON.parse(new TextDecoder().decode(bytes)) as Partial<T>;
  } catch {
    return {};
  }
}

interface Lifecycle {
  error: string;
  terminal: boolean;
  reason: string;
  text: string;
  question: WireQuestion;
  answer: { question_id: string; decision?: string; choice?: string; text?: string; note?: string; by?: string; at?: string; expired?: boolean };
}
interface Tool {
  id: string;
  name: string;
  phase: string;
  result: string;
  error: string;
}
/** What the platform reports when an agent publishes a file. */
interface Published {
  name: string;
  summary: string;
  error: string;
  data: {
    id: string;
    title: string;
    kind: ArtifactKind;
    mime: string;
    size: number;
    version: number;
    description?: string;
    preview?: string;
  };
}
/** The tool an agent publishes a file with. */
const PUBLISH = "publish_artifact";
/** The tool an agent hands work to another agent with, without waiting for it. */
const HAND_ON = "delegate_task";
const CALL_AGENT = "call_agent";
/** mandatum names the tool that hands work to another agent after that agent. */
const DELEGATE = /^tag_(.+)$/;

/** The agent a tool call hands work to, when the tool is a call to another agent. */
export function delegateOf(tool?: string): string | undefined {
  return DELEGATE.exec(tool ?? "")?.[1];
}

interface Cite {
  label: string;
  text: string;
  index: number;
  source: { document_title?: string; source_uri?: string; section_heading?: string };
}

/**
 * Returns a function that turns one run's envelopes into TurnEvents. It is
 * stateful because two facts span events: whether text already streamed (the
 * completed event repeats the whole answer), and each tool call's name.
 */
export function adapter(
  opts: {
    /** An agent's colour, 0 to 360, so a call to it reads as that agent. */
    hue?: (agent: string) => number;
    /**
     * Where a platform path such as `/api/artifacts/a1/content` is reached from
     * this surface. The console is served by the platform, so it leaves paths
     * as they are. A surface elsewhere returns an absolute address, or
     * undefined when it has none to offer, and the link is left out.
     */
    href?: (path: string) => string | undefined;
  } = {},
): (e: BusEvent) => TurnEvent[] {
  const href = opts.href ?? ((path: string) => path);
  let streamed = false;
  const tools = new Map<string, string>();
  // Publish calls that have not reported yet, oldest first. A file is shown
  // from the moment the agent starts publishing it, and the report that
  // follows fills in the same card rather than adding a second one.
  const publishing: string[] = [];

  // Helper calls link to their own run. Their complete result stays in
  // execution history rather than becoming another answer preview.
  // The run each call to another agent started, by tool call. The platform
  // says which run it was on the caller's stream, after the call opens.
  const started = new Map<string, { run: string; agent: string; lifecycle?: string }>();
  // Calls to another agent that have not been told their run yet, per tool.
  const calling = new Map<string, string[]>();
  // What each task last looked like, so a later fact adds to it.
  const shown = new Map<string, Partial<Task>>();
  // Questions this run asked that have no answer yet, and the tool call an
  // approval holds.
  const asked = new Map<string, { question: WireQuestion; interrupt: Interrupt }>();

  const task = (id: string, name: string | undefined, rest: Partial<Task>): Task => {
    const all = { ...shown.get(id), ...rest };
    shown.set(id, all);
    const run = started.get(id);
    const to = delegateOf(name) ?? run?.agent;
    return to
      ? {
          id,
          title: to,
          status: "in_progress",
          tool: name,
          ...all,
          subagent: {
            name: to,
            runId: run?.run,
            summary: all.status === "error" ? "Helper failed. Open execution history for details."
              : run?.lifecycle === "delegated" ? "Background work. Open execution history."
              : all.status === "complete" ? "Result returned to caller" : undefined,
            hue: opts.hue?.(to),
          },
        }
      : { id, title: title(name), status: "in_progress", tool: name, ...all };
  };

  return (e) => {
    switch (e.kind) {
      case "lifecycle.accepted":
        streamed = false;
        tools.clear();
        started.clear();
        calling.clear();
        shown.clear();
        asked.clear();
        return [{ type: "turn.started", turnId: e.run_id, agent: { name: e.agent } }];
      case "lifecycle.started":
        return [{ type: "activity", activity: "thinking" }];
      case "lifecycle.resumed": {
        const a = body<Lifecycle>(e).answer;
        const open = a?.question_id ? asked.get(a.question_id) : undefined;
        if (!a || !open) return [{ type: "activity", activity: "thinking" }];
        asked.delete(a.question_id);
        const settled = settledOf(a);
        const out: TurnEvent[] = [];
        const resolved = interruptOf(open.question, settled);
        if (resolved) out.push({ type: "interrupt.resolved", interrupt: resolved });
        const call = open.question.tool?.call_id;
        if (call && open.question.kind === "approval") {
          const denied = settled.action !== "approve";
          out.push({ type: "task.upserted", task: task(call, open.question.tool?.name, { status: denied ? "denied" : "in_progress" }) });
        }
        out.push({ type: "activity", activity: "thinking" });
        return out;
      }
      case "lifecycle.parked": {
        // Blocked on a person. vultus does not animate waiting.
        const q = body<Lifecycle>(e).question;
        const interrupt = q?.id ? interruptOf(q, undefined) : null;
        if (!q || !interrupt) return [{ type: "activity", activity: "waiting" }];
        asked.set(q.id, { question: q, interrupt });
        const out: TurnEvent[] = [{ type: "interrupt.raised", interrupt }];
        // The call an approval holds reads "waiting", not "running". It is not
        // tied to the approval (`interruptId`): vultus then gives the web inline
        // row controls and Slack the card, and the two surfaces would differ.
        // vultus: one rendering of a task-owned approval on both surfaces.
        const call = q.tool?.call_id;
        if (call && q.kind === "approval") {
          tools.set(call, q.tool?.name ?? "tool");
          out.push({ type: "task.upserted", task: task(call, q.tool?.name, { status: "waiting" }) });
        }
        out.push({ type: "activity", activity: "waiting" });
        return out;
      }
      case "lifecycle.child.parked": {
        // A run this one waits on is waiting on a person. The question is
        // that run's, answered there or in the inbox: here the step that
        // called it reads waiting.
        const cq = body<{ run_id: string; agent: string }>(e);
        const id = [...started].find(([, s]) => s.run === cq.run_id)?.[0];
        const out: TurnEvent[] = [];
        if (id) out.push({ type: "task.upserted", task: task(id, tools.get(id), { status: "waiting" }) });
        out.push({ type: "activity", activity: "waiting" });
        return out;
      }
      case "delta.text": {
        const text = body<{ text: string }>(e).text ?? "";
        if (!text) return [];
        // The first words of an answer are the moment it starts composing.
        const first = !streamed;
        streamed = true;
        return first
          ? [
              { type: "activity", activity: "composing" },
              { type: "text.delta", text },
            ]
          : [{ type: "text.delta", text }];
      }
      case "delta.thinking": {
        const text = body<{ text: string }>(e).text ?? "";
        return text ? [{ type: "reasoning.delta", text }] : [];
      }
      case "delta.tool.call": {
        const t = body<Tool>(e);
        if (!t.id) return [];
        tools.set(t.id, t.name ?? "tool");
        const out: TurnEvent[] = [
          { type: "activity", activity: doing(t.name) },
          { type: "task.upserted", task: task(t.id, t.name, {}) },
        ];
        if (t.name && (delegateOf(t.name) || t.name === HAND_ON || t.name === CALL_AGENT)) {
          const pending = calling.get(t.name) ?? [];
          if (!pending.includes(t.id) && !started.has(t.id)) calling.set(t.name, [...pending, t.id]);
        }
        if (t.name === PUBLISH) {
          publishing.push(t.id);
          out.push({
            type: "attachment.upserted",
            attachment: { type: "artifact", id: t.id, title: "Publishing a file", kind: "document", phase: "generating" },
          });
        }
        return out;
      }
      case "delta.tool.done": {
        const t = body<Tool>(e);
        if (!t.id) return [];
        const name = t.name ?? tools.get(t.id);
        return [
          { type: "activity", activity: "thinking" },
          { type: "task.upserted", task: task(t.id, name, { status: t.error ? "error" : "complete", output: t.error || t.result }) },
        ];
      }
      case "action.taken": {
        const p = body<Published>(e);
        const called = p.data as unknown as { run?: string; agent?: string; lifecycle?: string; tool_call_id?: string } | undefined;
        if (p.name && called?.run && (delegateOf(p.name) || p.name === HAND_ON || p.name === CALL_AGENT)) {
          // Which run a call to another agent started. From here the step
          // can be opened as that run.
          const pending = calling.get(p.name) ?? [];
          const id = called.tool_call_id ?? pending[0];
          if (!id || tools.get(id) !== p.name) return [];
          calling.set(p.name, pending.filter((candidate) => candidate !== id));
          started.set(id, { run: called.run, agent: called.agent ?? delegateOf(p.name) ?? "", lifecycle: called.lifecycle });
          return [{ type: "task.upserted", task: task(id, p.name, {}) }];
        }
        if (p.name !== PUBLISH) return [];
        // The card the call opened, or a new one if this is all that is left
        // of the run's record.
        const id = publishing.shift() ?? p.data?.id ?? `published-${e.seq}`;
        const a = p.data;
        if (p.error || !a) {
          return [
            {
              type: "attachment.upserted",
              attachment: {
                type: "artifact",
                id,
                title: p.summary || "File",
                kind: "document",
                phase: "failed",
                error: lastLine(p.error) || "The file could not be published.",
              },
            },
          ];
        }
        const content = `/api/artifacts/${encodeURIComponent(a.id)}/content`;
        return [
          {
            type: "attachment.upserted",
            attachment: {
              type: "artifact",
              id,
              title: a.title,
              kind: a.kind,
              phase: "ready",
              mimeType: a.mime,
              sizeBytes: a.size,
              version: a.version,
              // Inline is honored only for types that cannot run script. For
              // everything else this link downloads, like the one below it.
              url: href(`${content}?inline=1`),
              downloadUrl: href(content),
              description: a.description,
              previewText: a.preview,
            },
          },
        ];
      }
      case "delta.citation": {
        const c = body<Cite>(e);
        return [
          {
            type: "citation.added",
            citation: {
              id: c.label ?? String(c.index ?? e.seq),
              title: c.source?.document_title ?? c.source?.section_heading ?? "Source",
              url: c.source?.source_uri,
              snippet: c.text,
            },
          },
        ];
      }
      case "lifecycle.completed": {
        const out: TurnEvent[] = [];
        const text = body<Lifecycle>(e).text;
        if (!streamed && text) out.push({ type: "text.delta", text });
        out.push({ type: "turn.completed" });
        return out;
      }
      case "lifecycle.stopped": {
        // The run was stopped and the platform says so on its stream. Until it
        // does, a stopped run is ended from its record (see `settled`). The
        // body carries what the agent had said, for a reader that did not
        // see it stream.
        const out: TurnEvent[] = [];
        const text = body<Lifecycle>(e).text;
        if (!streamed && text) out.push({ type: "text.delta", text });
        // A question nobody answered is settled with the run.
        for (const { question } of asked.values()) {
          const settled = interruptOf(question, { action: "stopped", by: "system", at: e.at, note: "The run was stopped" });
          if (settled) out.push({ type: "interrupt.resolved", interrupt: settled });
        }
        asked.clear();
        out.push({ type: "turn.stopped" }, { type: "stop.updated", stop: "confirmed" });
        return out;
      }
      case "lifecycle.failed": {
        const l = body<Lifecycle>(e);
        // A failed attempt may still be retried by the durable engine. Only a
        // terminal failure ends the turn.
        if (!l.terminal) return [];
        return [
          {
            type: "turn.failed",
            failure: { kind: "internal", message: lastLine(l.error) || "The run failed.", reference: e.run_id },
          },
        ];
      }
      default:
        return [];
    }
  };
}

/** What a tool call looks like from outside: looking something up, handing work on, or acting. */
function doing(tool?: string): "searching" | "delegating" | "calling" {
  const name = (tool ?? "").toLowerCase();
  if (/^(delegate|call|agent|tag)[_:.]|subagent|handoff/.test(name)) return "delegating";
  if (/search|recall|retriev|lookup|find|read|fetch|get/.test(name)) return "searching";
  return "calling";
}

/** Runtime errors nest one per line, and the cause is the last. */
function lastLine(error?: string): string {
  const lines = (error ?? "").split("\n").filter((l) => l.trim());
  return lines[lines.length - 1]?.trim() ?? "";
}

/** A tool name as a person would read it: "github.comment" becomes "Github comment". */
function title(name?: string): string {
  if (!name) return "Tool call";
  const words = name.replace(/[._-]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/**
 * The turn to show before any event arrives, built from the run record. Old
 * runs fall out of the bus's retention, so their record is all there is.
 */
export function turnOf(run: Run): Turn {
  const status =
    run.status === "completed"
      ? "done"
      : run.status === "failed"
        ? "failed"
        : run.status === "stopped"
          ? "stopped"
        : run.status === "parked"
          ? "waiting"
          : run.status === "running"
            ? "working"
            : "queued";
  return {
    id: run.id,
    agent: { name: run.agent },
    status,
    text: run.answer ?? "",
    tasks: [],
    citations: [],
    // The record says what a parked run waits on, for a reader who missed
    // the event that raised it.
    interrupts: (() => {
      const i = run.status === "parked" && run.question ? interruptOf(run.question, undefined) : null;
      return i ? [i] : [];
    })(),
    notices: [],
    activity: run.status === "parked" ? "waiting" : undefined,
    failure:
      run.status === "failed"
        ? { kind: "internal", message: lastLine(run.error) || "The run failed.", reference: run.id }
        : undefined,
  };
}
