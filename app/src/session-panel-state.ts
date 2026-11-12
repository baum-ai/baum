import { useSyncExternalStore } from "react";

// The session panel's own state, kept for the whole window: whether it is
// open, the conversation it shows, its height and what is typed in it. Open,
// height and conversation are remembered across reloads in this window's
// storage, as the sidebar's fold is. It has a module of its own so any page
// can open the panel (Sessions' "Open in panel") without drawing it.
const STORE_KEY = "baum.session-panel.v1";
export const MIN_HEIGHT = 200;
export const DEFAULT_HEIGHT = 380;

export type PanelState = {
  open: boolean;
  /** The conversation shown; null for a new one. */
  conversation: string | null;
  height: number;
  /** Whether the daemon runs the session panel; null until known. */
  available: boolean | null;
  /** What is typed in the composer. */
  draft: string;
  /** Counts the requests to put the keys in the composer. */
  focus: number;
};

function loadState(): PanelState {
  const loaded: PanelState = {
    open: false,
    conversation: null,
    height: DEFAULT_HEIGHT,
    available: null,
    draft: "",
    focus: 0,
  };
  try {
    const saved = JSON.parse(localStorage.getItem(STORE_KEY) ?? "null") as Partial<PanelState>;
    if (saved && typeof saved === "object") {
      loaded.open = saved.open === true;
      loaded.conversation = typeof saved.conversation === "string" ? saved.conversation : null;
      if (typeof saved.height === "number" && saved.height >= MIN_HEIGHT)
        loaded.height = saved.height;
    }
  } catch {
    // Starts closed.
  }
  return loaded;
}

let state = loadState();
const listeners = new Set<() => void>();
function change(patch: Partial<PanelState>) {
  state = { ...state, ...patch };
  try {
    localStorage.setItem(
      STORE_KEY,
      JSON.stringify({ open: state.open, conversation: state.conversation, height: state.height }),
    );
  } catch {
    // Not remembered across reloads.
  }
  for (const listener of listeners) listener();
}

/** What had the keys before the panel opened, where they go back when it
 *  hides. */
let returnTo: Element | null = null;

/** What opens, hides and points the session panel, from anywhere in the app:
 *  its button, its key, Go to and the Sessions page's "Open in panel". */
export const sessionPanel = {
  /** Opens it with the keys in its composer; on `conversation` when given
   *  (null for a new one), else on the one it showed. */
  show(conversation?: string | null) {
    if (!state.open) returnTo = document.activeElement;
    change({
      open: true,
      focus: state.focus + 1,
      ...(conversation !== undefined ? { conversation } : {}),
    });
  },
  hide() {
    if (state.open) change({ open: false });
  },
  toggle() {
    if (state.open) sessionPanel.hide();
    else sessionPanel.show();
  },
  select(conversation: string | null) {
    change({ conversation, focus: state.focus + 1 });
  },
  setHeight(height: number) {
    change({ height: Math.max(MIN_HEIGHT, Math.round(height)) });
  },
  setAvailable(available: boolean) {
    if (state.available !== available) change({ available });
  },
  setDraft(draft: string) {
    change({ draft });
  },
  /** What is typed in the composer now. */
  draft() {
    return state.draft;
  },
  /** What had the keys before the panel opened, once: the panel gives them
   *  back as it hides. */
  takeReturnTo() {
    const element = returnTo;
    returnTo = null;
    return element;
  },
};

/** Puts the panel back as a fresh window starts it; for tests. */
export function resetSessionPanel() {
  try {
    localStorage.removeItem(STORE_KEY);
  } catch {
    // Nothing kept.
  }
  state = loadState();
  returnTo = null;
  for (const listener of listeners) listener();
}

const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
};
export function useSessionPanel(): PanelState {
  return useSyncExternalStore(subscribe, () => state);
}
