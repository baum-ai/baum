import { MAC } from "./shortcuts";
import { useTopLayer } from "./top-layer";
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
import { cn } from "@baum/viewer/components/classnames";
import { CheckIcon, MoreIcon } from "@baum/viewer/components/icons";
import { Menu, MenuItem as MenuItemButton, MenuSeparator } from "@baum/viewer/components/menu";
import {
  MENU_ITEM_CHECK,
  MENU_ITEM_KEY,
  MENU_ITEM_LABEL,
  MENU_ITEM_NOTE,
} from "@baum/viewer/components/primitive-classes";

/** One choice of a menu; `hint` is the key that does the same, `danger`
 *  draws one that stops or removes something in the critical tone, set apart
 *  below the rest, and `checked` makes it a choice that is on or off. A
 *  `disabled` one is shown but cannot be chosen, and the keys pass over it;
 *  its `note` says why, on a line under its label. */
export type MenuItem = {
  label: string;
  onSelect: () => void;
  hint?: string;
  danger?: boolean;
  checked?: boolean;
  disabled?: boolean;
  note?: string;
  /** What can be done with the choice itself (a session in a picker): a
   *  right click on it, or the Menu key, Shift+F10 or ⌘. while it has the
   *  keys, shows these in a menu of their own over it, the list staying open
   *  under them. */
  actions?: () => MenuItem[];
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
  /** Where the keys go back to when it closes, when not to `anchor`: a menu
   *  a shortcut opened gives them back to where they were. */
  returnTo?: Element | null;
  /** The actions of one of its choices, open over it. */
  actions?: Actions;
};

/** A choice's actions, open over the menu that holds the choice. */
type Actions = {
  x: number;
  y: number;
  items: MenuItem[];
  label: string;
  /** The choice they are for: Escape gives it the keys back. */
  choice: HTMLElement;
  /** What opened the menu under them: picking one gives it the keys. */
  opener: Element | null;
  /** Counts the actions opened, so each is a menu of its own. */
  count: number;
};

/** The key that opens the menu of what has the keys, as the app's own
 *  shortcut for the Menu key or Shift+F10 (see `useActionsKey`). */
export const ACTIONS_KEY = MAC ? "⌘." : "Ctrl+.";

function moveMenuFocus(event: KeyboardEvent): boolean {
  if (event.metaKey || event.ctrlKey || event.altKey || event.nativeEvent.isComposing) return false;
  const list = [
    ...event.currentTarget.querySelectorAll<HTMLElement>("[role^=menuitem]:not(:disabled)"),
  ];
  const index = list.indexOf(document.activeElement as HTMLElement);
  const next =
    event.key === "ArrowDown" || event.key === "j"
      ? index + 1
      : event.key === "ArrowUp" || event.key === "k"
        ? index - 1
        : event.key === "Home"
          ? 0
          : event.key === "End"
            ? list.length - 1
            : null;
  if (next === null || list.length === 0) return false;
  event.preventDefault();
  event.stopPropagation();
  list[(next + list.length) % list.length]?.focus();
  return true;
}

/** Inline choice menus take the keys while open, just as context menus do. */
export function useMenuFocus(open: boolean, close: () => void) {
  const ref = useRef<HTMLDivElement>(null);
  const opener = useRef<Element | null>(null);
  useTopLayer(ref, open);
  useLayoutEffect(() => {
    if (!open) return;
    opener.current = document.activeElement;
    ref.current?.querySelector<HTMLElement>("[role^=menuitem]:not(:disabled)")?.focus();
  }, [open]);
  const onKeyDown = (event: KeyboardEvent) => {
    if (moveMenuFocus(event)) return;
    if (event.key !== "Escape" && event.key !== "Tab") return;
    event.preventDefault();
    event.stopPropagation();
    close();
    if (opener.current instanceof HTMLElement && opener.current.isConnected) opener.current.focus();
  };
  return { ref, onKeyDown };
}

/** The app's menus: `open` shows `items` at the pointer on a right click (or
 *  under the focused element when the keyboard asked, with the Menu key,
 *  Shift+F10 or ⌘.) in place of the browser's; `openFrom` hangs them under a
 *  row's ⋯ button (`MenuButton`); and `menu` is the element to render. The
 *  browser's own stays where it is useful: in a field and over selected
 *  text; elsewhere `blockBrowserMenu` keeps it off. A list keeps one for all its rows, so
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
  const openFrom = (
    anchor: HTMLElement,
    items: MenuItem[],
    label?: string,
    last = false,
    returnTo?: Element | null,
  ) => {
    if (items.length === 0) return;
    const rect = anchor.getBoundingClientRect();
    setState({ x: rect.right, y: rect.bottom + 4, items, label, anchor, last, returnTo });
  };
  // A choice's actions open over the menu where they were asked for, which
  // stays open under them.
  const nest = (actions: Omit<Actions, "count">) =>
    setState((was) =>
      was ? { ...was, actions: { ...actions, count: (was.actions?.count ?? 0) + 1 } } : was,
    );
  const closeAll = () => setState(null);
  const closeActions = () => setState((was) => (was ? { ...was, actions: undefined } : was));
  const { actions, ...list } = state ?? {};
  const menu = state ? (
    <>
      <ContextMenu
        {...(list as Omit<Open, "actions">)}
        id={id}
        group={id}
        close={closeAll}
        closeAll={closeAll}
        covered={actions !== undefined}
        nest={nest}
      />
      {actions ? (
        <ContextMenu
          key={actions.count}
          x={actions.x}
          y={actions.y}
          items={actions.items}
          label={actions.label}
          returnTo={actions.choice}
          onList
          picked={actions.opener}
          over={state.anchor}
          id={`${id}-actions`}
          group={id}
          close={closeActions}
          closeAll={closeAll}
          nest={nest}
        />
      ) : null}
    </>
  ) : null;
  return { open, openFrom, menu, id, anchor: state?.anchor ?? null };
}

/** Whether a key press is the actions key, ⌘. (Ctrl+. elsewhere). */
function isActionsKey(event: {
  key: string;
  metaKey: boolean;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
}) {
  const command = MAC ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
  return command && event.key === "." && !event.altKey && !event.shiftKey;
}

/** Opens the right-click menu of what has the keys with ⌘. (Ctrl+. elsewhere),
 *  as the Menu key or Shift+F10 do where there are such keys: a context menu
 *  event on the focused element, which opens under it. Not in a text field
 *  or under a dialog. An element marked `data-actions-target` takes the event
 *  for a control inside it, even a field. Mounted once, for the whole app. */
export function useActionsKey() {
  useEffect(() => {
    const onKeyDown = (event: globalThis.KeyboardEvent) => {
      if (!isActionsKey(event) || event.defaultPrevented) return;
      const active = document.activeElement;
      // A control inside an element marked `data-actions-target` (a radio in
      // a thumbnail's label) counts as that element having the keys.
      const host = active instanceof HTMLElement ? active.closest("[data-actions-target]") : null;
      const target = host instanceof HTMLElement ? host : active;
      if (
        !(target instanceof HTMLElement) ||
        target === document.body ||
        (!host && target.closest("input, textarea, select, [contenteditable]")) ||
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
  returnTo,
  picked,
  over,
  id,
  group,
  close,
  closeAll,
  covered = false,
  onList = false,
  nest,
}: Omit<Open, "actions"> & {
  id: string;
  /** Names the menu and the actions open over it alike, so a click in one
   *  is not a click outside the other. */
  group: string;
  /** Closes this menu: a choice's actions close and leave its list open. */
  close: () => void;
  /** Closes this menu and the one under or over it. */
  closeAll: () => void;
  /** Whether a choice's actions are open over it: they have the keys. */
  covered?: boolean;
  /** Whether it is a choice's actions, open over the list that holds it. */
  onList?: boolean;
  /** Where the keys go when a choice is picked, when not where they go when
   *  it closes: the actions of a choice give them to what opened its list. */
  picked?: Element | null;
  /** For a choice's actions, the button the list under them hangs from. */
  over?: HTMLElement;
  nest: (actions: Omit<Actions, "count">) => void;
}): ReactNode {
  const ref = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState({ left: x, top: y });
  // What had the keys before the menu took them, given back when it closes.
  const [opener] = useState(() => returnTo ?? anchor ?? document.activeElement);
  const giveTo = (element: Element | null | undefined) => {
    if (element instanceof HTMLElement && element.isConnected) element.focus();
  };
  const giveBack = () => giveTo(opener);
  const dismiss = () => {
    close();
    giveBack();
  };
  const choices = () => [...(ref.current?.querySelectorAll<HTMLElement>("[role^=menuitem]") ?? [])];
  // Raised again when it moves to another target, over what opened since.
  useTopLayer(ref, `${x},${y}`);
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
  // A click elsewhere, a scroll or the window losing focus closes it, and
  // the menu under or over it.
  useEffect(() => {
    const shut = closeAll;
    const outside = (event: PointerEvent) => {
      const target = event.target as Node;
      if (ref.current?.contains(target)) return;
      // A click in the list under a choice's actions closes only them; one in
      // the actions leaves their list open.
      if (target instanceof Element && target.closest(`[data-menu-group="${group}"]`)) {
        if (onList) {
          close();
          // The click that follows only dismissed the actions: it must not
          // also pick the choice under it.
          const swallow = (click: Event) => {
            click.preventDefault();
            click.stopImmediatePropagation();
          };
          document.addEventListener("click", swallow, { capture: true, once: true });
          // A press that ends in no click (a drag off it) leaves the next
          // click alone.
          document.addEventListener(
            "pointerdown",
            () => document.removeEventListener("click", swallow, true),
            { capture: true, once: true },
          );
        }
        return;
      }
      const button = anchor ?? over;
      // Its own button toggles it: the click that follows leaves it closed.
      if (button?.contains(target)) button.dataset.closing = "";
      shut();
    };
    // A menu hung under its button closes only when what scrolled moves the
    // button: the page, or a box that holds it. Another box scrolling, such
    // as the session panel's transcript following its agent, leaves it open.
    const scrolled = (event: Event) => {
      const target = event.target;
      const button = anchor ?? over;
      if (button && target instanceof Element && !target.contains(button)) return;
      shut();
    };
    document.addEventListener("pointerdown", outside, true);
    window.addEventListener("scroll", scrolled, true);
    window.addEventListener("resize", shut);
    window.addEventListener("blur", shut);
    return () => {
      document.removeEventListener("pointerdown", outside, true);
      window.removeEventListener("scroll", scrolled, true);
      window.removeEventListener("resize", shut);
      window.removeEventListener("blur", shut);
    };
  }, [close, closeAll, anchor, over, group, onList]);
  // Escape closes it wherever the keys are, and does nothing else: the page's
  // own Escape (clearing its row) waits for the next one.
  useEffect(() => {
    const escape = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape" || covered) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      dismiss();
    };
    window.addEventListener("keydown", escape, true);
    return () => window.removeEventListener("keydown", escape, true);
  });
  // The menu's keys are its own: the page's list keys wait until it closes.
  // Up/Down and j/k move and wrap, Home and End go to either end, a letter goes
  // to the next choice that starts with it, and Tab leaves the menu.
  const keys = (event: KeyboardEvent) => {
    event.stopPropagation();
    // The window's ⌘. (useActionsKey) never sees a key the menu keeps: the
    // menu opens the actions of the choice that has the keys itself.
    if (isActionsKey(event)) {
      event.preventDefault();
      document.activeElement?.dispatchEvent(
        new globalThis.MouseEvent("contextmenu", { bubbles: true, cancelable: true }),
      );
      return;
    }
    if (moveMenuFocus(event)) return;
    const list = choices();
    const index = list.indexOf(document.activeElement as HTMLElement);
    const go = (next: number) => {
      event.preventDefault();
      list[(next + list.length) % list.length]?.focus();
    };
    if (event.key === "Tab") {
      event.preventDefault();
      dismiss();
    } else if (event.key.length === 1 && /\S/.test(event.key) && !event.metaKey && !event.ctrlKey) {
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
    // `context-menu` and the `context-menu-*` names below carry no style: the
    // rail and the tests find the menu and its parts by them.
    // `w-max` keeps the width measured before the menu is placed under its
    // button once it is there.
    <Menu
      ref={ref}
      id={id}
      aria-label={label ? `Actions for ${label}` : "Actions"}
      className="context-menu fixed w-max max-w-[320px] min-w-[200px]"
      data-menu-group={group}
      style={{ left: at.left, top: at.top }}
      onKeyDown={keys}
      onContextMenu={(event) => {
        event.preventDefault();
        const choice = (event.target as HTMLElement).closest<HTMLElement>("[role^=menuitem]");
        const item = choice ? items[choices().indexOf(choice)] : undefined;
        const actions = item?.actions?.() ?? [];
        if (!choice || !item || actions.length === 0) return;
        let { clientX: x, clientY: y } = event;
        // The keyboard's menu event has no pointer: it opens beside the choice.
        if (x === 0 && y === 0) {
          const rect = choice.getBoundingClientRect();
          x = rect.left + 12;
          y = rect.bottom;
        }
        nest({ x, y, items: actions, label: item.label, choice, opener: picked ?? opener });
      }}
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
            closeAll();
            giveTo(picked ?? opener);
            item.onSelect();
          }}
        />
      ))}
    </Menu>
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
      {separated ? <MenuSeparator /> : null}
      <MenuItemButton
        tabIndex={-1}
        role={checkbox ? "menuitemcheckbox" : "menuitem"}
        aria-checked={checkbox ? item.checked : undefined}
        tone={item.danger ? "danger" : "default"}
        disabled={item.disabled}
        onClick={select}
      >
        {ticks ? (
          <span className={cn("context-menu-check", MENU_ITEM_CHECK)} aria-hidden="true">
            {item.checked ? <CheckIcon size={14} /> : null}
          </span>
        ) : null}
        <span className={cn("context-menu-label", MENU_ITEM_LABEL)}>
          {item.label}
          {item.note ? (
            <span className={cn("context-menu-note", MENU_ITEM_NOTE)}>{item.note}</span>
          ) : null}
        </span>
        {item.hint ? (
          <kbd aria-hidden="true" className={MENU_ITEM_KEY}>
            {item.hint}
          </kbd>
        ) : null}
      </MenuItemButton>
    </>
  );
}
