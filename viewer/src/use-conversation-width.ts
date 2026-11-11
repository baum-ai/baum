import { useCallback, useState } from "react";

import { useStoredSetting } from "./use-stored-setting";

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
 *  The stored value comes from `useStoredSetting`, which tolerates storage
 *  that throws and keeps the server and first client render identical. */
export function usePaneWidth(limits: PaneLimits) {
  const [stored, store] = useStoredSetting(limits.storageKey);
  // What the row can give; unlimited until the row measures itself.
  const [available, setAvailable] = useState(Number.POSITIVE_INFINITY);

  const number = Number(stored);
  // What the reader asked for, which outlives a window too narrow to show it.
  const chosen =
    stored !== null && Number.isFinite(number) && number > 0
      ? clampWidth(number, limits)
      : limits.fallback;
  const width = clampWidth(chosen, limits, available);

  /** A width the reader picked, by dragging or by keyboard. Remembered. */
  const choose = useCallback((next: number) => store(String(next)), [store]);

  /** The chosen width brought inside what the row can give. Not remembered. */
  const fit = useCallback((room: number) => setAvailable(room), []);

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
