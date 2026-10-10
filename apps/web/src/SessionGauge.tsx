import { useEffect, useState } from "react";
import type { Run } from "./adapter.ts";
import { api } from "./api.ts";
import { button, dollars, Popover, Switch, type UsageState, usageOf } from "./ui.tsx";

/** How much of something is used. A limit of 0 means nothing caps it. */
export type Fill = { used: number; limit: number };

/** The share of a limit that is used, from 0 to 1. Null when there is no limit to fill toward. */
export function shareOf(fill: Fill | undefined): number | null {
  if (!fill || fill.limit <= 0) return null;
  return Math.min(Math.max(fill.used / fill.limit, 0), 1);
}

const tokens = (n: number) => (n >= 1000 ? `${Math.round(n / 100) / 10}k` : String(n));

/** What each edge says in words. The icon's label, its tooltip and the panel all read from this. */
export function gaugeWords(context: Fill | undefined, budget: Fill | undefined, usual?: number): { context: string; budget: string } {
  const c = shareOf(context);
  const b = shareOf(budget);
  return {
    context:
      context && c !== null
        ? `${tokens(context.used)} of ${tokens(context.limit)} tokens, ${Math.round(c * 100)}%`
        : "Not reported. The platform does not count tokens yet.",
    budget:
      budget && b !== null
        ? `${dollars(budget.used)} of ${dollars(budget.limit)} handed to calls, ${Math.round(b * 100)}%${
            usual ? `. The usual limit is ${dollars(usual)}` : ""
          }`
        : usual
          ? `The latest turn holds no budget. The usual limit is ${dollars(usual)}.`
          : "The latest turn holds no budget.",
  };
}

const ink: Record<UsageState, string> = {
  none: "currentColor",
  under: "currentColor",
  warning: "var(--aui-warn)",
  over: "var(--aui-bad)",
};

// The icon's two edges run between these x positions, in a 24 by 16 box.
const X0 = 3;
const X1 = 21;

/** One edge: a faint track, and a fill when there is something to measure. A track with nothing to measure is dashed. */
function Edge({ y, fill, state }: { y: number; fill: number | null; state: UsageState }) {
  return (
    <>
      <path d={`M${X0} ${y}H${X1}`} stroke="currentColor" opacity={0.3} strokeDasharray={fill === null ? "1.5 3" : undefined} />
      {fill !== null && fill > 0 && <path d={`M${X0} ${y}H${X0 + (X1 - X0) * fill}`} stroke={ink[state]} />}
    </>
  );
}

function GearIcon({ size = 14 }: { size?: number }) {
  return (
    <svg aria-hidden="true" focusable="false" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
      <path d="M12.22 2h-.44a2 2 0 0 0-2 2v.18a2 2 0 0 1-1 1.73l-.43.25a2 2 0 0 1-2 0l-.15-.08a2 2 0 0 0-2.73.73l-.22.38a2 2 0 0 0 .73 2.73l.15.1a2 2 0 0 1 1 1.72v.51a2 2 0 0 1-1 1.74l-.15.09a2 2 0 0 0-.73 2.73l.22.38a2 2 0 0 0 2.73.73l.15-.08a2 2 0 0 1 2 0l.43.25a2 2 0 0 1 1 1.73V20a2 2 0 0 0 2 2h.44a2 2 0 0 0 2-2v-.18a2 2 0 0 1 1-1.73l.43-.25a2 2 0 0 1 2 0l.15.08a2 2 0 0 0 2.73-.73l.22-.39a2 2 0 0 0-.73-2.73l-.15-.08a2 2 0 0 1-1-1.74v-.5a2 2 0 0 1 1-1.74l.15-.09a2 2 0 0 0 .73-2.73l-.22-.38a2 2 0 0 0-2.73-.73l-.15.08a2 2 0 0 1-2 0l-.43-.25a2 2 0 0 1-1-1.73V4a2 2 0 0 0-2-2z" />
      <circle cx="12" cy="12" r="3" />
    </svg>
  );
}

const trigger =
  "inline-flex h-7 shrink-0 items-center justify-center rounded-aui px-1.5 text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent aria-expanded:text-aui-fg";

/**
 * Where a conversation stands, as two short lines. The top edge fills as the
 * context window does. The bottom edge fills as the latest turn hands its
 * budget to the agents it calls, and a notch on it marks the usual limit. An
 * edge with nothing to measure is dashed. Opening it says both in words. The
 * gear beside it holds compaction: on its own past a threshold, or now.
 * Presentational: it fetches nothing.
 */
export function SessionGauge({
  context,
  budget,
  usual,
  compaction,
}: {
  /** Tokens in the context window. Leave out until the platform reports them. */
  context?: Fill;
  /** Micro-USD the latest turn handed on, against what it holds. */
  budget?: Fill;
  /** Micro-USD a run may hold when nothing says otherwise. */
  usual?: number;
  /** Leave out while the platform cannot compact: the controls are then shown but cannot be used. */
  compaction?: { auto: boolean; onAuto: (next: boolean) => void; onCompact: () => void };
}) {
  const c = shareOf(context);
  const b = shareOf(budget);
  const words = gaugeWords(context, budget, usual);
  // The notch sits on the budget edge's own scale, so it reads as "this turn against the usual".
  const scale = Math.max(budget?.limit ?? 0, usual ?? 0);
  const notch = usual && scale > 0 ? X0 + ((X1 - X0) * usual) / scale : null;
  const held = budget && scale > 0 ? (budget.limit / scale) * (b ?? 0) : b;

  return (
    <div data-testid="session-gauge" className="flex shrink-0 items-center">
      <Popover
        label={`Context: ${words.context} Budget: ${words.budget}`}
        align="right"
        narrow
        className={trigger}
        button={
          <svg aria-hidden="true" focusable="false" width={24} height={16} viewBox="0 0 24 16" fill="none" strokeWidth={1.75} strokeLinecap="round">
            <Edge y={5} fill={c} state={usageOf(context?.used ?? 0, context?.limit)} />
            <Edge y={11} fill={held} state={usageOf(budget?.used ?? 0, budget?.limit)} />
            {notch !== null && <path d={`M${notch} 8.5V13.5`} stroke="currentColor" strokeWidth={1} />}
          </svg>
        }
      >
        {() => (
          <dl className="flex flex-col gap-2 px-2 py-1.5 text-aui-xs">
            <div>
              <dt className="text-aui-fg">Context</dt>
              <dd className="text-aui-muted">{words.context}</dd>
            </div>
            <div>
              <dt className="text-aui-fg">Budget</dt>
              <dd className="text-aui-muted">{words.budget}</dd>
            </div>
          </dl>
        )}
      </Popover>
      <Popover label="Compaction" align="right" className={trigger} button={<GearIcon />}>
        {(close) => (
          <div className="flex flex-col gap-2 px-2 py-1.5">
            <Switch
              label="Compact automatically"
              hint="Summarize the oldest turns when the context is nearly full."
              checked={compaction?.auto ?? false}
              disabled={!compaction}
              onChange={(next) => compaction?.onAuto(next)}
            />
            <button
              type="button"
              className={`${button} h-7 self-start px-2 text-aui-xs`}
              disabled={!compaction}
              onClick={() => {
                compaction?.onCompact();
                close();
              }}
            >
              Compact Now
            </button>
            {!compaction && <p className="text-aui-xs text-aui-muted">Compaction is not built yet. The platform does not count tokens.</p>}
          </div>
        )}
      </Popover>
    </div>
  );
}

/**
 * The gauge for a conversation, from its latest turn. It asks once for the
 * workspace's limits, to mark the usual budget.
 */
export function SessionGaugeFor({ tail }: { tail: Run | undefined }) {
  const [usual, setUsual] = useState<number | undefined>();

  useEffect(() => {
    let on = true;
    api
      .usage()
      .then((scopes) => on && setUsual(scopes.find((s) => s.scope.kind === "tenant")?.max_lease || undefined))
      .catch(() => {});
    return () => {
      on = false;
    };
  }, []);

  return <SessionGauge budget={tail && tail.lease > 0 ? { used: tail.granted, limit: tail.lease } : undefined} usual={usual} />;
}
