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
/** The Up next inbox's keys, which `UpNextPage` handles itself. */
const UP_NEXT_SHORTCUTS = [
  { keys: ["j", "ArrowDown"], label: "Next pull request" },
  { keys: ["k", "ArrowUp"], label: "Previous pull request" },
  { keys: ["Enter"], label: "Open the pull request" },
  { keys: ["m"], label: "Merge, where offered" },
  { keys: ["s"], label: "Snooze, then 1–4 to choose until when" },
  { keys: ["u"], label: "Undo a snooze" },
];
/** Where each group of keys works; the sheet lists the current one first. */
export type ShortcutsPlace = "anywhere" | "inbox" | "pulls" | "page";
const KEY_NAMES: Record<string, string> = { ArrowDown: "↓", ArrowUp: "↑", Escape: "Esc" };
const MAC = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

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

/** Shows the app's keyboard hints (`.key-hint`) while Alt, Option on a Mac,
 *  is held, and hides them when it is let go or the window loses focus. */
export function useKeyHints() {
  useEffect(() => {
    const root = document.documentElement;
    const show = (shown: boolean) => root.toggleAttribute("data-key-hints", shown);
    const down = (event: KeyboardEvent) => show(event.key === "Alt" || event.altKey);
    const up = (event: KeyboardEvent) => show(event.key !== "Alt" && event.altKey);
    const hide = () => show(false);
    window.addEventListener("keydown", down);
    window.addEventListener("keyup", up);
    window.addEventListener("blur", hide);
    return () => {
      window.removeEventListener("keydown", down);
      window.removeEventListener("keyup", up);
      window.removeEventListener("blur", hide);
      hide();
    };
  }, []);
}

/** A key that is the page itself's: pressed with nothing focused in it. */
export function onBody(event: KeyboardEvent) {
  return (
    event.target === document ||
    event.target === document.body ||
    event.target === document.documentElement
  );
}

/** Every key of the app, grouped by where it works, as a small native dialog
 *  like the others. `?` opens it from any page. */
export function ShortcutsSheet({
  close,
  current = "anywhere",
}: {
  close: () => void;
  /** Where it was opened, listed first. */
  current?: ShortcutsPlace;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    dialog.current?.showModal();
  }, []);
  const groups: {
    place: ShortcutsPlace;
    title: string;
    rows: { keys: string[]; label: string }[];
  }[] = [
    {
      place: "anywhere",
      title: "Anywhere",
      rows: [
        { keys: ["?"], label: "Show or hide this list" },
        { keys: [MAC ? "⌥ Option" : "Alt"], label: "Hold to show the keys on buttons" },
        { keys: ["Escape"], label: "Close this list" },
      ],
    },
    { place: "inbox", title: "Inbox", rows: UP_NEXT_SHORTCUTS },
    {
      place: "pulls",
      title: "Pull requests",
      rows: [...INBOX_SHORTCUTS, { keys: ["Escape"], label: "Clear the row focus" }],
    },
    {
      place: "page",
      title: "A pull request, group or session",
      rows: [{ keys: ["u", "Escape"], label: "Back to the list" }],
    },
  ];
  const ordered = [
    ...groups.filter((group) => group.place === current),
    ...groups.filter((group) => group.place !== current),
  ];
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
      {ordered.map((group) => (
        <section key={group.place} aria-label={group.title} className="shortcuts-group">
          <h3>{group.title}</h3>
          <dl className="shortcuts-list">
            {group.rows.map((row) => (
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
        </section>
      ))}
      <div className="mt-4 flex justify-end">
        <Button size="compact" onClick={close}>
          Close
        </Button>
      </div>
    </dialog>
  );
}
