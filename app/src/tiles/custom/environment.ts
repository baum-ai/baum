import { useEffect, useState, useSyncExternalStore, type RefObject } from "react";

/** What the custom tile host needs to know about where it runs: whether it
 *  may mount frames here, the app's theme, and when a frame is near enough
 *  to the reader to be worth mounting. */

/** False only inside the desktop app on Windows. There, a sandboxed frame may
 *  be able to reach the desktop shell's commands, and until that is checked
 *  the page mounts no tile frame and says so. The desktop shell's bridge
 *  (`__TAURI_INTERNALS__`) marks the desktop app, as Settings reads it, and
 *  WebView2's user agent names Windows. Everywhere else frames mount. */
export function framesAllowedHere(
  win: Window & { __TAURI_INTERNALS__?: unknown } = window,
): boolean {
  const desktop = Boolean(win.__TAURI_INTERNALS__);
  const windows = /Windows/i.test(win.navigator.userAgent || win.navigator.platform || "");
  return !(desktop && windows);
}

export type Theme = "light" | "dark";

const themeNow = (): Theme =>
  document.documentElement.dataset.theme === "dark" ? "dark" : "light";

function watchTheme(changed: () => void) {
  const observer = new MutationObserver(changed);
  observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
  return () => observer.disconnect();
}

/** The app's own theme (its `data-theme`, set from the reader's setting),
 *  followed live. Not `prefers-color-scheme`, which is the system's. */
export function useAppTheme(): Theme {
  return useSyncExternalStore(watchTheme, themeNow, () => "light");
}

/** At most this many frames are mounted at once; more wait their turn. */
export const MAX_FRAMES = 16;
/** A frame mounts once its box is this near the visible area... */
const NEAR = "600px 0px";
/** ...and is unmounted once it is farther than this. */
const FAR = "2400px 0px";

type Slot = {
  element: Element;
  near: boolean;
  inRange: boolean;
  mounted: boolean;
  set: (on: boolean) => void;
};

const slots = new Set<Slot>();
let mounted = 0;
type Observers = { near: IntersectionObserver; far: IntersectionObserver; users: number };
const observers = new Map<Element | null, Observers>();
const byElement = new Map<Element, Slot>();

/** The nearest ancestor that scrolls, which clips what an observer sees. */
function scrollRoot(element: Element): Element | null {
  for (let node = element.parentElement; node; node = node.parentElement) {
    const { overflowY } = getComputedStyle(node);
    if (overflowY === "auto" || overflowY === "scroll") return node;
  }
  return null;
}

function settle() {
  for (const slot of slots) {
    if (slot.mounted && !slot.inRange) {
      slot.mounted = false;
      mounted--;
      slot.set(false);
    }
  }
  for (const slot of slots) {
    if (!slot.mounted && slot.near && mounted < MAX_FRAMES) {
      slot.mounted = true;
      mounted++;
      slot.set(true);
    }
  }
}

function observersFor(root: Element | null): Observers {
  let pair = observers.get(root);
  if (!pair) {
    const near = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          const slot = byElement.get(entry.target);
          if (slot) slot.near = entry.isIntersecting;
        }
        settle();
      },
      { root, rootMargin: NEAR },
    );
    const far = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          const slot = byElement.get(entry.target);
          if (slot) slot.inRange = entry.isIntersecting;
        }
        settle();
      },
      { root, rootMargin: FAR },
    );
    pair = { near, far, users: 0 };
    observers.set(root, pair);
  }
  pair.users++;
  return pair;
}

/** Whether the frame in `ref`'s box should be mounted: once it comes near the
 *  visible area and a slot is free, until it is scrolled far away. `eager`
 *  mounts it at once, outside the count (a detail, which is what the reader
 *  opened). Where the browser cannot observe intersections, it mounts. */
export function useFrameSlot(ref: RefObject<Element | null>, eager: boolean): boolean {
  const lazy = !eager && typeof IntersectionObserver === "function";
  const [on, setOn] = useState(!lazy);
  useEffect(() => {
    const element = ref.current;
    if (!lazy || !element) {
      setOn(true);
      return;
    }
    const slot: Slot = { element, near: false, inRange: false, mounted: false, set: setOn };
    slots.add(slot);
    byElement.set(element, slot);
    const root = scrollRoot(element);
    const pair = observersFor(root);
    pair.near.observe(element);
    pair.far.observe(element);
    return () => {
      pair.near.unobserve(element);
      pair.far.unobserve(element);
      if (--pair.users === 0) {
        pair.near.disconnect();
        pair.far.disconnect();
        observers.delete(root);
      }
      slots.delete(slot);
      byElement.delete(element);
      if (slot.mounted) mounted--;
      settle();
    };
  }, [lazy, ref]);
  return on;
}

/** The frames mounted through slots now, for tests. */
export function mountedSlots(): number {
  return mounted;
}
