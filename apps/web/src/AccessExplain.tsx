import { useEffect, useState } from "react";
import { doing, explained, PERMISSIONS } from "./access.ts";
import { type AccessDecision, ApiError, api } from "./api.ts";
import { card, Label, Menu, Problem } from "./ui.tsx";

/**
 * "Why can I do this?": one permission, decided for the person reading, and
 * each step from their sign-in to the rule that decided. Only ever about the
 * reader; the platform takes no one else's name.
 */
export function AccessExplain({ name, permission, onPermission }: { name: string; permission: string; onPermission: (p: string) => void }) {
  const [decision, setDecision] = useState<AccessDecision | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setProblem(null);
    api
      .explain(permission)
      .then((d) => live && setDecision(d))
      .catch((e) => live && setProblem(e instanceof ApiError ? e.message : "The explanation could not be loaded."));
    return () => {
      live = false;
    };
  }, [permission]);

  const shown = decision && decision.permission === permission ? explained(decision, name) : null;

  return (
    <section className={`${card} flex flex-col gap-4`} aria-labelledby="why-title" aria-busy={!shown && !problem}>
      <Label text="Explain" hint="Pick something to do. The answer is about you, from the policy that decides it.">
        <Menu
          label="Permission to explain"
          value={permission}
          options={PERMISSIONS.map((p) => ({ value: p, label: `${doing[p]} (${p})` }))}
          onChange={onPermission}
        />
      </Label>
      <Problem message={problem} />
      {shown && (
        <>
          <div className="flex flex-col gap-1">
            <h2 id="why-title" className="collegium-display text-aui-lg font-semibold text-aui-fg">
              {shown.question}
            </h2>
            <p className="text-aui-sm text-aui-muted">{shown.answer}</p>
          </div>
          <ol className="flex flex-col gap-2.5 text-aui-sm">
            {shown.steps.map((s, i) => (
              // Steps are fixed in number and order for one decision.
              // biome-ignore lint/suspicious/noArrayIndexKey: see above
              <li key={i} className="flex gap-3">
                <span className="w-20 shrink-0 text-aui-muted">{s.label}</span>
                {s.code ? <code className="min-w-0 break-words font-aui-mono text-aui-xs text-aui-fg">{s.text}</code> : <span className="min-w-0 text-aui-fg">{s.text}</span>}
              </li>
            ))}
          </ol>
          {shown.fix && (
            <p className="text-aui-xs text-aui-muted">
              {shown.fix}{" "}
              <a
                className="text-aui-fg underline"
                href="https://github.com/urmzd/collegium/blob/main/docs/authorization.md"
                target="_blank"
                rel="noreferrer"
              >
                Read the policy language
              </a>
            </p>
          )}
        </>
      )}
    </section>
  );
}
