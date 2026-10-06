import { riskKey } from "./row-keys";
import type { ReviewDocument } from "./review-document";

/** Compare identities, not titles, severity, or position in the triage list. */
export function reviewVersionChanges(current: ReviewDocument, previous: ReviewDocument) {
  const previousSections = new Set(previous.sections.map((section) => section.id));
  const previousRisks = new Set(
    previous.sections.flatMap((section) =>
      section.risks.map((risk) => riskKey(section.id, risk.id)),
    ),
  );
  return {
    sections: new Set(
      current.sections
        .filter((section) => !previousSections.has(section.id))
        .map((section) => section.id),
    ),
    risks: new Set(
      current.sections.flatMap((section) =>
        section.risks
          .filter((risk) => !previousRisks.has(riskKey(section.id, risk.id)))
          .map((risk) => riskKey(section.id, risk.id)),
      ),
    ),
  };
}

/** The versions, among `documents`, in which each section still had hunks
 *  of its own, by section id: where an obsolete or mitigated risk's code
 *  evidence can still be read. */
export function sectionActiveVersions(documents: ReviewDocument[]): Map<string, number[]> {
  const active = new Map<string, number[]>();
  for (const document of documents) {
    for (const section of document.sections) {
      if (section.derived.state === "stale") continue;
      active.set(section.id, [...(active.get(section.id) ?? []), document.review.version]);
    }
  }
  return active;
}
