import { useCallback, useEffect, useRef, useState } from "react";

export const CONVERSATION_MIN = 320;
export const CONVERSATION_MAX = 760;
export const CONVERSATION_DEFAULT = 400;
/** What the review pane keeps whatever the reader drags: below this the trade
 *  stops being a trade and the document just breaks. */
const REVIEW_MIN = 520;

/** A pane beside the review pane that the reader can resize: its range, the
 *  width it starts at, and where the reader's choice is remembered. */
export type PaneLimits = { min: number; max: number; fallback: number; storageKey: string };

export const CONVERSATION_LIMITS: PaneLimits = {
  min: CONVERSATION_MIN,
  max: CONVERSATION_MAX,
  fallback: CONVERSATION_DEFAULT,
  storageKey: "baum.review.conversation-width",
};

/** The section rail: wide enough to widen for long file names, never so wide
 *  that it stops being a rail. */
export const RAIL_LIMITS: PaneLimits = {
  min: 200,
  max: 480,
  fallback: 264,
  storageKey: "baum.review.rail-width",
};

/** Holds a pane inside the range the row can honour. `available` is the width
 *  it shares with the review pane, so on a narrower window the ceiling comes
 *  down with it rather than letting the review pane be squeezed out. */
export function clampWidth(
  width: number,
  limits: PaneLimits,
  available = Number.POSITIVE_INFINITY,
): number {
  const ceiling = Math.max(limits.min, Math.min(limits.max, available - REVIEW_MIN));
  return Math.min(Math.max(Math.round(width), limits.min), ceiling);
}

/** `clampWidth` for the conversation pane. */
export function clampPaneWidth(width: number, available = Number.POSITIVE_INFINITY): number {
  return clampWidth(width, CONVERSATION_LIMITS, available);
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
export function usePaneWidth(limits: PaneLimits) {
  const [width, setWidth] = useState(limits.fallback);
  // What the reader asked for, which outlives a window too narrow to show it.
  const chosen = useRef(limits.fallback);
  // The limits are constants per pane; a ref keeps the callbacks stable.
  const limitsRef = useRef(limits);

  useEffect(() => {
    try {
      const stored = Number(localStorage.getItem(limitsRef.current.storageKey));
      if (Number.isFinite(stored) && stored > 0) {
        const remembered = clampWidth(stored, limitsRef.current);
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
      localStorage.setItem(limitsRef.current.storageKey, String(next));
    } catch {
      // Not remembering the split is better than failing the drag.
    }
  }, []);

  /** The chosen width brought inside what the row can give. Not remembered. */
  const fit = useCallback((available: number) => {
    setWidth(clampWidth(chosen.current, limitsRef.current, available));
  }, []);

  return { width, choose, fit };
}

/** The conversation pane's width. */
export function useConversationWidth() {
  return usePaneWidth(CONVERSATION_LIMITS);
}

/** The section rail's width. */
export function useRailWidth() {
  return usePaneWidth(RAIL_LIMITS);
}
