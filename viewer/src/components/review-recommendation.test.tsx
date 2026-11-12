// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { reviewDocumentFor } from "../../test/review-document";
import {
  reviewExchangeFacts,
  reviewRecommendation,
  type Block,
  type Recommendation,
  type ReviewDocument,
} from "../review-document";
import { ReviewExchange } from "./review-exchange";
import { ReviewSection } from "./review-section";
import { ReviewSummary } from "./review-summary";
import { act } from "react";
import { createRoot } from "react-dom/client";

const documentWith = (recommendation?: Recommendation | null) => {
  const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  if (recommendation === undefined) delete document.summary.recommendation;
  else document.summary.recommendation = recommendation;
  return document;
};

const summaryHtml = (document: ReviewDocument) =>
  renderToStaticMarkup(<ReviewSummary document={document} onSelectSection={() => {}} />);

describe("review recommendation", () => {
  it("reads absent and null as no recommendation", () => {
    expect(reviewRecommendation(documentWith(undefined))).toBeNull();
    expect(reviewRecommendation(documentWith(null))).toBeNull();
  });

  it("shows nothing new for an older document or a null recommendation", () => {
    for (const document of [documentWith(undefined), documentWith(null)]) {
      const html = summaryHtml(document);
      expect(html).not.toContain('aria-label="Recommendation"');
      expect(html).not.toContain("Look first");
    }
  });

  it("shows the decision, its sentence and a chip for each known ref", () => {
    const html = summaryHtml(
      documentWith({
        decision: "look_first",
        text: "The shared link reads the repository from the URL.",
        refs: ["behavior", "rsk_old", "rsk_missing"],
      }),
    );
    expect(html).toContain('aria-label="Recommendation"');
    expect(html).toContain("Look first");
    expect(html).toContain("The shared link reads the repository from the URL.");
    expect(html).toContain('aria-label="Where to look"');
    // The section by its title, the risk by its text; an unknown ID is dropped.
    expect(html).toContain('title="Section behavior"');
    expect(html).toContain('title="Risk rsk_old"');
    expect(html).not.toContain("rsk_missing");
  });

  it("tones each decision and labels it in words", () => {
    const tone = (decision: Recommendation["decision"]) =>
      summaryHtml(documentWith({ decision, text: "Fine.", refs: [] }));
    expect(tone("approve")).toContain("Approve");
    expect(tone("approve")).toContain("text-(--status-positive)");
    expect(tone("look_first")).toContain("text-(--status-caution)");
    expect(tone("request_changes")).toContain("Request changes");
    expect(tone("request_changes")).toContain("text-(--status-critical)");
    // An approval with no refs draws no list.
    expect(tone("approve")).not.toContain('aria-label="Where to look"');
  });

  it("opens the section and item a chip names", () => {
    const onSelectSection = vi.fn();
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    act(() =>
      root.render(
        <ReviewSummary
          document={documentWith({
            decision: "request_changes",
            text: "One risk blocks.",
            refs: ["behavior", "rsk_old"],
          })}
          onSelectSection={onSelectSection}
        />,
      ),
    );
    const chips = [
      ...host.querySelectorAll<HTMLButtonElement>('[aria-label="Where to look"] button'),
    ];
    expect(chips).toHaveLength(2);
    act(() => chips[0]!.click());
    expect(onSelectSection).toHaveBeenLastCalledWith("behavior", undefined);
    act(() => chips[1]!.click());
    expect(onSelectSection).toHaveBeenLastCalledWith("overview", "rsk_old");
    act(() => root.unmount());
  });
});

const exchangeDocument = (text: string | null | undefined) => {
  const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  document.summary.review_exchange = {
    ...(text === undefined ? {} : { summary: text }),
    reviews: 1,
    back_and_forths: 0,
    critical_issues_found: 0,
    changes_after_open: null,
  };
  return document;
};

describe("review exchange without a model summary", () => {
  it("reads a null summary as no sentence", () => {
    expect(reviewExchangeFacts(exchangeDocument(null))?.summary).toBeNull();
    expect(reviewExchangeFacts(exchangeDocument("It went well."))?.summary).toBe("It went well.");
  });

  it("renders the figures and no model sentence", () => {
    const html = renderToStaticMarkup(
      <ReviewExchange document={exchangeDocument(null)} onSelectSection={() => {}} />,
    );
    expect(html).toContain("Review exchange");
    expect(html).not.toContain("It went well.");
    expect(
      renderToStaticMarkup(
        <ReviewExchange document={exchangeDocument("It went well.")} onSelectSection={() => {}} />,
      ),
    ).toContain("It went well.");
  });

  it("says no review tool assessed the pull request when nothing was exchanged", () => {
    const document = exchangeDocument(null);
    document.agent_reviews = [];
    document.index.comments = [];
    document.summary.review_exchange = {
      summary: null,
      reviews: 0,
      back_and_forths: 0,
      critical_issues_found: 0,
      changes_after_open: null,
    };
    expect(
      renderToStaticMarkup(<ReviewExchange document={document} onSelectSection={() => {}} />),
    ).toContain("No review tool assessed this pull request.");
  });
});

const sectionHtml = (mutate?: (document: ReviewDocument) => void) => {
  const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  mutate?.(document);
  return renderToStaticMarkup(
    <ReviewSection
      document={document}
      section={document.sections[0]!}
      onSelectSection={() => {}}
    />,
  );
};

describe("mitigated risks in a section", () => {
  it("holds a mitigated risk behind a disclosure, with its text and state", () => {
    const html = sectionHtml();
    const disclosure = html.indexOf("1 mitigated risk");
    expect(disclosure).toBeGreaterThan(-1);
    // The risk and its lifecycle badge sit inside the disclosure, after its summary.
    expect(html.slice(0, disclosure)).toContain("<details");
    expect(html.indexOf('id="overview-rsk_old"')).toBeGreaterThan(disclosure);
    expect(html.indexOf("Mitigated")).toBeGreaterThan(-1);
  });

  it("draws no disclosure when no risk is mitigated", () => {
    const html = sectionHtml((document) => {
      for (const risk of document.sections[0]!.risks) risk.state = "active";
    });
    expect(html).not.toContain("mitigated risk");
  });
});

describe("evidence items on the page", () => {
  it("gives evidence an element ID on the first step that cites it, once", () => {
    const html = sectionHtml((document) => {
      const behavior = document.sections[0]!.blocks.find((block) => block.kind === "behavior") as
        | Extract<Block, { kind: "behavior" }>
        | undefined;
      const first = behavior!.steps[0]!;
      first.evidence = ["ev_1"];
      behavior!.steps.push({ ...structuredClone(first), id: "beh_again" });
    });
    expect(html.match(/id="overview-ev_1"/g)).toHaveLength(1);
  });
});
