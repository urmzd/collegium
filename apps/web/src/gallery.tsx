import { StrictMode, useState } from "react";
import { createRoot } from "react-dom/client";
import { orbLabel, type OrbState } from "@vultus/web";
import "@fontsource-variable/figtree";
import "@fontsource-variable/bricolage-grotesque/wdth.css";
import "./styles.css";
import { ActivityPanel } from "./Activity.tsx";
import { sampleTrees } from "./activity.fixtures.ts";
import { InboxPage } from "./InboxPage.tsx";
import { memoryTransport } from "./mail.fixtures.ts";
import { LimitsPanel } from "./Limits.tsx";
import { overLimit, parentBound, sessionBound, unlimited } from "./limits.fixtures.ts";
import { RunItem } from "./RunItem.tsx";
import { SessionGauge } from "./SessionGauge.tsx";
import { Storage, StorageUsed, StoredList } from "./Storage.tsx";
import { memoryStorage, sampleUse } from "./storage.fixtures.ts";
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
 * it is also what a browser test can load. It is not part of the built console.
 */
const states: StrokeState[] = ["idle", "working", "waiting", "failed", "done"];
const agents = ["lead", "oncall", "releaser", "helper", "billing", "triage"];
const M = 1_000_000;
// One inbox for the page's life, so what is resolved in it stays resolved.
const inbox = memoryTransport();
const trees = sampleTrees();
const files = memoryStorage();

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-4">
      <h2 className="collegium-display text-aui-lg font-semibold text-aui-fg">{title}</h2>
      {children}
    </section>
  );
}

function Gallery() {
  const [theme, setTheme] = useState<"dark" | "light">("dark");
  const [limit, setLimit] = useState<number | null>(null);
  const [period, setPeriod] = useState<Period>("month");
  const [run, setRun] = useState<string | null>(null);
  document.documentElement.dataset.theme = theme;

  return (
    <main className="flex max-w-[68rem] flex-col gap-10 px-5 py-8 md:px-10">
      <header className="flex items-end justify-between gap-4">
        <h1 className="collegium-display text-[1.875rem] leading-[1.15] font-semibold text-aui-fg">Console primitives</h1>
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

      <Section title="Agent marks: activity within the row">
        <div id="agent-marks" data-testid="gallery-agent-marks" className="grid grid-cols-2 gap-4 sm:grid-cols-5">
          {(["idle", "thinking", "searching", "calling", "delegating", "composing", "waiting", "done", "error", "stopped"] as OrbState[]).map((state) => (
            <div key={state} className="flex flex-col gap-2">
              <span className="text-aui-xs text-aui-muted">{orbLabel[state]}</span>
              {[12, 14, 16, 18, 24, 32].map((size) => (
                <div key={size} data-mark-size={size} className="flex items-center gap-2 overflow-hidden text-aui-xs" style={{ height: size }}>
                  <AgentMark name="oncall" size={size} state={state} />
                  <span>oncall · {size}px</span>
                </div>
              ))}
            </div>
          ))}
        </div>
      </Section>

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

      <Section title="Run item: the status, fill and export control under an answer">
        <div className="flex flex-wrap items-center gap-3" data-testid="gallery-run-items">
          {(() => {
            const tree = trees[1];
            const base = tree?.runs[0];
            if (!tree || !base) return null;
            const load = async () => tree;
            return (
              <>
                <RunItem run={{ ...base, status: "accepted" }} load={load} onActivity={setRun} />
                <RunItem run={{ ...base, status: "running", calls: 0 }} load={load} onActivity={setRun} />
                <RunItem run={{ ...base, status: "running", lease: 2_000_000, granted: 1_700_000 }} load={load} onActivity={setRun} />
                <RunItem run={{ ...base, status: "parked", reason: "approval to deploy" }} load={load} onActivity={setRun} />
                <RunItem run={{ ...base, status: "failed", agent: "releaser" }} load={load} onActivity={setRun} />
                <RunItem run={base} load={load} onActivity={setRun} />
                <RunItem run={{ ...base, agent: "oncall", lease: 2_000_000, granted: 500_000 }} load={load} onActivity={setRun} />
              </>
            );
          })()}
        </div>
      </Section>

      <Section title="Limits in the gear: session bound, parent bound, none, and over">
        <div className="flex flex-wrap items-center gap-6" data-testid="gallery-gauges">
          {[sessionBound(), parentBound(), unlimited(), overLimit()].map((l, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: a fixed list of four states
            <SessionGauge key={i} limits={l} onAnalytics={() => {}} onRaise={() => {}} />
          ))}
        </div>
        <div className="grid grid-cols-1 gap-6 md:grid-cols-2 lg:grid-cols-4">
          {[sessionBound(), parentBound(), unlimited(), overLimit()].map((l, i) => (
            // biome-ignore lint/suspicious/noArrayIndexKey: a fixed list of four states
            <div key={i} className="collegium-glass rounded-aui-lg p-1.5">
              <LimitsPanel data={l} onAnalytics={() => {}} onRaise={() => {}} />
            </div>
          ))}
        </div>
      </Section>

      <Section title="Activity: a conversation's runs, then one run's whole conversation">
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
          {/* The panel is a full sheet below the lg width. A transform keeps each one inside its box here. */}
          <div data-testid="gallery-activity" className="relative flex h-[40rem] overflow-hidden rounded-aui-lg border border-[var(--collegium-rule)] [transform:translateZ(0)] [&>aside]:w-full">
            <ActivityPanel trees={trees} selected={run} onSelect={setRun} onClose={() => setRun(null)} onJump={() => {}} />
          </div>
          <div className="relative flex h-[40rem] overflow-hidden rounded-aui-lg border border-[var(--collegium-rule)] [transform:translateZ(0)] [&>aside]:w-full">
            <ActivityPanel trees={trees} selected="r2a" onSelect={() => {}} onClose={() => {}} onJump={() => {}} />
          </div>
          <div className="relative flex h-[40rem] overflow-hidden rounded-aui-lg border border-[var(--collegium-rule)] [transform:translateZ(0)] [&>aside]:w-full">
            <ActivityPanel trees={[]} selected={null} onSelect={() => {}} onClose={() => {}} />
          </div>
        </div>
      </Section>

      <Section title="Inbox, held in memory with one message of every kind">
        <div data-testid="gallery-inbox" className="flex h-[44rem] flex-col overflow-hidden rounded-aui-lg border border-[var(--collegium-rule)]">
          <InboxPage signal={0} viewer="You" via={inbox} onOpen={() => {}} />
        </div>
      </Section>

      <Section title="Storage, held in memory, then its over-limit, loading and failed states">
        <div data-testid="gallery-storage" className="overflow-hidden rounded-aui-lg border border-[var(--collegium-rule)]">
          <Storage signal={0} agents={["lead", "billing", "oncall", "releaser"]} projects={["Launch"]} canDelete via={files} onOpenRun={() => {}} onOpenThread={() => {}} />
        </div>
        <div className="@container grid grid-cols-1 gap-6 md:grid-cols-3">
          <StorageUsed use={sampleUse({ used_bytes: 6 * 1024 ** 2, by_project: [] })} />
          <StoredList files={[]} selected={null} state="loading" narrowed={false} onSelect={() => {}} />
          <StoredList files={[]} selected={null} state="failed" narrowed={false} onSelect={() => {}} />
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
