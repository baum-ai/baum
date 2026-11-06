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
    let timer: ReturnType<typeof setInterval> | undefined;
    const stop = () => {
      clearInterval(timer);
      timer = undefined;
    };
    const start = () => {
      if (timer === undefined) timer = setInterval(() => latest.current(), ms);
    };
    const changed = () => {
      if (document.hidden) {
        stop();
        return;
      }
      stop();
      latest.current();
      start();
    };
    if (!document.hidden) start();
    document.addEventListener("visibilitychange", changed);
    return () => {
      document.removeEventListener("visibilitychange", changed);
      stop();
    };
  }, [ms]);
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
