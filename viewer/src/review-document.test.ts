import { describe, expect, it } from "vitest";
import { sha256Hex } from "./sha256";

import {
  pullRequestStateTone,
  riskLabel,
  type Anchor,
  type Block,
  behaviourChanges,
  checkStatus,
  evidenceTone,
  formatLines,
  humanize,
  type Implements,
  implementsGroups,
  intentPrompts,
  livePullRequestState,
  normalizeReviewDocument,
  type FindingSeverity,
  type ReviewDocument,
  type RiskScore,
  type Risk,
  type RiskTier,
  repositoryLink,
  resolveAnchor,
  reviewDecisions,
  reviewExchange,
  reviewExchangeFacts,
  reviewThreadStatus,
  type ReviewExchangeFacts,
  combineExchangeFacts,
  riskEvidenceReviewVersion,
  riskLifecycleLabel,
  riskLifecycleTone,
  sectionBadgeState,
  riskSegments,
  type Section,
  sectionFindings,
  sectionHunks,
  sectionsInReviewOrder,
  sectionStateLabel,
  summaryIntent,
  tierTone,
  triageRisks,
  riskScoreTone,
  riskScoreView,
  type TriageRisk,
} from "./review-document";

const document = {
  schema: "baum.review/1",
  review: {
    repository: "acme/widgets",
    pull_request: 42,
    pr_state: "open",
    head: "a".repeat(40),
    base: "b".repeat(40),
    merge_base: "b".repeat(40),
    merged_commit: null,
    version: 1,
    previous_version: null,
    generation_state: "current",
    generated_at: "2026-09-14T15:03:12Z",
    evidence_refreshed_at: "2026-09-14T16:52:00Z",
    generator: { model: "m", review_md: null, schema_revision: "r", truncated: [] },
    sources: {},
    relations: [],
  },
  summary: {
    title: "t",
    text: "",
    change_kind: "feature",
    implements: [],
    release_note: null,
    breaking_change: null,
    attention: null,
    author: { login: "alex", association: "MEMBER", bot: false },
    authorship: {
      declared: "none",
      declared_tools: [],
      anchors: [],
      observed_sessions: 0,
      observed_vendors: [],
      observed_models: [],
    },
  },
  stats: {
    sections: 1,
    files: 2,
    hunks: 3,
    additions: 10,
    deletions: 2,
    risk_loc: { critical: 0, high: 5, medium: 0, low: 1, unrated: 4, mechanical: 0 },
    role_loc: {},
    checks: { required_failed: 0, optional_failed: 0, pending: 0, skipped: 0 },
    agent_findings: { open: 0, resolved: 0, dismissed: 0 },
  },
  sections: [],
  agent_reviews: [
    {
      id: "ar_1",
      tool: "cursor",
      assessed_head: "c".repeat(40),
      stale: true,
      verdict: "approve",
      blocked_on: null,
      counts: { must_fix: 0, should_fix: 0, consider: 2 },
      anchors: [],
      findings: [
        {
          id: "f_1",
          severity: "consider",
          text: "in a",
          section: "sec_a",
          anchors: [],
          state: "dismissed",
          addressed_by: null,
        },
        {
          id: "f_2",
          severity: "consider",
          text: "in b",
          section: "sec_b",
          anchors: [],
          state: "open",
          addressed_by: null,
        },
      ],
    },
    {
      id: "ar_2",
      tool: "codex",
      assessed_head: "a".repeat(40),
      stale: false,
      verdict: "comment",
      blocked_on: null,
      counts: { must_fix: 0, should_fix: 1, consider: 0 },
      anchors: [],
      findings: [
        {
          id: "f_3",
          severity: "should_fix",
          text: "also a",
          section: "sec_a",
          anchors: [],
          state: "resolved",
          addressed_by: "d".repeat(10),
        },
      ],
    },
  ],
  unassigned_hunks: [],
  index: {
    hunks: [
      {
        id: "h_1",
        path: "pkg/a.go",
        new_range: [1, 20],
        additions: 8,
        deletions: 1,
        change: "modified",
        role: "code",
        github_url: "https://github.com/acme/widgets/pull/42/files#h1",
      },
      {
        id: "h_2",
        path: "pkg/a.go",
        new_range: [40, 50],
        additions: 2,
        deletions: 1,
        change: "modified",
        role: "code",
        github_url: "https://github.com/acme/widgets/pull/42/files#h2",
      },
      {
        id: "h_3",
        path: "pkg/a_test.go",
        new_range: [1, 30],
        additions: 30,
        deletions: 0,
        change: "added",
        role: "test",
        github_url: "https://github.com/acme/widgets/pull/42/files#h3",
      },
    ],
    checks: [
      {
        id: "chk_1",
        kind: "check_run",
        name: "Platform",
        conclusion: "success",
        required: true,
        head: "a".repeat(40),
        url: "https://ci/1",
      },
    ],
    reports: [],
    comments: [
      {
        id: 7,
        kind: "review",
        author: "cursor",
        bot: true,
        created_at: "2026-09-14T12:39:35Z",
        url: "https://github.com/acme/widgets/pull/42#pullrequestreview-7",
      },
    ],
    threads: [
      {
        id: "T1",
        path: "pkg/a.go",
        line: 12,
        resolved: true,
        outdated: false,
        url: "https://github.com/acme/widgets/pull/42#discussion_r1",
      },
    ],
    sessions: [
      {
        id: "s1",
        vendor: "codex",
        models: ["m"],
        title: null,
        restricted: false,
        prompts: [
          { index: 3, id: "p3", at: "2026-09-14T11:31:20Z", text: "test against real data" },
        ],
        events: [
          { id: "e1", at: "2026-09-14T11:32:43Z", verb: "execute", summary: "Ran migrations" },
        ],
      },
    ],
    prs: [],
  },
} satisfies ReviewDocument;

describe("resolveAnchor", () => {
  const resolve = (anchor: Anchor) => resolveAnchor(document, anchor);

  it("links a hunk to its GitHub position and names the lines", () => {
    // The fixture's `#h1` is not an anchor GitHub knows, so the link is rebuilt
    // the way GitHub names a file on "Files changed": diff-<sha256(path)>.
    expect(resolve({ type: "hunk", id: "h_1", lines: [3, 9] })).toMatchObject({
      label: "pkg/a.go · lines 3–9",
      href: `https://github.com/acme/widgets/pull/42/files#diff-${sha256Hex("pkg/a.go")}R${document.index.hunks[0]!.new_range[0]}`,
      title: "Modified code hunk, +8 −1",
    });
  });

  it("names an external link without a label by its URL", () => {
    expect(resolve({ type: "external_url", url: "https://example.com/x" }).label).toBe(
      "https://example.com/x",
    );
  });

  it("keeps a supplied GitHub diff anchor on this pull request as it is", () => {
    const doc = structuredClone(document);
    const anchor = `#diff-${sha256Hex("pkg/a.go")}R12`;
    doc.index.hunks[0]!.github_url = `https://github.com/acme/widgets/pull/42/files${anchor}`;
    expect(resolveAnchor(doc, { type: "hunk", id: "h_1" }).href).toBe(
      `https://github.com/acme/widgets/pull/42/files${anchor}`,
    );
  });

  it("never links into another repository", () => {
    const doc = structuredClone(document);
    doc.index.hunks[0]!.github_url = "https://github.com/other/repo/pull/1/files#diff-x";
    doc.index.comments[0]!.url = "https://github.com/other/repo/pull/1#issuecomment-1";
    expect(resolveAnchor(doc, { type: "hunk", id: "h_1" }).href).toMatch(
      /^https:\/\/github\.com\/acme\/widgets\/pull\/42\/files#diff-[0-9a-f]{64}/,
    );
    const comment = resolveAnchor(doc, { type: "pr_comment", id: doc.index.comments[0]!.id });
    expect(comment.href).toBeUndefined();
    expect(repositoryLink(doc, "https://ci.example.com/run/1")).toBe(
      "https://ci.example.com/run/1",
    );
    expect(repositoryLink(doc, "mailto:someone@example.com")).toBeUndefined();
  });

  it("holds external links and attachments to the outbound-link rules", () => {
    const external = (url: string) => resolve({ type: "external_url", url, label: "Docs" }).href;
    expect(external("https://docs.example.com/a")).toBe("https://docs.example.com/a");
    expect(external("https://github.com/acme/widgets/issues/7")).toBe(
      "https://github.com/acme/widgets/issues/7",
    );
    expect(external("https://github.com/other/repo/issues/7")).toBeUndefined();
    expect(external(["javascript", "alert(1)"].join(":"))).toBeUndefined();
    expect(external("data:text/html,<p>hi</p>")).toBeUndefined();
    expect(external("not a url")).toBeUndefined();
    expect(resolve({ type: "attachment", url: "data:image/png;base64,AAAA" }).href).toBeUndefined();
    expect(resolve({ type: "attachment", url: "https://files.example.com/a.png" }).href).toBe(
      "https://files.example.com/a.png",
    );
  });

  it("keeps GitHub's uploaded attachments, which belong to no repository", () => {
    const asset = "https://github.com/user-attachments/assets/0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0";
    const file = "https://github.com/user-attachments/files/123/trace.log";
    expect(resolve({ type: "attachment", url: asset }).href).toBe(asset);
    expect(resolve({ type: "external_url", url: file, label: "Trace" }).href).toBe(file);
    expect(repositoryLink(document, "https://github.com/user-attachments/other/1")).toBeUndefined();
    expect(repositoryLink(document, "https://github.com/user-attachments/assets")).toBeUndefined();
  });

  it("keeps an unresolved hunk visible by its identifier", () => {
    expect(resolve({ type: "hunk", id: "h_missing" })).toEqual({
      label: "Hunk h_missing",
      kind: "Unresolved hunk",
    });
  });

  it("resolves index-backed anchors to their pages", () => {
    expect(resolve({ type: "check", id: "chk_1", test: "TestX" })).toMatchObject({
      label: "Platform › TestX",
      href: "https://ci/1",
      title: "Success, required",
    });
    expect(resolve({ type: "pr_comment", id: 7 })).toMatchObject({
      label: "cursor review",
      title: "Bot comment",
    });
    expect(resolve({ type: "review_thread", id: "T1" })).toMatchObject({
      label: "Thread on pkg/a.go:12 · resolved",
    });
    expect(resolve({ type: "pr", number: 9 }).href).toBe("https://github.com/acme/widgets/pull/9");
    expect(resolve({ type: "pr_body", section: "Validation" })).toMatchObject({
      label: "Description § Validation",
      href: "https://github.com/acme/widgets/pull/42",
    });
  });

  it("carries session content in the tooltip rather than the chip", () => {
    expect(resolve({ type: "prompt", session: "s1", index: 3 })).toEqual({
      label: "Prompt 3",
      title: "test against real data",
      kind: "codex session prompt",
    });
    expect(resolve({ type: "session_event", session: "s1", event: "e1" }).label).toBe(
      "Ran migrations",
    );
  });

  it("shows issue keys and spec assertions as text without inventing URLs", () => {
    expect(resolve({ type: "issue", system: "linear", key: "WEB-14" })).toEqual({
      label: "WEB-14",
      title: "Linear issue",
      kind: "Issue",
    });
    expect(resolve({ type: "spec", page: "REQ-117", assertion: "REQ-117-A01" }).label).toBe(
      "REQ-117-A01",
    );
    expect(resolve({ type: "spec", page: "REQ-117" }).label).toBe("REQ-117");
  });

  it("renders an anchor type it does not know instead of failing", () => {
    expect(resolve({ type: "hologram", id: "x" } as unknown as Anchor)).toEqual({
      label: "Hologram",
      kind: "Unsupported anchor",
    });
  });
});

describe("riskSegments", () => {
  it("orders tiers by severity, drops empty tiers and shares the total", () => {
    const segments = riskSegments(document.stats.risk_loc);
    expect(segments.map((segment) => segment.tier)).toEqual(["high", "low", "unrated"]);
    expect(segments.reduce((sum, segment) => sum + segment.share, 0)).toBeCloseTo(1);
    expect(segments[0]).toEqual({ tier: "high", lines: 5, share: 0.5 });
  });

  it("is empty when nothing was measured", () => {
    expect(
      riskSegments({ critical: 0, high: 0, medium: 0, low: 0, unrated: 0, mechanical: 0 }),
    ).toEqual([]);
  });
});

describe("sectionHunks", () => {
  it("groups a section's hunks by file with totals and ignores unknown ids", () => {
    const groups = sectionHunks(document, ["h_3", "h_1", "h_2", "h_unknown"]);
    expect(groups.map((group) => group.path)).toEqual(["pkg/a.go", "pkg/a_test.go"]);
    expect(groups[0]).toMatchObject({ additions: 10, deletions: 2 });
    expect(groups[0]!.hunks.map((hunk) => hunk.id)).toEqual(["h_1", "h_2"]);
  });
});

const section = (id: string, title: string, blocks: Block[]): Section => ({
  id,
  title,
  kind: "logical",
  hunks: [],
  summary: { text: "", anchors: [] },
  attention: null,
  risks: [],
  evidence: [],
  blocks,
  derived: {
    risk_tier: "low",
    additions: 0,
    deletions: 0,
    role_loc: {},
    reviewers: [],
    state: "new",
    evidence_summary: {},
    open_findings: 0,
  },
});

const prompt = (session: string, index: number): Anchor => ({ type: "prompt", session, index });

const intended: ReviewDocument = {
  ...document,
  sections: [
    section("sec_a", "Retry policy and backoff", [
      { kind: "intent", text: "Retry failed jobs with backoff.", anchors: [prompt("s1", 3)] },
      { kind: "behavior", steps: [] },
    ]),
    section("sec_b", "Dead-letter store", [
      {
        kind: "intent",
        text: "Bury exhausted jobs in one step.",
        anchors: [prompt("s2", 1), prompt("s1", 3)],
      },
    ]),
    section("sec_c", "Mechanical changes", [{ kind: "entities", items: [] }]),
    section("sec_d", "Empty intent", [{ kind: "intent", text: "", anchors: [] }]),
  ],
  index: {
    ...document.index,
    sessions: [
      ...document.index.sessions,
      { id: "s2", vendor: "claude", models: ["m2"], title: null, restricted: false },
    ],
  },
};

describe("summaryIntent", () => {
  it("collects section intent in section order and keeps each line attributable", () => {
    expect(
      summaryIntent(intended).map(({ section: id, title, text }) => ({ id, title, text })),
    ).toEqual([
      { id: "sec_a", title: "Retry policy and backoff", text: "Retry failed jobs with backoff." },
      { id: "sec_b", title: "Dead-letter store", text: "Bury exhausted jobs in one step." },
    ]);
  });

  it("is empty when no section argued an intent", () => {
    expect(summaryIntent(document)).toEqual([]);
  });
});

describe("intentPrompts", () => {
  it("de-duplicates cited prompts and orders them by session then prompt", () => {
    expect(intentPrompts(intended, summaryIntent(intended))).toEqual([
      {
        id: "s1:3",
        session: "s1",
        vendor: "codex",
        index: 3,
        text: "test against real data",
        at: "2026-09-14T11:31:20Z",
      },
      { id: "s2:1", session: "s2", vendor: "claude", index: 1, text: null },
    ]);
  });

  it("cites nothing when the intent carries no prompt anchor", () => {
    const lines = summaryIntent({
      ...intended,
      sections: [section("sec_a", "A", [{ kind: "intent", text: "why", anchors: [] }])],
    });
    expect(intentPrompts(intended, lines)).toEqual([]);
  });
});

const behaviourStep = (id: string) => ({ id, text: id, anchors: [], evidence: [] });

const risk = (id: string, tier: RiskTier): Risk => ({
  id,
  tier,
  category: "concurrency",
  source: "model",
  text: id,
  anchors: [],
});

describe("checkStatus", () => {
  const withChecks = (conclusions: [string, boolean][]): ReviewDocument => ({
    ...document,
    index: {
      ...document.index,
      checks: conclusions.map(([conclusion, required], position) => ({
        ...document.index.checks[0]!,
        id: `chk_${position}`,
        name: `Check ${position}`,
        conclusion: conclusion as "success",
        required,
      })),
    },
  });

  it("is green only when every check settled successfully", () => {
    expect(
      checkStatus(
        withChecks([
          ["success", true],
          ["success", false],
        ]),
      ),
    ).toMatchObject({ state: "passing", passed: 2, total: 2, blocking: false });
  });

  it("counts a cancelled check as failed, because it will never finish", () => {
    const status = checkStatus(
      withChecks([
        ["cancelled", false],
        ["success", true],
      ]),
    );
    expect(status.state).toBe("failing");
    expect(status.failed.map((check) => check.conclusion)).toEqual(["cancelled"]);
  });

  it("puts required failures first and reports that they block", () => {
    const status = checkStatus(
      withChecks([
        ["failure", false],
        ["failure", true],
        ["pending", false],
      ]),
    );
    expect(status.state).toBe("failing");
    expect(status.failed.map((check) => check.required)).toEqual([true, false]);
    expect(status.blocking).toBe(true);
    expect(status.pending).toBe(1);
  });

  it("is pending while something is still running and nothing has failed", () => {
    expect(
      checkStatus(
        withChecks([
          ["pending", true],
          ["success", false],
        ]),
      ).state,
    ).toBe("pending");
  });

  it("distinguishes a head nothing ran on", () => {
    expect(checkStatus(withChecks([])).state).toBe("none");
  });

  // A refresh may leave an earlier revision's runs in the index; one of those
  // must not paint this head red, nor green.
  it("counts only the checks that ran on the head under review", () => {
    const mixed: ReviewDocument = {
      ...document,
      index: {
        ...document.index,
        checks: [
          { ...document.index.checks[0]!, id: "old", conclusion: "failure", head: "z".repeat(40) },
          { ...document.index.checks[0]!, id: "now", conclusion: "success" },
        ],
      },
    };
    expect(checkStatus(mixed)).toMatchObject({ state: "passing", passed: 1, total: 1 });
  });

  it("does not call a head green when nothing on it succeeded", () => {
    const status = checkStatus(
      withChecks([
        ["skipped", false],
        ["neutral", false],
      ]),
    );
    expect(status).toMatchObject({ state: "inconclusive", passed: 0, skipped: 2, total: 2 });
  });

  it("still passes when something succeeded beside a skip, and counts the skip", () => {
    expect(
      checkStatus(
        withChecks([
          ["success", true],
          ["skipped", false],
        ]),
      ),
    ).toMatchObject({ state: "passing", passed: 1, skipped: 1, total: 2 });
  });
});

describe("behaviourChanges", () => {
  const stated = (
    state: "new" | "changed" | "unchanged",
    ids: string[],
    sectionId: string,
  ): Section => ({
    ...section(sectionId, sectionId.toUpperCase(), [
      { kind: "behavior" as const, steps: ids.map(behaviourStep) },
    ]),
    derived: { ...section(sectionId, "x", []).derived, state },
  });

  const changed: ReviewDocument = {
    ...document,
    sections: [
      stated("unchanged", ["old_1", "old_2"], "sec_old"),
      stated("changed", ["b_1", "b_2"], "sec_changed"),
      stated("new", ["b_3"], "sec_new"),
    ],
  };

  it("gives one block per touched section and leaves untouched ones out", () => {
    expect(
      behaviourChanges(changed).map((block) => [block.section, block.steps.map((s) => s.id)]),
    ).toEqual([
      ["sec_changed", ["b_1", "b_2"]],
      ["sec_new", ["b_3"]],
    ]);
  });

  it("caps each block on its own so one section cannot crowd out the next", () => {
    expect(behaviourChanges(changed, 1).map((block) => [block.steps.length, block.more])).toEqual([
      [1, 1],
      [1, 0],
    ]);
  });

  it("skips a touched section that states no behaviour", () => {
    expect(
      behaviourChanges({ ...changed, sections: [stated("changed", [], "sec_quiet")] }),
    ).toEqual([]);
  });

  it("tags the surfaces the section changes and never invents an audience", () => {
    const withEntities: ReviewDocument = {
      ...document,
      sections: [
        {
          ...stated("changed", ["b_1"], "sec_api"),
          blocks: [
            { kind: "behavior", steps: [behaviourStep("b_1")] },
            {
              kind: "entities",
              items: [
                { kind: "endpoint", name: "GET /x", change: "added", hunks: [] },
                { kind: "function", name: "helper", change: "modified", hunks: [] },
                { kind: "endpoint", name: "POST /x", change: "added", hunks: [] },
                { kind: "migration", name: "000028", change: "added", hunks: [] },
              ],
            },
          ],
        },
      ],
    };
    // "function" names nothing outside the change, so it earns no tag, and a
    // surface named twice is one tag.
    expect(behaviourChanges(withEntities)[0]!.surfaces).toEqual(["Endpoint", "Migration"]);
  });

  it("is empty when nothing changed, rather than falling back to everything", () => {
    expect(
      behaviourChanges({ ...changed, sections: [stated("unchanged", ["old_1"], "sec_old")] }),
    ).toEqual([]);
  });

  // The union carries a catch-all member, so a block can hold a known kind
  // with no payload. The file's own rule is that such a block renders as
  // nothing rather than failing the page.
  it("survives a block with a known kind and no payload", () => {
    const malformed = {
      ...document,
      sections: [
        {
          ...section("sec_a", "A", [{ kind: "behavior" }, { kind: "entities" }]),
          derived: { ...section("sec_a", "A", []).derived, state: "changed" as const },
        },
      ],
    } as unknown as ReviewDocument;
    expect(() => behaviourChanges(malformed)).not.toThrow();
    expect(behaviourChanges(malformed)).toEqual([]);
  });
});

describe("triageRisks", () => {
  const risky: ReviewDocument = {
    ...document,
    sections: [
      {
        ...section("sec_a", "Retry policy and backoff", []),
        risks: [risk("high_a", "high"), risk("medium_a", "medium")],
        derived: { ...section("sec_a", "A", []).derived, state: "changed" },
      },
      {
        ...section("sec_b", "Operator endpoints", []),
        risks: [risk("low_b", "low"), risk("critical_b", "critical")],
        derived: { ...section("sec_b", "B", []).derived, state: "unchanged" },
      },
    ],
  };

  it("leads with the must-read tiers across sections, critical first", () => {
    expect(triageRisks(risky).lead.map((item) => [item.id, item.section])).toEqual([
      ["critical_b", "sec_b"],
      ["high_a", "sec_a"],
    ]);
  });

  it("holds the lower tiers back in tier order rather than discarding them", () => {
    expect(triageRisks(risky).rest.map((item) => [item.id, item.tier])).toEqual([
      ["medium_a", "medium"],
      ["low_b", "low"],
    ]);
  });

  it("carries the state of the section a risk came from", () => {
    expect(triageRisks(risky).lead.map((item) => [item.id, item.sectionState])).toEqual([
      ["critical_b", "unchanged"],
      ["high_a", "changed"],
    ]);
  });

  it("has no lead when nothing reached the must-read tiers", () => {
    const quiet = {
      ...risky,
      sections: [{ ...section("sec_c", "C", []), risks: [risk("low_c", "low")] }],
    };
    expect(triageRisks(quiet).lead).toEqual([]);
    expect(triageRisks(quiet).rest.map((item) => item.id)).toEqual(["low_c"]);
  });

  it("excludes mitigated risks and every risk in obsolete sections", () => {
    const historical = {
      ...risky,
      sections: [
        {
          ...risky.sections[0]!,
          risks: [
            {
              ...risk("active", "high"),
              state: "active" as const,
              introduced_version: 1,
              mitigated_version: null,
            },
            {
              ...risk("mitigated", "critical"),
              state: "mitigated" as const,
              introduced_version: 1,
              mitigated_version: 2,
            },
          ],
        },
        {
          ...risky.sections[1]!,
          derived: { ...risky.sections[1]!.derived, state: "stale" as const },
        },
      ],
    };
    expect(triageRisks(historical).lead.map((item) => item.id)).toEqual(["active"]);
    expect(triageRisks(historical).rest).toEqual([]);
  });
});

describe("normalizeReviewDocument", () => {
  it("drops a risk repeated by id within its own section", () => {
    const duplicated: ReviewDocument = {
      ...document,
      sections: [
        {
          ...section("sec_a", "A", []),
          risks: [risk("rsk_1", "high"), risk("rsk_1", "high")],
        },
        { ...section("sec_b", "B", []), risks: [risk("rsk_1", "medium")] },
      ],
    };
    const normalized = normalizeReviewDocument(duplicated);
    expect(normalized.sections[0]!.risks.map((item) => item.id)).toEqual(["rsk_1"]);
    // The same id in a different section is a different risk, not a repeat.
    expect(normalized.sections[1]!.risks.map((item) => item.id)).toEqual(["rsk_1"]);
  });

  it("leaves a document with no repeats untouched", () => {
    const clean: ReviewDocument = {
      ...document,
      sections: [{ ...section("sec_a", "A", []), risks: [risk("rsk_1", "high")] }],
    };
    expect(normalizeReviewDocument(clean)).toEqual(clean);
  });

  it("labels a section stored with a blank title by its position", () => {
    const blank: ReviewDocument = {
      ...document,
      sections: [section("sec_a", "A", []), section("sec_b", "", []), section("sec_c", " \t", [])],
    };
    expect(normalizeReviewDocument(blank).sections.map((item) => item.title)).toEqual([
      "A",
      "Section 2",
      "Section 3",
    ]);
  });
});

describe("review history labels", () => {
  it("uses product language for obsolete sections and risk lifecycle", () => {
    expect(sectionStateLabel("stale")).toBe("Obsolete");
    expect(
      riskLifecycleLabel(
        {
          ...risk("new", "high"),
          state: "active",
          introduced_version: 2,
          mitigated_version: null,
        },
        { version: 2, previous_version: 1 },
        "unchanged",
      ),
    ).toBe("New");
    expect(
      riskLifecycleLabel(
        {
          ...risk("old", "low"),
          state: "active",
          introduced_version: 1,
          mitigated_version: null,
        },
        { version: 2, previous_version: 1 },
        "unchanged",
      ),
    ).toBe("Since v1");
    expect(
      riskLifecycleLabel(
        {
          ...risk("done", "medium"),
          state: "mitigated",
          introduced_version: 1,
          mitigated_version: 2,
        },
        { version: 2, previous_version: 1 },
        "unchanged",
      ),
    ).toBe("Mitigated in v2");
    expect(
      riskLifecycleLabel(risk("legacy", "low"), { version: 2, previous_version: 1 }, "changed"),
    ).toBeNull();
  });

  it("names every risk of an obsolete section obsolete, not mitigated", () => {
    const review = { version: 3, previous_version: 2 };
    const active = {
      ...risk("kept", "high"),
      state: "active" as const,
      introduced_version: 1,
      mitigated_version: null,
    };
    const mitigated = {
      ...risk("done", "medium"),
      state: "mitigated" as const,
      introduced_version: 1,
      mitigated_version: 2,
    };
    expect(riskLifecycleLabel(active, review, "stale")).toBe("Obsolete");
    expect(riskLifecycleLabel(mitigated, review, "stale")).toBe("Obsolete");
    expect(riskLifecycleLabel(risk("legacy", "low"), review, "stale")).toBe("Obsolete");
    expect(riskLifecycleTone(mitigated, "stale")).toBe("neutral");
    expect(riskLifecycleTone(mitigated, "changed")).toBe("positive");
    expect(riskLifecycleLabel(active, review, "changed")).toBe("Since v1");
  });

  it("leaves new off the first review, where everything is new", () => {
    const first = { version: 1, previous_version: null };
    expect(
      riskLifecycleLabel(
        { ...risk("new", "high"), state: "active", introduced_version: 1, mitigated_version: null },
        first,
        "new",
      ),
    ).toBeNull();
    expect(sectionBadgeState("new", first)).toBe("unchanged");
    expect(sectionBadgeState("new", { previous_version: 1 })).toBe("new");
    expect(sectionBadgeState("changed", { previous_version: 1 })).toBe("changed");
  });

  it("resolves mitigated risk evidence to the last active review", () => {
    const mitigated = {
      ...risk("done", "medium"),
      state: "mitigated" as const,
      introduced_version: 1,
      mitigated_version: 4,
    };
    const active = {
      ...risk("active", "medium"),
      state: "active" as const,
      introduced_version: 1,
      mitigated_version: null,
    };
    expect(riskEvidenceReviewVersion(mitigated, "changed", 5)).toBe(3);
    expect(riskEvidenceReviewVersion(active, "changed", 5)).toBeUndefined();
    // Versions in which the section had no hunks hold no evidence either.
    expect(riskEvidenceReviewVersion(mitigated, "changed", 5, [1, 2, 4, 5])).toBe(2);
  });

  it("resolves an obsolete risk's evidence to its section's last review with hunks", () => {
    const active = {
      ...risk("kept", "high"),
      state: "active" as const,
      introduced_version: 1,
      mitigated_version: null,
    };
    expect(riskEvidenceReviewVersion(active, "stale", 5)).toBe(4);
    expect(riskEvidenceReviewVersion(active, "stale", 5, [1, 2])).toBe(2);
    expect(riskEvidenceReviewVersion(active, "stale", 5, [])).toBeUndefined();
    const mitigated = { ...active, state: "mitigated" as const, mitigated_version: 2 };
    expect(riskEvidenceReviewVersion(mitigated, "stale", 5, [1, 2, 3])).toBe(1);
  });
});

describe("reviewExchange", () => {
  // ar_1 is posted second but dated first, so the run is not document order.
  const exchanged: ReviewDocument = {
    ...document,
    agent_reviews: [
      {
        ...document.agent_reviews[1]!,
        anchors: [{ type: "pr_comment", id: 9 }],
        findings: [{ ...document.agent_reviews[1]!.findings[0]!, addressed_by: "fix1234567" }],
      },
      { ...document.agent_reviews[0]!, anchors: [{ type: "pr_comment", id: 8 }] },
    ],
    index: {
      ...document.index,
      comments: [
        {
          id: 8,
          kind: "review",
          author: "cursor",
          bot: true,
          created_at: "2026-09-14T12:00:00Z",
          url: "https://github.com/acme/widgets/pull/42#pullrequestreview-8",
        },
        {
          id: 9,
          kind: "review_comment",
          author: "codex",
          bot: true,
          created_at: "2026-09-14T13:00:00Z",
          url: "https://github.com/acme/widgets/pull/42#discussion_r9",
        },
        {
          id: 10,
          kind: "review_comment",
          author: "alex",
          bot: false,
          created_at: "2026-09-14T12:30:00Z",
          url: "https://github.com/acme/widgets/pull/42#discussion_r10",
        },
        {
          id: 11,
          kind: "issue",
          author: "alex",
          bot: false,
          created_at: "2026-09-14T14:00:00Z",
          url: "https://github.com/acme/widgets/pull/42#issuecomment-11",
        },
      ],
    },
  };

  it("orders the run by when each review was posted, not by document order", () => {
    expect(reviewExchange(exchanged).map((event) => [event.actor, event.by])).toEqual([
      ["review", "cursor"],
      ["human", "alex"],
      ["review", "codex"],
      ["implementation", "Implementation"],
    ]);
  });

  it("puts the commit that answered a finding under the review that raised it", () => {
    const answered = reviewExchange(exchanged).at(-1);
    expect(answered).toMatchObject({ actor: "implementation", commit: "fix1234567", at: null });
    expect(answered?.findings.map((finding) => finding.id)).toEqual(["f_3"]);
  });

  it("dates a review by the comment it was posted as and keeps its link", () => {
    expect(reviewExchange(exchanged)[0]).toMatchObject({
      actor: "review",
      at: "2026-09-14T12:00:00Z",
      verdict: "approve",
      url: "https://github.com/acme/widgets/pull/42#pullrequestreview-8",
    });
  });

  it("counts a bot's own comment as a review round without a verdict, once", () => {
    const withBot: ReviewDocument = {
      ...exchanged,
      index: {
        ...exchanged.index,
        comments: [
          ...exchanged.index.comments,
          {
            id: 12,
            kind: "issue",
            author: "cursor[bot]",
            bot: true,
            created_at: "2026-09-14T15:00:00Z",
            url: "https://github.com/acme/widgets/pull/42#issuecomment-12",
          },
        ],
      },
    };
    const rounds = reviewExchange(withBot).filter((event) => event.actor === "review");
    // The comment an agent review was posted as is that review, not a second round.
    expect(rounds.map((event) => event.by)).toEqual(["cursor", "codex", "cursor[bot]"]);
    expect(rounds.at(-1)).toMatchObject({
      at: "2026-09-14T15:00:00Z",
      url: "https://github.com/acme/widgets/pull/42#issuecomment-12",
      findings: [],
    });
    expect(rounds.at(-1)?.verdict).toBeUndefined();
  });

  it("keeps human replies on the diff and leaves standalone issue comments out", () => {
    const people = reviewExchange(exchanged).filter((event) => event.actor === "human");
    expect(people.map((event) => event.at)).toEqual(["2026-09-14T12:30:00Z"]);
  });

  it("names the implementation generically, since no anchor ties a commit to a session", () => {
    const answered = reviewExchange(exchanged).find((event) => event.actor === "implementation");
    expect(answered?.by).toBe("Implementation");
  });

  it("dates a review posted as a bot report through the comment that carries it", () => {
    const reported: ReviewDocument = {
      ...document,
      agent_reviews: [
        { ...document.agent_reviews[0]!, anchors: [{ type: "bot_report", id: "rep_1" }] },
      ],
      index: {
        ...document.index,
        reports: [
          {
            id: "rep_1",
            tool: "cursor",
            marker: "m",
            comment: 8,
            head: null,
            summary: "s",
            url: "https://github.com/acme/widgets/pull/42#issuecomment-8",
          },
        ],
        comments: [
          {
            id: 8,
            kind: "review",
            author: "cursor",
            bot: true,
            created_at: "2026-09-14T12:00:00Z",
            url: "https://github.com/acme/widgets/pull/42#pullrequestreview-8",
          },
        ],
      },
    };
    expect(reviewExchange(reported)[0]).toMatchObject({ at: "2026-09-14T12:00:00Z" });
  });

  // Keyed per review, the commit would be emitted once carrying only the first
  // review's findings, leaving the rest resolved by nothing.
  it("keeps every finding a shared commit answered, across reviews", () => {
    const shared: ReviewDocument = {
      ...exchanged,
      agent_reviews: exchanged.agent_reviews.map((review) => ({
        ...review,
        findings: review.findings.map((finding) => ({ ...finding, addressed_by: "fix1234567" })),
      })),
    };
    const answered = reviewExchange(shared).filter((event) => event.actor === "implementation");
    expect(answered).toHaveLength(1);
    expect(answered[0]!.findings.map((finding) => finding.id).toSorted()).toEqual([
      "f_1",
      "f_2",
      "f_3",
    ]);
  });

  // Dating the commit by the first review it answers puts it above a review it
  // also answers, claiming it resolved a finding that did not exist yet. The
  // array order that hides this is the one the test above happens to use.
  it("never places an answering commit above a review whose finding it answers", () => {
    const shared: ReviewDocument = {
      ...exchanged,
      agent_reviews: [
        {
          ...document.agent_reviews[0]!,
          id: "ar_early",
          anchors: [{ type: "pr_comment", id: 8 }],
          findings: [{ ...document.agent_reviews[0]!.findings[0]!, addressed_by: "cccc123456" }],
        },
        {
          ...document.agent_reviews[1]!,
          id: "ar_late",
          anchors: [{ type: "pr_comment", id: 9 }],
          findings: [{ ...document.agent_reviews[1]!.findings[0]!, addressed_by: "cccc123456" }],
        },
      ],
    };
    const run = reviewExchange(shared).map((event) => event.actor);
    expect(run.indexOf("implementation")).toBeGreaterThan(run.lastIndexOf("review"));
  });

  it("is empty when no tool reviewed and nobody replied on the diff", () => {
    expect(
      reviewExchange({
        ...document,
        agent_reviews: [],
        index: { ...document.index, comments: [] },
      }),
    ).toEqual([]);
  });
});

const decision = (id: string, by: "human" | "agent", anchors: Anchor[]) => ({
  id,
  by,
  text: id,
  anchors,
});

describe("reviewExchangeFacts", () => {
  const withExchange = (review_exchange: unknown): ReviewDocument => ({
    ...document,
    summary: { ...document.summary, review_exchange } as ReviewDocument["summary"],
  });

  it("reads the figures and the paragraph the generator stated", () => {
    expect(
      reviewExchangeFacts(
        withExchange({
          summary: "how it went",
          reviews: 3,
          back_and_forths: 2,
          critical_issues_found: 0,
          changes_after_open: { commits: 5, lines_added: 754, lines_removed: 487 },
        }),
      ),
    ).toEqual({
      reviews: 3,
      backAndForths: 2,
      criticalIssues: 0,
      changesAfterOpen: { commits: 5, additions: 754, deletions: 487 },
      summary: "how it went",
      counted: true,
    });
  });

  // The field was a string before it was an object, and those documents are
  // still in the store. A string is the summary it always was.
  it("reads an older document's plain string as the summary", () => {
    expect(reviewExchangeFacts(withExchange("how it went"))).toEqual({
      reviews: null,
      backAndForths: null,
      criticalIssues: null,
      changesAfterOpen: null,
      summary: "how it went",
      counted: false,
    });
  });

  it("keeps a figure the document did not carry distinct from a zero", () => {
    const partial = reviewExchangeFacts(withExchange({ reviews: 0 }));
    expect(partial).toMatchObject({ reviews: 0, backAndForths: null, changesAfterOpen: null });
    const unknown = reviewExchangeFacts(
      withExchange({ reviews: 2, back_and_forths: null, critical_issues_found: null }),
    );
    expect(unknown).toMatchObject({
      reviews: 2,
      backAndForths: null,
      criticalIssues: null,
      counted: true,
    });
  });

  it("is absent when the document states no account at all", () => {
    expect(reviewExchangeFacts(withExchange(undefined))).toBeNull();
  });
});

const indexedThread = (id: string, resolved: boolean, outdated = false) => ({
  id,
  path: "pkg/a.go",
  line: outdated ? null : 12,
  resolved,
  outdated,
});

const openIds = (status: ReturnType<typeof reviewThreadStatus>) =>
  status?.open.map((open) => open.thread.id);

const exchangeFacts = (overrides: Partial<ReviewExchangeFacts>): ReviewExchangeFacts => ({
  reviews: null,
  backAndForths: null,
  criticalIssues: null,
  changesAfterOpen: null,
  summary: "One account",
  counted: true,
  ...overrides,
});

describe("combineExchangeFacts", () => {
  it("sums each figure over the accounts that state it, with no summary", () => {
    const combined = combineExchangeFacts([
      exchangeFacts({
        reviews: 3,
        criticalIssues: 1,
        changesAfterOpen: { commits: 2, additions: 10, deletions: 4 },
      }),
      exchangeFacts({ reviews: 2, changesAfterOpen: { commits: 1, additions: 5, deletions: 1 } }),
    ]);
    expect(combined).toEqual({
      reviews: 5,
      backAndForths: null,
      criticalIssues: 1,
      changesAfterOpen: { commits: 3, additions: 15, deletions: 5 },
      summary: null,
      counted: true,
    });
  });

  it("has nothing to combine without an account", () => {
    expect(combineExchangeFacts([])).toBeNull();
  });
});

describe("reviewThreadStatus", () => {
  const withThreads = (
    synced: boolean,
    threads: ReturnType<typeof indexedThread>[],
    findings: { thread: string; severity: FindingSeverity }[] = [],
  ): ReviewDocument => ({
    ...document,
    review: { ...document.review, sources: { review_threads: synced } },
    agent_reviews: [
      {
        ...document.agent_reviews[0]!,
        findings: findings.map(({ thread, severity }) => ({
          id: `f_${thread}`,
          severity,
          text: `on ${thread}`,
          section: null,
          anchors: [{ type: "review_thread", id: thread }],
          state: "open",
          addressed_by: null,
        })),
      },
    ],
    index: { ...document.index, threads },
  });

  // GitHub still lists an outdated thread nobody resolved as unresolved.
  it("lists unresolved threads, outdated ones included", () => {
    const status = reviewThreadStatus(
      withThreads(true, [
        indexedThread("T1", true),
        indexedThread("T2", false),
        indexedThread("T3", false, true),
      ]),
    );
    expect(openIds(status)).toEqual(["T2", "T3"]);
    expect(status?.total).toBe(3);
  });

  it("puts the worst finding first and a person's thread after every finding", () => {
    const status = reviewThreadStatus(
      withThreads(
        true,
        [
          indexedThread("T1", false),
          indexedThread("T2", false),
          indexedThread("T3", false),
          indexedThread("T4", false),
        ],
        [
          { thread: "T2", severity: "consider" },
          { thread: "T3", severity: "should_fix" },
          { thread: "T4", severity: "must_fix" },
        ],
      ),
    );
    expect(openIds(status)).toEqual(["T4", "T3", "T2", "T1"]);
    expect(status?.open[0]).toMatchObject({ tool: "cursor", finding: { text: "on T4" } });
    expect(status?.open[3]).toMatchObject({ tool: null, finding: null });
  });

  it("reads synced threads with none open as all clear", () => {
    expect(reviewThreadStatus(withThreads(true, []))).toEqual({ open: [], total: 0 });
  });

  it("is unknown when the document carries no synced threads", () => {
    expect(reviewThreadStatus(withThreads(false, [indexedThread("T1", false)]))).toBeNull();
    expect(reviewThreadStatus(document)).toBeNull();
  });
});

describe("reviewDecisions", () => {
  const decided: ReviewDocument = {
    ...document,
    sections: [
      section("sec_a", "Retry policy and backoff", [
        {
          kind: "decisions",
          items: [
            decision("d_prompt", "human", [{ type: "prompt", session: "s1", index: 3 }]),
            decision("d_other", "agent", [{ type: "prompt", session: "s2", index: 1 }]),
            decision("d_loose", "agent", [{ type: "hunk", id: "h_1" }]),
            {
              ...decision("d_approved", "agent", [{ type: "prompt", session: "s1", index: 4 }]),
              approved_by_human: true,
            },
          ],
        },
      ]),
      section("sec_b", "Operator endpoints", [
        {
          kind: "decisions",
          items: [
            decision("d_event", "agent", [{ type: "session_event", session: "s1", event: "e1" }]),
          ],
        },
      ]),
    ],
  };

  it("keeps every decision, in section order", () => {
    expect(reviewDecisions(decided).map((item) => item.id)).toEqual([
      "d_prompt",
      "d_other",
      "d_loose",
      "d_approved",
      "d_event",
    ]);
  });

  it("keeps a person's approval on the agent decision it was given to", () => {
    expect(
      reviewDecisions(decided)
        .filter((item) => item.approved_by_human)
        .map((item) => [item.id, item.by]),
    ).toEqual([["d_approved", "agent"]]);
  });

  it("carries the section that stated each decision", () => {
    expect(reviewDecisions(decided).map((item) => [item.id, item.sectionTitle])).toEqual([
      ["d_prompt", "Retry policy and backoff"],
      ["d_other", "Retry policy and backoff"],
      ["d_loose", "Retry policy and backoff"],
      ["d_approved", "Retry policy and backoff"],
      ["d_event", "Operator endpoints"],
    ]);
  });

  // A decision a person prompted cites that prompt; one the agent took while
  // building cites the code. Selecting on session anchors would keep the first
  // kind and drop the second, which is the implementation decisions.
  it("does not filter out the decisions that cite code rather than a prompt", () => {
    const codeOnly = reviewDecisions(decided).filter((item) =>
      item.anchors.every((anchor) => anchor.type !== "prompt" && anchor.type !== "session_event"),
    );
    expect(codeOnly.map((item) => item.id)).toEqual(["d_loose"]);
  });

  it("survives a decisions block with no items", () => {
    const malformed = {
      ...document,
      sections: [section("sec_a", "A", [{ kind: "decisions" } as never])],
    } as ReviewDocument;
    expect(() => reviewDecisions(malformed)).not.toThrow();
    expect(reviewDecisions(malformed)).toEqual([]);
  });

  it("is empty when no section recorded a decision", () => {
    expect(reviewDecisions({ ...document, sections: [] })).toEqual([]);
  });
});

describe("implementsGroups", () => {
  // Every heading here is a value out of the document. Nothing in the renderer
  // knows what a tracker or a page is called.
  it("groups by the tracker and page the document names, in its order", () => {
    const items: Implements[] = [
      { type: "spec", page: "REQ-117", assertion: "REQ-117-A01" },
      { type: "issue", system: "linear", key: "WEB-14" },
      { type: "spec", page: "REQ-117", assertion: "REQ-117-A02" },
      { type: "issue", system: "linear", key: "WEB-14" },
      { type: "issue", system: "jira", key: "OPS-3" },
      { type: "issue", system: "shortcut", key: "SC-9" },
      { type: "spec", page: "RFC-2", assertion: "RFC-2-A01" },
    ];
    expect(implementsGroups(items)).toEqual([
      { kind: "spec", label: "REQ-117", items: ["REQ-117-A01", "REQ-117-A02"] },
      { kind: "issue", label: "Linear", items: ["WEB-14"] },
      { kind: "issue", label: "Jira", items: ["OPS-3"] },
      { kind: "issue", label: "Shortcut", items: ["SC-9"] },
      { kind: "spec", label: "RFC-2", items: ["RFC-2-A01"] },
    ]);
  });

  it("shows a type it does not know as itself rather than as one it does", () => {
    const grown = [
      { type: "incident", tracker: "statuspage", ref: "INC-4" },
    ] as unknown as Implements[];
    expect(implementsGroups(grown)).toEqual([
      { kind: "incident", label: "Incident", items: ["statuspage", "INC-4"] },
    ]);
  });

  it("keeps a page cited without an assertion as a page with nothing under it", () => {
    expect(implementsGroups([{ type: "spec", page: "REQ-9" }])).toEqual([
      { kind: "spec", label: "REQ-9", items: [] },
    ]);
  });
});

describe("sectionFindings", () => {
  it("collects findings for one section across tools with their staleness", () => {
    expect(sectionFindings(document, "sec_a").map((f) => [f.id, f.tool, f.stale])).toEqual([
      ["f_1", "cursor", true],
      ["f_3", "codex", false],
    ]);
    expect(sectionFindings(document, "sec_none")).toEqual([]);
  });
});

describe("formatting", () => {
  it("humanizes closed-list values", () => {
    expect(humanize("public_endpoint")).toBe("Public endpoint");
    expect(humanize("should_fix")).toBe("Should fix");
    expect(humanize("")).toBe("");
  });

  it("formats a diff stat with thousands separators", () => {
    expect(formatLines(3372, 70)).toBe("+3,372 −70");
  });

  it("maps only the two must-read tiers onto colour", () => {
    expect(tierTone("critical")).toBe("critical");
    expect(tierTone("high")).toBe("caution");
    expect(tierTone("medium")).toBe("neutral");
    expect(tierTone("mechanical")).toBe("neutral");
  });

  it("distinguishes evidence a reader can trust from evidence they cannot", () => {
    expect(evidenceTone("verified")).toBe("positive");
    expect(evidenceTone("observed")).toBe("info");
    expect(evidenceTone("missing")).toBe("caution");
    expect(evidenceTone("failed")).toBe("critical");
    expect(evidenceTone("asserted")).toBe("neutral");
  });
});

describe("livePullRequestState", () => {
  it("reads a draft pull request as draft rather than open", () => {
    // The browse API carries draft beside the state; this schema carries it as
    // one of the states, and a review generated on a draft says "draft".
    expect(livePullRequestState({ state: "open", draft: true })).toBe("draft");
    expect(livePullRequestState({ state: "open", draft: false })).toBe("open");
    expect(livePullRequestState({ state: "merged", draft: false })).toBe("merged");
  });

  it("reads a pull request closed while it was a draft as closed", () => {
    // GitHub keeps the flag on a draft that was closed rather than merged, and
    // the generator derives the state from merged/closed alone, so the two
    // arrive together.
    expect(livePullRequestState({ state: "closed", draft: true })).toBe("closed");
    expect(livePullRequestState({ state: "merged", draft: true })).toBe("merged");
  });

  it("has no answer before a pull request has loaded", () => {
    expect(livePullRequestState(null)).toBeUndefined();
    expect(livePullRequestState(undefined)).toBeUndefined();
  });
});

const scored = (risk_score?: RiskScore) => ({
  summary: { risk_score } as ReviewDocument["summary"],
  sections: [{ id: "sec_auth", title: "Approval rules" }] as ReviewDocument["sections"],
});

describe("riskScoreTone", () => {
  it("colours every level: calm for 1-2, neutral for 3, alarm for 4-5", () => {
    expect([1, 2, 3, 4, 5].map((score) => riskScoreTone(score as 1 | 2 | 3 | 4 | 5))).toEqual([
      "positive",
      "positive",
      "neutral",
      "caution",
      "critical",
    ]);
  });
});

describe("riskScoreView", () => {
  it("reads a document generated before scores existed as not assessed", () => {
    expect(riskScoreView(scored())).toEqual({ assessed: false, legacy: true, limitations: [] });
  });

  it("shows the stored score with its meaning and the reasons' sections", () => {
    const view = riskScoreView(
      scored({
        rubric: "risk-v1",
        status: "assessed",
        score: 4,
        reasons: [{ section: "sec_auth", risk: null, text: "Changes who may approve." }],
        limitations: [],
      }),
    );
    expect(view).toEqual({
      assessed: true,
      score: 4,
      lowerBound: false,
      meaning: "Sensitive or broad impact",
      reasons: [
        { section: "sec_auth", sectionTitle: "Approval rules", text: "Changes who may approve." },
      ],
      limitations: [],
    });
  });

  it("marks a lower bound and names what the input was missing", () => {
    const view = riskScoreView(
      scored({
        rubric: "risk-v1",
        status: "lower_bound",
        score: 2,
        reasons: [],
        limitations: [
          { kind: "unassessed_sections", count: 1 },
          { kind: "omitted_source", count: 3 },
        ],
      }),
    );
    expect(view).toMatchObject({
      assessed: true,
      score: 2,
      lowerBound: true,
      limitations: ["1 section not assessed", "3 changed files unreadable"],
    });
  });

  it("keeps a generated but unrated review apart from a legacy one", () => {
    const view = riskScoreView(
      scored({
        rubric: "risk-v1",
        status: "not_assessed",
        score: null,
        reasons: [],
        limitations: [{ kind: "unassessed_sections", count: 2 }],
      }),
    );
    expect(view).toEqual({
      assessed: false,
      legacy: false,
      limitations: ["2 sections not assessed"],
    });
  });
});

const ids = (sections: Section[]) => sectionsInReviewOrder(sections).map((item) => item.id);

describe("sectionsInReviewOrder", () => {
  const ordered = (
    id: string,
    review_score?: Section["derived"]["review_score"],
    options: { kind?: Section["kind"]; state?: Section["derived"]["state"] } = {},
  ): Section => {
    const base = section(id, id, []);
    return {
      ...base,
      kind: options.kind ?? "logical",
      derived: { ...base.derived, review_score, state: options.state ?? "new" },
    };
  };
  it("puts the highest score first and keeps the review's order within a score", () => {
    expect(
      ids([ordered("a", 2), ordered("b", 4), ordered("c", 2), ordered("d", 5), ordered("e", 4)]),
    ).toEqual(["d", "b", "e", "a", "c"]);
  });

  it("prioritizes attention within the same score while preserving score order", () => {
    const attention = {
      ...ordered("attention", 4),
      attention: { text: "Check recovery", refs: [] },
    };
    expect(
      ids([ordered("plain", 4), ordered("highest", 5), attention, ordered("also-plain", 4)]),
    ).toEqual(["highest", "attention", "plain", "also-plain"]);
  });

  it("puts the mechanical section, unscored and obsolete sections last, in that order", () => {
    expect(
      ids([
        ordered("stale", undefined, { state: "stale" }),
        ordered("unscored"),
        ordered("fmt", 1, { kind: "mechanical" }),
        ordered("low", 1),
        ordered("high", 4),
      ]),
    ).toEqual(["high", "low", "fmt", "unscored", "stale"]);
  });

  it("keeps a document generated before section scores in the review's order", () => {
    const legacy = [ordered("b"), ordered("a"), ordered("c", undefined, { state: "stale" })];
    expect(sectionsInReviewOrder(legacy)).toBe(legacy);
  });
});

describe("pullRequestStateTone", () => {
  it("colours merged and open, and leaves closed and unknown states neutral", () => {
    expect(pullRequestStateTone("merged")).toBe("positive");
    expect(pullRequestStateTone("open")).toBe("info");
    expect(pullRequestStateTone("closed")).toBe("neutral");
    expect(pullRequestStateTone("draft")).toBe("neutral");
  });
});

describe("riskLabel", () => {
  it("leads with the category and follows with the section", () => {
    expect(
      riskLabel({
        id: "rsk_1",
        tier: "high",
        category: "data_integrity",
        text: "",
        anchors: [],
        section: "sec_a",
        sectionTitle: "Atomic save",
      } as unknown as TriageRisk),
    ).toBe("Data integrity · Atomic save");
  });
});
