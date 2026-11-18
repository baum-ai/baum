import { useEffect, useRef } from "react";

/** A pull request's key, as the daemon names it in its `added_tiles`
 *  events: `owner/repo#123`, lowercased. */
export function addedTilesKey(repository: string, number: number): string {
  return `${repository.toLowerCase()}#${number}`;
}

const bus = new EventTarget();

/** Passes an `added_tiles` event of the app's event stream to the open page:
 *  `{pr}`, the key of the pull request whose added tiles (or pending cards)
 *  changed. */
export function publishAddedTilesEvent(data: string) {
  let event: { pr?: unknown };
  try {
    event = JSON.parse(data) as typeof event;
  } catch {
    return;
  }
  if (typeof event?.pr !== "string") return;
  bus.dispatchEvent(new CustomEvent("added_tiles", { detail: event.pr.toLowerCase() }));
}

/** Tells the open page the event stream reconnected, so it may have missed
 *  an event: it reads its tiles again. */
export function publishAddedTilesResync() {
  bus.dispatchEvent(new CustomEvent("added_tiles", { detail: null }));
}

/** Calls `reread` on each `added_tiles` event for the pull request `key`,
 *  and after a reconnection. */
export function useAddedTilesEvents(key: string, reread: () => void) {
  const latest = useRef({ key, reread });
  latest.current = { key, reread };
  useEffect(() => {
    const listener = (event: Event) => {
      const pr = (event as CustomEvent<string | null>).detail;
      if (pr !== null && pr !== latest.current.key) return;
      latest.current.reread();
    };
    bus.addEventListener("added_tiles", listener);
    return () => bus.removeEventListener("added_tiles", listener);
  }, []);
}
