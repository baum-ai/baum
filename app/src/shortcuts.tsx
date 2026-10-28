import { useEffect, useRef, useState } from "react";
import { Button } from "@baum/viewer/components/ui";
import { PAGES, type Page } from "./pages";

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
/** Pull requests' views (For you, Requested, Mine…), one either way. */
export const VIEW_KEYS = { previous: ["h", "ArrowLeft"], next: ["l", "ArrowRight"] };
/** The Up next inbox's keys, which `UpNextPage` handles itself. */
const UP_NEXT_SHORTCUTS = [
  { keys: ["j", "ArrowDown"], label: "Next pull request" },
  { keys: ["k", "ArrowUp"], label: "Previous pull request" },
  { keys: ["Enter"], label: "Open the pull request" },
  { keys: ["m"], label: "Merge, where offered" },
  { keys: ["s"], label: "Snooze, then 1–4 to choose until when" },
  { keys: ["u"], label: "Undo a snooze" },
];
/** The walkthrough's keys, which `Walkthrough` handles itself. */
const WALKTHROUGH_SHORTCUTS = [
  { keys: ["j", "ArrowDown"], label: "Next behaviour" },
  { keys: ["k", "ArrowUp"], label: "Previous behaviour" },
  { keys: ["ArrowRight"], label: "Next chapter" },
  { keys: ["ArrowLeft"], label: "Previous chapter" },
  { keys: ["o"], label: "Show the behaviour's code" },
  { keys: ["c"], label: "Comment on the behaviour" },
];
/** The keys of every other list (Sessions, Jobs, Repositories), which
 *  `listKeys` handles. */
const LIST_SHORTCUTS = [
  { keys: ["j", "ArrowDown"], label: "Next row" },
  { keys: ["k", "ArrowUp"], label: "Previous row" },
  { keys: ["Enter", "o"], label: "Open the row" },
  { keys: ["Escape"], label: "Clear the row focus" },
];
/** Where each group of keys works; the sheet lists the current one first. */
export type ShortcutsPlace =
  | "anywhere"
  | "inbox"
  | "pulls"
  | "review"
  | "sessions"
  | "jobs"
  | "repositories"
  | "page"
  | "walkthrough";
export const MAC = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
const KEY_NAMES: Record<string, string> = {
  ArrowDown: "↓",
  ArrowUp: "↑",
  ArrowLeft: "←",
  ArrowRight: "→",
  Escape: "Esc",
};
/** The app's command modifier as drawn: ⌘ on a Mac, Ctrl elsewhere. */
const COMMAND = MAC ? "⌘" : "Ctrl+";

/** A page's key as drawn on its sidebar link and in the sheet: "⌘2". */
export function pageKeyLabel(page: Page) {
  return `${COMMAND}${PAGES.findIndex((item) => item.page === page) + 1}`;
}

/** A page's key for `aria-keyshortcuts`: "Meta+2" or "Control+2". */
export function pageKeyShortcut(page: Page) {
  return `${MAC ? "Meta" : "Control"}+${PAGES.findIndex((item) => item.page === page) + 1}`;
}

/** What a key with the command modifier asks of the whole app. */
export type CommandAction =
  | { kind: "page"; page: Page }
  | { kind: "back" }
  | { kind: "forward" }
  | { kind: "go-to" };

/** The app-wide key a keydown is, if any: ⌘1 to ⌘7 (Ctrl elsewhere) for the
 *  sidebar's pages, ⌘, for Settings, ⌘K for Go to, and ⌘[ and ⌘] on a Mac
 *  or Alt+← and Alt+→ elsewhere for back and forward. Digits are read by
 *  their place on the keyboard, so a layout that shifts them still works. */
export function commandAction(
  event: Pick<KeyboardEvent, "key" | "code" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey">,
  mac = MAC,
): CommandAction | null {
  if (!mac && event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey) {
    if (event.key === "ArrowLeft") return { kind: "back" };
    if (event.key === "ArrowRight") return { kind: "forward" };
    return null;
  }
  const command = mac ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
  if (!command || event.altKey || event.shiftKey) return null;
  const digit = /^(?:Digit|Numpad)([1-9])$/.exec(event.code)?.[1];
  if (digit) {
    const item = PAGES[Number(digit) - 1];
    return item ? { kind: "page", page: item.page } : null;
  }
  if (event.key === ",") return { kind: "page", page: "settings" };
  if (event.key.toLowerCase() === "k") return { kind: "go-to" };
  if (mac && (event.key === "[" || event.code === "BracketLeft")) return { kind: "back" };
  if (mac && (event.key === "]" || event.code === "BracketRight")) return { kind: "forward" };
  return null;
}

/** Calls `handler` for the app-wide keys (`commandAction`), which work in
 *  text fields too, but not under a dialog other than the shortcuts sheet
 *  or Go to, which the handler closes. */
export function useCommandKeys(handler: (action: CommandAction) => void) {
  const latest = useRef(handler);
  useEffect(() => {
    latest.current = handler;
  });
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing) return;
      const action = commandAction(event);
      if (!action) return;
      if (document.querySelector("dialog[open]:not([data-shortcuts-sheet]):not([data-go-to])"))
        return;
      event.preventDefault();
      latest.current(action);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);
}

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

/** A row focus that the keyboard moves through a list's rows: each row is
 *  drawn with `data-row-id` (and `data-focused` while focused) inside
 *  `list`, and focus goes to the row's own link (`link`), so a screen reader
 *  reads the row as it moves, and the row scrolls into view. A row that
 *  left `ids` (collapsed, filtered out, gone) is no longer focused. */
export function useRowFocus<List extends HTMLElement = HTMLElement>(
  ids: string[],
  link = "[data-row-link]",
) {
  const list = useRef<List>(null);
  const [focusedId, setFocused] = useState<string | null>(null);
  const focused = focusedId !== null && ids.includes(focusedId) ? focusedId : null;
  const rowLink = (id: string) =>
    Array.from(list.current?.querySelectorAll<HTMLElement>("[data-row-id]") ?? [])
      .find((element) => element.dataset.rowId === id)
      ?.querySelector<HTMLElement>(link);
  useEffect(() => {
    if (!focused) return;
    const target = rowLink(focused);
    target?.focus({ preventScroll: true });
    target?.closest("[data-row-id]")?.scrollIntoView?.({ block: "nearest" });
  }, [focused]);
  return {
    list,
    focused,
    setFocused,
    rowLink,
    move: (step: 1 | -1) => setFocused(moveFocus(ids, focused, step)),
    clear: () => {
      setFocused(null);
      if (document.activeElement instanceof HTMLElement) document.activeElement.blur();
    },
  };
}

/** The keys every list shares (`LIST_SHORTCUTS`): j/↓ and k/↑ move the row
 *  focus, Enter or o opens the focused row's link and Escape clears it.
 *  Returns whether it took the key; a page's own keys go after. */
export function listKeys(
  event: KeyboardEvent,
  rows: Pick<ReturnType<typeof useRowFocus>, "focused" | "move" | "clear" | "rowLink">,
) {
  const key = event.key;
  if (key === "j" || key === "ArrowDown" || key === "k" || key === "ArrowUp") {
    event.preventDefault();
    rows.move(key === "j" || key === "ArrowDown" ? 1 : -1);
    return true;
  }
  if (key === "Escape") {
    if (!rows.focused) return false;
    event.preventDefault();
    rows.clear();
    return true;
  }
  if (key === "Enter" || key === "o") {
    // Enter on a link or button already does what it says.
    if (key === "Enter" && event.target instanceof HTMLElement && event.target.closest("a, button"))
      return false;
    if (!rows.focused) return false;
    event.preventDefault();
    rows.rowLink(rows.focused)?.click();
    return true;
  }
  return false;
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
  const title = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    dialog.current?.showModal();
    // The list scrolls; it opens at its top, on the current page's keys,
    // rather than on Close at its foot.
    title.current?.focus({ preventScroll: true });
    if (dialog.current) dialog.current.scrollTop = 0;
  }, []);
  const back = { keys: ["u", "Escape"], label: "Back to the list" };
  const groups: {
    place: ShortcutsPlace;
    title: string;
    rows: { keys: string[]; label: string }[];
  }[] = [
    {
      place: "anywhere",
      title: "Anywhere",
      rows: [
        { keys: [`${COMMAND}K`], label: "Go to a page, pull request or session" },
        ...PAGES.map((item) => ({ keys: [pageKeyLabel(item.page)], label: item.label })),
        MAC
          ? { keys: ["⌘[", "⌘]"], label: "Back, forward" }
          : { keys: ["Alt+←", "Alt+→"], label: "Back, forward" },
        { keys: ["?"], label: "Show or hide this list" },
        { keys: [MAC ? "⌥ Option" : "Alt"], label: "Hold to show the keys on buttons" },
        { keys: ["Escape"], label: "Close this list" },
      ],
    },
    { place: "inbox", title: "Inbox", rows: UP_NEXT_SHORTCUTS },
    {
      place: "pulls",
      title: "Pull requests",
      rows: [
        ...INBOX_SHORTCUTS,
        { keys: ["Escape"], label: "Clear the row focus" },
        { keys: VIEW_KEYS.previous, label: "Previous view" },
        { keys: VIEW_KEYS.next, label: "Next view" },
      ],
    },
    {
      place: "review",
      title: "A pull request or group",
      rows: [
        { keys: ["j"], label: "Next section" },
        { keys: ["k"], label: "Previous section" },
        { keys: ["m"], label: "Merge, where offered" },
        { keys: ["f"], label: "Fix, where offered" },
        { keys: ["g"], label: "Generate or regenerate the review" },
        back,
      ],
    },
    { place: "walkthrough", title: "Walkthrough", rows: WALKTHROUGH_SHORTCUTS },
    {
      place: "sessions",
      title: "Sessions",
      rows: [...LIST_SHORTCUTS, { keys: ["p"], label: "Open the session's pull request" }],
    },
    {
      place: "jobs",
      title: "Jobs",
      rows: [
        ...LIST_SHORTCUTS,
        { keys: ["t"], label: "Watch the agent or read its transcript, where there is one" },
      ],
    },
    {
      place: "repositories",
      title: "Repositories",
      rows: [
        ...LIST_SHORTCUTS.map((row) =>
          row.keys.includes("Enter") ? { ...row, label: "Open the repository's settings" } : row,
        ),
        { keys: ["a"], label: "Add repositories, or finish adding" },
        { keys: ["/"], label: "Search GitHub repositories while adding" },
      ],
    },
    { place: "page", title: "A session or job transcript", rows: [back] },
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
      <h2
        ref={title}
        id="shortcuts-title"
        tabIndex={-1}
        className="text-lg font-semibold outline-none"
      >
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
