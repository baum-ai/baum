import { useEffect, useRef, useState } from "react";

import { type LoadReviewVersion, type ReviewDocument, riskEvidenceBound } from "./review-document";
import { sectionActiveVersions } from "./review-versions";

/** The largest version that `riskEvidenceReviewVersion` can ask about for any
 *  risk of `documents`; versions from it upward are never read. */
function earliestEvidenceBound(documents: ReviewDocument[]): number {
  let bound = 0;
  for (const document of documents)
    for (const section of document.sections)
      for (const risk of section.risks)
        bound = Math.max(
          bound,
          riskEvidenceBound(risk, section.derived.state, document.review.version) ?? 0,
        );
  return bound;
}

/** The review versions in which each section of `document` had hunks, once
 *  the earlier versions that a stale section or a mitigated risk can ask about
 *  have been read. Null until then, and null when nothing on the page needs
 *  an earlier version. The earlier versions come through the host's loader,
 *  which should cache them for the section pane's "Since v1" links. `shown` is
 *  an earlier version or comparison on screen, whose own stale sections and
 *  mitigated risks ask about versions too. */
export function useSectionHistory(
  document: ReviewDocument | null,
  versions: number[],
  load: LoadReviewVersion,
  shown: ReviewDocument | null = null,
): { active: Map<string, number[]> } | null {
  const [loaded, setLoaded] = useState<{ key: string; active: Map<string, number[]> } | null>(null);
  // Keyed on the versions' values and read through the latest loader, so a
  // host that passes a new array or function on each render reads nothing
  // again.
  const bound = document ? earliestEvidenceBound(shown ? [document, shown] : [document]) : 0;
  const list = versions.filter((version) => version < bound).join(",");
  // The document and the version on screen are read when the effect runs, not
  // depended on: a refetch that yields the same version reads nothing again.
  // The key names the review too, as another one can share a version number.
  const key = document
    ? `${document.review.repository}#${document.review.pull_request}@${document.review.generated_at}:${document.review.version}:${shown?.review.version ?? ""}:${list}`
    : "";
  const latest = useRef({ document, shown });
  latest.current = { document, shown };
  const loader = useRef(load);
  loader.current = load;
  useEffect(() => {
    const { document: current, shown: onScreen } = latest.current;
    if (!current || !list) return;
    let cancelled = false;
    const wanted = list.split(",").map(Number);
    void Promise.all(wanted.map((version) => loader.current(version))).then((states) => {
      if (cancelled) return;
      const previous = states.flatMap((state) =>
        state.status === "ready" ? [state.document] : [],
      );
      const known =
        onScreen && onScreen.review.version !== current.review.version ? [onScreen] : [];
      const documents = [...previous, ...known, current].filter(
        (candidate, position, all) =>
          all.findIndex((other) => other.review.version === candidate.review.version) === position,
      );
      setLoaded({ key, active: sectionActiveVersions(documents) });
    });
    return () => {
      cancelled = true;
    };
  }, [list, key]);
  return loaded?.key === key ? { active: loaded.active } : null;
}
