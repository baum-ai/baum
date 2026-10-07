// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { reviewDocumentFor } from "../../test/review-document";
import type { Block, Decision, ReviewDocument } from "../review-document";
import { ReviewSummary } from "./review-summary";
import { ReviewSection } from "./review-section";

const source = [{ type: "hunk" as const, id: "h_picker" }];

function render(blocks: Block[]) {
  const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  const section = { ...document.sections[0]!, blocks };
  return renderToStaticMarkup(
    <ReviewSection document={document} section={section} onSelectSection={() => {}} />,
  );
}

describe("review section blocks", () => {
  it("renders generated review prose as safe markdown in the summary and section", () => {
    const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
    const section = document.sections[0]!;
    document.summary.text = "**Summary** with [details](https://example.com/details)";
    section.summary.text = "**Section** overview";
    section.attention!.text = "Check `access` before release";
    section.risks[0]!.text = "- First risk\n- Second risk";
    const behavior = section.blocks.find((block) => block.kind === "behavior") as
      | Extract<Block, { kind: "behavior" }>
      | undefined;
    if (!behavior) throw new Error("behavior fixture missing");
    behavior.steps[0]!.text = "Use **one scope**. <script>alert(1)</script>";

    const summaryHtml = renderToStaticMarkup(
      <ReviewSummary document={document} onSelectSection={() => {}} />,
    );
    const sectionHtml = renderToStaticMarkup(
      <ReviewSection document={document} section={section} onSelectSection={() => {}} />,
    );

    expect(summaryHtml).toContain("<strong>Summary</strong>");
    expect(summaryHtml).toContain('href="https://example.com/details"');
    expect(sectionHtml).toContain("<strong>Section</strong>");
    expect(sectionHtml).toContain("<code");
    expect(sectionHtml).toContain("<li>First risk</li>");
    expect(sectionHtml).toContain("<strong>one scope</strong>");
    expect(sectionHtml).not.toContain("<script");
  });

  it("renders presentational blocks from their content and source anchors", () => {
    const html = render([
      { kind: "diagram", format: "mermaid", content: "graph TD; picker-->store", source },
      {
        kind: "code",
        language: "ts",
        content: "const picked = pick();",
        caption: "The new call",
        source,
      },
      { kind: "media", url: "https://example.com/shot.png", caption: "Screenshot", source },
      { kind: "repro", steps: ["Open the picker"], source },
      { kind: "table", columns: ["Before", "After"], rows: [["one", "two"]], source },
    ]);
    expect(html).toContain("Diagram (mermaid)");
    expect(html).toContain("graph TD; picker--&gt;store");
    expect(html).toContain("const picked = pick();");
    expect(html).toContain("The new call");
    expect(html).toContain("Screenshot");
    expect(html).toContain("Open the picker");
    expect(html).toContain("two");
  });

  it("renders included pull requests with their notes", () => {
    const html = render([
      { kind: "included_prs", items: [{ pr: 17, note: "Split out the store" }, { pr: 18 }] },
    ]);
    expect(html).toContain("Included pull requests");
    expect(html).toContain("#17");
    expect(html).toContain("#18");
    expect(html).toContain("Split out the store");
  });
});

/** The first mock section, with its tier and state set and, unless `keepRisks`,
 *  its risks removed. Its own risks are one active and one mitigated. */
function header(tier: "unrated" | "high", state: "new" | "stale", keepRisks = false) {
  const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  const base = document.sections[0]!;
  const section = {
    ...base,
    risks: keepRisks ? base.risks : [],
    derived: { ...base.derived, risk_tier: tier, state },
  };
  return renderToStaticMarkup(
    <ReviewSection document={document} section={section} onSelectSection={() => {}} />,
  );
}

describe("review section decisions", () => {
  const anchors = [{ type: "hunk" as const, id: "h_picker" }];
  const approval = "Proposed by the agent and approved by a person";

  /** Each decision's badge, found by the decision's item id rather than by
   *  how the badge is styled. */
  function badges(items: Decision[]) {
    const html = render([{ kind: "decisions", items }]);
    const page = new DOMParser().parseFromString(html, "text/html");
    const part = page.querySelector('section[aria-label="Key decisions"]')!;
    return Object.fromEntries(
      items.map((item) => [item.id, part.querySelector(`[id$="-${item.id}"] span`)!]),
    );
  }

  it("says who took each decision, and that a person approved an agent's", () => {
    const found = badges([
      { id: "dec_h", by: "human", text: "Person chose", anchors },
      { id: "dec_a", by: "agent", text: "Agent chose", anchors },
      {
        id: "dec_ok",
        by: "agent",
        text: "Agent proposed, person agreed",
        approved_by_human: true,
        anchors,
      },
    ]);
    expect(found.dec_h!.textContent).toBe("Human");
    expect(found.dec_a!.textContent).toBe("Agent");
    expect(found.dec_h!.getAttribute("title")).toBeNull();
    expect(found.dec_a!.getAttribute("title")).toBeNull();
    // The approval is in words and a tick, not only a colour; the tick is
    // decoration, so a screen reader hears the words alone.
    const approved = found.dec_ok!;
    expect(approved.textContent).toBe("Agent · approved");
    expect(approved.getAttribute("title")).toBe(approval);
    const tick = approved.querySelector("svg");
    expect(tick).not.toBeNull();
    expect(tick!.getAttribute("aria-hidden")).toBe("true");
  });

  // The schema allows the flag only on an agent decision, but an old or
  // hand-edited document can carry it on a person's own.
  it("keeps a person's decision plainly Human even if it carries the approval flag", () => {
    const found = badges([
      {
        id: "dec_bad",
        by: "human",
        text: "Person chose",
        approved_by_human: true,
        anchors,
      } as Decision,
    ]);
    expect(found.dec_bad!.textContent).toBe("Human");
    expect(found.dec_bad!.getAttribute("title")).toBeNull();
    expect(found.dec_bad!.querySelector("svg")).toBeNull();
  });

  it("shows what prompted a decision, why, and the session's words, as plain text", () => {
    const html = render([
      {
        kind: "decisions",
        items: [
          {
            id: "dec_ctx",
            by: "agent",
            text: "Retry with backoff",
            trigger: "failure",
            rationale: "The upload test **flaked** twice",
            evidence: "retry with backoff instead",
            anchors,
          },
          { id: "dec_bare", by: "human", text: "Person chose", anchors },
        ],
      },
    ]);
    const page = new DOMParser().parseFromString(html, "text/html");
    const withContext = page.querySelector('[id$="-dec_ctx"]')!;
    const why = [...withContext.querySelectorAll("p")].find((p) =>
      p.textContent!.startsWith("Prompted"),
    );
    expect(why!.textContent).toBe("Prompted by a failure: The upload test **flaked** twice");
    expect(withContext.querySelector("strong")).toBeNull();
    expect(withContext.querySelector("blockquote")!.textContent).toBe(
      "“retry with backoff instead”",
    );
    const bare = page.querySelector('[id$="-dec_bare"]')!;
    expect(bare.querySelector("blockquote")).toBeNull();
    expect(bare.textContent).not.toContain("Prompted");
  });
});

describe("review section tier", () => {
  it("says a section without any risk has none found, not that it is unrated", () => {
    const html = header("unrated", "new");
    expect(html).toContain("No risks found");
    expect(html).not.toContain("Unrated");
  });

  it("says a section whose risks are all mitigated has none open, not none found", () => {
    const html = header("unrated", "new", true);
    expect(html).toContain("No open risks");
    expect(html).not.toContain("No risks found");
  });

  it("gives an obsolete section no tier, since obsolete sections are stored unrated", () => {
    expect(header("unrated", "stale")).not.toContain("No risks found");
    expect(header("high", "new")).toContain("High risk");
  });

  it("shows the review score that ranked the section beside its tier", () => {
    expect(header("high", "new")).toContain("3 · Shared behavior or compatibility");
  });
});
