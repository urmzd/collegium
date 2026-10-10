import { emptyTurn, reduce, type Turn } from "@agent-ui/core";
import { useEffect, useReducer } from "react";
import { adapter, type BusEvent, type Run, turnOf } from "./adapter.ts";
import { subscribe } from "./api.ts";
import { ago, Chip, Empty, Page, Status } from "./ui.tsx";

type Action = { type: "seed"; turn: Turn } | { type: "open" } | { type: "event"; event: BusEvent };

/**
 * One run's live turn. It starts from the run record, then the stream takes
 * over: every connect replays the run from its first event, so the turn is
 * rebuilt from empty on each open rather than appended to.
 */
export function useRunTurn(run: Run | undefined): Turn | undefined {
  const [state, dispatch] = useReducer(
    (s: { turn?: Turn; to: ReturnType<typeof adapter>; fresh: boolean }, a: Action) => {
      switch (a.type) {
        case "seed":
          return { turn: a.turn, to: adapter(), fresh: true };
        case "open":
          return { ...s, to: adapter(), fresh: true };
        case "event": {
          const base = s.fresh ? emptyTurn(a.event.run_id, { name: a.event.agent }) : s.turn;
          if (!base) return s;
          return { ...s, fresh: false, turn: s.to(a.event).reduce(reduce, base) };
        }
      }
    },
    { to: adapter(), fresh: true },
  );

  const id = run?.id;
  const agent = run?.agent;
  // biome-ignore lint/correctness/useExhaustiveDependencies: reseed only when the selected run changes, not on every poll of its record
  useEffect(() => {
    if (!run || !id || !agent) return;
    dispatch({ type: "seed", turn: turnOf(run) });
    return subscribe(`agent:${agent}:${id}`, {
      onOpen: () => dispatch({ type: "open" }),
      onEvent: (event) => dispatch({ type: "event", event }),
    });
  }, [id, agent]);

  return run ? state.turn : undefined;
}

/** Every run in the tenant, newest first. */
export function RunList({ runs, onSelect }: { runs: Run[]; onSelect: (id: string) => void }) {
  return (
    <Page title="Runs" lede="Every run in this tenant, from people, schedules and other agents.">
        {runs.length === 0 ? (
          <Empty title="No runs yet">Start one with New Run.</Empty>
        ) : (
          <ul className="overflow-hidden rounded-aui-lg border border-aui-border">
            {runs.map((r) => (
              <li key={r.id} className="border-aui-border border-b last:border-b-0">
                <button
                  type="button"
                  onClick={() => onSelect(r.id)}
                  className="flex w-full items-center gap-3 bg-aui-surface px-4 py-2.5 text-left transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent focus-visible:-outline-offset-2"
                >
                  <span className="w-28 shrink-0 truncate text-aui-sm font-medium text-aui-fg">
                    {r.depth > 0 && <span aria-hidden>↳ </span>}
                    {r.agent}
                  </span>
                  <span className="min-w-0 flex-1 truncate text-aui-sm text-aui-muted">
                    {r.text || "No request text"}
                  </span>
                  <Chip>{r.origin}</Chip>
                  <span className="hidden w-24 shrink-0 text-right text-aui-xs text-aui-muted sm:inline">
                    {ago(r.created_at)}
                  </span>
                  <span className="w-20 shrink-0">
                    <Status status={r.status} />
                  </span>
                </button>
              </li>
            ))}
          </ul>
        )}
    </Page>
  );
}
