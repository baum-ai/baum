import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { reviewDocumentFor } from "../../test/review-document";
import type { ReviewDocument } from "../review-document";
import { ReviewSummary } from "./review-summary";

function render(reviewMdChanged: boolean | undefined, baseDigest: string | null = "base") {
  const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  document.review.generator.review_md_changed = reviewMdChanged;
  document.review.generator.review_md = baseDigest;
  return renderToStaticMarkup(<ReviewSummary document={document} onSelectSection={() => {}} />);
}

describe("review summary policy note", () => {
  it("says the base REVIEW.md applied when the pull request changes it", () => {
    const html = render(true);
    expect(html).toContain("Review policy changed");
    expect(html).toContain("followed the base branch");
  });

  it("says the default policy applied when the base branch has no REVIEW.md", () => {
    const html = render(true, null);
    expect(html).toContain("base branch has no REVIEW.md");
    expect(html).toContain("default review policy");
    expect(html).not.toContain("followed the base branch");
  });

  it("shows no note when the pull request leaves REVIEW.md alone", () => {
    expect(render(false)).not.toContain("Review policy changed");
    expect(render(undefined)).not.toContain("Review policy changed");
  });
});

describe("review summary parts", () => {
  it("hides Risks when only lower-tier risks remain", () => {
    const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
    for (const section of document.sections) {
      for (const risk of section.risks) risk.tier = "medium";
    }
    const html = renderToStaticMarkup(
      <ReviewSummary document={document} onSelectSection={() => {}} />,
    );
    expect(html).not.toContain('aria-label="Risks"');
    expect(html).not.toContain("None to read first");
    expect(html).not.toContain("View all risks");
  });

  it("leaves the details and activity to the conversation pane", () => {
    const html = render(false);
    for (const label of ["Checks", "Author", "Change size", "Linked sessions", "Review exchange"])
      expect(html).not.toContain(`aria-label="${label}"`);
  });
});
