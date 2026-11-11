import { useCallback, useSyncExternalStore } from "react";

const listeners = new Set<() => void>();
// What was chosen this session, for browsers whose storage refuses writes.
const session = new Map<string, string>();
const refused = new Set<string>();

function read(key: string): string | null {
  if (session.has(key) && refused.has(key)) return session.get(key) ?? null;
  try {
    return localStorage.getItem(key);
  } catch {
    // Storage is unavailable; this session's choice is all there is.
    return session.get(key) ?? null;
  }
}

function write(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
    refused.delete(key);
  } catch {
    session.set(key, value);
    refused.add(key);
    // Not remembering the choice is better than refusing to make it.
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** A string the reader chose, remembered in `localStorage`, or null when none.
 *
 *  Read through `useSyncExternalStore` so a client-only render sees the stored
 *  value on its first pass, with no default-then-correct second render. The
 *  server snapshot is always null: the hosted UI renders on a server without
 *  storage, and hydration must produce the same markup; React then re-renders
 *  with the stored value once hydrated. */
export function useStoredSetting(key: string) {
  const value = useSyncExternalStore(
    subscribe,
    () => read(key),
    () => null,
  );
  const set = useCallback((next: string) => write(key, next), [key]);
  return [value, set] as const;
}
