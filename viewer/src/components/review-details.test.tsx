import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { reviewDocumentFor } from "../../test/review-document";
import { reviewDecisions, type Block, type ReviewDocument } from "../review-document";
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

describe("author agents", () => {
  it("shows models below their harness, without model icons, and folds additional models", () => {
    const document = fixture();
    document.summary.authorship.observed_sessions = 2;
    document.summary.authorship.observed_vendors = ["claude_code", "codex"];
    document.summary.authorship.observed_models = ["claude-opus", "claude-sonnet", "gpt-6"];
    document.summary.authorship.anchors = [];
    document.index.sessions = [
      {
        id: "claude",
        restricted: false,
        vendor: "claude_code",
        models: ["claude-opus", "claude-sonnet"],
        title: null,
      },
      { id: "codex", vendor: "codex", models: ["gpt-6"], title: null, restricted: false },
    ];
    const html = renderToStaticMarkup(<ReviewDetails document={document} />);
    const author = html.slice(
      html.indexOf('aria-label="Author"'),
      html.indexOf('aria-label="Implements"'),
    );
    expect(author).toContain("claude-opus + 1");
    expect(author).toContain("gpt-6");
    expect(author).not.toContain(">claude-sonnet<");
    expect(author.match(/<svg/g)).toHaveLength(2);
    // Stack summaries have harness/model lists but no indexed sessions.
    document.index.sessions = [];
    const summaryOnly = renderToStaticMarkup(<ReviewDetails document={document} />);
    expect(summaryOnly).toContain("claude-opus + 1");
    expect(summaryOnly).not.toContain("claude-opus + 2");
    expect(summaryOnly).toMatch(/Codex<[^]*?text-xs[^]*?>gpt-6</);

    expect(author.indexOf("Claude Code")).toBeLessThan(author.indexOf("claude-opus"));
    expect(author.indexOf("Codex")).toBeLessThan(author.indexOf(">gpt-6<"));
  });
});

describe("review details without optional model lists", () => {
  it("renders a session without models and observed sessions without observed_models", () => {
    const document = fixture();
    const session = document.index.sessions.find((candidate) => !candidate.restricted)!;
    delete session.models;
    document.summary.authorship.observed_sessions = 1;
    delete document.summary.authorship.observed_models;
    expect(() => renderToStaticMarkup(<ReviewDetails document={document} />)).not.toThrow();
    expect(() =>
      renderToStaticMarkup(<ReviewActivity document={document} onSelectSection={() => {}} />),
    ).not.toThrow();
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

  it("draws decision navigation as safe inline text", () => {
    const document = fixture();
    const block = document.sections
      .flatMap((section) => section.blocks)
      .find((candidate) => candidate.kind === "decisions") as Extract<Block, { kind: "decisions" }>;
    block.items![0]!.text = "Use [the cache](https://example.com) <b>now</b>";
    block.items![0]!.rejected = "- A [list](https://example.com)";
    const html = renderToStaticMarkup(
      <ReviewActivity document={document} onSelectSection={() => {}} />,
    );
    const decisions = html.slice(html.indexOf('aria-label="Decisions"'));
    expect(decisions).toContain("Use the cache &lt;b&gt;now&lt;/b&gt;");
    expect(decisions).not.toContain("A list");
    expect(decisions).not.toContain('href="https://example.com"');
    // Each title holds no block and no control.
    for (const [, title] of decisions.matchAll(/<summary[^>]*>([^]*?)<\/summary>/g))
      expect(title).not.toMatch(/<(p|ul|a)[ >]/);
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
    return {
      number,
      document,
      href: `/pull/${number}`,
      tag: <a href={`/pull/${number}`}>#{number}</a>,
    };
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
    expect(html.indexOf('aria-label="Decisions"')).toBeLessThan(
      html.indexOf('aria-label="Linked sessions"'),
    );
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

  it("lists repeated session decisions once with only the first member destination", () => {
    const one = member(51);
    const two = member(52);
    const decision = reviewDecisions(one.document)[0]!;
    const html = render([one, two]);
    const decisions = html.slice(html.indexOf('aria-label="Decisions"'));
    expect(decisions.split(decision.title || decision.text).length - 1).toBe(1);
    expect(decisions).toContain("Open decision in PR #51");
    expect(decisions).not.toContain("Open decision in PR #52");
    expect(decisions).toContain("/pull/51?");
    expect(decisions).not.toContain("/pull/52?");
    // A matching short title alone does not establish the same decision.
    for (const section of two.document.sections) {
      for (const block of section.blocks) {
        if (block.kind === "decisions") {
          for (const item of (block as Extract<Block, { kind: "decisions" }>).items) {
            item.text += " A distinct choice.";
          }
        }
      }
    }
    const distinct = render([one, two]);
    expect(distinct.split("A distinct choice.").length - 1).toBeGreaterThan(0);
  });

  it("waits for the members' reviews before drawing their parts", () => {
    const html = render([member(51)], true);
    expect(html).toContain("Member note");
    expect(html).not.toContain('aria-label="Review exchange"');
  });

  it("uses compact decision links to the exact member version, section and decision", () => {
    const one = member(51);
    const decision = reviewDecisions(one.document)[0]!;
    const html = render([one]);
    const decisions = html.slice(html.indexOf('aria-label="Decisions"'));
    expect(decisions).not.toContain("<details");
    expect(decisions).not.toContain("Agent · approved");
    const encoded = [...decisions.matchAll(/href="([^"]+)"/g)]
      .map((match) => match[1]!)
      .find((href) => href.includes(`decision=${decision.id}`))!;
    const url = new URL(encoded.replaceAll("&amp;", "&"), "https://viewer.invalid");
    expect(url.pathname).toBe("/pull/51");
    expect(url.searchParams.get("section")).toBe(decision.section);
    expect(url.searchParams.get("decision")).toBe(decision.id);
    expect(url.searchParams.get("review")).toBe(String(one.document.review.version));
  });

  it("puts people's decisions before agent-only decisions across stack members", () => {
    const agent = member(51);
    const person = member(52);
    for (const entry of [agent, person]) {
      for (const section of entry.document.sections) {
        for (const block of section.blocks) {
          if (block.kind !== "decisions") continue;
          for (const decision of (block as Extract<Block, { kind: "decisions" }>).items) {
            decision.by = entry === person ? "human" : "agent";
            delete decision.approved_by_human;
            decision.text = entry === person ? "Person's choice" : "Agent's choice";
          }
        }
      }
    }
    const html = render([agent, person]);
    expect(html.indexOf("Made or approved by a person")).toBeLessThan(
      html.indexOf("Agent&#x27;s own calls"),
    );
    expect(html.indexOf("Person&#x27;s choice")).toBeLessThan(html.indexOf("Agent&#x27;s choice"));
  });
});

describe("review activity sessions", () => {
  it("puts decisions before linked sessions", () => {
    const html = renderToStaticMarkup(
      <ReviewActivity document={fixture()} onSelectSection={() => {}} />,
    );
    expect(html.indexOf('aria-label="Decisions"')).toBeLessThan(
      html.indexOf('aria-label="Linked sessions"'),
    );
  });

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
  it("uses a short title for decision navigation and falls back for older reviews", () => {
    const document = fixture();
    const block = document.sections
      .flatMap((section) => section.blocks)
      .find((candidate) => candidate.kind === "decisions") as Extract<Block, { kind: "decisions" }>;
    block.items[0]!.title = "Keep the picker beside activity";
    const full = block.items[0]!.text;
    const html = renderToStaticMarkup(
      <ReviewActivity document={document} onSelectSection={() => {}} />,
    );
    const rows = [...html.matchAll(/<button[^>]*>([^]*?)<\/button>/g)];
    const row = rows.find((match) => match[1]!.includes("Keep the picker beside activity"))![1]!;
    expect(row).toContain("Keep the picker beside activity");
    expect(row).not.toContain(full);
    delete block.items[0]!.title;
    const legacy = renderToStaticMarkup(
      <ReviewActivity document={document} onSelectSection={() => {}} />,
    );
    expect(legacy).toContain(full);
  });
  it("marks an agent decision a person approved and leaves the others as they were", () => {
    const html = renderToStaticMarkup(
      <ReviewActivity document={fixture()} onSelectSection={() => {}} />,
    );
    expect(html).not.toContain(">Human</span>");
    expect(html).not.toContain(">Agent</span>");
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
    expect(html).toContain("Agent&#x27;s own calls · 1");
    expect(html.indexOf("Made or approved by a person")).toBeLessThan(
      html.indexOf("Agent&#x27;s own calls"),
    );
  });
});
