import { useEffect, useState } from "react";

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
  const key = document ? `${document.review.version}:${versions.join(",")}` : "";
  useEffect(() => {
    if (!document || versions.length === 0) return;
    let cancelled = false;
    void Promise.all(versions.map((version) => load(version))).then((states) => {
      if (cancelled) return;
      const previous = states.flatMap((state) =>
        state.status === "ready" ? [state.document] : [],
      );
      setLoaded({ key, active: sectionActiveVersions([...previous, document]) });
    });
    return () => {
      cancelled = true;
    };
  }, [document, versions, load, key]);
  return loaded?.key === key ? { active: loaded.active } : null;
}
