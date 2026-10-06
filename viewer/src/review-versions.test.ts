import { describe, expect, it } from "vitest";
import { reviewDocumentFor } from "../test/review-document";
import type { ReviewDocument } from "./review-document";
import { riskKey } from "./row-keys";
import { reviewVersionChanges, sectionActiveVersions } from "./review-versions";

describe("reviewVersionChanges", () => {
  const current = reviewDocumentFor("acme/widgets", 42) as ReviewDocument;
  const previous = reviewDocumentFor("acme/widgets", 42, 1) as ReviewDocument;
  it("finds added sections and risks across the entire review", () => {
    const changes = reviewVersionChanges(current, previous);
    expect([...changes.sections]).toEqual(["tests"]);
    expect([...changes.risks]).toEqual([riskKey("behavior", "rsk_1")]);
  });
  it("does not call renamed, reordered, retiered or reactivated identities new", () => {
    const updated = structuredClone(previous);
    updated.sections.reverse();
    for (const section of updated.sections) {
      section.title = "Renamed";
      section.risks.reverse();
      for (const risk of section.risks) {
        risk.tier = "critical";
        risk.state = "active";
      }
    }
    const changes = reviewVersionChanges(updated, previous);
    expect(changes.sections.size).toBe(0);
    expect(changes.risks.size).toBe(0);
  });
});

describe("sectionActiveVersions", () => {
  const current = reviewDocumentFor("acme/widgets", 42) as ReviewDocument;
  const previous = reviewDocumentFor("acme/widgets", 42, 1) as ReviewDocument;
  it("lists the versions in which a section still had hunks", () => {
    const active = sectionActiveVersions([previous, current]);
    // The shared-label section had hunks in v1 and is obsolete in the current review.
    expect(active.get("duplicate-a")).toEqual([previous.review.version]);
    expect(active.get("behavior")).toEqual([previous.review.version, current.review.version]);
  });
});
