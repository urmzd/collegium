import { useCallback, useEffect, useState } from "react";
import { ApiError, api, type Client, type ClientCaps } from "./api.ts";
import { button, card, Problem, Switch } from "./ui.tsx";

const surfaces: Record<Client["surface"], string> = { slack: "Slack", github: "GitHub" };

/** Who a client answers for, in the words of its surface. */
function identity(c: Client): string {
  const id = c.identity;
  const parts =
    c.surface === "slack"
      ? [id.team && `team ${id.team}`, id.app_id && `app ${id.app_id}`]
      : [id.owners?.length && id.owners.join(", "), id.installation && `installation ${id.installation}`, id.app_id && `app ${id.app_id}`];
  return parts.filter(Boolean).join(" · ");
}

/** The agents a client may raise runs for. */
function reach(c: Client): string {
  if (c.agents?.includes("*")) return "Any agent";
  const names = [c.default_agent, ...(c.agents ?? [])].filter((n, i, all): n is string => !!n && all.indexOf(n) === i);
  return names.length ? names.join(", ") : "No agent";
}

/** A client's own limits, set under Limits with the client as the scope. */
function limits(c: Client): string | null {
  const l = c.limits;
  if (!l) return null;
  const parts = [l.max_concurrent && `${l.max_concurrent} at once`, l.max_runs && `${l.max_runs} runs a ${l.period}`];
  return parts.filter(Boolean).join(", ") || null;
}

/** The Slack apps and GitHub installations this workspace answers through. */
export function ClientSettings({ canWrite }: { canWrite: boolean }) {
  const [clients, setClients] = useState<Client[] | null>(null);
  const [caps, setCaps] = useState<ClientCaps | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

  const fail = (fallback: string) => (e: unknown) => setProblem(e instanceof ApiError ? e.message : fallback);
  const reload = useCallback(() => {
    api
      .clients()
      .then((r) => {
        setClients(r.clients);
        setCaps(r.caps);
      })
      .catch(fail("The clients could not be loaded."));
  }, []);
  useEffect(reload, [reload]);

  const enable = (c: Client, enabled: boolean) => {
    setProblem(null);
    api.saveClient(c, enabled).then(reload).catch(fail("The client could not be changed."));
  };
  const remove = (c: Client) => {
    setProblem(null);
    api.deleteClient(c.id).then(reload).catch(fail("The client could not be removed."));
  };

  return (
    <>
      <header>
        <h1 className="text-aui-xl font-semibold text-aui-fg">Connections</h1>
        <p className="text-aui-sm text-aui-muted">
          The Slack apps and GitHub installations this workspace answers through. Each one reaches only the agents it is given.
        </p>
      </header>
      <Problem message={problem} />
      {clients?.length === 0 && (
        <section className={`${card} flex flex-col gap-2`}>
          <p className="text-aui-sm text-aui-fg">No clients yet.</p>
          <p className="text-aui-sm text-aui-muted">
            An admin creates one with <code className="font-aui-mono text-aui-xs">POST /api/clients</code>. Its secrets are written once and never
            shown again.
          </p>
        </section>
      )}
      {clients?.map((c) => (
        <section key={c.id} className={`${card} flex flex-col gap-3`}>
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <h3 className="text-aui-sm font-medium text-aui-fg">
              {c.name} <span className="font-normal text-aui-muted">{surfaces[c.surface]}</span>
            </h3>
            <span className="font-aui-mono text-aui-2xs text-aui-muted">{c.id}</span>
          </div>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-1.5 text-aui-sm">
            <dt className="text-aui-muted">Answers for</dt>
            <dd className="text-aui-fg">{identity(c) || "Not stated"}</dd>
            <dt className="text-aui-muted">Reaches</dt>
            <dd className="text-aui-fg">{reach(c)}</dd>
            <dt className="text-aui-muted">Secrets</dt>
            <dd className="text-aui-fg">
              {c.credentials.token.ref || c.credentials.signing_secret.ref ? "In the server's environment" : "Held by the server"}
            </dd>
            {limits(c) && (
              <>
                <dt className="text-aui-muted">Limits</dt>
                <dd className="text-aui-fg">{limits(c)}</dd>
              </>
            )}
          </dl>
          {c.managed ? (
            <p className="text-aui-xs text-aui-muted">Configured by the server's environment. It is changed there, not here.</p>
          ) : (
            canWrite && (
              <div className="flex flex-wrap items-center justify-between gap-3">
                <Switch
                  label="Enabled"
                  hint="A disabled client answers nothing and carries nothing."
                  checked={c.enabled}
                  onChange={(next) => enable(c, next)}
                />
                <button type="button" className={button} onClick={() => remove(c)}>
                  Remove
                </button>
              </div>
            )
          )}
          {!c.managed && !canWrite && !c.enabled && <p className="text-aui-xs text-aui-muted">Disabled.</p>}
        </section>
      ))}
      {caps && clients && clients.length > 0 && (
        <p className="text-aui-xs text-aui-muted">
          {clients.length} of {caps.max} clients, and at most {caps.max_per_surface} on one surface.
        </p>
      )}
    </>
  );
}
