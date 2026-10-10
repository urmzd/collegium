import { ArrowUpIcon, FileIcon, XIcon } from "@agent-ui/render-web";
import { type QueueControls, QueueStrip } from "@agent-ui/web";
import { type KeyboardEvent, useEffect, useLayoutEffect, useRef, useState } from "react";

const MAX_HEIGHT = 260;
const MAX_FILES = 5;
const MAX_FILE_BYTES = 200 * 1024;

// Files are read in the browser and sent as part of the message, so only text
// can be attached. These are the extensions treated as text when the browser
// does not say.
const TEXT_EXT = /\.(txt|md|markdown|csv|tsv|json|ya?ml|toml|xml|html?|css|log|sql|sh|py|go|rs|ts|tsx|js|jsx|java|rb|c|h|cpp)$/i;

interface Attached {
  id: string;
  name: string;
  text: string;
}

// The browser's speech recognizer, where it exists. It has no standard type yet.
interface Recognizer {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  onresult: ((e: { resultIndex: number; results: ArrayLike<ArrayLike<{ transcript: string }> & { isFinal: boolean }> }) => void) | null;
  onend: (() => void) | null;
  onerror: (() => void) | null;
  start: () => void;
  stop: () => void;
}
function recognizer(): (new () => Recognizer) | undefined {
  const w = window as unknown as { SpeechRecognition?: new () => Recognizer; webkitSpeechRecognition?: new () => Recognizer };
  return w.SpeechRecognition ?? w.webkitSpeechRecognition;
}

function Glyph({ d, size = 18 }: { d: string; size?: number }) {
  return (
    <svg
      aria-hidden="true"
      focusable="false"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      className="shrink-0"
    >
      <path d={d} />
    </svg>
  );
}
const PLUS = "M5 12h14M12 5v14";
const MIC = "M12 2a3 3 0 0 0-3 3v7a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3zM19 10v2a7 7 0 0 1-14 0v-2M12 19v3";

const tool =
  "inline-flex size-9 items-center justify-center rounded-full text-aui-muted transition-colors hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent disabled:opacity-40";

/**
 * The message box. Type, attach text files, or dictate. Enter sends and
 * Shift+Enter adds a line. While a reply is in flight, sending queues the
 * message behind it.
 */
export function Composer({
  placeholder,
  queue,
  autoFocus,
  onDraft,
}: {
  placeholder: string;
  queue: QueueControls;
  autoFocus?: boolean;
  /** Called as the text changes, for anything that previews it. */
  onDraft?: (text: string) => void;
}) {
  const [value, setValue] = useState("");
  const [files, setFiles] = useState<Attached[]>([]);
  const [notice, setNotice] = useState<string | null>(null);
  const [listening, setListening] = useState(false);
  const input = useRef<HTMLTextAreaElement>(null);
  const picker = useRef<HTMLInputElement>(null);
  const speech = useRef<Recognizer | null>(null);
  const canDictate = recognizer() !== undefined;

  const change = (next: string) => {
    setValue(next);
    onDraft?.(next);
  };

  // Grow with the text up to a cap, then scroll.
  // biome-ignore lint/correctness/useExhaustiveDependencies: re-measure whenever the text changes
  useLayoutEffect(() => {
    const el = input.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, MAX_HEIGHT)}px`;
    el.style.overflowY = el.scrollHeight > MAX_HEIGHT ? "auto" : "hidden";
  }, [value]);

  useEffect(() => {
    if (autoFocus) input.current?.focus();
  }, [autoFocus]);

  useEffect(() => () => speech.current?.stop(), []);

  const ready = value.trim().length > 0 || files.length > 0;

  const send = (now: boolean) => {
    if (!ready) return;
    const parts = [value.trim(), ...files.map((f) => `Attached file: ${f.name}\n\`\`\`\n${f.text}\n\`\`\``)];
    const text = parts.filter(Boolean).join("\n\n");
    if (now) queue.sendNow(text);
    else queue.send(text);
    change("");
    setFiles([]);
    setNotice(null);
    input.current?.focus();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      send(e.metaKey || e.ctrlKey);
    }
  };

  const attach = async (picked: FileList | null) => {
    if (!picked) return;
    const skipped: string[] = [];
    const next = [...files];
    for (const file of Array.from(picked)) {
      const textual = file.type.startsWith("text/") || /json|xml|yaml|javascript/.test(file.type) || TEXT_EXT.test(file.name);
      if (next.length >= MAX_FILES) skipped.push(`${file.name}: at most ${MAX_FILES} files`);
      else if (!textual) skipped.push(`${file.name}: only text files can be attached`);
      else if (file.size > MAX_FILE_BYTES) skipped.push(`${file.name}: larger than ${MAX_FILE_BYTES / 1024} KB`);
      else {
        const text = await file.text();
        // A NUL byte means the file is not text, whatever its name says.
        if (text.includes("\u0000")) skipped.push(`${file.name}: only text files can be attached`);
        else next.push({ id: `${file.name}:${file.size}:${file.lastModified}`, name: file.name, text });
      }
    }
    setFiles(next);
    setNotice(skipped.length > 0 ? skipped.join(". ") : null);
  };

  const dictate = () => {
    if (listening) return speech.current?.stop();
    const Speech = recognizer();
    if (!Speech) return;
    const r = new Speech();
    r.continuous = true;
    r.interimResults = false;
    r.lang = navigator.language;
    r.onresult = (e) => {
      let heard = "";
      for (let i = e.resultIndex; i < e.results.length; i++) {
        const result = e.results[i];
        if (result?.isFinal) heard += result[0]?.transcript ?? "";
      }
      if (heard) {
        const el = input.current;
        const base = el?.value ?? "";
        change(base + (base && !base.endsWith(" ") ? " " : "") + heard.trim());
      }
    };
    r.onend = () => setListening(false);
    r.onerror = () => {
      setListening(false);
      setNotice("Dictation stopped. Check that the microphone is allowed for this page.");
    };
    speech.current = r;
    setListening(true);
    r.start();
  };

  return (
    <form
      aria-label="Message"
      onSubmit={(e) => {
        e.preventDefault();
        send(false);
      }}
      className="oap-glass flex flex-col gap-1 rounded-[1.5rem] p-2 transition-[border-color,box-shadow] focus-within:border-aui-accent/60 focus-within:ring-4 focus-within:ring-aui-accent-soft"
    >
      <QueueStrip queue={queue} />
      {files.length > 0 && (
        <ul className="flex flex-wrap gap-1.5 px-2 pt-1" aria-label="Attached files">
          {files.map((f) => (
            <li
              key={f.id}
              className="inline-flex h-7 max-w-60 items-center gap-1.5 rounded-full border border-aui-border pr-1 pl-2.5 text-aui-xs text-aui-fg"
            >
              <FileIcon size={12} className="text-aui-muted" />
              <span className="truncate">{f.name}</span>
              <button
                type="button"
                aria-label={`Remove ${f.name}`}
                onClick={() => setFiles(files.filter((x) => x.id !== f.id))}
                className="inline-flex size-5 items-center justify-center rounded-full text-aui-muted hover:bg-aui-hover hover:text-aui-fg focus-visible:outline-2 focus-visible:outline-aui-accent"
              >
                <XIcon size={12} />
              </button>
            </li>
          ))}
        </ul>
      )}
      <label className="sr-only" htmlFor="composer-input">
        Message
      </label>
      <textarea
        id="composer-input"
        ref={input}
        rows={2}
        value={value}
        placeholder={placeholder}
        onChange={(e) => change(e.target.value)}
        onKeyDown={onKeyDown}
        className="max-h-[260px] min-h-14 w-full resize-none bg-transparent px-3 pt-2 text-aui-lg text-aui-fg outline-none placeholder:text-aui-muted"
      />
      {notice && (
        <p role="status" className="px-3 text-aui-xs text-aui-muted">
          {notice}
        </p>
      )}
      <div className="flex items-center gap-1">
        <input
          ref={picker}
          type="file"
          multiple
          className="sr-only"
          tabIndex={-1}
          aria-hidden
          onChange={(e) => {
            void attach(e.target.files);
            e.target.value = "";
          }}
        />
        <button type="button" className={tool} aria-label="Attach files" title="Attach text files" onClick={() => picker.current?.click()}>
          <Glyph d={PLUS} />
        </button>
        <span className="flex-1" />
        {canDictate && (
          <button
            type="button"
            className={`${tool} ${listening ? "bg-aui-accent-soft text-aui-fg" : ""}`}
            aria-label={listening ? "Stop dictating" : "Dictate"}
            aria-pressed={listening}
            title={listening ? "Stop dictating" : "Dictate"}
            onClick={dictate}
          >
            <Glyph d={MIC} />
          </button>
        )}
        <button
          type="submit"
          disabled={!ready}
          aria-label={queue.running ? "Queue message" : "Send message"}
          title={queue.running ? "Queue behind the reply in flight" : "Send"}
          className="inline-flex size-9 shrink-0 items-center justify-center rounded-full bg-aui-primary text-aui-primary-fg shadow-aui-sm transition-opacity hover:opacity-90 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-aui-accent disabled:opacity-40"
        >
          <ArrowUpIcon size={16} strokeWidth={2.5} />
        </button>
      </div>
    </form>
  );
}
