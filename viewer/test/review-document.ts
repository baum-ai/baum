/** A generated review document for a sample repository, in the shape of
 *  `baum.review/1`, and one earlier version of it. Small on purpose: enough
 *  sections and blocks for the tests to exercise every core slot. */

const pull = "https://github.com/acme/widgets/pull/42";
const hunk = (
  id: string,
  path: string,
  range: [number, number],
  additions: number,
  deletions: number,
  change: string,
  role: string,
) => ({
  id,
  path,
  new_range: range,
  additions,
  deletions,
  change,
  role,
  github_url: `${pull}/files#${id}`,
  diff: {
    header: `@@ -${change === "added" ? 0 : range[0]},${change === "added" ? 0 : range[1]} +${range[0]},${range[1]} @@`,
    lines: Array.from({ length: range[1] }, (_, offset) =>
      change === "added"
        ? { kind: "add", new_line: range[0] + offset, text: `added line ${range[0] + offset}` }
        : {
            kind: "context",
            old_line: range[0] + offset,
            new_line: range[0] + offset,
            text: `context line ${range[0] + offset}`,
          },
    ),
  },
});

const reviewDocument = {
  schema: "baum.review/1",
  review: {
    repository: "acme/widgets",
    pull_request: 42,
    pr_state: "open",
    head: "afd406e3e902dd31b741474e7bfb1727ba444a68",
    base: "43688e43e2b548912f3572a8491014248d92a4e4",
    merge_base: "43688e43e2b548912f3572a8491014248d92a4e4",
    merged_commit: null,
    version: 2,
    previous_version: 1 as number | null,
    generation_state: "current",
    generated_at: "2026-07-29T15:03:12Z",
    evidence_refreshed_at: "2026-07-29T16:52:00Z",
    generator: {
      model: "provider-model-id",
      review_md: null,
      schema_revision: "2026-09-15",
      truncated: ["Session event summaries are paraphrased; raw commands are not included"],
    },
    sources: {
      diff: true,
      sessions: 1,
      checks: 2,
      test_reports: 0,
      review_threads: true,
      agent_reviews: 1,
      spec_links: false,
      review_md: false,
    },
    relations: [],
  },
  summary: {
    title: "Ship analytics filters",
    text: "Filter activity by repository and share the selected view with a teammate. A repository picker above the activity list updates the visible activity and totals together.",
    change_kind: "feature",
    implements: [{ type: "issue", system: "linear", key: "AGE-46" }],
    release_note: null,
    breaking_change: null,
    review_exchange: {
      summary:
        "Cursor commented on an earlier head and left one should-fix finding open about reading a repository from the URL. No commit answers it yet, and one human reply is indexed on the diff.",
      reviews: 2,
      back_and_forths: 1,
      critical_issues_found: 3,
      changes_after_open: { commits: 4, lines_added: 210, lines_removed: 17 },
    },
    attention: {
      text: "The picker reads the repository from the URL, so a shared link now names a repository the reader may not be able to see.",
      refs: ["behavior"],
    },
    author: { login: "alex", association: "MEMBER", bot: false },
    authorship: {
      declared: "none",
      declared_tools: [],
      anchors: [],
      observed_sessions: 1,
      observed_vendors: ["codex"],
      observed_models: ["gpt-6-astra"],
    },
    risk_score: {
      rubric: "risk-v1",
      status: "lower_bound",
      score: 4,
      reasons: [
        {
          section: "behavior",
          risk: null,
          text: "A shared link now decides which repository a reader is shown.",
        },
      ],
      limitations: [{ kind: "unassessed_sections", count: 1 }],
    },
  },
  stats: {
    sections: 5,
    files: 4,
    hunks: 5,
    additions: 212,
    deletions: 18,
    risk_loc: { critical: 0, high: 40, medium: 96, low: 0, unrated: 60, mechanical: 16 },
    role_loc: { code: 140, test: 56, generated: 16 },
    checks: { required_failed: 0, optional_failed: 0, pending: 0, skipped: 0 },
    agent_findings: { open: 1, resolved: 0, dismissed: 0 },
  },
  sections: [
    {
      id: "overview",
      title: "Overview",
      kind: "logical",
      hunks: ["h_picker", "h_picker_test"],
      summary: {
        text: "A repository picker above the activity list updates the visible activity and totals together.",
        anchors: [{ type: "hunk", id: "h_picker" }],
      },
      attention: {
        text: "The picker exposes every repository the organisation has, not only those the reader can open.",
        refs: ["rsk_1"],
      },
      risks: [
        {
          id: "rsk_1",
          tier: "medium",
          category: "permissions",
          source: "model",
          text: "Repository names are listed from the organisation index rather than the reader's access list",
          anchors: [{ type: "hunk", id: "h_picker", lines: [12, 30] }],
          state: "active",
          introduced_version: 1,
          mitigated_version: null,
        },
        {
          id: "rsk_old",
          tier: "low",
          category: "usability",
          source: "model",
          text: "The first picker layout could overflow a narrow toolbar",
          anchors: [{ type: "hunk", id: "h_picker_old" }],
          state: "mitigated",
          introduced_version: 1,
          mitigated_version: 2,
        },
      ],
      evidence: [
        {
          id: "ev_1",
          kind: "test",
          claim: "Selecting a repository filters activity and totals",
          status: "verified",
          granularity: "test",
          source: "ci",
          anchors: [
            { type: "check", id: "chk_ui", test: "picker filters activity" },
            { type: "hunk", id: "h_picker_test" },
          ],
          detail: "picker filters activity",
        },
      ],
      blocks: [
        {
          kind: "intent",
          text: "Let a reader narrow the activity view to one repository without losing the totals that put it in context.",
          anchors: [
            { type: "prompt", session: "s_1", index: 1 },
            { type: "issue", system: "linear", key: "AGE-46" },
          ],
        },
        {
          kind: "entities",
          items: [
            {
              kind: "function",
              name: "RepositoryPicker",
              path: "web/app/components/repository-picker.tsx",
              change: "added",
              hunks: ["h_picker"],
            },
          ],
        },
        {
          kind: "behavior",
          steps: [
            {
              id: "beh_1",
              text: "Choosing a repository re-queries activity and totals with the same scope",
              anchors: [
                { type: "hunk", id: "h_picker", lines: [31, 48] },
                { type: "pr_comment", id: 4005516815 },
              ],
              evidence: ["ev_1"],
            },
          ],
        },
        {
          kind: "decisions",
          items: [
            {
              id: "dec_1",
              by: "human",
              text: "Keep the picker beside the activity list rather than in the page header",
              rejected: "A global filter in the header",
              anchors: [
                { type: "pr_comment", id: 4005516815 },
                { type: "prompt", session: "s_1", index: 2 },
              ],
            },
          ],
        },
      ],
      assessment: {
        level: 3,
        factors: [],
        reasons: [
          {
            text: "Every review page reads its header from here.",
            anchors: [{ type: "hunk", id: "h_picker" }],
          },
        ],
      },
      derived: {
        risk_tier: "medium",
        additions: 96,
        deletions: 4,
        role_loc: { code: 60, test: 40 },
        reviewers: [{ login: "morgan", source: "requested" }],
        state: "unchanged",
        evidence_summary: { verified: 1 },
        open_findings: 0,
        review_score: 3,
        review_reason: { risk: null, text: "Every review page reads its header from here." },
      },
    },
    {
      id: "behavior",
      title: "Behavior changes",
      kind: "logical",
      hunks: ["h_url"],
      summary: {
        text: "The URL preserves the selected repository. Clearing the filter restores all repositories.",
        anchors: [{ type: "hunk", id: "h_url" }],
      },
      attention: null,
      risks: [
        {
          id: "rsk_1",
          tier: "high",
          category: "permissions",
          source: "agent_review",
          text: "A shared link names a repository the reader may not be able to see",
          anchors: [
            { type: "pr_comment", id: 5197744335 },
            { type: "hunk", id: "h_url" },
          ],
          state: "active",
          introduced_version: 2,
          mitigated_version: null,
        },
      ],
      evidence: [
        {
          id: "ev_1",
          kind: "test",
          claim: "The selected repository round-trips through the URL",
          status: "asserted",
          source: "model",
          anchors: [{ type: "hunk", id: "h_url" }],
          expects: "A browser test that reloads a filtered view",
        },
      ],
      blocks: [
        {
          kind: "behavior",
          steps: [
            {
              id: "beh_1",
              text: "The selected repository is written to the URL and read back on load",
              anchors: [{ type: "hunk", id: "h_url" }],
              evidence: ["ev_1"],
            },
          ],
        },
        {
          kind: "entities",
          items: [
            {
              kind: "endpoint",
              name: "GET /activity",
              path: "server/internal/api/activity.go",
              change: "modified",
              hunks: ["h_url"],
            },
            {
              kind: "function",
              name: "readRepositoryFilter",
              path: "web/app/filters.ts",
              change: "added",
              hunks: ["h_url"],
            },
          ],
        },
      ],
      derived: {
        risk_tier: "high",
        additions: 40,
        deletions: 6,
        role_loc: { code: 40 },
        reviewers: [],
        state: "changed",
        evidence_summary: { asserted: 1 },
        open_findings: 1,
        review_score: 4,
        review_reason: {
          risk: "rsk_1",
          text: "A shared link names a repository the reader may not be able to see",
        },
      },
    },
    {
      id: "tests",
      title: "Tests",
      kind: "logical",
      hunks: ["h_e2e"],
      summary: {
        text: "Example passing result: selecting a repository updates the activity. Shared-link coverage is pending.",
        anchors: [{ type: "hunk", id: "h_e2e" }],
      },
      attention: null,
      risks: [],
      evidence: [
        {
          id: "ev_1",
          kind: "ci",
          claim: "The UI job passed on this head",
          status: "verified",
          granularity: "job",
          source: "ci",
          anchors: [{ type: "check", id: "chk_ui" }],
        },
        {
          id: "ev_2",
          kind: "test",
          claim: "A shared link opens the filtered view",
          status: "missing",
          source: "model",
          anchors: [{ type: "hunk", id: "h_e2e" }],
          expects: "A browser test that opens a filtered link in a second context",
        },
      ],
      blocks: [],
      derived: {
        risk_tier: "low",
        additions: 60,
        deletions: 0,
        role_loc: { test: 16, code: 44 },
        reviewers: [],
        state: "new",
        evidence_summary: { verified: 1, missing: 1 },
        open_findings: 0,
      },
    },
    {
      id: "duplicate-a",
      title: "Shared label",
      kind: "logical",
      hunks: [],
      summary: { text: "The first of two sections that share a title.", anchors: [] },
      attention: null,
      risks: [],
      evidence: [],
      blocks: [],
      derived: {
        risk_tier: "unrated",
        additions: 0,
        deletions: 0,
        role_loc: {},
        reviewers: [],
        state: "stale",
        evidence_summary: {},
        open_findings: 0,
      },
    },
    {
      id: "duplicate-b",
      title: "Shared label",
      kind: "mechanical",
      hunks: ["h_generated"],
      summary: { text: "Regenerated route types.", anchors: [{ type: "hunk", id: "h_generated" }] },
      attention: null,
      risks: [],
      evidence: [],
      blocks: [
        {
          kind: "groups",
          items: [
            {
              id: "grp_1",
              rule: "Route types regenerated by the framework",
              derived_from: "behavior",
              hunks: ["h_generated"],
              count: 1,
            },
          ],
        },
        { kind: "hologram", anchors: [{ type: "pr_body", section: null }] },
      ],
      derived: {
        risk_tier: "mechanical",
        additions: 16,
        deletions: 8,
        role_loc: { generated: 16 },
        reviewers: [],
        state: "unchanged",
        evidence_summary: {},
        open_findings: 0,
        review_score: 1,
      },
    },
  ],
  agent_reviews: [
    {
      id: "ar_cursor",
      tool: "cursor",
      assessed_head: "afd406e3e902dd31b741474e7bfb1727ba444a68",
      stale: false,
      verdict: "comment",
      blocked_on: null,
      counts: { must_fix: 0, should_fix: 1, consider: 0 },
      anchors: [{ type: "pr_comment", id: 5197744335 }],
      findings: [
        {
          id: "fnd_1",
          severity: "should_fix",
          text: "Guard the repository read from the URL against repositories the reader cannot open",
          section: "behavior",
          anchors: [{ type: "hunk", id: "h_url" }],
          state: "open",
          addressed_by: null,
        },
      ],
    },
  ],
  unassigned_hunks: [],
  index: {
    hunks: [
      hunk("h_picker", "web/app/components/repository-picker.tsx", [1, 96], 96, 0, "added", "code"),
      hunk(
        "h_picker_test",
        "web/app/components/repository-picker.test.tsx",
        [1, 40],
        40,
        4,
        "added",
        "test",
      ),
      hunk("h_url", "web/app/routes/activity.tsx", [20, 66], 40, 6, "modified", "code"),
      hunk("h_e2e", "web/e2e/activity.spec.ts", [1, 60], 60, 0, "added", "test"),
      hunk(
        "h_generated",
        "web/.react-router/types/routes.ts",
        [1, 16],
        16,
        8,
        "modified",
        "generated",
      ),
    ],
    checks: [
      {
        id: "chk_ui",
        kind: "check_run",
        name: "UI",
        conclusion: "success",
        required: true,
        head: "afd406e3e902dd31b741474e7bfb1727ba444a68",
        started_at: "2026-07-29T14:56:59Z",
        completed_at: "2026-07-29T14:59:31Z",
        url: "https://github.com/acme/widgets/actions/runs/1/job/1",
      },
      {
        id: "chk_platform",
        kind: "check_run",
        name: "Platform",
        conclusion: "success",
        required: false,
        head: "afd406e3e902dd31b741474e7bfb1727ba444a68",
        started_at: "2026-07-29T14:57:00Z",
        completed_at: "2026-07-29T15:00:22Z",
        url: "https://github.com/acme/widgets/actions/runs/1/job/2",
      },
    ],
    reports: [],
    comments: [
      {
        id: 5197744335,
        kind: "review",
        author: "cursor",
        bot: true,
        created_at: "2026-07-29T12:39:35Z",
        url: `${pull}#pullrequestreview-5197744335`,
      },
      {
        id: 4005516815,
        kind: "review_comment",
        author: "morgan",
        bot: false,
        created_at: "2026-07-29T13:12:20Z",
        url: `${pull}#discussion_r4005516815`,
      },
    ],
    threads: [],
    sessions: [
      {
        id: "s_1",
        vendor: "codex",
        models: ["gpt-6-astra"],
        title: null,
        git_branch: "codex/age46-filters",
        started_at: "2026-07-29T11:20:31Z",
        ended_at: "2026-07-29T13:55:57Z",
        duration_minutes: 155,
        outcome: "committed",
        prompt_count: 2,
        tool_calls: 120,
        restricted: false,
        events: [
          {
            id: "e_1",
            at: "2026-07-29T11:32:43Z",
            verb: "execute",
            summary: "Ran the UI unit tests",
          },
        ],
        prompts: [
          {
            index: 1,
            id: "p_1",
            at: "2026-07-29T11:20:31Z",
            text: "Implement AGE-46: repository filters on the activity view.",
          },
          {
            index: 2,
            id: "p_2",
            at: "2026-07-29T12:45:18Z",
            text: "Keep the picker next to the list please.",
          },
        ],
      },
    ],
    prs: [],
  },
};

const historicalV1 = structuredClone(reviewDocument);
historicalV1.review.version = 1;
historicalV1.review.previous_version = null;
historicalV1.review.head = "1111111111111111111111111111111111111111";
historicalV1.review.generated_at = "2026-07-28T10:00:00Z";
historicalV1.summary.text = "The first review covered the repository picker before URL sharing.";
historicalV1.sections = historicalV1.sections.filter((section) => section.id !== "tests");
historicalV1.stats.sections = historicalV1.sections.length;
const historicalOverview = historicalV1.sections.find((section) => section.id === "overview");
const historicalBehavior = historicalV1.sections.find((section) => section.id === "behavior");
const historicalObsolete = historicalV1.sections.find((section) => section.id === "duplicate-a");
if (!historicalOverview || !historicalBehavior || !historicalObsolete) {
  throw new Error("review history fixture sections are missing");
}
historicalOverview.summary.text =
  "A repository picker updates the activity list before URL sharing was added.";
const historicalRisk = historicalOverview.risks.find((risk) => risk.id === "rsk_old");
if (!historicalRisk) throw new Error("historical risk fixture is incomplete");
const historicalPicker = historicalV1.index.hunks.find((candidate) => candidate.id === "h_picker");
if (!historicalPicker) throw new Error("historical hunk fixture is incomplete");
historicalV1.index.hunks.push({ ...structuredClone(historicalPicker), id: "h_picker_old" });
historicalRisk.tier = "high";
historicalRisk.text = `${historicalRisk.text} ${"The longer historical explanation remains readable inside its pane. ".repeat(24)}`;
for (const risk of historicalOverview.risks) {
  risk.state = "active";
  risk.introduced_version = 1;
  risk.mitigated_version = null;
}
historicalBehavior.risks = [];
historicalObsolete.hunks = ["h_generated"];
historicalObsolete.summary.text = "The shared label section was still part of the diff.";
historicalObsolete.derived.state = "changed";

export function reviewDocumentFor(
  repository: string,
  number: number,
  version?: number,
): unknown | null {
  if (repository !== "acme/widgets" || number !== 42) return null;
  if (version === undefined) return reviewDocument;
  return version === 1 ? historicalV1 : null;
}

export function reviewHistoryFor(repository: string, number: number): unknown | null {
  if (repository !== "acme/widgets" || number !== 42) return null;
  return [reviewDocument, historicalV1].map((document, position) => ({
    version: document.review.version,
    head: document.review.head,
    base: document.review.base,
    generated_at: document.review.generated_at,
    current: position === 0,
  }));
}
