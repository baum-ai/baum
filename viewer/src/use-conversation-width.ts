import { useCallback, useEffect, useRef, useState } from "react";

export const CONVERSATION_MIN = 320;
export const CONVERSATION_MAX = 760;
export const CONVERSATION_DEFAULT = 400;
/** What the review pane keeps whatever the reader drags: below this the trade
 *  stops being a trade and the document just breaks. */
const REVIEW_MIN = 520;

const STORAGE_KEY = "baum.review.conversation-width";

/** Holds the conversation pane inside the range the row can honour. `available`
 *  is the width the two panes share, so on a narrower window the ceiling comes
 *  down with it rather than letting the review pane be squeezed out. */
export function clampPaneWidth(width: number, available = Number.POSITIVE_INFINITY): number {
  const ceiling = Math.max(CONVERSATION_MIN, Math.min(CONVERSATION_MAX, available - REVIEW_MIN));
  return Math.min(Math.max(Math.round(width), CONVERSATION_MIN), ceiling);
}

/** The reader's split: what they chose, and what the row can currently show.
 *
 *  The two are kept apart on purpose. Fitting a remembered 700px pane into a
 *  1280px window has to narrow it, but writing that back would forget the 700
 *  — the reader would widen the window again and find their split gone. Only a
 *  width they chose is stored.
 *
 *  The stored value is read after mount rather than during render: the server
 *  has no `localStorage`, and a width that differed between the two would
 *  hydrate into a mismatch. Every access is guarded because storage throws
 *  rather than returning null in a blocked-cookies browser. */
export function useConversationWidth() {
  const [width, setWidth] = useState(CONVERSATION_DEFAULT);
  // What the reader asked for, which outlives a window too narrow to show it.
  const chosen = useRef(CONVERSATION_DEFAULT);

  useEffect(() => {
    try {
      const stored = Number(localStorage.getItem(STORAGE_KEY));
      if (Number.isFinite(stored) && stored > 0) {
        const remembered = clampPaneWidth(stored);
        chosen.current = remembered;
        setWidth(remembered);
      }
    } catch {
      // Storage is unavailable; the default split is a fine answer.
    }
  }, []);

  /** A width the reader picked, by dragging or by keyboard. Remembered. */
  const choose = useCallback((next: number) => {
    chosen.current = next;
    setWidth(next);
    try {
      localStorage.setItem(STORAGE_KEY, String(next));
    } catch {
      // Not remembering the split is better than failing the drag.
    }
  }, []);

  /** The chosen width brought inside what the row can give. Not remembered. */
  const fit = useCallback((available: number) => {
    setWidth(clampPaneWidth(chosen.current, available));
  }, []);

  return { width, choose, fit };
}
