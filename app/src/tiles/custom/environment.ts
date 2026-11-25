import { desktopShell, type DesktopWindow } from "../../desktop/native";
import { useEffect, useState, useSyncExternalStore, type RefObject } from "react";

/** What the custom tile host needs to know about where it runs: whether it
 *  may mount frames here, the app's theme, and when a frame is near enough
 *  to the reader to be worth mounting. */

/** What a desktop shell declares about itself, by init script in the main
 *  frame before the page's scripts run. `tileFrames: true` says the shell
 *  refuses a frame's navigations to anything but the daemon's own pages (so
 *  a tile cannot open the person's browser at an address of its choosing)
 *  and that its commands are out of a sandboxed frame's reach. A frame cannot
 *  set it: it cannot write the page's globals. */
type DesktopDeclaration = { tileFrames?: unknown };

/** Whether the page may mount tile frames here: always in a browser; in a
 *  desktop shell (its bridge, `window.baumDesktop` in Electron or
 *  `__TAURI_INTERNALS__` in Tauri, is on the page) only
 *  when the shell declares `window.__BAUM_DESKTOP__ = { tileFrames: true }`.
 *  A shell that declares nothing, or anything else, is an older or
 *  unchecked one (the Windows shell, until its separate check is done), and
 *  the page mounts no frame there. */
export function framesAllowedHere(win: DesktopWindow = window): boolean {
  if (!desktopShell(win)) return true;
  const declared = win.__BAUM_DESKTOP__;
  if (typeof declared !== "object" || declared === null) return false;
  return (
    Object.hasOwn(declared, "tileFrames") && (declared as DesktopDeclaration).tileFrames === true
  );
}

/** What a tile says where frames are not mounted. */
export const NOT_IN_THIS_SHELL =
  "Custom tiles aren't available in this version of the desktop app yet.";

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
