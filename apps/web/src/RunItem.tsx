import type { Run } from "./adapter.ts";
import { api } from "./api.ts";
import { deliver, download } from "./CopyAs.tsx";
import { COPY_LIMIT, type Format, fileName, formats, type Item, type RunData, redact, runManifest, statusWord } from "./copyas.ts";
import { elapsed, FlashNote, Menu, type Option, Stroke, type StrokeState, strokeOf, took, UsageMeter, useFlash } from "./ui.tsx";

export { runManifest, runMarkdown } from "./copyas.ts";

/**
 * A run's status in specific words. `state` picks the stroke: working moves,
 * waiting is dashed and still, failed is red.
 *
 * These are every state the platform tells apart today. A run refused at a
 * limit is never started, so it has no item: it arrives in the inbox instead.
 */
export function statusOf(run: Run): { word: string; state: StrokeState } {
  const state: StrokeState =
    run.status === "accepted" ? "idle" : run.status === "running" ? "working" : run.status === "parked" ? "waiting" : strokeOf(run.status);
  return { word: statusWord(run), state };
}

/**
 * The item under an answer that stands for its run. Its outline is the run's
 * status, in the agent's color: moving while it works, dashed and still while
 * it waits on a person, red when it failed. It says the status in words, how
 * long the run took, and how much of its budget it handed on, when it has one.
 * Its menu opens the run in Activity, copies it in any of a run's formats, or
 * saves it as a file. The formats are the same ones "Copy as" offers anywhere.
 */
export function RunItem({
  run,
  onActivity,
  load = () => api.trace(run.id),
  manifest = () => api.manifest(run.id),
}: {
  run: Run;
  /** Open the conversation's activity on this run. */
  onActivity?: (runId: string) => void;
  /** The run's call tree and tool spans, fetched when something is copied or saved. */
  load?: () => Promise<RunData>;
  /** The platform's own manifest for the run: every run under it, each agent's definition, and every event. */
  manifest?: () => Promise<unknown>;
}) {
  const { flash, say } = useFlash();
  const status = statusOf(run);
  const ended = run.status === "completed" || run.status === "failed";

  // The menu is built from the run's formats before its call tree is fetched. Only the text needs the tree.
  const shape = formats({ kind: "run", run, data: { runs: [run], spans: [] } });
  const options: Option[] = [
    ...(onActivity ? [{ value: "activity", label: "Open in Activity", hint: "This run, the agents it called and the tools they used." }] : []),
    ...shape.map((f) => ({ value: `copy:${f.id}`, label: `Copy as ${f.label}`, hint: f.hint })),
    { value: "save:markdown", label: "Export Markdown", hint: "The same Markdown, as a file." },
    { value: "save:json", label: "Download JSON Manifest", hint: "The same manifest, as a file." },
  ];

  const act = (what: string) => {
    if (what === "activity") return onActivity?.(run.id);
    const [how, id] = what.split(":");
    if (id === "json") {
      // The manifest is the platform's record, taken as it is. If it cannot be had, one is put together here.
      const name = `run-${run.id}.json`;
      const built = () =>
        load()
          .catch((): RunData => ({ runs: [run], spans: [] }))
          .then((data) => runManifest(run, data));
      manifest()
        .catch(built)
        .then(async (doc) => {
          const text = `${JSON.stringify(redact(doc), null, 2)}\n`;
          if (how === "copy" && text.length <= COPY_LIMIT && navigator.clipboard?.writeText) {
            await navigator.clipboard.writeText(text);
            say("Copied");
          } else {
            download(name, "application/json", text);
            say(how === "copy" ? "Saved as a file" : "Manifest downloaded");
          }
        })
        .catch(() => say("That did not work. Try again.", true));
      return;
    }
    // If the call tree cannot be fetched, the run's own record is still worth having.
    load()
      .catch((): RunData => ({ runs: [run], spans: [] }))
      .then(async (data) => {
        const item: Item = { kind: "run", run, data };
        const format = formats(item).find((f): f is Format => f.id === id);
        if (!format) return;
        if (how === "save") {
          download(fileName(item, format), format.mime, format.text());
          say("Markdown exported");
        } else {
          say((await deliver(item, format)) === "copied" ? "Copied" : "Saved as a file");
        }
      })
      .catch(() => say("That did not work. Try again.", true));
  };

  return (
    <Stroke agent={run.agent} state={status.state} edge="around" testId="run-item" className="inline-flex max-w-full items-center rounded-full">
      <FlashNote flash={flash} align="left" />
      <Menu
        quiet
        label={`Run by ${run.agent}, ${status.word.toLowerCase()}`}
        value=""
        options={options}
        onChange={act}
        header={
          <>
            <span className="text-aui-fg">{run.agent}</span>, revision {run.rev}
            {run.mode ? `, ${run.mode}` : ""}
          </>
        }
        className="[&>button]:max-w-none [&>button]:rounded-full [&>button]:px-2.5"
        shows={
          <span className="inline-flex items-center gap-2 text-aui-xs">
            <span className="text-aui-fg">{status.word}</span>
            <span className="tabular-nums text-aui-muted">{ended ? took(elapsed(run)) : ""}</span>
            {run.lease > 0 && (
              <UsageMeter size="mini" label="Budget handed to calls" spent={run.granted} limit={run.lease} testId="run-item-budget" />
            )}
          </span>
        }
      />
    </Stroke>
  );
}
