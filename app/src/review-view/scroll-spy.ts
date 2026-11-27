// How it works as one scrolling page: which of its parts ("Where this fits"
// and each section) the reader is on, decided from where the parts stand.
// Kept apart from the components, so the decisions are plain functions a
// test can call without a layout.

/** A part as the spy measures it: its top and bottom against the top of
 *  what the reader sees of the page (0 is the line under the sticky bars;
 *  above it is negative), in pixels. */
export type PartBox = { id: string; top: number; bottom: number };

/** How far down what the reader sees a part's top may stand and still be the
 *  part they are on, as a share of its height. */
export const READING_REACH = 1 / 3;

/** The part the reader is on: of the parts whose top has come within the
 *  upper third of what they see, the one whose top is nearest its top (the
 *  section that has just reached the top, or the long one still filling the
 *  page); before any has, the first. At the end of the page the last part,
 *  which may be too short ever to reach the top. A part the reader was taken
 *  to (a rail row, a key, a link) holds while they have not scrolled
 *  themselves (`pinned`), even when it cannot reach the top either. */
export function readingPart(
  parts: PartBox[],
  view: { height: number; atEnd: boolean },
  pinned: string | null = null,
): string | null {
  if (parts.length === 0) return null;
  if (pinned && parts.some((part) => part.id === pinned)) return pinned;
  if (view.atEnd) return parts[parts.length - 1]!.id;
  const reach = view.height * READING_REACH;
  let best: PartBox | null = null;
  for (const part of parts)
    if (part.top <= reach && (!best || Math.abs(part.top) <= Math.abs(best.top))) best = part;
  return (best ?? parts[0]!).id;
}

/** Whether a section the address now names is one the spy wrote itself (and
 *  so not a place to scroll to), given the sections it wrote that the
 *  address had not shown yet, oldest first. Returns what is still pending:
 *  every write up to the one that landed is spent, as the address may fold
 *  several into one change; none when the section is not among them. */
export function landedWrite(
  pending: string[],
  section: string,
): { own: boolean; pending: string[] } {
  const at = pending.indexOf(section);
  if (at < 0) return { own: false, pending: [] };
  return { own: true, pending: pending.slice(at + 1) };
}

/** Where the scroll should stand for a part's top to sit on the line under
 *  the sticky bars: from where it stands now (`scrollTop`), the part's top
 *  and that line in the window's coordinates, and how far the page can
 *  scroll. */
export function partScrollTop(
  scrollTop: number,
  partTop: number,
  line: number,
  maxScroll: number,
): number {
  return Math.max(0, Math.min(maxScroll, Math.round(scrollTop + partTop - line)));
}

// A rail row asks the page to scroll to its part even when the address
// already names it (the reader scrolled away within it), when choosing the
// row changes nothing the page would see.
type JumpListener = (part: string) => void;
const jumpListeners = new Set<JumpListener>();

/** Asks the How it works page on screen to scroll to `part`. */
export function askJump(part: string) {
  for (const listener of jumpListeners) listener(part);
}

/** Hears the rail's requests; returns the way to stop. */
export function onJump(listener: JumpListener): () => void {
  jumpListeners.add(listener);
  return () => {
    jumpListeners.delete(listener);
  };
}
