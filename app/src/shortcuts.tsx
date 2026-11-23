import { usePreferences, type PageModifier } from "./preferences";
import { useModal } from "./modal";
import { useCallback, useEffect, useRef, useState, type RefObject } from "react";
import type { ReviewSteps } from "@baum/viewer/components/review-viewer";
import { CloseButton } from "@baum/viewer/components/ui";
import { PAGES, type Page } from "./pages";
import { openExternal } from "./api";

export type InboxAction =
  | "next"
  | "previous"
  | "open"
  | "select"
  | "peek"
  | "merge"
  | "fix"
  | "transcript"
  | "refresh";

/** A key as the shortcuts sheet lists it; `short` names it in the page's key
 *  bar (`KeyBar`), and a key without one is listed in the sheet only. */
type Shortcut<Extra = object> = { keys: string[]; label: string; short?: string } & Extra;

/** The fix transcript key's label: `t` wherever a pull request is in hand. */
const TRANSCRIPT_LABEL = "Watch the fix agent, or read its last transcript";

/** The inbox's keys, the one table both the key handling and the shortcuts
 *  sheet read, so the sheet lists exactly the keys that work. */
export const INBOX_SHORTCUTS: Shortcut<{ action: InboxAction }>[] = [
  { keys: ["j", "ArrowDown"], action: "next", label: "Next row", short: "move" },
  { keys: ["k", "ArrowUp"], action: "previous", label: "Previous row", short: "move" },
  { keys: ["Enter", "o"], action: "open", label: "Open the pull request or group", short: "open" },
  { keys: ["x"], action: "select", label: "Select or deselect", short: "select" },
  {
    keys: [" "],
    action: "peek",
    label: "Peek at the review below the row, or show or hide a group's pull requests",
    short: "peek",
  },
  { keys: ["m"], action: "merge", label: "Merge, where offered", short: "merge" },
  { keys: ["f"], action: "fix", label: "Fix, where offered", short: "fix" },
  { keys: ["t"], action: "transcript", label: TRANSCRIPT_LABEL },
  { keys: ["r"], action: "refresh", label: "Refresh from GitHub" },
];
/** Pull requests' views (For you, Requested, Mine…), one either way. */
export const VIEW_KEYS = { previous: ["h", "ArrowLeft"], next: ["l", "ArrowRight"] };
/** The Up next inbox's keys, which `UpNextPage` handles itself. */
const UP_NEXT_SHORTCUTS: Shortcut[] = [
  { keys: ["j", "ArrowDown"], label: "Next pull request", short: "move" },
  { keys: ["k", "ArrowUp"], label: "Previous pull request", short: "move" },
  { keys: ["Enter"], label: "Open the pull request or group", short: "act" },
  { keys: [" "], label: "Show or hide a group's pull requests" },
  { keys: ["m"], label: "Merge, where offered", short: "merge" },
  { keys: ["f"], label: "Fix, where offered", short: "fix" },
  { keys: ["t"], label: TRANSCRIPT_LABEL },
  {
    keys: ["v"],
    label: "Review: approve, comment or request changes",
    short: "review",
  },
  { keys: ["s"], label: "Snooze, then 1–4 to choose until when", short: "snooze" },
  {
    keys: ["e"],
    label: "Archive: never show it in an inbox again (its page unarchives it)",
    short: "archive",
  },
  { keys: ["u"], label: "Undo a snooze or an archive", short: "undo" },
];
/** The walkthrough's keys, which `Walkthrough` handles itself. */
const WALKTHROUGH_SHORTCUTS: Shortcut[] = [
  { keys: ["j", "ArrowDown"], label: "Next behaviour", short: "move" },
  { keys: ["k", "ArrowUp"], label: "Previous behaviour", short: "move" },
  { keys: ["ArrowRight"], label: "Next chapter", short: "chapter" },
  { keys: ["ArrowLeft"], label: "Previous chapter", short: "chapter" },
  { keys: ["o"], label: "Show the behaviour's code", short: "code" },
  { keys: ["c"], label: "Comment on the behaviour", short: "comment" },
  { keys: ["v"], label: "Open the full review", short: "full review" },
  { keys: ["a"], label: "Ask the review's agent a question", short: "ask" },
];
/** The keys of every other list (Sessions, Jobs, Repositories, Timeline), which
 *  `listKeys` handles. */
const LIST_SHORTCUTS: Shortcut[] = [
  { keys: ["j", "ArrowDown"], label: "Next row", short: "move" },
  { keys: ["k", "ArrowUp"], label: "Previous row", short: "move" },
  { keys: ["Enter", "o"], label: "Open the row", short: "open" },
  { keys: ["Escape"], label: "Clear the row focus" },
];
export const MAC = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
/** Settings' keys, which `SettingsPage` handles itself. */
const SETTINGS_SHORTCUTS: Shortcut[] = [
  { keys: ["j", "ArrowDown"], label: "Next control in the section", short: "move" },
  { keys: ["k", "ArrowUp"], label: "Previous control in the section", short: "move" },
  {
    keys: MAC ? ["⇧J", "⇧↓"] : ["Shift+J", "Shift+↓"],
    label: "Next section",
    short: "sections",
  },
  {
    keys: MAC ? ["⇧K", "⇧↑"] : ["Shift+K", "Shift+↑"],
    label: "Previous section",
    short: "sections",
  },
  { keys: ["1–6"], label: "Go to a section", short: "section" },
  { keys: ["Enter", "l", "ArrowRight"], label: "Go into the section", short: "go in" },
  { keys: ["Space", "Enter"], label: "Change or press the focused control" },
  { keys: ["Escape", "h", "ArrowLeft"], label: "Back to the sections", short: "back" },
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
  | "timeline"
  | "settings"
  | "page"
  | "walkthrough";
const KEY_NAMES: Record<string, string> = {
  ArrowDown: "↓",
  ArrowUp: "↑",
  ArrowLeft: "←",
  ArrowRight: "→",
  Escape: "Esc",
  " ": "Space",
};
/** The app's command modifier as drawn: ⌘ on a Mac, Ctrl elsewhere. */
export const COMMAND = MAC ? "⌘" : "Ctrl+";

/** The modifier the sidebar's page keys use: the one chosen in Settings, or
 *  the platform's command key. The Meta key is offered only on a Mac, where
 *  it is Command; elsewhere a saved one falls back to Ctrl. */
export function pageModifier(saved: PageModifier, mac = MAC): Exclude<PageModifier, ""> {
  if (saved === "ctrl" || saved === "alt" || (saved === "meta" && mac)) return saved;
  return mac ? "meta" : "ctrl";
}

/** Each modifier as drawn and for `aria-keyshortcuts`. */
export const MODIFIER_KEYS: Record<Exclude<PageModifier, "">, { label: string; aria: string }> = {
  meta: { label: MAC ? "⌘" : "Meta+", aria: "Meta" },
  ctrl: { label: MAC ? "⌃" : "Ctrl+", aria: "Control" },
  alt: { label: MAC ? "⌥" : "Alt+", aria: "Alt" },
};

/** A page's key as drawn on its sidebar link and in the sheet: "⌘2". */
export function pageKeyLabel(page: Page, modifier: PageModifier = "") {
  const { label } = MODIFIER_KEYS[pageModifier(modifier)];
  return `${label}${PAGES.findIndex((item) => item.page === page) + 1}`;
}

/** A page's key for `aria-keyshortcuts`: "Meta+2" or "Control+2". */
export function pageKeyShortcut(page: Page, modifier: PageModifier = "") {
  const { aria } = MODIFIER_KEYS[pageModifier(modifier)];
  return `${aria}+${PAGES.findIndex((item) => item.page === page) + 1}`;
}

/** What a key with the command modifier asks of the whole app. */
export type CommandAction =
  | { kind: "page"; page: Page }
  | { kind: "back" }
  | { kind: "forward" }
  | { kind: "go-to" }
  | { kind: "refresh" };

/** The app-wide key a keydown is, if any: ⌘1 to ⌘8 (Ctrl elsewhere, or the
 *  modifier chosen in Settings) for the sidebar's pages, ⌘, for Settings, ⌘K for Go to, ⌘R for the page's
 *  refresh (⇧⌘R stays the browser's reload), and ⌘[ and ⌘] on a Mac or
 *  Alt+← and Alt+→ elsewhere for back and forward. Digits are read by their
 *  place on the keyboard, so a layout that shifts them still works, and
 *  letters too on a layout without Latin ones. */
export function commandAction(
  event: Pick<KeyboardEvent, "key" | "code" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey">,
  mac = MAC,
  pages: PageModifier = "",
): CommandAction | null {
  const digit = /^(?:Digit|Numpad)([1-9])$/.exec(event.code)?.[1];
  if (digit) {
    if (!onlyModifier(event, pageModifier(pages, mac))) return null;
    const item = PAGES[Number(digit) - 1];
    return item ? { kind: "page", page: item.page } : null;
  }
  if (!mac && event.altKey && !event.ctrlKey && !event.metaKey && !event.shiftKey) {
    if (event.key === "ArrowLeft") return { kind: "back" };
    if (event.key === "ArrowRight") return { kind: "forward" };
    return null;
  }
  if (!onlyModifier(event, mac ? "meta" : "ctrl")) return null;
  if (event.key === ",") return { kind: "page", page: "settings" };
  // A letter by itself, or by its place on a layout without Latin letters.
  const typed = event.key?.toLowerCase() ?? "";
  const letter = /^[a-z]$/.test(typed)
    ? typed
    : /^Key([A-Z])$/.exec(event.code)?.[1]?.toLowerCase();
  if (letter === "k") return { kind: "go-to" };
  if (letter === "r") return { kind: "refresh" };
  if (mac && (event.key === "[" || event.code === "BracketLeft")) return { kind: "back" };
  if (mac && (event.key === "]" || event.code === "BracketRight")) return { kind: "forward" };
  return null;
}

/** Whether `modifier` is the only modifier held. */
function onlyModifier(
  event: Pick<KeyboardEvent, "ctrlKey" | "metaKey" | "altKey" | "shiftKey">,
  modifier: Exclude<PageModifier, "">,
) {
  return (
    event.metaKey === (modifier === "meta") &&
    event.ctrlKey === (modifier === "ctrl") &&
    event.altKey === (modifier === "alt") &&
    !event.shiftKey
  );
}

/** The page refresh key as drawn and for `aria-keyshortcuts`: ⌘R on a Mac,
 *  Ctrl+R elsewhere. */
export const REFRESH_KEY_LABEL = `${COMMAND}R`;
export const REFRESH_KEY_SHORTCUT = `${MAC ? "Meta" : "Control"}+R`;

/** The open-on-GitHub key as drawn and for `aria-keyshortcuts`: ⇧⌘G on a
 *  Mac, Ctrl+Shift+G elsewhere, as GitHub Desktop's View on GitHub. */
export const GITHUB_KEY_LABEL = MAC ? "⇧⌘G" : "Ctrl+Shift+G";
export const GITHUB_KEY_SHORTCUT = `${MAC ? "Meta" : "Control"}+Shift+G`;

/** Whether a keydown is ⇧⌘G (Ctrl+Shift+G elsewhere), by its letter or, on a
 *  layout without Latin ones, its place. */
export function isGitHubKey(
  event: Pick<KeyboardEvent, "key" | "code" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey">,
  mac = MAC,
) {
  if (!event.shiftKey || event.altKey) return false;
  if (event.metaKey !== mac || event.ctrlKey === mac) return false;
  const typed = event.key ?? "";
  return /^[a-z]$/i.test(typed) ? typed.toLowerCase() === "g" : event.code === "KeyG";
}

/** Opens `url`, the pull request in hand (the one a page shows, or a list's
 *  selected row), on GitHub on ⇧⌘G (Ctrl+Shift+G elsewhere), in text fields
 *  too, as the app-wide keys, but not under a dialog. Without a `url` the key
 *  is left alone. */
export function useGitHubKey(url: string | undefined) {
  const latest = useRef(url);
  useEffect(() => {
    latest.current = url;
  });
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing || !isGitHubKey(event)) return;
      const target = latest.current;
      if (!target || document.querySelector("dialog[open]")) return;
      event.preventDefault();
      openExternal(target);
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);
}

/** Ctrl+N and Ctrl+P (the readline keys, Control on a Mac too) as ↓ and ↑,
 *  everywhere the arrows move: they are turned into those keys on the same
 *  element before anything else sees them, so the lists, the context menu
 *  and Go to follow without knowing them, and the browser's new window and
 *  print stay out of the way. A text field keeps its own, except one that
 *  steers a list (a combobox). Mounted once, for the whole app. */
export function useReadlineArrows() {
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!event.ctrlKey || event.metaKey || event.altKey || event.shiftKey || event.isComposing)
        return;
      const key = { n: "ArrowDown", p: "ArrowUp" }[event.key.toLowerCase()];
      const target = event.target;
      if (!key || !(target instanceof HTMLElement)) return;
      const field = target.closest("input, textarea, select, [contenteditable]");
      if (field && field.getAttribute("role") !== "combobox") return;
      event.preventDefault();
      event.stopImmediatePropagation();
      target.dispatchEvent(
        new KeyboardEvent("keydown", { key, code: key, bubbles: true, cancelable: true }),
      );
    };
    // Captured, so it comes before every other listener.
    window.addEventListener("keydown", onKeyDown, true);
    return () => window.removeEventListener("keydown", onKeyDown, true);
  }, []);
}

/** Calls `handler` for the app-wide keys (`commandAction`), which work in
 *  text fields too, but not under a dialog other than the shortcuts sheet
 *  or Go to, which the handler closes. A handler that returns false leaves
 *  the key to the browser. */
export function useCommandKeys(handler: (action: CommandAction) => boolean | void) {
  const { preferences } = usePreferences();
  const latest = useRef(handler);
  const pages = useRef(preferences.page_modifier);
  useEffect(() => {
    latest.current = handler;
    pages.current = preferences.page_modifier;
  });
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.isComposing) return;
      const action = commandAction(event, MAC, pages.current);
      if (!action) return;
      if (document.querySelector("dialog[open]:not([data-shortcuts-sheet]):not([data-go-to])")) {
        // Reloading the app would lose the dialog.
        if (action.kind === "refresh") event.preventDefault();
        return;
      }
      if (latest.current(action) !== false) event.preventDefault();
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

/** Whether Tab moved the focus since the pointer was last pressed: until
 *  then the sidebar's arrows, Escape back to the row (`returnsToRow`) and
 *  Enter and Space pressing a focused button (`shouldIgnore`) stay out of
 *  the way, so whoever never presses Tab sees no change. */
let tabbed = false;

/** A list row, which keeps the keys while it has the focus. */
const ROW = "[data-row-id], [data-row]";

/** Whether a keydown belongs to something else: a modified key (the browser's
 *  or the app's own), typing, or an open dialog. Checkboxes and radios take
 *  no text, so the keys still work after one was clicked. Once Tab is in use
 *  (`tabbed`), Enter and Space on a focused link or button press it; on a
 *  row, or in Settings, whose keys step through its controls, they stay the
 *  page's. */
export function shouldIgnore(event: KeyboardEvent, allowSheet = false) {
  if (event.ctrlKey || event.metaKey || event.altKey || event.isComposing) return true;
  const target = event.target;
  if (target instanceof HTMLElement) {
    if (target.isContentEditable || target instanceof HTMLTextAreaElement) return true;
    if (target instanceof HTMLSelectElement) return true;
    if (target instanceof HTMLInputElement && !["checkbox", "radio"].includes(target.type))
      return true;
    if (
      tabbed &&
      (event.key === "Enter" || event.key === " ") &&
      target.closest("a[href], button, summary") &&
      !target.closest(`${ROW}, .settings-page`)
    )
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

/** Space on the block the keys are on (`data-review-block-active`, which j
 *  and k move): presses its first fold, a disclosure button or a details
 *  summary, as Space opens or closes a row elsewhere. False when the block
 *  has none, or no block is marked. */
export function toggleActiveBlock(): boolean {
  const block = document.querySelector<HTMLElement>("[data-review-block-active]");
  const fold = block?.querySelector<HTMLElement>("button[aria-expanded], summary");
  if (!fold) return false;
  fold.click();
  return true;
}

/** Enter on the block the keys are on: presses what opens it
 *  (`data-block-open`, such as a commit row's subject). False when the block
 *  opens nothing, or no block is marked. */
export function openActiveBlock(): boolean {
  const block = document.querySelector<HTMLElement>("[data-review-block-active]");
  const open = block?.querySelector<HTMLElement>("[data-block-open]");
  if (!open) return false;
  open.click();
  return true;
}

/** Where a key moves a review page, as the navigator does: j and k (↓ ↑)
 *  through the blocks of the section being read; Shift+J and Shift+K
 *  (Shift+↓ ↑), or → and ←, to the next or previous section. */
export function reviewKeyStep(event: KeyboardEvent, steps: ReviewSteps | null) {
  if (!steps) return null;
  const key = event.key.length === 1 ? event.key.toLowerCase() : event.key;
  const next = key === "j" || key === "ArrowDown";
  if (next || key === "k" || key === "ArrowUp") {
    if (!event.shiftKey) return (next ? steps.item?.next : steps.item?.previous) ?? null;
    return next ? (steps.rail?.next ?? steps.next) : (steps.rail?.previous ?? steps.previous);
  }
  if (key === "ArrowRight") return steps.rail?.next ?? steps.next;
  if (key === "ArrowLeft") return steps.rail?.previous ?? steps.previous;
  return null;
}

/** Each list's last row, by the list's name, for as long as the app runs. */
const lastRows = new Map<string, string>();
/** Forgets every list's last row, as a fresh start of the app does. */
export function forgetLastRows() {
  lastRows.clear();
}

/** A list's selected row that outlives the list, as `useState` does not: a
 *  list drawn again (Escape, `u` or Back from the page one of its rows
 *  opened, or its page from the sidebar) comes back on the same row. A link
 *  clicked in a row (`[data-row-id]` inside `list`) makes that row the one
 *  to come back to, so a row opened by the pointer is kept as one opened by
 *  the keyboard is; a row button that opens the row in place (`ROW_LINK`)
 *  selects it there, so the keys go on from it. Tab landing on a row's link (`useTabStops`) makes that
 *  row the selected one, so the row's keys act where the focus is. */
export function useLastRow(name: string, list?: RefObject<HTMLElement | null>) {
  const [id, setId] = useState(() => lastRows.get(name) ?? null);
  const set = useCallback(
    (next: string | null) => {
      if (next === null) lastRows.delete(name);
      else lastRows.set(name, next);
      setId(next);
    },
    [name],
  );
  useEffect(() => {
    if (!list) return;
    const click = (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      const row = target?.closest<HTMLElement>("[data-row-id]");
      if (!row?.dataset.rowId || !list.current?.contains(row)) return;
      if (target?.closest("a[href]")) lastRows.set(name, row.dataset.rowId);
      else if (target?.closest(`button:is(${ROW_LINK})`)) set(row.dataset.rowId);
    };
    const focusin = (event: FocusEvent) => {
      if (!tabbed || !(event.target instanceof Element)) return;
      const row = event.target.closest<HTMLElement>("[data-row-id]");
      if (row?.dataset.rowId && event.target.matches(ROW_LINK) && list.current?.contains(row))
        set(row.dataset.rowId);
    };
    document.addEventListener("click", click, true);
    document.addEventListener("focusin", focusin);
    return () => {
      document.removeEventListener("click", click, true);
      document.removeEventListener("focusin", focusin);
    };
  }, [name, list, set]);
  return [id, set] as const;
}

/** Focuses a row's link and scrolls the row into view when any of it is out
 *  of sight: to the middle when a list comes back on its last row, which may
 *  be far down, and just enough as the keys move it. The list's first row
 *  (`first`) takes it back to the top, with what is above the row, such as
 *  its heading. */
export function useFocusRow(
  focused: string | null,
  rowLink: (id: string) => HTMLElement | null | undefined,
  first = false,
) {
  const returning = useRef(focused !== null);
  useEffect(() => {
    if (!focused) return;
    const target = rowLink(focused);
    target?.focus({ preventScroll: true });
    const row = target?.closest<HTMLElement>("[data-row-id]");
    if (row && first && !returning.current) scrollAreas(row)[0]?.scrollTo?.({ top: 0 });
    else if (row && !inSight(row))
      row.scrollIntoView?.({ block: returning.current ? "center" : "nearest" });
    returning.current = false;
  }, [focused]);
}

/** The elements that scroll `row`, innermost first, ending at the page's. */
function scrollAreas(row: HTMLElement): Element[] {
  const areas: Element[] = [];
  for (let up = row.parentElement; up; up = up.parentElement)
    if (/auto|scroll/.test(getComputedStyle(up).overflowY)) areas.push(up);
  return [...areas, document.scrollingElement ?? document.documentElement];
}

/** Whether all of `row` shows: inside the window and the areas that scroll
 *  it. */
function inSight(row: HTMLElement) {
  const box = row.getBoundingClientRect();
  let head = 0;
  let foot = window.innerHeight;
  for (const up of scrollAreas(row).slice(0, -1)) {
    const area = up.getBoundingClientRect();
    head = Math.max(head, area.top);
    foot = Math.min(foot, area.bottom);
  }
  return box.top >= head && box.bottom <= foot;
}

/** A row focus that the keyboard moves through a list's rows: each row is
 *  drawn with `data-row-id` (and `data-focused` while focused) inside
 *  `list`, and focus goes to the row's own link (`link`), so a screen reader
 *  reads the row as it moves, and the row scrolls into view. A row that
 *  left `ids` (collapsed, filtered out, gone) is no longer focused. `name`
 *  keeps the row for when the list is drawn again (`useLastRow`). */
export function useRowFocus<List extends HTMLElement = HTMLElement>(
  name: string,
  ids: string[],
  link = "[data-row-link]",
) {
  const list = useRef<List>(null);
  const [focusedId, setFocused] = useLastRow(name, list);
  const focused = focusedId !== null && ids.includes(focusedId) ? focusedId : null;
  const rowLink = (id: string) =>
    Array.from(list.current?.querySelectorAll<HTMLElement>("[data-row-id]") ?? [])
      .find((element) => element.dataset.rowId === id)
      ?.querySelector<HTMLElement>(link);
  useFocusRow(focused, rowLink, focused !== null && focused === ids[0]);
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
    const link = rows.rowLink(rows.focused);
    if (returnsToRow(link)) link?.focus({ preventScroll: true });
    else rows.clear();
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
  const { preferences } = usePreferences();
  useEffect(() => {
    const root = document.documentElement;
    const show = (shown: boolean) => root.toggleAttribute("data-key-hints", shown);
    const down = (event: KeyboardEvent) =>
      show(preferences.key_hints && (event.key === "Alt" || event.altKey));
    const up = (event: KeyboardEvent) =>
      show(preferences.key_hints && event.key !== "Alt" && event.altKey);
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
  }, [preferences.key_hints]);
}

/** Whether Escape takes the focus back to a list's selected row, through
 *  `link`, rather than clearing the row: Tab left the focus on a link or
 *  button outside the row. Tabs and menus, and Settings, whose Escape goes
 *  back to its sections, keep their own Escape. */
export function returnsToRow(link: HTMLElement | null | undefined) {
  const focused = document.activeElement;
  if (!tabbed || !link || !(focused instanceof HTMLElement)) return false;
  if (link.closest(ROW)?.contains(focused)) return false;
  return (
    !!focused.closest("a[href], button, summary") &&
    !focused.closest('[role="tab"], [role="menu"], .local-rail-menu, .settings-page')
  );
}

/** The selected row's link, where the page draws a list with one. */
function selectedRowLink() {
  const row = document.querySelector<HTMLElement>(":is([data-row-id], [data-row])[data-focused]");
  return row?.querySelector<HTMLElement>(ROW_LINK) ?? null;
}

/** Escape gives up whatever the keyboard focused, so the page's keys act on
 *  its selection again: a link or button Tab reached hands the focus back to
 *  the list's selected row (`returnsToRow`), anything else is let go. It
 *  leaves alone an Escape something else used: a dialog closing, a menu or a
 *  field clearing itself, a list giving up its row. */
export function useEscapeBlurs() {
  useEffect(() => {
    const down = (event: KeyboardEvent) => {
      // An Escape that closes a dialog hands the focus back to what opened it.
      if (event.key !== "Escape" || event.isComposing || document.querySelector("dialog[open]"))
        return;
      // Page handlers may run after this one; look once the event is done.
      setTimeout(() => {
        if (event.defaultPrevented) return;
        const focused = document.activeElement;
        if (!(focused instanceof HTMLElement) || focused === document.body) return;
        const link = selectedRowLink();
        if (returnsToRow(link)) link?.focus({ preventScroll: true });
        else focused.blur();
      });
    };
    window.addEventListener("keydown", down);
    return () => window.removeEventListener("keydown", down);
  }, []);
}

/** Where a click's focus is the keys' place and stays: list rows, whose
 *  focus follows the selection, tabs and menus, which arrows move through,
 *  dialogs, and Settings, whose keys step through its controls. */
const KEEPS_FOCUS = [
  "[data-row]",
  "[data-row-id]",
  '[role="tab"]',
  '[role="menu"]',
  '[role="menuitem"]',
  "dialog",
  ".settings-page",
].join(", ");

/** A link or button clicked with the pointer gives up the focus the click
 *  gave it. Left there, the next key the page takes, j or k, would show its
 *  ring (the browser shows one once the keyboard is used), on a control
 *  nobody chose with the keyboard, such as Inbox in the sidebar. A click that
 *  moved the focus on, as one opening a menu does, keeps it where it went. */
export function usePointerBlurs() {
  useEffect(() => {
    const click = (event: MouseEvent) => {
      // A keyboard's Enter or Space clicks with no pointer: detail is 0.
      if (event.detail === 0 || !(event.target instanceof Element)) return;
      const control = event.target.closest<HTMLElement>("a[href], button, summary");
      if (!control || control.closest(KEEPS_FOCUS)) return;
      setTimeout(() => {
        if (document.activeElement === control) control.blur();
      });
    };
    // Captured: the click's own handler may redraw what was clicked, as the
    // refresh button swaps its glyph, and leave the target outside it.
    window.addEventListener("click", click, true);
    return () => window.removeEventListener("click", click, true);
  }, []);
}

/** A form opened inside a row, such as a repository's clone form in
 *  Settings: its controls are stops of their own, as they have no keys. */
const ROW_PANEL = "form, .local-repo-panel";
/** A row's own link, where Tab and Escape put the focus. */
const ROW_LINK = "[data-row-link], .inbox-pr-title, .upnext-row";
const TABBABLE = [
  "a[href]",
  "button:not(:disabled)",
  'input:not(:disabled):not([type="hidden"])',
  "select:not(:disabled)",
  "textarea:not(:disabled)",
  "summary",
  "[tabindex]",
].join(", ");

/** What Tab can reach inside `root`, in document order: shown, enabled, not
 *  taken out of the order, and one radio of each group (the checked one). */
function tabbables(root: ParentNode, all = false) {
  return Array.from(root.querySelectorAll<HTMLElement>(TABBABLE)).filter((element) => {
    if ((!all && element.tabIndex < 0) || element.closest("[inert]")) return false;
    if (element.getClientRects().length === 0) return false;
    if (element instanceof HTMLInputElement && element.type === "radio" && element.name) {
      const group = Array.from(
        (element.form ?? document).querySelectorAll<HTMLInputElement>(
          `input[type="radio"][name="${CSS.escape(element.name)}"]`,
        ),
      ).filter((radio) => !radio.disabled && radio.getClientRects().length > 0);
      return element === (group.find((radio) => radio.checked) ?? group[0]);
    }
    return true;
  });
}

/** Where Tab lands in a place: a list's selected row, else its first; the
 *  sidebar's current page; else the place's first control. */
function entry(place: HTMLElement, rows: boolean) {
  if (rows) {
    const selected = place.querySelector<HTMLElement>(
      ":is([data-row-id], [data-row])[data-focused]",
    );
    for (const row of [selected, place.querySelector<HTMLElement>(ROW)]) {
      const link = row?.querySelector<HTMLElement>(ROW_LINK) ?? (row && tabbables(row)[0]);
      if (link) return link;
    }
  }
  const current = place.querySelector<HTMLElement>('[aria-current="page"]');
  if (current && tabbables(place).includes(current)) return current;
  return tabbables(place)[0] ?? null;
}

/** Tab moves the focus by place rather than by control, so a long list or
 *  the sidebar costs one press: the sidebar (`data-tab-region`, whose links
 *  ↑ and ↓ then move through) is one stop, and so are a page's list rows,
 *  landing on the selected row so its keys go on working; every other
 *  control (a toolbar's, a detail pane's) is a stop of its own. In a dialog
 *  Tab stays inside it and goes through each of its controls. A menu keeps
 *  its own Tab, which closes it. Nothing changes for whoever never presses
 *  Tab: a click still gives up its focus (`usePointerBlurs`), the page's
 *  keys still act on the selection, and ↑, ↓ and Escape keep their old
 *  meaning until Tab is pressed again after the pointer (`tabbed`). */
export function useTabStops() {
  useEffect(() => {
    const down = (event: KeyboardEvent) => {
      if (event.ctrlKey || event.metaKey || event.altKey || event.isComposing) return;
      const focused = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      // A menu keeps its keys, the filter menu's panel too.
      if (focused?.closest('[role="menu"], .local-rail-menu, .filter-menu-panel')) return;
      // A box that completes what is typed takes Tab while it has something
      // to complete (the filter bar's `data-own-tab`).
      if (event.key === "Tab" && !event.shiftKey && focused?.closest("[data-own-tab]")) return;
      const dialog = Array.from(document.querySelectorAll<HTMLDialogElement>("dialog[open]")).at(
        -1,
      );
      const region = focused?.closest<HTMLElement>("[data-tab-region]");
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (!tabbed || dialog || !region || event.shiftKey) return;
        const controls = tabbables(region, true);
        const next = controls[controls.indexOf(focused!) + (event.key === "ArrowDown" ? 1 : -1)];
        event.preventDefault();
        next?.focus();
        return;
      }
      if (event.key !== "Tab") return;
      event.preventDefault();
      tabbed = true;
      const root = dialog ?? document.body;
      // Each stop is a control, or a place whose controls count as one.
      const stops: (HTMLElement | { place: HTMLElement; rows: boolean })[] = [];
      const places = new Map<HTMLElement, { place: HTMLElement; rows: boolean }>();
      const stopOf = (element: HTMLElement) => {
        const place = dialog
          ? null
          : (element.closest<HTMLElement>("[data-tab-region]") ??
            (element.closest(ROW) && !element.closest(ROW_PANEL) ? root : null));
        if (!place) return element;
        let stop = places.get(place);
        if (!stop) places.set(place, (stop = { place, rows: place === root }));
        return stop;
      };
      const elements = tabbables(root);
      for (const element of elements) {
        const stop = stopOf(element);
        if (!stops.includes(stop)) stops.push(stop);
      }
      if (stops.length === 0) return;
      const step = event.shiftKey ? -1 : 1;
      let index: number;
      if (!focused || focused === document.body || !root.contains(focused)) {
        index = event.shiftKey ? stops.length - 1 : 0;
      } else if (elements.includes(focused) || focused.closest("[data-tab-region]")) {
        index = (stops.indexOf(stopOf(focused)) + step + stops.length) % stops.length;
      } else {
        // Focus Tab cannot reach itself, such as a heading a page focused:
        // on from where it is.
        const after = (element: HTMLElement) =>
          !!(focused.compareDocumentPosition(element) & Node.DOCUMENT_POSITION_FOLLOWING);
        const next = event.shiftKey
          ? elements.findLast((element) => !after(element))
          : elements.find(after);
        index = next ? stops.indexOf(stopOf(next)) : event.shiftKey ? stops.length - 1 : 0;
      }
      const stop = stops[index]!;
      (stop instanceof HTMLElement ? stop : entry(stop.place, stop.rows))?.focus();
    };
    const pointer = () => {
      tabbed = false;
    };
    // Captured, so it comes before the page's own keys.
    window.addEventListener("keydown", down, true);
    window.addEventListener("pointerdown", pointer, true);
    return () => {
      window.removeEventListener("keydown", down, true);
      window.removeEventListener("pointerdown", pointer, true);
      tabbed = false;
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

const back: Shortcut = { keys: ["u", "Escape"], label: "Back to the list", short: "back" };
/** Every key of the app, grouped by where it works: the table the shortcuts
 *  sheet lists and each page's key bar reads. */
function shortcutGroups(
  pages: PageModifier,
): { place: ShortcutsPlace; title: string; rows: Shortcut[] }[] {
  return [
    {
      place: "anywhere",
      title: "Anywhere",
      rows: [
        { keys: [`${COMMAND}K`], label: "Go to a page, pull request or session" },
        { keys: [`${COMMAND}.`], label: "Actions for the focused row, as a right click" },
        { keys: [REFRESH_KEY_LABEL], label: "Refresh the page" },
        {
          keys: MAC ? ["⌃N", "⌃P"] : ["Ctrl+N", "Ctrl+P"],
          label: "Down, up in any list or menu, as ↓ and ↑",
        },
        ...PAGES.map((item) => ({ keys: [pageKeyLabel(item.page, pages)], label: item.label })),
        MAC
          ? { keys: ["⌘[", "⌘]"], label: "Back, forward" }
          : { keys: ["Alt+←", "Alt+→"], label: "Back, forward" },
        { keys: ["?"], label: "Help menu (then Enter shows this list), or hide this list" },
        {
          keys: [MAC ? "⌥ Option" : "Alt"],
          label: "Hold to show the page's keys and those on buttons",
        },
        { keys: ["Escape"], label: "Close this list" },
      ],
    },
    {
      place: "inbox",
      title: "Inbox",
      rows: [
        ...UP_NEXT_SHORTCUTS,
        { keys: [GITHUB_KEY_LABEL], label: "Open the pull request on GitHub" },
        { keys: ["/"], label: "Open the filter" },
        {
          keys: MAC ? ["⇧J", "⇧K", "⌃↓", "⌃↑"] : ["Shift+J", "Shift+K", "Ctrl+↓", "Ctrl+↑"],
          label: "Next, previous inbox",
        },
      ],
    },
    {
      place: "pulls",
      title: "Pull requests",
      rows: [
        ...INBOX_SHORTCUTS,
        { keys: [GITHUB_KEY_LABEL], label: "Open the focused pull request on GitHub" },
        { keys: ["/"], label: "Open the filter" },
        { keys: ["Escape"], label: "Clear the row focus" },
        { keys: VIEW_KEYS.previous, label: "Previous view", short: "views" },
        { keys: VIEW_KEYS.next, label: "Next view", short: "views" },
      ],
    },
    {
      place: "review",
      title: "A pull request or group",
      rows: [
        { keys: ["j", "ArrowDown"], label: "Next item in the section", short: "move" },
        { keys: ["k", "ArrowUp"], label: "Previous item in the section", short: "move" },
        {
          keys: MAC ? ["⇧J", "⇧↓", "ArrowRight"] : ["Shift+J", "Shift+↓", "ArrowRight"],
          label: "Next section",
          short: "sections",
        },
        {
          keys: MAC ? ["⇧K", "⇧↑", "ArrowLeft"] : ["Shift+K", "Shift+↑", "ArrowLeft"],
          label: "Previous section",
          short: "sections",
        },
        {
          keys: [" "],
          label: "Open or close the item's details, or the selected group in the sidebar",
          short: "expand",
        },
        { keys: ["Enter"], label: "Open the item, such as a commit in the list", short: "open" },
        { keys: ["m"], label: "Merge, where offered", short: "merge" },
        { keys: ["f"], label: "Fix, where offered", short: "fix" },
        { keys: ["t"], label: TRANSCRIPT_LABEL },
        { keys: [GITHUB_KEY_LABEL], label: "Open the pull request on GitHub" },
        {
          keys: ["c"],
          label: `Comment on the section, risk or file being read; ${COMMAND}↵ saves it`,
          short: "comment",
        },
        {
          keys: ["v"],
          label: "Send your review: approve, comment or request changes",
          short: "review",
        },
        {
          keys: ["a"],
          label: "Ask the review's agent a question; Esc in the panel closes it",
          short: "ask",
        },
        {
          keys: ["]"],
          label: "Toggle the right-hand sidebar; opening it gives it the keys",
          short: "sidebar",
        },
        {
          keys: ["h", "l"],
          label: "In the sidebar: Details, Activity and Comments; Esc goes back to the review",
        },
        back,
      ],
    },
    { place: "walkthrough", title: "Walkthrough", rows: WALKTHROUGH_SHORTCUTS },
    {
      place: "sessions",
      title: "Sessions",
      rows: [
        ...LIST_SHORTCUTS,
        { keys: ["p"], label: "Open the session's pull request" },
        { keys: ["r"], label: "Reply to the session; Enter sends it", short: "reply" },
        {
          keys: ["f"],
          label: "Bring the session's terminal or window forward, where offered",
          short: "focus",
        },
        { keys: [" "], label: "Peek at the session below its row", short: "peek" },
        {
          keys: ["1–9"],
          label: "Answer the focused question or permission; Enter submits several",
          short: "answer",
        },
      ],
    },
    {
      place: "jobs",
      title: "Jobs",
      rows: [
        ...LIST_SHORTCUTS,
        {
          keys: ["t"],
          label: "Watch the fix agent or read its transcript, where there is one",
          short: "transcript",
        },
      ],
    },
    {
      place: "repositories",
      title: "Repositories",
      rows: [
        ...LIST_SHORTCUTS.map((row) =>
          row.keys.includes("Enter")
            ? { ...row, label: "Open the repository's settings", short: "settings" }
            : row,
        ),
        { keys: ["a"], label: "Add repositories, or finish adding", short: "add" },
        { keys: ["/"], label: "Search GitHub repositories while adding", short: "search" },
      ],
    },
    {
      place: "timeline",
      title: "Timeline",
      rows: [
        ...LIST_SHORTCUTS.map((row) =>
          row.keys.includes("Enter")
            ? { ...row, label: "Open or close the feature or decision" }
            : row,
        ),
        { keys: ["/"], label: "Search the features or decisions", short: "search" },
      ],
    },
    { place: "settings", title: "Settings", rows: SETTINGS_SHORTCUTS },
    {
      place: "page",
      title: "A session or job transcript",
      rows: [
        back,
        { keys: ["j"], label: "Next message of a session's transcript", short: "next" },
        { keys: ["k"], label: "Previous message", short: "previous" },
        { keys: [" "], label: "Show all of the message, or less again", short: "show all" },
        { keys: ["Enter"], label: "Open the message on its own", short: "open" },
        {
          keys: ["f"],
          label: "Bring the session's terminal or window forward, where offered",
          short: "focus",
        },
        {
          keys: ["r"],
          label: "Reply to the session, or write a message for when its turn ends",
          short: "reply",
        },
      ],
    },
  ];
}

/** Where a row menu opens with ⌘.: the lists. */
const LISTS: ShortcutsPlace[] = ["pulls", "sessions", "jobs", "repositories"];

/** The keys of where the person is, in one line along the foot of the page,
 *  shown while Alt (Option on a Mac) is held, with the keys on buttons
 *  (`useKeyHints`): each key the place's group names `short`, keys sharing a
 *  name together ("j k move"), then the row menu's key on a list. Only keys
 *  for this page belong here. It is drawn for the eye; the sheet (`?`) lists
 *  these and the app-wide keys aloud. */
export function KeyBar({ place, className }: { place: ShortcutsPlace; className?: string }) {
  const items: { keys: string[]; short: string }[] = [];
  // Page keys have no `short`, so the bar does not depend on their modifier.
  for (const row of shortcutGroups("").find((group) => group.place === place)?.rows ?? []) {
    if (!row.short) continue;
    const last = items.at(-1);
    if (last?.short === row.short) last.keys.push(row.keys[0]!);
    else items.push({ keys: [row.keys[0]!], short: row.short });
  }
  if (LISTS.includes(place)) items.push({ keys: [`${COMMAND}.`], short: "actions" });
  return (
    <footer
      aria-hidden="true"
      className={["key-bar key-hint", className].filter(Boolean).join(" ")}
    >
      {items.map((item) => (
        <span key={item.short}>
          {item.keys.map((key) => (
            <kbd key={key}>{key === "Enter" ? "↵" : (KEY_NAMES[key] ?? key)}</kbd>
          ))}{" "}
          {item.short}
        </span>
      ))}
    </footer>
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
  const dialog = useModal();
  const title = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    // The list scrolls; it opens at its top, on the current page's keys,
    // rather than on its close control.
    title.current?.focus({ preventScroll: true });
    if (dialog.current) dialog.current.scrollTop = 0;
  }, []);
  const groups = shortcutGroups(usePreferences().preferences.page_modifier);
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
      <div className="local-dialog-head shortcuts-head">
        <h2
          ref={title}
          id="shortcuts-title"
          tabIndex={-1}
          className="text-lg font-semibold outline-none"
        >
          Keyboard shortcuts
        </h2>
        <CloseButton onClick={close} />
      </div>
      <div className="shortcuts-body">
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
      </div>
    </dialog>
  );
}
