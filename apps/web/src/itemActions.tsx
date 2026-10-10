import type { ItemActions, TurnItem } from "@vultus/render-web";
import type { Run } from "./adapter.ts";
import { CopyAs } from "./CopyAs.tsx";
import type { Item } from "./copyas.ts";

/**
 * An item inside a turn as something "Copy as" understands. A tool call keeps
 * its name and whatever came back. A call to another agent made inside a turn
 * has no run of its own on the platform, so it is copied as the call it was:
 * its name and its answer.
 */
export function itemOf(run: Run, item: TurnItem): Item | null {
  if (item.kind === "answer") return { kind: "answer", run: { ...run, answer: item.turn.text || run.answer } };
  const task = item.task;
  if (!task) return null;
  const failed = task.status === "error";
  const ended = task.status === "complete" || failed;
  return {
    kind: "tool",
    tool: {
      name: task.tool ?? task.title,
      run: run.id,
      agent: run.agent,
      ...(failed ? { error: task.output || "The call failed without saying why." } : ended ? { result: task.output ?? "" } : {}),
    },
  };
}

/**
 * "Copy as" on every item vultus draws in a turn: each tool call, each call
 * to another agent, and the answer. Pass the result to `AgentMessage` as
 * `itemActions`.
 *
 * On the answer this is the only copy control. vultus draws its own plain
 * copy button there only when a host returns nothing for the answer, so there
 * is one icon and never two. Return null itself to get that button back: an
 * element that renders nothing would leave the answer with no way to copy it.
 */
export function itemActions(run: Run): ItemActions {
  return (item) => {
    const found = itemOf(run, item);
    if (!found) return null;
    // The answer's toolbar starts at the left edge, so its menu opens rightward. Rows have theirs at the right edge.
    return <CopyAs compact align={item.kind === "answer" ? "left" : "right"} label={item.kind === "answer" ? "Copy answer as" : "Copy as"} testId="item-copy" item={found} />;
  };
}
