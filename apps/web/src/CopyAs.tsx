import { CopyIcon } from "@vultus/render-web";
import { useEffect, useState } from "react";
import { COPY_LIMIT, type Format, fileName, formats, type Item } from "./copyas.ts";
import { FlashNote, Menu, type Option, useFlash } from "./ui.tsx";

/** Hand a file to the person. */
export function download(name: string, mime: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: mime }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

/**
 * Put an item on the clipboard in a format. When the clipboard is not
 * available, or the text is too large to be worth pasting, it is saved as a
 * file instead. Says which of the two happened.
 */
export async function deliver(item: Item, format: Format): Promise<"copied" | "downloaded"> {
  const text = format.text();
  if (text.length <= COPY_LIMIT && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      return "copied";
    } catch {
      // The browser refused, often because the page lost focus. Fall through to a file.
    }
  }
  download(fileName(item, format), format.mime, text);
  return "downloaded";
}

/**
 * "Copy as", for any item: a run, an agent's call, an answer, a tool call.
 * The formats come from one registry, so every place an item appears offers
 * the same ones. `item` may be a function, for items whose data is fetched
 * only when someone asks to copy them.
 */
export function CopyAs({
  item,
  label = "Copy as",
  align,
  compact,
  className = "",
  testId = "copy-as",
}: {
  item: Item | (() => Promise<Item>);
  label?: string;
  align?: "left" | "right";
  /** An icon alone, about 24px square, for sitting on a row. */
  compact?: boolean;
  className?: string;
  testId?: string;
}) {
  const { flash, say } = useFlash();
  // An item that has to be fetched lists its formats from what is known now, and is fetched on the click.
  const [loaded, setLoaded] = useState<Item | null>(typeof item === "function" ? null : item);

  useEffect(() => {
    if (typeof item !== "function") setLoaded(item);
  }, [item]);

  const open = () => {
    if (typeof item === "function" && !loaded) {
      item()
        .then(setLoaded)
        .catch(() => say("Could not load. Try again.", true));
    }
  };

  const list = loaded ? formats(loaded) : [];
  const options: Option[] = list.length > 0 ? list.map((f) => ({ value: f.id, label: f.label, hint: f.hint })) : [{ value: "", label: "Loading" }];

  const pick = (id: string) => {
    const format = list.find((f) => f.id === id);
    if (!loaded || !format) return;
    deliver(loaded, format)
      .then((how) => say(how === "copied" ? "Copied" : "Saved as a file"))
      .catch(() => say("Could not copy. Try again.", true));
  };

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: fetches the item's data as the menu is about to open
    <span data-testid={testId} className={`relative inline-flex ${className}`} onMouseDown={open} onFocus={open}>
      <FlashNote flash={flash} align={align === "right" ? "right" : "center"} />
      <Menu
        quiet
        align={align}
        label={label}
        value=""
        options={options}
        onChange={pick}
        className={compact ? "[&>button]:size-6 [&>button]:justify-center [&>button]:gap-0 [&>button]:px-0 [&>button>svg:last-child]:hidden" : ""}
        // The control never changes: same icon, same label, same size. The confirmation is the note beside it.
        shows={
          compact ? (
            <span className="inline-flex items-center justify-center">
              <CopyIcon size={14} />
            </span>
          ) : (
            <span className="text-aui-xs">{label}</span>
          )
        }
      />
    </span>
  );
}
