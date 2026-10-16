import type { ReactNode } from "react";

/** Before the first message, the prompt is the page. Chat owns its controls and submission. */
export function HomePage({ greeting, canRun, hasAgents, children }: {
  greeting: string;
  canRun: boolean;
  hasAgents: boolean;
  children: ReactNode;
}) {
  return (
    <section aria-label="New conversation" className="aui-scroll collegium-fade flex min-h-0 flex-1 flex-col justify-center overflow-y-auto py-8">
      <h1 className="collegium-display px-6 pb-6 text-center text-[2rem] font-semibold leading-tight text-aui-fg">{greeting}</h1>
      {!canRun ? (
        <p className="px-6 text-center text-aui-sm text-aui-muted">You do not have permission to start conversations in this workspace.</p>
      ) : !hasAgents ? (
        <p className="px-6 text-center text-aui-sm text-aui-muted">An agent needs to be configured before you can start a conversation.</p>
      ) : children}
    </section>
  );
}
