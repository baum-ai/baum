// @vitest-environment jsdom
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { reviewDocumentFor } from "../../test/review-document";
import type { Block, ReviewDocument } from "../review-document";
import { UrlStateProvider } from "../navigation";
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
    section.summary.text = "**Section** overview of `access`";
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
  it("links a quote to its session turn while preserving the review version", () => {
    const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
    document.index.sessions = [{ id: "run-1", vendor: "codex", title: null, restricted: false }];
    const section = {
      ...document.sections[0]!,
      risks: [],
      blocks: [
        {
          kind: "decisions" as const,
          items: [
            {
              id: "choice",
              by: "human" as const,
              title: "Skip assets",
              text: "Skip binary assets to keep the checkout small",
              evidence: "Leave binary files out",
              anchors: [{ type: "prompt" as const, session: "run-1", index: 8 }],
            },
          ],
        },
      ],
    };
    const html = renderToStaticMarkup(
      <UrlStateProvider
        value={{
          search: new URLSearchParams("pane=section&section=old&version=2"),
          update: () => {},
        }}
      >
        <ReviewSection document={document} section={section} onSelectSection={() => {}} />
      </UrlStateProvider>,
    );
    const page = new DOMParser().parseFromString(html, "text/html");
    const link = [...page.querySelectorAll("a")].find(
      (a) => a.textContent === "View in session →",
    )!;
    const query = new URLSearchParams(link.getAttribute("href")!.slice(1));
    expect(link.closest("blockquote")).toBeNull();
    expect(link.closest("details")).toBeNull();
    expect(page.querySelector('section[aria-label="Key decisions"] summary')).toBeNull();
    expect(page.body.textContent).toContain("Skip assets");
    expect(query.get("pane")).toBe("session");
    expect(query.get("session")).toBe("run-1");
    expect(query.get("turn")).toBe("8");
    expect(query.get("version")).toBe("2");
    expect(query.has("section")).toBe(false);
    expect(page.querySelector('section[aria-label="Risks"]')).toBeNull();
  });
  const anchors = [{ type: "hunk" as const, id: "h_picker" }];
  it("groups people and approved choices first, then the agent's own calls", () => {
    const html = render([
      {
        kind: "decisions",
        items: [
          { id: "dec_a", by: "agent", text: "Agent chose", anchors },
          { id: "dec_h", by: "human", text: "Person chose", anchors },
          { id: "dec_ok", by: "agent", text: "Person accepted", approved_by_human: true, anchors },
        ],
      },
    ]);
    const page = new DOMParser().parseFromString(html, "text/html");
    const part = page.querySelector('section[aria-label="Key decisions"]')!;
    const groups = [...part.querySelectorAll("ul")];
    expect(groups).toHaveLength(2);
    expect(groups[0]!.textContent).toContain("Person chose");
    expect(groups[0]!.textContent).toContain("Person accepted");
    expect(groups[0]!.textContent).not.toContain("Agent chose");
    expect(groups[1]!.textContent).toContain("Agent chose");
    expect(part.textContent).toContain("Made or approved by a person · 2");
    expect(part.textContent).toContain("Agent's own calls · 1");
    expect(part.querySelector("svg")).toBeNull();
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
          {
            id: "dec_own",
            by: "human",
            text: "Person chose unprompted",
            trigger: "initiative",
            anchors,
          },
        ],
      },
    ]);
    const page = new DOMParser().parseFromString(html, "text/html");
    const withContext = page.querySelector('[id$="-dec_ctx"]')!;
    const why = [...withContext.querySelectorAll("p")].find(
      (p) => p.textContent === "The upload test **flaked** twice",
    );
    expect(why!.textContent).toBe("The upload test **flaked** twice");
    expect(withContext.querySelector("details")!.hasAttribute("open")).toBe(false);
    expect(withContext.textContent).not.toContain("Instead of:");
    expect(withContext.querySelector("strong")).toBeNull();
    expect(withContext.querySelector("blockquote")!.textContent).toBe("retry with backoff instead");
    const bare = page.querySelector('[id$="-dec_bare"]')!;
    expect(bare.querySelector("blockquote")).toBeNull();
    expect(bare.textContent).not.toContain("Prompted");
    const own = page.querySelector('[id$="-dec_own"]')!;
    const ownWhy = [...own.querySelectorAll("p")].find((p) => p.textContent === "Own initiative");
    expect(ownWhy).toBeUndefined();
    expect(own.textContent).toContain("Person chose unprompted");
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

function attentionPage(
  refs: string[] | null,
  onlyAttentionRisk = false,
  tier: "high" | "medium" | "low" = "high",
) {
  const document = structuredClone(reviewDocumentFor("acme/widgets", 42)) as ReviewDocument;
  const section = document.sections[0]!;
  section.attention = refs === null ? null : { text: "Inspect these changes", refs };
  section.risks[0]!.tier = tier;
  if (onlyAttentionRisk) section.risks = section.risks.slice(0, 1);
  const html = renderToStaticMarkup(
    <ReviewSection
      document={document}
      section={section}
      onSelectSection={() => {}}
      idPrefix="old-"
    />,
  );
  return new DOMParser().parseFromString(html, "text/html");
}

describe("attention risks", () => {
  it.each(["medium", "low"] as const)(
    "keeps %s risks below decisions rather than in Attention",
    (tier) => {
      const found = attentionPage(["rsk_1"], true, tier);
      expect(found.querySelector('section[aria-label="Attention"]')).toBeNull();
      const risks = found.querySelector('section[aria-label="Risks"]')!;
      expect(risks.querySelector('[id$="-rsk_1"]')).not.toBeNull();
      const decisions = found.querySelector('section[aria-label="Key decisions"]')!;
      expect(decisions).not.toBeNull();
      expect(decisions.compareDocumentPosition(risks) & 4).toBeTruthy();
    },
  );

  it("shows risk text and evidence once in a flat attention callout", () => {
    const found = attentionPage(["rsk_1", "rsk_1"]);
    const attention = found.querySelector('section[aria-label="Attention"]')!;
    const risk = attention.querySelector('[id$="-rsk_1"]')!;
    expect(risk).not.toBeNull();
    expect(risk.id).toMatch(/^old-/);
    expect(risk.textContent).toContain("Repository names");
    expect(risk.textContent).not.toContain("Permissions");
    expect(risk.querySelector("details")).not.toBeNull();
    expect(attention.textContent).not.toContain("Inspect these changes");
    expect(risk.className).not.toContain("border");
    expect(found.querySelectorAll('[id$="-rsk_1"]')).toHaveLength(1);
    const other = found.querySelector('section[aria-label="Risks"]')!;
    expect(other.querySelector('[id$="-rsk_old"]')).not.toBeNull();
    expect(other.textContent).not.toContain("Repository names");
  });

  it("omits the bottom risk section when all risks are in attention", () => {
    const found = attentionPage(["rsk_1"], true);
    expect(found.querySelector('section[aria-label="Attention"] [id$="-rsk_1"]')).not.toBeNull();
    expect(found.querySelector('section[aria-label="Other risks"]')).toBeNull();
    expect(found.querySelector('section[aria-label="Risks"]')).toBeNull();
  });

  it.each([null, [], ["missing-reference"]])(
    "keeps Risks when attention contains no risks: %s",
    (refs) => {
      const found = attentionPage(refs);
      expect(found.querySelector('section[aria-label="Risks"] [id$="-rsk_1"]')).not.toBeNull();
      expect(found.querySelector('section[aria-label="Other risks"]')).toBeNull();
    },
  );
});
