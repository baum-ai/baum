import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type MouseEvent,
  type ReactNode,
} from "react";

/** One choice of a context menu; `hint` is the key that does the same, and
 *  `danger` draws one that stops or removes something in the critical tone. */
export type MenuItem = { label: string; onSelect: () => void; hint?: string; danger?: boolean };

type Open = { x: number; y: number; items: MenuItem[] };

/** A right-click menu of the app's own: `open` shows `items` at the pointer
 *  (under the focused element when the keyboard asked for it, with the Menu
 *  key or Shift+F10) in place of the browser's, and `menu` is the element to
 *  render. The browser's stays where it is useful: in a field, or over
 *  selected text, and when there is nothing to offer. */
export function useContextMenu() {
  const [state, setState] = useState<Open | null>(null);
  const open = (event: MouseEvent, items: MenuItem[]) => {
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
    setState({ x, y, items });
  };
  const menu = state ? <ContextMenu {...state} close={() => setState(null)} /> : null;
  return { open, menu };
}

function ContextMenu({ x, y, items, close }: Open & { close: () => void }): ReactNode {
  const ref = useRef<HTMLDivElement>(null);
  const [at, setAt] = useState({ left: x, top: y });
  // Kept inside the window, measured before it is painted.
  useLayoutEffect(() => {
    const menu = ref.current;
    if (!menu) return;
    const { width, height } = menu.getBoundingClientRect();
    setAt({
      left: Math.max(4, Math.min(x, window.innerWidth - width - 4)),
      top: Math.max(4, Math.min(y, window.innerHeight - height - 4)),
    });
    menu.querySelector<HTMLButtonElement>("button")?.focus();
  }, [x, y]);
  // A click elsewhere, a scroll or the window losing focus closes it.
  useEffect(() => {
    const outside = (event: PointerEvent) => {
      if (!ref.current?.contains(event.target as Node)) close();
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
  // The menu's keys are its own: the page's list keys wait until it closes.
  const keys = (event: KeyboardEvent) => {
    event.stopPropagation();
    const buttons = [...(ref.current?.querySelectorAll("button") ?? [])];
    const index = buttons.indexOf(document.activeElement as HTMLButtonElement);
    if (event.key === "Escape" || event.key === "Tab") {
      event.preventDefault();
      close();
    } else if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      buttons[(index + step + buttons.length) % buttons.length]?.focus();
    }
  };
  return (
    <div
      ref={ref}
      role="menu"
      className="context-menu"
      style={{ left: at.left, top: at.top }}
      onKeyDown={keys}
      onContextMenu={(event) => event.preventDefault()}
    >
      {items.map((item) => (
        <button
          key={item.label}
          type="button"
          role="menuitem"
          className={item.danger ? "danger" : undefined}
          onClick={() => {
            close();
            item.onSelect();
          }}
        >
          {item.label}
          {item.hint ? <kbd aria-hidden="true">{item.hint}</kbd> : null}
        </button>
      ))}
    </div>
  );
}
