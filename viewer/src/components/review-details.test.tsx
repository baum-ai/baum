import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { reviewDocumentFor } from "../../test/review-document";
import type { Block, ReviewDocument } from "../review-document";
import { ReviewActivity, ReviewDetails } from "./review-details";

function fixture() {
  return structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
}

describe("review details", () => {
  it("leads with the checks, then the author, what it implements and its size", () => {
    const html = renderToStaticMarkup(<ReviewDetails document={fixture()} />);
    const order = ["Checks", "Author", "Implements", "Change size"].map((label) =>
      html.indexOf(`aria-label="${label}"`),
    );
    expect(order.every((position) => position >= 0)).toBe(true);
    expect(order).toEqual(order.toSorted((left, right) => left - right));
  });
});

describe("review activity sessions", () => {
  it("says why a restricted indexed session carries no content", () => {
    const document = fixture();
    document.index.sessions = [
      { id: "restricted", vendor: "claude_code", models: [], title: null, restricted: true },
    ];
    const html = renderToStaticMarkup(
      <ReviewActivity document={document} onSelectSection={() => {}} />,
    );
    expect(html).toContain("Details restricted");
    expect(html).toContain(
      "This repository is not a connected member of this session, so the review carries none of its prompts or events.",
    );
    expect(html).not.toContain("belongs to another repository");
  });

  it("offers the whole list only when it can open it", () => {
    const document = fixture();
    const without = renderToStaticMarkup(
      <ReviewActivity document={document} onSelectSection={() => {}} />,
    );
    expect(without).not.toContain("View all sessions");
    const withLink = renderToStaticMarkup(
      <ReviewActivity document={document} onSelectSection={() => {}} onViewSessions={() => {}} />,
    );
    expect(withLink).toContain("View all sessions");
  });
});

describe("review activity decisions", () => {
  it("marks an agent decision a person approved and leaves the others as they were", () => {
    const html = renderToStaticMarkup(
      <ReviewActivity document={fixture()} onSelectSection={() => {}} />,
    );
    expect(html).toContain("Agent · approved");
    expect(html).toContain('title="Proposed by the agent and approved by a person"');
    expect(html).toContain(">Human</span>");
    // An approved decision is still the agent's in the tally.
    expect(html).toContain("1 agent · 1 person");
  });

  it("does not mark a plain agent decision as approved", () => {
    const document = fixture();
    for (const section of document.sections)
      for (const block of section.blocks)
        if (block.kind === "decisions")
          for (const item of (block as Extract<Block, { kind: "decisions" }>).items)
            delete item.approved_by_human;
    const html = renderToStaticMarkup(
      <ReviewActivity document={document} onSelectSection={() => {}} />,
    );
    expect(html).not.toContain("Agent · approved");
    expect(html).not.toContain("Proposed by the agent and approved by a person");
    expect(html).toContain(">Agent</span>");
  });
});
