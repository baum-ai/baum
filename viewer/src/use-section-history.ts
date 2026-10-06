import { useEffect, useRef, useState } from "react";

import type { LoadReviewVersion, ReviewDocument } from "./review-document";
import { sectionActiveVersions } from "./review-versions";

/** The review versions in which each section of `document` had hunks, once
 *  every earlier version has been read. Null until then, and null for a review
 *  with no earlier version. The earlier versions come through the host's
 *  loader, which should cache them for the section pane's "From v1" links. */
export function useSectionHistory(
  document: ReviewDocument | null,
  versions: number[],
  load: LoadReviewVersion,
): { active: Map<string, number[]> } | null {
  const [loaded, setLoaded] = useState<{ key: string; active: Map<string, number[]> } | null>(null);
  // Keyed on the versions' values and read through the latest loader, so a
  // host that passes a new array or function on each render reads nothing
  // again.
  const list = versions.join(",");
  const key = document ? `${document.review.version}:${list}` : "";
  const loader = useRef(load);
  loader.current = load;
  useEffect(() => {
    if (!document || !list) return;
    let cancelled = false;
    const wanted = list.split(",").map(Number);
    void Promise.all(wanted.map((version) => loader.current(version))).then((states) => {
      if (cancelled) return;
      const previous = states.flatMap((state) =>
        state.status === "ready" ? [state.document] : [],
      );
      setLoaded({ key, active: sectionActiveVersions([...previous, document]) });
    });
    return () => {
      cancelled = true;
    };
  }, [document, list, key]);
  return loaded?.key === key ? { active: loaded.active } : null;
}
