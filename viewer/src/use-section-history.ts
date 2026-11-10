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
  // The earlier versions read so far for the key on screen, kept so a retry
  // asks only for the ones that failed.
  const read = useRef<{ key: string; documents: Map<number, ReviewDocument>; complete: boolean }>({
    key: "",
    documents: new Map(),
    complete: false,
  });
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
  // A load that left a version out runs again on the next refetch of the
  // document (a new object of the same version), not at once, so a failing
  // host is not asked in a loop.
  const seen = useRef(document);
  const refetches = useRef(0);
  if (seen.current !== document) {
    seen.current = document;
    refetches.current += 1;
  }
  const refetch = refetches.current;
  useEffect(() => {
    const { document: current, shown: onScreen } = latest.current;
    if (!current || !list) return;
    let cancelled = false;
    if (read.current.key !== key) read.current = { key, documents: new Map(), complete: false };
    if (read.current.complete) return;
    const documentsRead = read.current.documents;
    const wanted = list
      .split(",")
      .map(Number)
      .filter((version) => !documentsRead.has(version));
    void Promise.all(
      wanted.map((version) =>
        loader.current(version).then(
          (state) => {
            if (state.status === "ready") documentsRead.set(version, state.document);
            return state.status !== "failed";
          },
          () => false,
        ),
      ),
    ).then((settled) => {
      if (cancelled) return;
      read.current.complete = settled.every(Boolean);
      const previous = [...documentsRead.values()];
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
  }, [list, key, refetch]);
  return loaded?.key === key ? { active: loaded.active } : null;
}
