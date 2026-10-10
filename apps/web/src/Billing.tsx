import { type FormEvent, useCallback, useEffect, useState } from "react";
import { ApiError, api, type Policy, type PolicyChange, type PolicyScope, type ScopeUsage } from "./api.ts";
import { ago, button, card, dollars, field, FlashNote, Label, LimitField, Menu, PolicyBadge, primary, Problem, SkeletonRows, UsageMeter, useFlash } from "./ui.tsx";

const kindWord: Record<PolicyScope["kind"], string> = {
  tenant: "Workspace",
  project: "Project",
  agent: "Agent",
};
const same = (a: PolicyScope, b: PolicyScope) => a.kind === b.kind && (a.name ?? "") === (b.name ?? "");
const keyOf = (s: PolicyScope) => `${s.kind}:${s.name ?? ""}`;
const plain = (n: number) => n.toLocaleString();

/** A scope in words: "Workspace", "Project Launch". */
export function scopeName(s: PolicyScope): string {
  return s.name ? `${kindWord[s.kind]} ${s.name}` : kindWord[s.kind];
}

/** A policy's limits in one line, for lists and history. */
function brief(p: Policy): string {
  const parts = [
    p.max_runs ? `${plain(p.max_runs)} runs a ${p.period}` : "",
    p.max_concurrent ? `${plain(p.max_concurrent)} at once` : "",
    p.spend_limit ? `${dollars(p.spend_limit)} a ${p.period}` : "",
    p.max_lease ? `${dollars(p.max_lease)} a run` : "",
  ].filter(Boolean);
  return parts.join(", ") || "no limits";
}

/** Every policy in the workspace and where each scope stands. Reloads with `tick`. */
export function useBilling(tick = 0) {
  const [policies, setPolicies] = useState<Policy[]>([]);
  const [usage, setUsage] = useState<ScopeUsage[]>([]);
  const [changes, setChanges] = useState<PolicyChange[]>([]);
  const [loaded, setLoaded] = useState(false);
  const reload = useCallback(() => {
    Promise.all([api.policies(), api.usage(), api.policyHistory()])
      .then(([p, u, h]) => {
        setPolicies(p);
        setUsage(u);
        setChanges(h);
        setLoaded(true);
      })
      .catch(() => setLoaded(true));
  }, []);
  // biome-ignore lint/correctness/useExhaustiveDependencies: tick is the signal to load again
  useEffect(reload, [reload, tick]);
  return { policies, usage, changes, loaded, reload };
}

/** Where one scope stands against each limit it sets. Limits it does not set are left out. */
export function Meters({ usage, size = "row" }: { usage: ScopeUsage; size?: "row" | "header" }) {
  const warnAt = usage.warn_at / 100;
  // A limit that blocks cannot be passed, so a projection beyond it would promise something that will not happen.
  const ahead = (m: { limit: number; projected?: number }) =>
    usage.action === "block" && m.projected !== undefined && m.projected > m.limit ? undefined : m.projected;
  const named = (label: string) => (size === "row" ? <span className="text-aui-xs text-aui-muted">{label}</span> : null);
  if (usage.runs.limit <= 0 && usage.concurrent.limit <= 0 && usage.spend.limit <= 0) return null;
  return (
    <div className="grid grid-cols-1 gap-x-6 gap-y-3 @lg:grid-cols-2 @4xl:grid-cols-3">
      {usage.runs.limit > 0 && (
        <div className="flex flex-col gap-1">
          {named(`Runs a ${usage.period}`)}
          <UsageMeter
            label="Runs"
            size={size}
            spent={usage.runs.used}
            limit={usage.runs.limit}
            projected={ahead(usage.runs)}
            warnAt={warnAt}
            period={usage.period}
            format={plain}
          />
        </div>
      )}
      {usage.concurrent.limit > 0 && (
        <div className="flex flex-col gap-1">
          {named("Running now")}
          <UsageMeter
            label="Running now"
            size={size}
            spent={usage.concurrent.used}
            limit={usage.concurrent.limit}
            warnAt={warnAt}
            format={plain}
          />
        </div>
      )}
      {usage.spend.limit > 0 && (
        <div className="flex flex-col gap-1">
          {named(`Spend a ${usage.period}`)}
          <UsageMeter
            label="Spend"
            size={size}
            spent={usage.spend.used}
            limit={usage.spend.limit}
            projected={usage.spend.metered ? ahead(usage.spend) : undefined}
            warnAt={warnAt}
            period={usage.period}
          />
          {!usage.spend.metered && <span className="text-aui-2xs text-aui-muted">Recorded, not enforced yet.</span>}
        </div>
      )}
    </div>
  );
}

const count = (text: string) => (text.trim() === "" ? 0 : Number(text));

/**
 * The limits one scope sets: how it stands against them, and a form to change
 * them. A narrower scope can only tighten what the workspace allows, and the
 * server says so when a change would loosen it.
 */
export function Limits({
  scope,
  policy,
  usage,
  canWrite,
  onChanged,
}: {
  scope: PolicyScope;
  policy?: Policy;
  usage?: ScopeUsage;
  canWrite: boolean;
  onChanged: () => void;
}) {
  const [runs, setRuns] = useState(policy?.max_runs ? String(policy.max_runs) : "");
  const [atOnce, setAtOnce] = useState(policy?.max_concurrent ? String(policy.max_concurrent) : "");
  const [spend, setSpend] = useState<number | null>(policy?.spend_limit || null);
  const [period, setPeriod] = useState<Policy["period"]>(policy?.period ?? "day");
  const [problem, setProblem] = useState<string | null>(null);
  const { flash, say } = useFlash();

  // Follow the saved policy when it changes underneath the form.
  // biome-ignore lint/correctness/useExhaustiveDependencies: the version is what says it changed
  useEffect(() => {
    setRuns(policy?.max_runs ? String(policy.max_runs) : "");
    setAtOnce(policy?.max_concurrent ? String(policy.max_concurrent) : "");
    setSpend(policy?.spend_limit || null);
    setPeriod(policy?.period ?? "day");
  }, [policy?.version]);

  const fail = (fallback: string) => (e: unknown) => setProblem(e instanceof ApiError ? e.message : fallback);
  const empty = count(runs) === 0 && count(atOnce) === 0 && !spend;
  const dirty =
    count(runs) !== (policy?.max_runs ?? 0) ||
    count(atOnce) !== (policy?.max_concurrent ?? 0) ||
    (spend ?? 0) !== (policy?.spend_limit ?? 0) ||
    (policy !== undefined && period !== policy.period);

  const save = (e: FormEvent) => {
    e.preventDefault();
    setProblem(null);
    api
      .savePolicy({
        scope,
        period,
        max_runs: count(runs),
        max_concurrent: count(atOnce),
        spend_limit: spend ?? 0,
        max_lease: policy?.max_lease,
        warn_at: policy?.warn_at,
      })
      .then(() => {
        say("Saved");
        onChanged();
      })
      .catch(fail("The limits could not be saved."));
  };

  return (
    <div className="flex flex-col gap-4">
      <Problem message={problem} />
      {usage && <Meters usage={usage} />}
      {canWrite ? (
        <form onSubmit={save} className="flex flex-col gap-3">
          <div className="grid grid-cols-1 gap-3 @lg:grid-cols-2 @3xl:grid-cols-[1fr_1fr_1.6fr]">
            <Label text={`Runs a ${period}`}>
              <input
                className={field}
                type="number"
                min={0}
                step={1}
                placeholder="No limit"
                value={runs}
                onChange={(e) => setRuns(e.target.value)}
              />
            </Label>
            <Label text="Running at once">
              <input
                className={field}
                type="number"
                min={0}
                step={1}
                placeholder="No limit"
                value={atOnce}
                onChange={(e) => setAtOnce(e.target.value)}
              />
            </Label>
            <div className="@lg:col-span-2 @3xl:col-span-1">
              <LimitField
                label="Spend"
                value={spend}
                period={period}
                onChange={(v, p) => {
                  setSpend(v);
                  setPeriod(p);
                }}
              />
            </div>
          </div>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-aui-xs text-aui-muted">
              {policy
                ? `Version ${policy.version}, ${ago(policy.updated_at)}${policy.updated_by ? ` by ${policy.updated_by}` : ""}`
                : "No limits set."}
            </span>
            <span className="flex gap-2">
              {policy && (
                <button
                  type="button"
                  className={button}
                  onClick={() => {
                    setProblem(null);
                    api.deletePolicy(scope).then(onChanged).catch(fail("The limits could not be removed."));
                  }}
                >
                  Remove Limits
                </button>
              )}
              <span className="relative">
                <FlashNote flash={flash} align="right" />
                <button type="submit" className={primary} disabled={!dirty || empty}>
                  Save Limits
                </button>
              </span>
            </span>
          </div>
        </form>
      ) : (
        !policy && <p className="text-aui-sm text-aui-muted">No limits set.</p>
      )}
    </div>
  );
}

/** The limits of one project or agent, with what the workspace allows named beside it. */
export function ScopeLimits({ scope, canWrite, tick }: { scope: PolicyScope; canWrite: boolean; tick?: number }) {
  const { policies, usage, loaded, reload } = useBilling(tick);
  if (!loaded) return <SkeletonRows count={2} />;
  const policy = policies.find((p) => same(p.scope, scope));
  const above = policies.find((p) => p.scope.kind === "tenant");
  return (
    <div className="flex flex-col gap-3">
      <Limits scope={scope} policy={policy} usage={usage.find((u) => same(u.scope, scope))} canWrite={canWrite} onChanged={reload} />
      {above && <p className="text-aui-xs text-aui-muted">The workspace allows {brief(above)}. Limits here can only be tighter.</p>}
    </div>
  );
}

/** Every scope with limits, as meters. For a page that reports rather than edits. */
export function Burn({ tick }: { tick?: number }) {
  const { usage, loaded } = useBilling(tick);
  if (!loaded || usage.length === 0) return null;
  return (
    <section className={`${card} flex flex-col gap-4`}>
      <h2 className="text-aui-base font-medium text-aui-fg">Usage against limits</h2>
      <ul className="flex flex-col gap-4">
        {usage.map((u) => (
          <li
            key={keyOf(u.scope)}
            data-state={u.state}
            className="flex flex-col gap-2 border-aui-border border-t pt-3 first:border-t-0 first:pt-0"
          >
            <div className="flex flex-wrap items-center justify-between gap-2">
              <PolicyBadge scope={kindWord[u.scope.kind]} name={u.scope.name} action={u.action} />
              <span className="text-aui-xs text-aui-muted">
                Resets{" "}
                {new Date(u.period_end).toLocaleString(undefined, {
                  month: "short",
                  day: "numeric",
                  hour: "2-digit",
                  minute: "2-digit",
                })}
              </span>
            </div>
            <Meters usage={u} />
          </li>
        ))}
      </ul>
    </section>
  );
}

/** The settings section: workspace limits, the narrower ones, and who changed what. */
export function BillingSettings({ projects, agents, canWrite }: { projects: string[]; agents: string[]; canWrite: boolean }) {
  const { policies, usage, changes, loaded, reload } = useBilling();
  const [adding, setAdding] = useState<PolicyScope | null>(null);
  const tenant: PolicyScope = { kind: "tenant" };
  const narrower = policies.filter((p) => p.scope.kind !== "tenant");
  const open: PolicyScope[] = [
    ...projects.map((name): PolicyScope => ({ kind: "project", name })),
    ...agents.map((name): PolicyScope => ({ kind: "agent", name })),
  ].filter((s) => !narrower.some((p) => same(p.scope, s)));
  const shown =
    adding && !narrower.some((p) => same(p.scope, adding)) ? [...narrower.map((p) => p.scope), adding] : narrower.map((p) => p.scope);
  const changed = () => {
    setAdding(null);
    reload();
  };

  return (
    <div className="@container flex flex-col gap-4">
      <header>
        <h1 className="text-aui-xl font-semibold text-aui-fg">Limits</h1>
        <p className="text-aui-sm text-aui-muted">
          A run that would pass a limit is refused. {canWrite ? "" : "Only an admin can change these."}
        </p>
      </header>
      {!loaded && <SkeletonRows count={4} height="4rem" />}
      {loaded && (
        <>
          <section className={`${card} flex flex-col gap-3`}>
            <h2 className="text-aui-sm font-medium text-aui-fg">Workspace</h2>
            <Limits
              scope={tenant}
              policy={policies.find((p) => p.scope.kind === "tenant")}
              usage={usage.find((u) => u.scope.kind === "tenant")}
              canWrite={canWrite}
              onChanged={changed}
            />
          </section>

          <section className={`${card} flex flex-col gap-3`}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h2 className="text-aui-sm font-medium text-aui-fg">Projects and agents</h2>
              {canWrite && open.length > 0 && (
                <Menu
                  label="Add limits for"
                  className="w-52"
                  value=""
                  options={[
                    { value: "", label: "Add limits for" },
                    ...open.map((s) => ({
                      value: keyOf(s),
                      label: scopeName(s),
                    })),
                  ]}
                  onChange={(k) => setAdding(open.find((s) => keyOf(s) === k) ?? null)}
                />
              )}
            </div>
            {shown.length === 0 ? (
              <p className="text-aui-sm text-aui-muted">None. Everything shares the workspace limits.</p>
            ) : (
              <ul className="flex flex-col">
                {shown.map((s) => (
                  <li key={keyOf(s)} className="flex flex-col gap-3 border-aui-border border-t py-3 first:border-t-0 first:pt-0 last:pb-0">
                    <h3 className="text-aui-sm text-aui-fg">{scopeName(s)}</h3>
                    <Limits
                      scope={s}
                      policy={policies.find((p) => same(p.scope, s))}
                      usage={usage.find((u) => same(u.scope, s))}
                      canWrite={canWrite}
                      onChanged={changed}
                    />
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className={`${card} flex flex-col gap-2`}>
            <h2 className="text-aui-sm font-medium text-aui-fg">Changes</h2>
            {changes.length === 0 ? (
              <p className="text-aui-sm text-aui-muted">No changes yet.</p>
            ) : (
              <ul>
                {changes.slice(0, 30).map((c) => (
                  <li
                    key={`${keyOf(c.scope)}:${c.at}:${c.after?.version ?? "x"}`}
                    className="collegium-enter border-aui-border border-t py-2 text-aui-sm first:border-t-0"
                  >
                    <span className="text-aui-fg">
                      {scopeName(c.scope)}: {c.after ? brief(c.after) : "limits removed"}
                    </span>
                    <span className="block text-aui-xs text-aui-muted">
                      {c.before && c.after ? `Was ${brief(c.before)}. ` : ""}
                      {c.by}, {ago(c.at)}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </>
      )}
    </div>
  );
}
