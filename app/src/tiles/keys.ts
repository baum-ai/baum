import { useEffect, useRef } from "react";
import type { UrlState } from "@baum/viewer/navigation";
import { readSelection, withoutSelection, withSelection } from "@baum/viewer/review-selection";
import { shouldIgnore } from "../shortcuts";

/** The tiles page's places other than its landing page. */
const TILE_PLACES = new Set(["tile", "section", "risk"]);

/** Escape or `u` in a tile's detail goes back to the landing page, unless the
 *  keys are in the rail, the side pane or a menu, which keep their own
 *  Escape. True when it did. */
export function backToLanding(event: KeyboardEvent, url: UrlState): boolean {
  if (event.key !== "Escape" && event.key !== "u") return false;
  if (!TILE_PLACES.has(readSelection(url.search).kind)) return false;
  if (
    event.target instanceof HTMLElement &&
    event.target.closest("#review-rail, #review-aside, [role='menu']")
  )
    return false;
  event.preventDefault();
  leaveTile(url);
  return true;
}

/** The history entry of the landing page last shown: its review, and its
 *  depth in the app's history. */
let landingEntry: { review: string; depth: number } | null = null;

const reviewOf = (search: URLSearchParams) => withoutSelection(search).toString();
/** The current entry's depth in the app's history (`writeHistory`), which
 *  an entry the app did not write, such as the one a window opened on, has
 *  as 0. */
const depthNow = () => {
  const depth = (window.history.state as { depth?: unknown } | null)?.depth;
  return typeof depth === "number" ? depth : 0;
};

/** Notes the landing page's history entry, as it is shown. */
export function noteLanding(search: URLSearchParams) {
  landingEntry = { review: reviewOf(search), depth: depthNow() };
}

/** Back from a tile to the landing page: back through history when the entry
 *  before is that landing page, so Back does not open the tile again; else in
 *  the tile's place. */
export function leaveTile(url: UrlState) {
  const depth = depthNow();
  if (
    landingEntry &&
    depth === landingEntry.depth + 1 &&
    landingEntry.review === reviewOf(url.search)
  ) {
    window.history.back();
    return;
  }
  url.update((current) => withSelection(current, { kind: "summary" }), {
    replace: true,
    preventScrollReset: true,
  });
}

/** `backToLanding` while `enabled`, ahead of the page's own Escape and `u`,
 *  which leave the pull request for its list: inside a tile they first lead
 *  back to the landing page. */
export function useBackToLanding(enabled: boolean, url: UrlState) {
  const latest = useRef({ enabled, url });
  latest.current = { enabled, url };
  useEffect(() => {
    const down = (event: KeyboardEvent) => {
      if (!latest.current.enabled || event.defaultPrevented || shouldIgnore(event)) return;
      backToLanding(event, latest.current.url);
    };
    // Captured, so it runs before the app's own keys, which listen as the event bubbles.
    window.addEventListener("keydown", down, true);
    return () => window.removeEventListener("keydown", down, true);
  }, []);
}

/** The tile whose glance card the keys are on, on the landing page. */
export function activeCard(): string | null {
  return (
    document.querySelector<HTMLElement>("[data-review-block-active][data-tile]")?.dataset.tile ??
    null
  );
}
