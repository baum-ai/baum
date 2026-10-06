import { useEffect, useRef, useState } from "react";

import { useUrlState } from "./navigation";
import { readSelection, sameSelection, type Selection, withSelection } from "./review-selection";

/** Where the reader is in the review pane, kept in the URL and painted from
 *  a local copy.
 *
 *  The URL is the record — it is what a reload, a shared link and Back read.
 *  But the router applies a navigation inside a transition, a render behind a
 *  plain `setState`, and two rail clicks in quick succession would paint the
 *  first click's pane while the second's was still in flight (`flushSync` on
 *  the navigation was tried and did not close that gap). So the pane renders
 *  from this copy, which only has to win the race for what paints.
 *
 *  `pending` holds the places our own clicks have sent the URL that the URL
 *  has not shown yet, oldest first. A `search` change matching one of them is
 *  that click arriving (the router may fold several into one change, so
 *  everything pushed before it is spent too) — the copy already paints the
 *  latest, so it is not news. A change matching none of them is Back/Forward,
 *  or a click of ours the router never landed as-is: either way the URL is now
 *  the truth, so the copy follows it and the list is cleared rather than left
 *  to swallow every later Back. */
export function useReviewSelection(): [Selection, (next: Selection) => void] {
  const { search, update: writeSearch } = useUrlState();
  const [selection, setSelection] = useState<Selection>(() => readSelection(search));
  const pending = useRef<Selection[]>([]);
  // The latest selection, whether from a click or the URL. `select` compares
  // against this rather than the `selection` its render closed over, so two
  // clicks inside one frame each see the other's result.
  const latest = useRef(selection);
  latest.current = selection;
  useEffect(() => {
    const fromUrl = readSelection(search);
    const landed = pending.current.findIndex((item) => sameSelection(item, fromUrl));
    if (landed !== -1) {
      pending.current.splice(0, landed + 1);
      return;
    }
    pending.current = [];
    latest.current = fromUrl;
    setSelection(fromUrl);
  }, [search]);
  const select = (next: Selection) => {
    // Re-clicking the row already open — including collapsing or reopening
    // its own group — must not push a second history entry for the pane
    // already on screen: Back would then have to be clicked twice to leave it.
    if (sameSelection(latest.current, next)) return;
    latest.current = next;
    setSelection(next);
    pending.current.push(next);
    writeSearch((current) => withSelection(current, next), { preventScrollReset: true });
  };
  return [selection, select];
}
