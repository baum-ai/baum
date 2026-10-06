import { useCallback, useEffect, useRef } from "react";

const SCROLL_IDLE_MS = 800;

/** A scroll handler that marks its element `data-scrolling` until it has been
 *  still for a moment, which is what `.scroll-on-demand` paints the bar on.
 *  It writes the attribute directly: a re-render per scroll event would
 *  redraw whatever sits beside the row for nothing. */
export function useScrollingFlag() {
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  return useCallback((event: { currentTarget: HTMLElement }) => {
    const element = event.currentTarget;
    element.dataset.scrolling = "";
    clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      delete element.dataset.scrolling;
    }, SCROLL_IDLE_MS);
  }, []);
}
