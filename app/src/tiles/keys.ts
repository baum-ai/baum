import { useEffect, useRef } from "react";
import type { UrlState } from "@baum/viewer/navigation";
import { readSelection, withSelection } from "@baum/viewer/review-selection";
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
  url.update((current) => withSelection(current, { kind: "summary" }), {
    preventScrollReset: true,
  });
  return true;
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
