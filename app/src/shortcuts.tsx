import { useEffect, useRef } from "react";
import { Button } from "@baum/viewer/components/ui";

export type InboxAction =
  | "next"
  | "previous"
  | "open"
  | "select"
  | "merge"
  | "fix"
  | "generate"
  | "refresh";

/** The inbox's keys, the one table both the key handling and the shortcuts
 *  sheet read, so the sheet lists exactly the keys that work. */
export const INBOX_SHORTCUTS: { keys: string[]; action: InboxAction; label: string }[] = [
  { keys: ["j", "ArrowDown"], action: "next", label: "Next row" },
  { keys: ["k", "ArrowUp"], action: "previous", label: "Previous row" },
  { keys: ["Enter", "o"], action: "open", label: "Open the pull request or group" },
  { keys: ["x"], action: "select", label: "Select or deselect" },
  { keys: ["m"], action: "merge", label: "Merge, where offered" },
  { keys: ["f"], action: "fix", label: "Fix, where offered" },
  { keys: ["g"], action: "generate", label: "Generate the review" },
  { keys: ["r"], action: "refresh", label: "Refresh from GitHub" },
];
const GENERAL_SHORTCUTS = [
  { keys: ["?"], label: "Show or hide this list" },
  { keys: ["Escape"], label: "Close this list, or clear the row focus" },
];
const KEY_NAMES: Record<string, string> = { ArrowDown: "↓", ArrowUp: "↑", Escape: "Esc" };

/** The action a keydown asks for on the inbox, if it is one of its keys. */
export function inboxAction(event: Pick<KeyboardEvent, "key">): InboxAction | null {
  return INBOX_SHORTCUTS.find((s) => s.keys.includes(event.key))?.action ?? null;
}

/** The id one step from `current` through `ids`, stopping at either end. From
 *  nothing, forward starts at the first and back at the last. */
export function moveFocus(ids: string[], current: string | null, step: 1 | -1): string | null {
  if (ids.length === 0) return null;
  const index = current === null ? -1 : ids.indexOf(current);
  if (index < 0) return step === 1 ? ids[0]! : ids[ids.length - 1]!;
  return ids[Math.min(ids.length - 1, Math.max(0, index + step))]!;
}

/** Whether a keydown belongs to something else: a modified key (the browser's
 *  or the app's own), typing, or an open dialog. Checkboxes and radios take
 *  no text, so the keys still work after one was clicked. */
export function shouldIgnore(event: KeyboardEvent, allowSheet = false) {
  if (event.ctrlKey || event.metaKey || event.altKey || event.isComposing) return true;
  const target = event.target;
  if (target instanceof HTMLElement) {
    if (target.isContentEditable || target instanceof HTMLTextAreaElement) return true;
    if (target instanceof HTMLSelectElement) return true;
    if (target instanceof HTMLInputElement && !["checkbox", "radio"].includes(target.type))
      return true;
  }
  const dialogs = Array.from(document.querySelectorAll("dialog[open]"));
  return dialogs.some((dialog) => !(allowSheet && dialog.hasAttribute("data-shortcuts-sheet")));
}

/** Calls `handler` for keydowns that are not ignored; a handler that acts on
 *  one calls `preventDefault`. */
export function useShortcuts(
  handler: (event: KeyboardEvent) => void,
  { allowSheet = false }: { allowSheet?: boolean } = {},
) {
  const latest = useRef(handler);
  useEffect(() => {
    latest.current = handler;
  });
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || shouldIgnore(event, allowSheet)) return;
      latest.current(event);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [allowSheet]);
}

/** A key that is the page itself's: pressed with nothing focused in it. */
export function onBody(event: KeyboardEvent) {
  return (
    event.target === document ||
    event.target === document.body ||
    event.target === document.documentElement
  );
}

/** The keys of the page, as a small native dialog like the others. */
export function ShortcutsSheet({ close }: { close: () => void }) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  const rows = [...INBOX_SHORTCUTS, ...GENERAL_SHORTCUTS];
  return (
    <dialog
      ref={dialog}
      data-shortcuts-sheet=""
      onCancel={close}
      aria-labelledby="shortcuts-title"
      className="shortcuts-sheet"
    >
      <h2 id="shortcuts-title" className="text-lg font-semibold">
        Keyboard shortcuts
      </h2>
      <dl className="shortcuts-list">
        {rows.map((row) => (
          <div key={row.keys.join()}>
            <dt>
              {row.keys.map((key) => (
                <kbd key={key}>{KEY_NAMES[key] ?? key}</kbd>
              ))}
            </dt>
            <dd>{row.label}</dd>
          </div>
        ))}
      </dl>
      <p className="mt-3 text-xs text-(--text-tertiary)">
        On a pull request or group page, <kbd>u</kbd> or <kbd>Esc</kbd> goes back to the inbox.
      </p>
      <div className="mt-4 flex justify-end">
        <Button size="compact" onClick={close}>
          Close
        </Button>
      </div>
    </dialog>
  );
}
