import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import "@fontsource-variable/figtree";
import "@fontsource-variable/bricolage-grotesque/wdth.css";
import "./styles.css";
import type { Me, Preferences, Settings } from "./api.ts";
import { Onboarding } from "./Onboarding.tsx";
import {
  AgentMark,
  counted,
  LimitField,
  type Period,
  PolicyBadge,
  Segmented,
  Stroke,
  type StrokeState,
  UsageMeter,
} from "./ui.tsx";

/**
 * Every state of the console's own primitives, drawn from props alone. Open
 * /gallery.html on the dev server. Rendering calls nothing on the platform, so
 * it is also what a browser test can load. The onboarding buttons are the one
 * exception: pressing them saves, as they do in the console. It is not part of
 * the built console.
 */
const states: StrokeState[] = ["idle", "working", "waiting", "failed", "done"];
const agents = ["lead", "oncall", "releaser", "helper", "billing", "triage"];
const M = 1_000_000;
const person: Me = {
  principal: { subject: "gallery", tenant: "default", kind: "user", name: "Ada Lovelace", email: "ada@example.com" },
  permissions: [],
};
const workspace = { baseline: "", history: "recent", history_limit: 10, projects: [], title_agent: "", memory_agent: "", memory_auto_approve: false } as Settings;

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-4">
      <h2 className="oap-display text-aui-lg font-semibold text-aui-fg">{title}</h2>
      {children}
    </section>
  );
}

function Gallery() {
  const [theme, setTheme] = useState<"dark" | "light">("dark");
  const [limit, setLimit] = useState<number | null>(null);
  const [period, setPeriod] = useState<Period>("month");
  const [prefs, setPrefs] = useState<Preferences>({ display_name: "", theme: "system", accent: "purple", has_photo: false, photo_version: 0 });
  document.documentElement.dataset.theme = theme;

  return (
    <main className="flex max-w-[68rem] flex-col gap-10 px-5 py-8 md:px-10">
      <header className="flex items-end justify-between gap-4">
        <h1 className="oap-display text-[1.875rem] leading-[1.15] font-semibold text-aui-fg">Console primitives</h1>
        <Segmented
          label="Theme"
          value={theme}
          options={[
            { id: "dark", label: "Dark" },
            { id: "light", label: "Light" },
          ]}
          onChange={setTheme}
        />
      </header>

      <Section title="Stroke, by state">
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-5">
          {states.map((s) => (
            <Stroke key={s} agent="oncall" state={s} testId={`stroke-${s}`} className="py-2 pl-4">
              <p className="text-aui-sm text-aui-fg">{s}</p>
              <p className="text-aui-xs text-aui-muted">oncall</p>
            </Stroke>
          ))}
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <Stroke testId="stroke-neutral" className="py-2 pl-4">
            <p className="text-aui-sm text-aui-fg">No agent</p>
            <p className="text-aui-xs text-aui-muted">A person or the system</p>
          </Stroke>
          <Stroke agent="lead" state="done" edge="under" testId="stroke-under" className="pb-2">
            <p className="text-aui-sm text-aui-fg">Edge: under</p>
          </Stroke>
          <Stroke agent="lead" state="waiting" edge="around" testId="stroke-around" className="rounded-aui-lg p-3">
            <p className="text-aui-sm text-aui-fg">Edge: around, waiting</p>
          </Stroke>
        </div>
      </Section>

      <Section title="Stroke, by agent">
        <div className="grid grid-cols-2 gap-4 sm:grid-cols-6">
          {agents.map((a) => (
            <Stroke key={a} agent={a} state="done" className="flex items-center gap-2 py-2 pl-4">
              <AgentMark name={a} size={14} />
              <span className="text-aui-sm text-aui-fg">{a}</span>
            </Stroke>
          ))}
        </div>
      </Section>

      <Section title="Usage meter, header size">
        <div className="grid grid-cols-1 gap-8 md:grid-cols-2">
          <UsageMeter size="header" label="Workspace spend" spent={12 * M} limit={50 * M} period="month" testId="meter-under" />
          <UsageMeter size="header" label="Project Launch" spent={43 * M} limit={50 * M} period="month" projected={58 * M} testId="meter-warning" />
          <UsageMeter size="header" label="Agent oncall" spent={61.5 * M} limit={50 * M} period="month" testId="meter-over" />
          <UsageMeter size="header" label="Agent helper" spent={3.2 * M} testId="meter-none" />
        </div>
      </Section>

      <Section title="Usage meter, row size">
        <div className="grid grid-cols-1 gap-x-8 gap-y-4 sm:grid-cols-2 md:grid-cols-4">
          <UsageMeter label="lead" spent={4 * M} limit={20 * M} period="day" />
          <UsageMeter label="oncall" spent={17 * M} limit={20 * M} period="day" />
          <UsageMeter label="releaser" spent={26 * M} limit={20 * M} period="day" />
          <UsageMeter label="Runs today" spent={840} limit={1000} projected={1150} format={counted("runs")} testId="meter-runs" />
        </div>
      </Section>

      <Section title="Limit field">
        <div className="grid grid-cols-1 gap-6 md:grid-cols-3">
          <LimitField
            label="Project limit"
            value={limit}
            period={period}
            inherits={{ scope: "the workspace", value: 50 * M, period: "month" }}
            onChange={(v, p) => {
              setLimit(v);
              setPeriod(p);
            }}
          />
          <LimitField label="Workspace limit" value={250 * M} period="month" onChange={() => {}} testId="limit-set" />
          <LimitField label="Agent limit" value={null} period="day" onChange={() => {}} disabled testId="limit-disabled" />
        </div>
      </Section>

      <Section title="Onboarding, for everyone and for the first admin">
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
          {[false, true].map((first) => (
            <div key={String(first)} className="h-[46rem] overflow-hidden rounded-aui-lg border border-[var(--oap-rule)]">
              <Onboarding
                me={person}
                prefs={prefs}
                workspace={workspace}
                firstAdmin={first}
                onPrefs={setPrefs}
                onWorkspace={() => {}}
                onDone={() => {}}
              />
            </div>
          ))}
        </div>
      </Section>

      <Section title="Policy badge">
        <div className="flex flex-wrap gap-2">
          <PolicyBadge scope="Workspace" action="block" />
          <PolicyBadge scope="Project" name="Launch" action="approve" />
          <PolicyBadge scope="Agent" name="oncall" action="block" />
        </div>
      </Section>
    </main>
  );
}

const root = document.getElementById("root");
if (!root) throw new Error("missing #root");
createRoot(root).render(
  <StrictMode>
    <Gallery />
  </StrictMode>,
);
