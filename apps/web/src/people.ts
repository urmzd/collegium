import type { AgentSpec } from "./api.ts";

/**
 * Who an agent waits on, as its spec says: the tools whose calls wait for a
 * person to approve them, who may approve, who may answer a question it asks,
 * and whether it may ask at all. The platform decides who may answer from the
 * revision a run was started on, so a change here applies to runs started
 * after it is saved.
 */

/** The built-in tool an agent asks a person a question with. */
export const ASK_PERSON = "ask_person";

/** The tools an agent holds that a person can be asked to approve: its own tools, and a call to each agent it may call. */
export function approvable(spec: AgentSpec): string[] {
  const own = (spec.tools ?? []).filter((t) => t !== ASK_PERSON);
  const calls = (spec.access?.spawn ?? []).map((a) => `tag_${a}`);
  return [...new Set([...own, ...calls, ...(spec.approvals ?? [])])];
}

/** People and roles written one per line or separated by commas: `ana`, `role:operator`. */
export function namesOf(text: string): string[] {
  return [...new Set(text.split(/[\n,]+/).map((s) => s.trim()).filter(Boolean))];
}

/** Turn one approval on or off, keeping the order the rest were set in. */
export function withApproval(spec: AgentSpec, tool: string, on: boolean): AgentSpec {
  const now = spec.approvals ?? [];
  const approvals = on ? [...new Set([...now, tool])] : now.filter((t) => t !== tool);
  return { ...spec, approvals: approvals.length > 0 ? approvals : undefined };
}

/** Let the agent ask people questions, or not. */
export function withAsking(spec: AgentSpec, on: boolean): AgentSpec {
  const tools = (spec.tools ?? []).filter((t) => t !== ASK_PERSON);
  const next = on ? [...tools, ASK_PERSON] : tools;
  return { ...spec, tools: next.length > 0 ? next : undefined };
}

/** Set who may approve or answer. Nobody named means anyone in the workspace may. */
export function withNames(spec: AgentSpec, field: "approvers" | "answerers", text: string): AgentSpec {
  const names = namesOf(text);
  return { ...spec, [field]: names.length > 0 ? names : undefined };
}
