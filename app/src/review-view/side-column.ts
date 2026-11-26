import { useEffect, useRef } from "react";
import type { PaneLimits } from "@baum/viewer/use-conversation-width";

// The column beside a wide section (its views) and beside "Where this fits"
// (a file it names) share one width and one fold, so moving between
// sections never changes them. Kept apart from the components, as fast
// refresh needs.

/** The side column's width: about as wide as a diff reads well to start,
 *  remembered once the reader drags it, and always leaving the Overview
 *  enough to read. */
export const SIDE_LIMITS: PaneLimits = {
  min: 360,
  max: 1600,
  fallback: 640,
  keep: 360,
  storageKey: "baum.review.section-side-width",
};

/** Whether the side column is folded to its strip, "false" once opened. */
export const SIDE_COLLAPSED_KEY = "baum.review.section-side-collapsed";

/** `]` by itself, anywhere but a text field or a dialog: the side column's
 *  fold. (The review's right-hand sidebar takes Control+] in the app.) */
export function useBracketKey(toggle: () => void, enabled = true) {
  const latest = useRef(toggle);
  latest.current = toggle;
  useEffect(() => {
    if (!enabled) return;
    const onKey = (event: KeyboardEvent) => {
      if (
        event.key !== "]" ||
        event.defaultPrevented ||
        event.ctrlKey ||
        event.metaKey ||
        event.altKey ||
        event.isComposing ||
        document.querySelector("dialog[open]")
      )
        return;
      if (
        event.target instanceof HTMLElement &&
        event.target.closest('input, textarea, select, [contenteditable], [role="textbox"]')
      )
        return;
      event.preventDefault();
      latest.current();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [enabled]);
}
