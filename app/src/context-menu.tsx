import { MAC } from "./shortcuts";
import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from "react";
import { CheckIcon, MoreIcon } from "@baum/viewer/components/icons";

/** One choice of a menu; `hint` is the key that does the same, `danger`
 *  draws one that stops or removes something in the critical tone, set apart
 *  below the rest, and `checked` makes it a choice that is on or off. */
export type MenuItem = {
  label: string;
  onSelect: () => void;
  hint?: string;
  danger?: boolean;
  checked?: boolean;
};

type Open = {
  x: number;
  y: number;
  items: MenuItem[];
  /** What the menu is about, read out as its name. */
  label?: string;
  /** The button that opened it: the menu hangs under it. */
  anchor?: HTMLElement;
  /** Whether the keys start on the last choice (Up on the button). */
  last?: boolean;
};

/** The key that opens the menu of what has the keys, as the app's own
 *  shortcut for the Menu key or Shift+F10 (see `useActionsKey`). */
export const ACTIONS_KEY = MAC ? "⌘." : "Ctrl+.";

/** The app's menus: `open` shows `items` at the pointer on a right click (or
 *  under the focused element when the keyboard asked, with the Menu key,
 *  Shift+F10 or ⌘.) in place of the browser's; `openFrom` hangs them under a
 *  row's ⋯ button (`MenuButton`); and `menu` is the element to render. The
 *  browser's own stays where it is useful: in a field, over selected text,
 *  and when there is nothing to offer. A list keeps one for all its rows, so
 *  a right click and the ⋯ button offer the same choices. */
export function useContextMenu() {
  const [state, setState] = useState<Open | null>(null);
  const id = useId();
  const open = (event: MouseEvent, items: MenuItem[], label?: string) => {
    const target = event.target as HTMLElement;
    if (
      items.length === 0 ||
      target.closest("input, textarea, select, [contenteditable]") ||
      window.getSelection()?.toString()
    )
      return;
    event.preventDefault();
    let { clientX: x, clientY: y } = event;
    // The keyboard's menu event has no pointer: it opens under the element.
    if (x === 0 && y === 0) {
      const rect = target.getBoundingClientRect();
      x = rect.left + 12;
      y = rect.top + Math.min(rect.height, 32);
    }
    setState({ x, y, items, label });
  };
  const openFrom = (anchor: HTMLElement, items: MenuItem[], label?: string, last = false) => {
    if (items.length === 0) return;
    const rect = anchor.getBoundingClientRect();
    setState({ x: rect.right, y: rect.bottom + 4, items, label, anchor, last });
  };
  const menu = state ? <ContextMenu {...state} id={id} close={() => setState(null)} /> : null;
  return { open, openFrom, menu, id, anchor: state?.anchor ?? null };
}

/** Opens the right-click menu of what has the keys with ⌘. (Ctrl+. elsewhere),
 *  as the Menu key or Shift+F10 do where there are such keys: a context menu
 *  event on the focused element, which opens under it. Not in a text field
 *  or under a dialog. Mounted once, for the whole app. */
export function useActionsKey() {
  useEffect(() => {
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      const command = MAC ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
      if (!command || event.key !== "." || event.altKey || event.shiftKey || event.defaultPrevented)
        return;
      const target = document.activeElement;
      if (
        !(target instanceof HTMLElement) ||
        target === document.body ||
        target.closest("input, textarea, select, [contenteditable]") ||
        document.querySelector("dialog[open]")
      )
        return;
      event.preventDefault();
      target.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);
}

/** A row's ⋯ button, the one control a list row carries: it opens the row's
 *  menu, the same one a right click or ⌘. opens. It is named for its row,
 *  says it opens a menu and whether that menu is open, and opens it from the
 *  keys as a menu button does (Enter, Space and Down on the first choice, Up
 *  on the last). The list shows it while the row is pointed at, holds the
 *  keys or has its menu open, and always on a touch screen. */
export function MenuButton({
  label,
  items,
  menu,
}: {
  /** The row's name, as in "Actions for acme/demo #7". */
  label: string;
  items: () => MenuItem[];
  menu: ReturnType<typeof useContextMenu>;
}) {
  const ref = useRef<HTMLButtonElement>(null);
  const open = menu.anchor !== null && menu.anchor === ref.current;
  const show = (last = false) => {
    if (ref.current) menu.openFrom(ref.current, items(), label, last);
  };
  return (
    <button
      ref={ref}
      type="button"
      className="row-menu-button"
      aria-label={`Actions for ${label}`}
      title={`Actions (${ACTIONS_KEY})`}
      aria-haspopup="menu"
      aria-expanded={open}
      aria-controls={open ? menu.id : undefined}
      onClick={(event) => {
        event.stopPropagation();
        const button = event.currentTarget;
        if (button.dataset.closing !== undefined) delete button.dataset.closing;
        else show();
      }}
      onPointerLeave={(event) => delete event.currentTarget.dataset.closing}
      onKeyDown={(event) => {
        // Enter and Space press the button as it is: the page's own keys for
        // them (Space peeking at the row) wait.
        if (event.key === " " || event.key === "Enter") {
          event.stopPropagation();
          return;
        }
        if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
        event.preventDefault();
        event.stopPropagation();
        show(event.key === "ArrowUp");
      }}
    >
      <MoreIcon size={16} />
    </button>
  );
}

function ContextMenu({
  x,
  y,
  items,
  label,
  anchor,
  last,
  id,
  close,
}: Open & { id: string; close: () => void }): ReactNode {
  const ref = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState({ left: x, top: y });
  // What had the keys before the menu took them, given back when it closes.
  const [opener] = useState(() => anchor ?? document.activeElement);
  const giveBack = () => {
    if (opener instanceof HTMLElement && opener.isConnected) opener.focus();
  };
  const dismiss = () => {
    close();
    giveBack();
  };
  const choices = () => [...(ref.current?.querySelectorAll<HTMLElement>("[role^=menuitem]") ?? [])];
  // Kept inside the window, measured before it is painted: under its button,
  // its right edge on the button's, or above the button when there is no
  // room below.
  useLayoutEffect(() => {
    const menu = ref.current;
    if (!menu) return;
    const { width, height } = menu.getBoundingClientRect();
    let left = anchor ? x - width : x;
    let top = y;
    if (anchor && top + height > window.innerHeight - 4)
      top = anchor.getBoundingClientRect().top - height - 4;
    left = Math.max(4, Math.min(left, window.innerWidth - width - 4));
    top = Math.max(4, Math.min(top, window.innerHeight - height - 4));
    setAt({ left, top });
    const list = choices();
    (last ? list[list.length - 1] : list[0])?.focus();
  }, [x, y]);
  // A click elsewhere, a scroll or the window losing focus closes it.
  useEffect(() => {
    const outside = (event: PointerEvent) => {
      const target = event.target as Node;
      if (ref.current?.contains(target)) return;
      // Its own button toggles it: the click that follows leaves it closed.
      if (anchor?.contains(target)) anchor.dataset.closing = "";
      close();
    };
    document.addEventListener("pointerdown", outside, true);
    window.addEventListener("scroll", close, true);
    window.addEventListener("resize", close);
    window.addEventListener("blur", close);
    return () => {
      document.removeEventListener("pointerdown", outside, true);
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("resize", close);
      window.removeEventListener("blur", close);
    };
  }, [close]);
  // Escape closes it wherever the keys are, and does nothing else: the page's
  // own Escape (clearing its row) waits for the next one.
  useEffect(() => {
    const escape = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopImmediatePropagation();
      dismiss();
    };
    window.addEventListener("keydown", escape, true);
    return () => window.removeEventListener("keydown", escape, true);
  });
  // The menu's keys are its own: the page's list keys wait until it closes.
  // Up and Down move and wrap, Home and End go to either end, a letter goes
  // to the next choice that starts with it, and Tab leaves the menu.
  const keys = (event: KeyboardEvent) => {
    event.stopPropagation();
    const list = choices();
    const index = list.indexOf(document.activeElement as HTMLElement);
    const go = (next: number) => {
      event.preventDefault();
      list[(next + list.length) % list.length]?.focus();
    };
    if (event.key === "Tab") {
      event.preventDefault();
      dismiss();
    } else if (event.key === "ArrowDown") go(index + 1);
    else if (event.key === "ArrowUp") go(index - 1);
    else if (event.key === "Home") go(0);
    else if (event.key === "End") go(list.length - 1);
    else if (event.key.length === 1 && /\S/.test(event.key) && !event.metaKey && !event.ctrlKey) {
      const letter = event.key.toLowerCase();
      for (let step = 1; step <= list.length; step++) {
        const candidate = list[(index + step) % list.length]!;
        if (candidate.textContent?.trim().toLowerCase().startsWith(letter)) {
          go(index + step);
          break;
        }
      }
    }
  };
  const firstDanger = items.findIndex((item) => item.danger);
  // With an on-or-off choice among them, every choice keeps the tick's column.
  const ticks = items.some((item) => item.checked !== undefined);
  return (
    <div
      ref={ref}
      id={id}
      role="menu"
      aria-label={label ? `Actions for ${label}` : "Actions"}
      className="context-menu"
      style={{ left: at.left, top: at.top }}
      onKeyDown={keys}
      onContextMenu={(event) => event.preventDefault()}
    >
      {items.map((item, index) => (
        <MenuChoice
          key={item.label}
          item={item}
          separated={index > 0 && index === firstDanger}
          ticks={ticks}
          select={() => {
            // The keys go back to the row first, so a dialog the choice
            // opens takes them from there and gives them back to it.
            close();
            giveBack();
            item.onSelect();
          }}
        />
      ))}
    </div>
  );
}

function MenuChoice({
  item,
  separated,
  ticks,
  select,
}: {
  item: MenuItem;
  separated: boolean;
  /** Whether the menu keeps a column for ticks. */
  ticks: boolean;
  select: () => void;
}) {
  const checkbox = item.checked !== undefined;
  return (
    <>
      {separated ? <div role="separator" className="context-menu-separator" /> : null}
      <button
        type="button"
        tabIndex={-1}
        role={checkbox ? "menuitemcheckbox" : "menuitem"}
        aria-checked={checkbox ? item.checked : undefined}
        className={item.danger ? "danger" : undefined}
        onClick={select}
      >
        {ticks ? (
          <span className="context-menu-check" aria-hidden="true">
            {item.checked ? <CheckIcon size={14} /> : null}
          </span>
        ) : null}
        <span className="context-menu-label">{item.label}</span>
        {item.hint ? <kbd aria-hidden="true">{item.hint}</kbd> : null}
      </button>
    </>
  );
}
