import { ActionId, canAct, type Resolution } from "@agent-ui/core";
import type { PartRenderer } from "@agent-ui/web";
import { useState } from "react";
import { limitWord, type PlatformMessage, reached, type Source, scopeName } from "./mail.ts";
import { ago, button, counted, field, LimitField, type Period, primary, UsageMeter } from "./ui.tsx";

/**
 * The parts the platform draws itself. Everything else in a message (text,
 * failures, memory to review, replies) is drawn by agent-ui's mailbox.
 *
 * - An alert, so it can name its rule and link to it.
 * - A limit that was reached, so it can say which limit and on what, show the
 *   console's meter, and take any new limit rather than a few offered ones.
 */

const link =
  "rounded-aui-sm text-aui-fg underline underline-offset-2 hover:no-underline focus-visible:outline-2 focus-visible:outline-aui-accent";

const done: Record<string, string> = {
  acknowledge: "Acknowledged",
  acknowledged: "Acknowledged",
  snooze: "Snoozed",
  snoozed: "Snoozed",
  raise_limit: "Limit raised",
  raised: "Limit raised",
};

/** What was done to a part, by whom and when. */
function Record({ resolution }: { resolution: Resolution }) {
  return (
    <p data-testid="part-resolved" className="text-aui-xs text-aui-muted">
      {done[resolution.outcome] ?? "Resolved"}
      {resolution.by ? ` by ${resolution.by}` : ""}, {ago(resolution.at)}.
    </p>
  );
}

const noRight = <p className="text-aui-xs text-aui-muted">You can read this, but someone with more access has to act on it.</p>;

const putAway = (message: PlatformMessage) => Boolean(message.dismissedAt || message.snoozedUntil);

/** The renderers the console registers with the mailbox. `onOpen` opens what a part links to. */
export function platformParts(onOpen: (source: Source) => void): Record<string, PartRenderer> {
  return {
    alert: ({ message, part, onAction }) => {
      if (part.type !== "alert") return null;
      const mail = message as PlatformMessage;
      const { alert } = part;
      const may = canAct(part, ActionId.acknowledge);
      const acknowledge = () => onAction({ id: ActionId.acknowledge, payload: { turnId: message.id, target: part.id } });
      // Snoozing hides the whole message until tomorrow. It is the message that sleeps, not this part.
      const snooze = () =>
        onAction({
          id: ActionId.snooze,
          payload: { turnId: message.id, target: message.id, option: new Date(Date.now() + 24 * 3_600_000).toISOString() },
        });
      const critical = alert.status === "down" || message.severity === "bad";
      return (
        <div data-testid="part-alert" className="flex flex-col gap-2">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <h4 className="text-aui-base font-medium text-aui-fg">{alert.title}</h4>
            <span className="inline-flex items-center gap-1.5 text-aui-xs text-aui-muted">
              <span aria-hidden className={`size-1.5 rounded-full ${critical ? "bg-aui-bad" : "bg-aui-warn"}`} />
              {critical ? "Critical" : "Warning"}
            </span>
          </div>
          {alert.body && <p className="max-w-[68ch] text-aui-sm text-aui-muted">{alert.body}</p>}
          {alert.service && (
            <p className="text-aui-xs text-aui-muted">
              Raised by the rule{" "}
              <button type="button" className={link} onClick={() => onOpen({ kind: "rule", id: alert.service, label: alert.service })}>
                {alert.service}
              </button>
              . Change what it watches for on the Alerts page.
            </p>
          )}
          {part.resolution ? (
            <Record resolution={part.resolution} />
          ) : putAway(mail) ? null : !may ? (
            noRight
          ) : (
            <div className="flex flex-wrap gap-2">
              <button type="button" className={primary} onClick={acknowledge}>
                Acknowledge
              </button>
              <button type="button" className={button} onClick={snooze}>
                Snooze for a Day
              </button>
            </div>
          )}
        </div>
      );
    },
    budget: (props) => <Budget {...props} />,
  };
}

/** A run refused at a limit: what the limit is, and a way to raise it here. */
function Budget({ message, part, onAction }: Parameters<PartRenderer>[0]) {
  const mail = message as PlatformMessage;
  // The mailbox's part carries the meter. Which limit it is, and on what, is the platform's to say.
  const data = mail.budgets?.[part.id] ?? { scope: { kind: "tenant" }, which: "runs" as const, period: "month" as const, current: {}, refused: true };
  const { value } = reached(data);
  const money = data.which === "spend";
  const [limit, setLimit] = useState<number | null>(money ? value * 2 || null : null);
  const [count, setCount] = useState(String(value ? value * 2 : ""));
  const [period, setPeriod] = useState<Period>(data.period);
  if (part.type !== "budget") return null;

  const raised = money ? limit : /^\d+$/.test(count.trim()) ? Number(count) : null;
  const mayRaise = canAct(part, ActionId.raiseBudget);
  const mayAck = canAct(part, ActionId.acknowledge);
  const where = scopeName(data.scope);
  // The new limit travels in the meter's unit: dollars for spend, a count for the rest.
  const raise = (to: number) =>
    onAction({ id: ActionId.raiseBudget, payload: { turnId: message.id, target: part.id, option: String(money ? to / 1_000_000 : to) } });
  const acknowledge = () => onAction({ id: ActionId.acknowledge, payload: { turnId: message.id, target: part.id } });

  return (
    <div data-testid="part-budget" className="flex flex-col gap-3">
      <p className="max-w-[68ch] text-aui-sm text-aui-fg">
        {data.refused
          ? `${where} reached its limit on ${limitWord[data.which]}, so a run was not started. It will not be tried again by itself.`
          : `${where} is close to its limit on ${limitWord[data.which]}. Nothing has been refused yet.`}
      </p>
      <UsageMeter
        labelled
        label={`${where}, ${limitWord[data.which]}`}
        spent={data.used ?? value}
        limit={value}
        period={data.which === "concurrent" ? undefined : data.period}
        format={money ? undefined : counted(data.which === "concurrent" ? "at once" : "runs")}
      />
      {part.resolution ? (
        <Record resolution={part.resolution} />
      ) : putAway(mail) ? null : !mayRaise && !mayAck ? (
        noRight
      ) : (
        <>
          {mayRaise &&
            (money ? (
              <LimitField label="New limit" value={limit} period={period} onChange={(v, p) => (setLimit(v), setPeriod(p))} />
            ) : (
              <label className="flex max-w-xs flex-col gap-1">
                <span className="text-aui-xs font-medium text-aui-muted">New limit</span>
                <input className={field} inputMode="numeric" value={count} onChange={(e) => setCount(e.target.value)} />
              </label>
            ))}
          <div className="flex flex-wrap gap-2">
            {mayRaise && (
              <button type="button" className={primary} disabled={raised === null || raised <= value} onClick={() => raised !== null && raise(raised)}>
                Raise Limit
              </button>
            )}
            {mayAck && (
              <button type="button" className={button} onClick={acknowledge}>
                Acknowledge
              </button>
            )}
          </div>
        </>
      )}
    </div>
  );
}
