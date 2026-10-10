import { type FormEvent, useEffect, useState } from "react";
import { ApiError, api, type Doc, type Hit, type Revision, type Source } from "./api.ts";
import { ago, button, card, Chip, Empty, field, Label, Page, Problem, primary } from "./ui.tsx";

export function Knowledge({
  sources,
  agents,
  canWrite,
  onChanged,
}: {
  sources: Source[];
  agents: Revision[];
  canWrite: boolean;
  onChanged: () => void;
}) {
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [draft, setDraft] = useState({ name: "", description: "" });
  const [problem, setProblem] = useState<string | null>(null);
  const current = sources.find((s) => s.name === selected) ?? sources[0];

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setProblem(null);
    try {
      await api.createSource(draft.name, draft.description);
      setSelected(draft.name);
      setDraft({ name: "", description: "" });
      setCreating(false);
      onChanged();
    } catch (err) {
      setProblem(err instanceof ApiError ? err.message : "The source could not be created.");
    }
  };

  return (
    <Page
      wide
      title="Knowledge"
      lede="Sources are collections of documents an agent can be grounded in. Add documents, check what a question retrieves, then assign the source to an agent."
      actions={
        canWrite && !creating ? (
          <button type="button" className={primary} onClick={() => setCreating(true)}>
            New Source
          </button>
        ) : undefined
      }
    >
      <p className="rounded-aui border border-aui-border px-3 py-2 text-aui-xs text-aui-muted">
        Search here is real. Feeding results into a run is not wired yet: the runtime's retriever is not told which
        tenant or agent is asking, so connecting it now would let one tenant's agent read another's documents.
      </p>
      {creating && (
        <form onSubmit={create} className={`${card} flex flex-col gap-3`}>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Label text="Name" hint="Lowercase letters, digits and dashes.">
              <input className={field} value={draft.name} required onChange={(e) => setDraft({ ...draft, name: e.target.value })} />
            </Label>
            <Label text="Description">
              <input
                className={field}
                value={draft.description}
                onChange={(e) => setDraft({ ...draft, description: e.target.value })}
              />
            </Label>
          </div>
          <Problem message={problem} />
          <div className="flex justify-end gap-2">
            <button type="button" className={button} onClick={() => setCreating(false)}>
              Cancel
            </button>
            <button type="submit" className={primary}>
              Create Source
            </button>
          </div>
        </form>
      )}

      {sources.length === 0 ? (
        <div className={card}>
          <Empty title="No sources">{canWrite ? "Create one, then add documents to it." : "Ask an admin to add one."}</Empty>
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-[16rem_1fr]">
          <ul className="flex flex-col gap-1">
            {sources.map((s) => (
              <li key={s.name}>
                <button
                  type="button"
                  onClick={() => setSelected(s.name)}
                  aria-current={s.name === current?.name}
                  className="flex w-full flex-col gap-0.5 rounded-aui border border-transparent px-3 py-2 text-left transition-colors hover:bg-aui-hover focus-visible:outline-2 focus-visible:outline-aui-accent aria-[current=true]:border-aui-border aria-[current=true]:bg-aui-surface"
                >
                  <span className="text-aui-sm font-medium text-aui-fg">{s.name}</span>
                  <span className="text-aui-xs text-aui-muted">
                    {s.documents} {s.documents === 1 ? "document" : "documents"}, {s.passages} passages
                  </span>
                </button>
              </li>
            ))}
          </ul>
          {current && (
            <SourceDetail
              key={current.name}
              source={current}
              usedBy={agents.filter((a) => (a.spec.sources ?? []).some((x) => x.name === current.name)).map((a) => a.spec.name)}
              canWrite={canWrite}
              onChanged={onChanged}
            />
          )}
        </div>
      )}
    </Page>
  );
}

function SourceDetail({
  source,
  usedBy,
  canWrite,
  onChanged,
}: {
  source: Source;
  usedBy: string[];
  canWrite: boolean;
  onChanged: () => void;
}) {
  const [docs, setDocs] = useState<Doc[]>([]);
  const [draft, setDraft] = useState({ title: "", uri: "", text: "" });
  const [query, setQuery] = useState("");
  const [hits, setHits] = useState<Hit[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);

  // biome-ignore lint/correctness/useExhaustiveDependencies: reload when the source's counts change
  useEffect(() => {
    let live = true;
    api
      .documents(source.name)
      .then((d) => live && setDocs(d))
      .catch(() => live && setDocs([]));
    return () => {
      live = false;
    };
  }, [source.name, source.documents]);

  const fail = (fallback: string) => (e: unknown) => setProblem(e instanceof ApiError ? e.message : fallback);

  const add = (e: FormEvent) => {
    e.preventDefault();
    setProblem(null);
    api
      .addDocument(source.name, draft)
      .then(() => {
        setDraft({ title: "", uri: "", text: "" });
        onChanged();
      })
      .catch(fail("The document could not be added."));
  };

  const search = (e: FormEvent) => {
    e.preventDefault();
    setProblem(null);
    api.searchKnowledge(query, source.name).then(setHits).catch(fail("The search failed."));
  };

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <section className={`${card} flex flex-col gap-2`}>
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <h2 className="text-aui-base font-medium text-aui-fg">{source.name}</h2>
            {source.description && <p className="text-aui-sm text-aui-muted">{source.description}</p>}
          </div>
          {canWrite && (
            <button
              type="button"
              className={button}
              onClick={() => api.deleteSource(source.name).then(onChanged).catch(fail("The source could not be deleted."))}
            >
              Delete Source
            </button>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <Chip>Updated {ago(source.updated_at)}</Chip>
          {usedBy.length === 0 ? <Chip>Assigned to no agent</Chip> : usedBy.map((a) => <Chip key={a}>Assigned to {a}</Chip>)}
        </div>
      </section>

      <Problem message={problem} />

      <section className={`${card} flex flex-col gap-2`}>
        <h3 className="text-aui-sm font-medium text-aui-fg">Try a question</h3>
        <form onSubmit={search} className="flex gap-2">
          <input
            aria-label="Question"
            className={field}
            placeholder="What would an agent ask this source?"
            value={query}
            required
            onChange={(e) => setQuery(e.target.value)}
          />
          <button type="submit" className={button}>
            Search
          </button>
        </form>
        {hits !== null &&
          (hits.length === 0 ? (
            <p className="text-aui-sm text-aui-muted">Nothing in this source matches those words.</p>
          ) : (
            <ol className="flex flex-col gap-2 pt-1">
              {hits.map((h, i) => (
                <li key={`${h.document_id}:${h.index}`} className="rounded-aui border border-aui-border p-3">
                  <div className="flex items-center justify-between gap-2 pb-1">
                    <span className="text-aui-xs font-medium text-aui-fg">
                      {i + 1}. {h.title}
                    </span>
                    <span className="text-aui-2xs tabular-nums text-aui-muted">score {h.score.toFixed(2)}</span>
                  </div>
                  <p className="whitespace-pre-wrap text-aui-sm text-aui-muted">{h.text}</p>
                </li>
              ))}
            </ol>
          ))}
      </section>

      <section className={`${card} flex flex-col gap-2`}>
        <h3 className="text-aui-sm font-medium text-aui-fg">Documents ({docs.length})</h3>
        {docs.length === 0 ? (
          <p className="text-aui-sm text-aui-muted">Empty. Paste a document below.</p>
        ) : (
          <ul>
            {docs.map((d) => (
              <li key={d.id} className="flex items-center gap-3 border-aui-border border-t py-2 first:border-t-0">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-aui-sm text-aui-fg">{d.title}</p>
                  <p className="truncate text-aui-xs text-aui-muted">
                    {d.passages} passages, {Math.max(1, Math.round(d.bytes / 1024))} KB, added {ago(d.added_at)}
                    {d.uri ? `, ${d.uri}` : ""}
                  </p>
                </div>
                {canWrite && (
                  <button
                    type="button"
                    className={button}
                    onClick={() =>
                      api.removeDocument(source.name, d.id).then(onChanged).catch(fail("The document could not be removed."))
                    }
                  >
                    Remove
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
        {canWrite && (
          <form onSubmit={add} className="flex flex-col gap-3 border-aui-border border-t pt-3">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <Label text="Title">
                <input className={field} value={draft.title} required onChange={(e) => setDraft({ ...draft, title: e.target.value })} />
              </Label>
              <Label text="Link" hint="Optional. Shown with citations.">
                <input className={field} value={draft.uri} onChange={(e) => setDraft({ ...draft, uri: e.target.value })} />
              </Label>
            </div>
            <Label text="Text" hint="Plain text or Markdown, up to 1 MB. It is split into passages on blank lines.">
              <textarea
                className={`${field} min-h-28`}
                value={draft.text}
                required
                onChange={(e) => setDraft({ ...draft, text: e.target.value })}
              />
            </Label>
            <div className="flex justify-end">
              <button type="submit" className={primary}>
                Add Document
              </button>
            </div>
          </form>
        )}
      </section>
    </div>
  );
}
