import type { Turn, TurnEvent } from "@agent-ui/core";

/**
 * The one place opentag's wire format meets agent-ui's event model. Everything
 * above this file speaks TurnEvent, and everything below it speaks envelopes.
 */

/** An opentag envelope.Event as the platform's event stream sends it. */
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

/** A run as the platform's index reports it. */
export interface Run {
  id: string;
  tenant: string;
  agent: string;
  rev: number;
  origin: string;
  text: string;
  actor?: { id: string; display?: string; bot?: boolean };
  root: string;
  parent?: string;
  depth: number;
  mode?: "subagent" | "handoff" | "delegate";
  /** The schedule that raised the run, if one did. */
  schedule?: string;
  lease: number;
  granted: number;
  calls: number;
  status: "accepted" | "running" | "parked" | "completed" | "failed";
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
}
interface Tool {
  id: string;
  name: string;
  phase: string;
  result: string;
  error: string;
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
export function adapter(): (e: BusEvent) => TurnEvent[] {
  let streamed = false;
  const tools = new Map<string, string>();

  return (e) => {
    switch (e.kind) {
      case "lifecycle.accepted":
        streamed = false;
        tools.clear();
        return [{ type: "turn.started", turnId: e.run_id, agent: { name: e.agent } }];
      case "lifecycle.started":
      case "lifecycle.resumed":
        return [{ type: "activity", activity: "thinking" }];
      case "lifecycle.parked":
        // Blocked on a person or an event. agent-ui does not animate waiting.
        return [{ type: "activity", activity: "waiting" }];
      case "delta.text": {
        const text = body<{ text: string }>(e).text ?? "";
        if (!text) return [];
        streamed = true;
        return [{ type: "text.delta", text }];
      }
      case "delta.thinking": {
        const text = body<{ text: string }>(e).text ?? "";
        return text ? [{ type: "reasoning.delta", text }] : [];
      }
      case "delta.tool.call": {
        const t = body<Tool>(e);
        if (!t.id) return [];
        tools.set(t.id, t.name ?? "tool");
        return [
          { type: "activity", activity: "calling" },
          {
            type: "task.upserted",
            task: { id: t.id, title: title(t.name), status: "in_progress", tool: t.name },
          },
        ];
      }
      case "delta.tool.done": {
        const t = body<Tool>(e);
        if (!t.id) return [];
        const name = t.name ?? tools.get(t.id);
        return [
          {
            type: "task.upserted",
            task: {
              id: t.id,
              title: title(name),
              status: t.error ? "error" : "complete",
              tool: name,
              output: t.error || t.result,
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
    interrupts: [],
    notices: [],
    activity: run.status === "parked" ? "waiting" : undefined,
    failure:
      run.status === "failed"
        ? { kind: "internal", message: lastLine(run.error) || "The run failed.", reference: run.id }
        : undefined,
  };
}
