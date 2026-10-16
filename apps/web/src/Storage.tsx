import { useEffect, useState } from "react";
import { bytes, contentUrl, type Filter, kindWord, type Stored, type StorageTransport, type StorageUse, showsInline, transport as platform, useStorage } from "./storage.ts";
import { AgentMark, agentOptions, ago, button, Chip, Empty, Menu, Page, Problem, primary, rows, SearchField, SkeletonRows, UsageMeter } from "./ui.tsx";

const linkish =
  "rounded-aui-sm text-aui-fg underline underline-offset-2 hover:no-underline focus-visible:outline-2 focus-visible:outline-aui-accent";

/** How much is stored, against the limit when there is one, and where it is. */
export function StorageUsed({ use }: { use: StorageUse }) {
  const top = [...use.by_project].sort((a, b) => b.bytes - a.bytes).slice(0, 4);
  return (
    <section data-testid="storage-use" className="grid grid-cols-1 gap-x-10 gap-y-5 border-[var(--collegium-rule)] border-y py-5 @3xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
      <UsageMeter
        size="header"
        label={`Storage, ${use.count} ${use.count === 1 ? "file" : "files"}`}
        spent={use.used_bytes}
        limit={use.limit_bytes}
        format={bytes}
        testId="storage-meter"
      />
      {top.length > 0 && (
        <ul className="flex flex-col gap-2" aria-label="Storage by project">
          {top.map((p) => (
            <li key={p.name} className="flex items-baseline justify-between gap-3 text-aui-sm">
              <span className="min-w-0 truncate text-aui-fg">{p.name || "Projectless"}</span>
              <span className="shrink-0 text-aui-xs tabular-nums text-aui-muted">
                {bytes(p.bytes)}, {p.count} {p.count === 1 ? "file" : "files"}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** One file, opened: what it is, a look at it where that is safe, its versions, and what it came from. */
export function StoredDetail({
  file,
  versions,
  canDelete,
  onClose,
  onDelete,
  onOpenRun,
  onOpenThread,
}: {
  file: Stored;
  /** Every version, newest first. Null while they are being fetched. */
  versions: Stored[] | null;
  canDelete: boolean;
  onClose: () => void;
  onDelete: (file: Stored) => void;
  onOpenRun: (id: string) => void;
  onOpenThread: (id: string) => void;
}) {
  const [confirming, setConfirming] = useState(false);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a different file starts unconfirmed
  useEffect(() => setConfirming(false), [file.id]);

  return (
    <section data-testid="stored-detail" aria-label={file.title} className="collegium-enter collegium-glass flex flex-col gap-4 rounded-aui-lg p-4">
      <header className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="collegium-display truncate text-aui-lg font-semibold text-aui-fg">{file.title}</h2>
          <p className="flex flex-wrap items-center gap-x-2 gap-y-1 pt-0.5 text-aui-xs text-aui-muted">
            <span>{kindWord[file.kind]}</span>
            <span>{bytes(file.size)}</span>
            <span>Version {file.version}</span>
            <span className="inline-flex items-center gap-1">
              <AgentMark name={file.agent} size={12} />
              {file.agent}
            </span>
            {file.project && <span>{file.project}</span>}
            <time dateTime={file.created_at}>{ago(file.created_at)}</time>
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          <a className={primary} href={contentUrl(file.id)} download={file.title}>
            Download
          </a>
          <button type="button" className={button} onClick={onClose}>
            Close
          </button>
        </div>
      </header>

      {file.description && <p className="max-w-[68ch] text-aui-sm text-aui-muted">{file.description}</p>}

      {showsInline(file) ? (
        <img
          src={contentUrl(file.id, true)}
          alt={file.description || file.title}
          className="max-h-96 w-auto max-w-full self-start rounded-aui border border-[var(--collegium-rule)] object-contain"
        />
      ) : file.preview ? (
        <pre data-testid="stored-preview" className="aui-scroll max-h-72 overflow-auto rounded-aui bg-aui-code p-3 font-aui-mono text-aui-xs text-aui-fg">
          {file.preview}
        </pre>
      ) : (
        <p className="text-aui-sm text-aui-muted">No preview for this kind of file. Download it to open it.</p>
      )}

      <p className="flex flex-wrap gap-x-4 gap-y-1 text-aui-xs text-aui-muted">
        <button type="button" className={linkish} onClick={() => onOpenThread(file.thread)}>
          Open the conversation
        </button>
        <button type="button" className={linkish} onClick={() => onOpenRun(file.run)}>
          Open the run
        </button>
        <span className="min-w-0 truncate" title={file.sha256}>
          SHA-256 {file.sha256.slice(0, 12)}
        </span>
      </p>

      <div className="flex flex-col gap-1">
        <h3 className="text-aui-xs text-aui-muted">Versions</h3>
        {versions === null ? (
          <p className="text-aui-sm text-aui-muted">Loading</p>
        ) : (
          <ul className={rows}>
            {versions.map((v) => (
              <li key={v.id} className="flex items-center justify-between gap-3 py-1.5 text-aui-sm">
                <span className="text-aui-fg">
                  Version {v.version}
                  {v.id === file.id && <span className="pl-2 text-aui-xs text-aui-muted">shown</span>}
                </span>
                <span className="flex shrink-0 items-center gap-3 text-aui-xs text-aui-muted">
                  <span className="tabular-nums">{bytes(v.size)}</span>
                  <time dateTime={v.created_at}>{ago(v.created_at)}</time>
                  <a className={linkish} href={contentUrl(v.id)} download={v.title}>
                    Download
                  </a>
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>

      {canDelete && (
        <div className="flex flex-wrap items-center gap-3 border-[var(--collegium-rule)] border-t pt-3">
          {confirming ? (
            <>
              <span className="text-aui-sm text-aui-fg">Delete version {file.version} of this file? This cannot be undone.</span>
              <button type="button" className={button} onClick={() => onDelete(file)}>
                Delete Version
              </button>
              <button type="button" className={button} onClick={() => setConfirming(false)}>
                Keep It
              </button>
            </>
          ) : (
            <button type="button" className={button} onClick={() => setConfirming(true)}>
              Delete
            </button>
          )}
        </div>
      )}
    </section>
  );
}

/** The list of files, with the state it is in said plainly when there is nothing to list. */
export function StoredList({
  files,
  selected,
  state,
  narrowed,
  onSelect,
}: {
  files: Stored[];
  selected: string | null;
  state: "ready" | "loading" | "failed";
  /** A filter or a search is on, so an empty list is "no match", not "nothing yet". */
  narrowed: boolean;
  onSelect: (id: string) => void;
}) {
  if (state === "failed") return <Empty title="Storage could not be loaded">It will be tried again when something changes. Reload the page to try now.</Empty>;
  if (files.length === 0) {
    if (state === "loading") return <SkeletonRows count={5} height="2.5rem" />;
    return narrowed ? (
      <Empty title="No files match">Change the filters or the search to see the rest.</Empty>
    ) : (
      <Empty title="Nothing is stored yet">When an agent publishes a file from its sandbox, it is kept here with every later version.</Empty>
    );
  }
  return (
    <ul className={rows} aria-label="Files">
      {files.map((f) => (
        <li key={f.id} className="collegium-enter">
          <button
            type="button"
            data-testid="stored-row"
            aria-current={f.id === selected ? "true" : undefined}
            onClick={() => onSelect(f.id)}
            className="flex w-full items-center gap-3 px-1 py-2.5 text-left transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent focus-visible:-outline-offset-2 aria-[current=true]:bg-aui-hover"
          >
            <span className="min-w-0 flex-1">
              <span className="block truncate text-aui-sm font-medium text-aui-fg">{f.title}</span>
              {f.description && <span className="block truncate text-aui-xs text-aui-muted">{f.description}</span>}
            </span>
            <Chip>{kindWord[f.kind]}</Chip>
            <span className="hidden w-14 shrink-0 text-right text-aui-xs text-aui-muted @xl:inline">v{f.version}</span>
            <span className="hidden w-16 shrink-0 text-right text-aui-xs tabular-nums text-aui-muted @lg:inline">{bytes(f.size)}</span>
            <span className="hidden w-32 shrink-0 items-center gap-1.5 truncate text-aui-xs text-aui-muted @2xl:inline-flex">
              <AgentMark name={f.agent} size={12} />
              <span className="truncate">{f.agent}</span>
            </span>
            <span className="hidden w-28 shrink-0 truncate text-aui-xs text-aui-muted @4xl:inline">{f.project ?? ""}</span>
            <span className="w-24 shrink-0 text-right text-aui-xs text-aui-muted">{ago(f.created_at)}</span>
          </button>
        </li>
      ))}
    </ul>
  );
}

/**
 * The storage center: every file agents have published, newest first, with
 * how much is stored against the limit. The filters and the search are the
 * platform's, so they reach every file and not only the ones loaded.
 */
export function Storage({
  signal,
  agents,
  projects,
  canDelete,
  onOpenRun,
  onOpenThread,
  via = platform,
}: {
  /** Changes when files may have: the list and the totals are fetched again. */
  signal: unknown;
  agents: string[];
  projects: string[];
  canDelete: boolean;
  onOpenRun: (id: string) => void;
  onOpenThread: (id: string) => void;
  via?: StorageTransport;
}) {
  const [filter, setFilter] = useState<Filter>({ project: "", agent: "", kind: "", q: "" });
  const [query, setQuery] = useState("");
  // The search is the platform's. It is sent once typing pauses, not on every key.
  useEffect(() => {
    const timer = window.setTimeout(() => setFilter((f) => (f.q === query.trim() ? f : { ...f, q: query.trim() })), 250);
    return () => window.clearTimeout(timer);
  }, [query]);
  const [selected, setSelected] = useState<string | null>(null);
  const [versions, setVersions] = useState<Stored[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const store = useStorage(filter, signal, via);

  const shown = store.files;
  const file = store.files.find((f) => f.id === selected) ?? null;
  const narrowed = query.trim() !== "" || filter.project !== "" || filter.agent !== "" || filter.kind !== "";
  const over = store.use?.limit_bytes ? store.use.used_bytes > store.use.limit_bytes : false;

  useEffect(() => {
    setVersions(null);
    if (!selected) return;
    let on = true;
    via
      .versions(selected)
      .then((v) => on && setVersions(v))
      .catch(() => on && setVersions([]));
    return () => {
      on = false;
    };
  }, [selected, via]);

  const remove = (f: Stored) => {
    setProblem(null);
    via
      .remove(f.id)
      .then(() => {
        setSelected(null);
        store.reload();
      })
      .catch((e: unknown) => setProblem(e instanceof Error && e.message ? `The file could not be deleted. ${e.message}` : "The file could not be deleted."));
  };

  return (
    <Page title="Storage" lede="Files agents have published from their sandboxes. Each keeps its versions, and what made it.">
      {store.use && <StorageUsed use={store.use} />}
      {over && <Problem message="Storage is over its limit. Agents cannot publish new files until some are deleted or the limit is raised in Settings." />}
      <Problem message={problem} />

      <div className="flex flex-wrap items-center gap-2">
        <SearchField label="Search files" placeholder="Search by title" value={query} onChange={setQuery} className="min-w-56 flex-1" />
        <Menu
          label="Project"
          className="w-40"
          value={filter.project}
          options={[{ value: "", label: "Any project" }, ...projects.map((p) => ({ value: p, label: p }))]}
          onChange={(project) => setFilter({ ...filter, project })}
        />
        <Menu label="Agent" className="w-40" value={filter.agent} options={agentOptions(agents, "Any agent")} onChange={(agent) => setFilter({ ...filter, agent })} />
        <Menu
          label="Kind"
          className="w-40"
          value={filter.kind}
          options={[{ value: "", label: "Any kind" }, ...Object.entries(kindWord).map(([value, label]) => ({ value, label }))]}
          onChange={(kind) => setFilter({ ...filter, kind })}
        />
      </div>

      {file && (
        <StoredDetail
          file={file}
          versions={versions}
          canDelete={canDelete}
          onClose={() => setSelected(null)}
          onDelete={remove}
          onOpenRun={onOpenRun}
          onOpenThread={onOpenThread}
        />
      )}

      <StoredList
        files={shown}
        selected={selected}
        state={store.failed ? "failed" : store.loading === "first" ? "loading" : "ready"}
        narrowed={narrowed}
        onSelect={setSelected}
      />

      {(store.hasMore || store.loading === "more") && (
        <div className="flex justify-center">
          <button
            type="button"
            onClick={store.more}
            disabled={store.loading === "more"}
            className="inline-flex h-6 items-center rounded-full border border-[var(--collegium-rule)] px-2.5 text-aui-xs text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent disabled:cursor-default"
          >
            {store.loading === "more" ? "Loading" : store.moreFailed ? "Could not load. Try again" : "See more"}
          </button>
        </div>
      )}
    </Page>
  );
}
