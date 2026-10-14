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

describe("review activity", () => {
  it("leads with the checks, then the review exchange", () => {
    const html = renderToStaticMarkup(
      <ReviewActivity document={fixture()} onSelectSection={() => {}} />,
    );
    const checks = html.indexOf('aria-label="Checks"');
    expect(checks).toBeGreaterThanOrEqual(0);
    expect(checks).toBeLessThan(html.indexOf('aria-label="Review exchange"'));
  });

  it("leaves the checks out when the host says so", () => {
    const html = renderToStaticMarkup(
      <ReviewActivity document={fixture()} checks={null} onSelectSection={() => {}} />,
    );
    expect(html).not.toContain('aria-label="Checks"');
  });
});

describe("a stack's review activity", () => {
  const member = (number: number) => {
    const document = fixture();
    document.review.pull_request = number;
    // One thread open and one resolved in each member.
    document.review.sources = { ...document.review.sources, review_threads: true };
    document.index.threads = [
      { id: `t${number}a`, path: `web/${number}.ts`, line: 3, resolved: false },
      { id: `t${number}b`, path: `web/${number}.ts`, line: 9, resolved: true },
    ];
    return { number, document, tag: <a href={`/pull/${number}`}>#{number}</a> };
  };
  const render = (members: ReturnType<typeof member>[], loading = false) =>
    renderToStaticMarkup(
      <ReviewActivity
        document={fixture()}
        checks={null}
        members={{ members, loading, note: <p>Member note</p> }}
        onSelectSection={() => {}}
      />,
    );

  it("gathers the members' threads, sessions and decisions, each tagged with its member", () => {
    const one = fixture();
    const html = render([member(51), member(52)]);
    expect(html).toContain("Member note");
    expect(html).toContain("2 unresolved threads");
    expect(html).toContain("of 4");
    expect(html).toContain("web/51.ts:3");
    expect(html).toContain("web/52.ts:3");
    // Each member's run of turns is read on its own page.
    expect(html).not.toContain("Full exchange");
    // The two members list the same sessions, so each is listed once.
    const sessions = html.slice(html.indexOf('aria-label="Linked sessions"'));
    expect(sessions.slice(0, sessions.indexOf("</section>")).split("<li").length - 1).toBe(
      one.index.sessions.length,
    );
    expect(html).toContain('href="/pull/51"');
    expect(html).toContain('href="/pull/52"');
  });

  it("waits for the members' reviews before drawing their parts", () => {
    const html = render([member(51)], true);
    expect(html).toContain("Member note");
    expect(html).not.toContain('aria-label="Review exchange"');
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
    // A decision a person approved is settled, so it is listed with theirs
    // rather than with the calls still to check, and still names the agent.
    expect(html).toContain("Made or approved by a person · 2");
    expect(html).not.toContain("own calls");
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
