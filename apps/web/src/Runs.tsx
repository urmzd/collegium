import { emptyTurn, reduce, type Turn } from "@agent-ui/core";
import { useEffect, useMemo, useReducer, useRef, useState } from "react";
import { adapter, type BusEvent, type Run, turnOf } from "./adapter.ts";
import { api, subscribe } from "./api.ts";
import { AgentMark, agentOptions, ago, Chip, elapsed, Empty, hueOf, isEnded, Menu, Page, rows, SearchField, Segmented, Status, Tape, took } from "./ui.tsx";

type Action = { type: "seed"; turn: Turn } | { type: "open" } | { type: "event"; event: BusEvent };

/**
 * One run's live turn. It starts from the run record, then the stream takes
 * over: every connect replays the run from its first event, so the turn is
 * rebuilt from empty on each open rather than appended to.
 */
/** An adapter that colors a call to another agent as that agent. */
const make = () => adapter({ hue: hueOf });

/** A finished run's turn, rebuilt from the events the journal still holds. Undefined when it holds none. */
export function replay(run: Run, events: BusEvent[]): Turn | undefined {
  if (events.length === 0) return undefined;
  const to = make();
  return events.reduce<Turn>((turn, e) => to(e).reduce(reduce, turn), emptyTurn(run.id, { name: run.agent }));
}

export function useRunTurn(run: Run | undefined): Turn | undefined {
  const [state, dispatch] = useReducer(
    (s: { turn?: Turn; to: ReturnType<typeof adapter>; fresh: boolean }, a: Action) => {
      switch (a.type) {
        case "seed":
          return { turn: a.turn, to: make(), fresh: true };
        case "open":
          return { ...s, to: make(), fresh: true };
        case "event": {
          const base = s.fresh ? emptyTurn(a.event.run_id, { name: a.event.agent }) : s.turn;
          if (!base) return s;
          return { ...s, fresh: false, turn: s.to(a.event).reduce(reduce, base) };
        }
      }
    },
    { to: make(), fresh: true },
  );

  const id = run?.id;
  const agent = run?.agent;
  // biome-ignore lint/correctness/useExhaustiveDependencies: reseed only when the selected run changes, not on every poll of its record
  useEffect(() => {
    if (!run || !id || !agent) return;
    dispatch({ type: "seed", turn: turnOf(run) });
    // A run that has ended is read from the journal, which still has its reasoning
    // and tool calls after the stream has let them go. Its record stands in until
    // then, and stays if the journal holds nothing.
    if (isEnded(run)) {
      let on = true;
      api
        .runEvents(id)
        .then(({ events }) => {
          const turn = replay(run, events);
          if (on && turn) dispatch({ type: "seed", turn });
        })
        .catch(() => {});
      return () => {
        on = false;
      };
    }
    return subscribe(`agent:${agent}:${id}`, {
      onOpen: () => dispatch({ type: "open" }),
      onEvent: (event) => dispatch({ type: "event", event }),
    });
  }, [id, agent]);

  return run ? state.turn : undefined;
}

type Show = "all" | "live" | "failed" | "done";

const live = (r: Run) => !isEnded(r);

/** Every word typed must appear somewhere a person would look for it. */
function matches(r: Run, words: string[]): boolean {
  const hay = `${r.agent} ${r.text} ${r.id} ${r.origin} ${r.project ?? ""} ${r.schedule ?? ""} ${r.error ?? ""}`.toLowerCase();
  return words.every((w) => hay.includes(w));
}

/** Every run in the tenant, newest first, narrowed by what a person types and picks. */
export function RunList({ runs: all, onSelect }: { runs: Run[]; onSelect: (id: string) => void }) {
  const [query, setQuery] = useState("");
  const [show, setShow] = useState<Show>("all");
  const [agent, setAgent] = useState("");
  const [origin, setOrigin] = useState("");
  const search = useRef<HTMLInputElement>(null);

  // "/" starts a search from anywhere on the page, unless something is being typed.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (e.key !== "/" || e.metaKey || e.ctrlKey || el?.closest("input, textarea, select, [contenteditable]")) return;
      e.preventDefault();
      search.current?.focus();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const agents = useMemo(() => [...new Set(all.map((r) => r.agent))].sort(), [all]);
  const origins = useMemo(() => [...new Set(all.map((r) => r.origin))].sort(), [all]);
  const inFlight = all.filter(live).length;
  const failed = all.filter((r) => r.status === "failed").length;

  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const found = all.filter(
    (r) =>
      (show === "all" || (show === "live" ? live(r) : show === "failed" ? r.status === "failed" : r.status === "completed")) &&
      (!agent || r.agent === agent) &&
      (!origin || r.origin === origin) &&
      matches(r, words),
  );
  // Stable, so runs keep their newest-first order within each group.
  const runs = [...found.filter(live), ...found.filter((r) => !live(r))];
  const narrowed = words.length > 0 || show !== "all" || agent !== "" || origin !== "";
  const reset = () => {
    setQuery("");
    setShow("all");
    setAgent("");
    setOrigin("");
  };

  return (
    <Page
      title="Runs"
      lede={`${inFlight === 0 ? "Nothing is running." : inFlight === 1 ? "1 run in flight, shown first." : `${inFlight} runs in flight, shown first.`} Every run from people, schedules, evals and other agents is here.`}
    >
      {all.length === 0 ? (
        <Empty title="No runs yet">Ask an agent something in a new chat, and the run appears here.</Empty>
      ) : (
        <>
          <div className="flex flex-col gap-1.5">
            <Tape runs={found} slots={96} tall onOpen={onSelect} />
            <p className="flex justify-between text-aui-2xs text-aui-muted">
              <span>Each mark is a run. Taller took longer.</span>
              <span>Newest</span>
            </p>
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <SearchField
              label="Search runs"
              placeholder="Search by request, agent, or run ID"
              hint="/"
              value={query}
              onChange={setQuery}
              inputRef={search}
              className="min-w-56 flex-1"
            />
            <Segmented
              label="Show"
              value={show}
              options={[
                { id: "all", label: `All ${all.length}` },
                { id: "live", label: `In flight ${inFlight}` },
                { id: "failed", label: `Failed ${failed}` },
                { id: "done", label: "Done" },
              ]}
              onChange={setShow}
            />
            <Menu label="Agent" className="w-40" value={agent} options={agentOptions(agents, "Any agent")} onChange={setAgent} />
            <Menu
              label="Started by"
              className="w-36"
              value={origin}
              options={[{ value: "", label: "Any origin" }, ...origins.map((o) => ({ value: o, label: o }))]}
              onChange={setOrigin}
            />
          </div>

          <p className="text-aui-xs text-aui-muted" aria-live="polite">
            {narrowed ? `${runs.length} of ${all.length} runs` : `${all.length} runs`}
            {narrowed && (
              <button
                type="button"
                onClick={reset}
                className="ml-2 rounded-aui-sm text-aui-fg underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-aui-accent"
              >
                Clear filters
              </button>
            )}
          </p>

          {runs.length === 0 ? (
            <Empty title="No runs match">Try fewer words, or clear the filters to see every run.</Empty>
          ) : (
            <ul className={`${rows} -mt-3`}>
              {runs.map((r) => (
                <li key={r.id} className="collegium-enter">
                  <button
                    type="button"
                    onClick={() => onSelect(r.id)}
                    className="flex w-full items-center gap-3 px-1 py-2.5 text-left transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent focus-visible:-outline-offset-2"
                  >
                    <AgentMark
                      name={r.agent}
                      size={14}
                      state={
                        r.status === "failed"
                          ? "error"
                          : r.status === "completed"
                            ? "done"
                            : r.status === "stopped"
                              ? "stopped"
                              : r.status === "parked"
                                ? "waiting"
                                : "thinking"
                      }
                    />
                    <span className="w-28 shrink-0 truncate text-aui-sm font-medium text-aui-fg">{r.agent}</span>
                    <span className="min-w-0 flex-1 truncate text-aui-sm text-aui-muted">{r.text || "No request text"}</span>
                    <span className="hidden @xl:inline-flex">
                      <Chip>{r.origin}</Chip>
                    </span>
                    <span className="hidden w-16 shrink-0 text-right text-aui-xs tabular-nums text-aui-muted @2xl:inline">
                      {took(elapsed(r))}
                    </span>
                    <span className="hidden w-28 shrink-0 text-right text-aui-xs text-aui-muted @lg:inline">{ago(r.created_at)}</span>
                    <span className="w-20 shrink-0">
                      <Status status={r.status} />
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </Page>
  );
}
