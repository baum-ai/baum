import { useLayoutEffect, useRef, type RefObject } from "react";

type Box = { x: number; y: number; width: number; height: number };

/** How long a card takes to reach its new place, in milliseconds. */
export const MOVE_MS = 320;

const EASE = "cubic-bezier(0.2, 0, 0, 1)";

/** The moves to animate when the cards under a root changed places: each card
 *  that was there before slides from its old box to its new one (`move`),
 *  each new one fades in (`enter`). Boxes are relative to the root, so a
 *  scroll between two layouts moves nothing. */
export function flipMoves(
  before: ReadonlyMap<string, Box>,
  after: ReadonlyMap<string, Box>,
): { id: string; kind: "move" | "enter"; dx: number; dy: number; resized: boolean }[] {
  const moves: { id: string; kind: "move" | "enter"; dx: number; dy: number; resized: boolean }[] =
    [];
  for (const [id, box] of after) {
    const old = before.get(id);
    if (!old) {
      moves.push({ id, kind: "enter", dx: 0, dy: 0, resized: false });
      continue;
    }
    const dx = Math.round(old.x - box.x);
    const dy = Math.round(old.y - box.y);
    const resized = Math.abs(old.width - box.width) > 1;
    if (dx !== 0 || dy !== 0 || resized) moves.push({ id, kind: "move", dx, dy, resized });
  }
  return moves;
}

/** Animates the cards (`[data-tile]`) under root from where they stood to
 *  where they stand whenever `layout` (a signature of the order and sizes)
 *  changes: First, Last, Invert, Play, with the Web Animations API, so
 *  nothing is left on the cards afterwards. Skipped where the person asked
 *  for reduced motion, or the browser cannot animate. */
export function useFlip(root: RefObject<HTMLElement | null>, layout: string) {
  const boxes = useRef(new Map<string, Box>());
  const drawn = useRef(layout);
  useLayoutEffect(() => {
    const element = root.current;
    if (!element) return;
    const origin = element.getBoundingClientRect();
    const cards = new Map<string, HTMLElement>();
    const now = new Map<string, Box>();
    for (const card of element.querySelectorAll<HTMLElement>("[data-tile]")) {
      const id = card.dataset.tile;
      if (!id) continue;
      const box = card.getBoundingClientRect();
      cards.set(id, card);
      now.set(id, {
        x: box.left - origin.left,
        y: box.top - origin.top,
        width: box.width,
        height: box.height,
      });
    }
    const changed = drawn.current !== layout;
    const reduced =
      typeof window.matchMedia === "function" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (changed && !reduced && boxes.current.size > 0) {
      for (const move of flipMoves(boxes.current, now)) {
        const card = cards.get(move.id);
        if (!card || typeof card.animate !== "function") continue;
        if (move.kind === "enter") {
          card.animate(
            [
              { opacity: 0, transform: "translateY(6px)" },
              { opacity: 1, transform: "none" },
            ],
            {
              duration: MOVE_MS,
              easing: EASE,
            },
          );
          continue;
        }
        card.animate(
          [
            {
              transform: `translate(${move.dx}px, ${move.dy}px)`,
              opacity: move.resized ? 0.55 : 1,
            },
            { transform: "none", opacity: 1 },
          ],
          { duration: MOVE_MS, easing: EASE },
        );
      }
    }
    boxes.current = now;
    drawn.current = layout;
  });
}
