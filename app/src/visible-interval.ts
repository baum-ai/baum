import { useEffect, useRef } from "react";

/** Calls `fn` every `ms` while the page is visible, with none while it is
 *  hidden (a closed window or tray panel), and once when it becomes visible
 *  again, so what changed meanwhile shows without waiting for the next tick.
 *  `ms` of null stops it. `fn` may change between renders without resetting
 *  the interval. */
export function useVisibleInterval(fn: () => void, ms: number | null) {
  const latest = useRef(fn);
  latest.current = fn;
  useEffect(() => {
    if (ms === null) return;
    return visibleInterval(() => latest.current(), ms);
  }, [ms]);
}

/** `useVisibleInterval` for an effect that owns its callback. Returns the
 *  function that stops it. */
export function visibleInterval(fn: () => void, ms: number): () => void {
  let timer: ReturnType<typeof setInterval> | undefined;
  const stop = () => {
    clearInterval(timer);
    timer = undefined;
  };
  const start = () => {
    if (timer === undefined) timer = setInterval(fn, ms);
  };
  const changed = () => {
    stop();
    if (document.hidden) return;
    fn();
    start();
  };
  if (!document.hidden) start();
  document.addEventListener("visibilitychange", changed);
  return () => {
    document.removeEventListener("visibilitychange", changed);
    stop();
  };
}

/** Calls `fn` once, `ms` from now. If the wait runs out while the page is
 *  hidden, `fn` runs as soon as the page is visible again. Returns the
 *  function that cancels it. For polls that schedule their next read after
 *  each one. */
export function visibleTimeout(fn: () => void, ms: number): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let due = false;
  const cancel = () => {
    clearTimeout(timer);
    timer = undefined;
    document.removeEventListener("visibilitychange", changed);
  };
  const changed = () => {
    if (document.hidden || !due) return;
    cancel();
    fn();
  };
  document.addEventListener("visibilitychange", changed);
  timer = setTimeout(() => {
    timer = undefined;
    if (document.hidden) due = true;
    else {
      cancel();
      fn();
    }
  }, ms);
  return cancel;
}

const shared = new Map<number, { listeners: Set<() => void>; release: () => void }>();

/** `useVisibleInterval`'s schedule for many subscribers: one interval and one
 *  `visibilitychange` listener per period however many rows ask, each
 *  subscriber called on every tick. Returns the unsubscribe. */
export function subscribeVisibleInterval(fn: () => void, ms: number): () => void {
  let entry = shared.get(ms);
  if (!entry) {
    const listeners = new Set<() => void>();
    let timer: ReturnType<typeof setInterval> | undefined;
    const tick = () => {
      for (const listener of listeners) listener();
    };
    const stop = () => {
      clearInterval(timer);
      timer = undefined;
    };
    const changed = () => {
      stop();
      if (document.hidden) return;
      tick();
      timer = setInterval(tick, ms);
    };
    if (!document.hidden) timer = setInterval(tick, ms);
    document.addEventListener("visibilitychange", changed);
    entry = {
      listeners,
      release: () => {
        document.removeEventListener("visibilitychange", changed);
        stop();
        shared.delete(ms);
      },
    };
    shared.set(ms, entry);
  }
  const { listeners, release } = entry;
  listeners.add(fn);
  return () => {
    listeners.delete(fn);
    if (listeners.size === 0) release();
  };
}

/** Marks <html data-hidden> while the page is hidden, so the stylesheet can
 *  pause every animation: a hidden window must not repaint. */
export function trackHidden() {
  const mark = () => {
    if (document.hidden) document.documentElement.dataset.hidden = "";
    else delete document.documentElement.dataset.hidden;
  };
  document.addEventListener("visibilitychange", mark);
  mark();
}
