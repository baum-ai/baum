// @vitest-environment jsdom
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";

import example from "../../../schema/review-example.json" with { type: "json" };
import { UrlStateProvider } from "../navigation";
import {
  checkStatus,
  parseReviewFacts,
  type ReviewDocument,
  type ReviewFacts,
} from "../review-document";
import { ReviewViewer } from "./review-viewer";

declare global {
  var IS_REACT_ACT_ENVIRONMENT: boolean;
}

let root: Root | null = null;

beforeAll(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [];
    }
  } as unknown as typeof IntersectionObserver;
  window.matchMedia ??= (query: string) =>
    ({
      matches: false,
      media: query,
      addEventListener() {},
      removeEventListener() {},
    }) as unknown as MediaQueryList;
  Element.prototype.scrollIntoView ??= () => {};
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  document.body.replaceChildren();
  localStorage.clear();
});

async function render(node: ReactNode) {
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root!.render(
      <UrlStateProvider value={{ search: new URLSearchParams(), update: () => {} }}>
        {node}
      </UrlStateProvider>,
    ),
  );
  return container;
}

/** A pull request's facts as the daemon answers them: two files, a failing
 *  required check on the head, one session and a review tool's open finding. */
export const facts: ReviewFacts = {
  schema: "baum.facts/1",
  review: {
    repository: "acme/demo",
    pull_request: 7,
    pr_state: "open",
    head: "1111111111111111111111111111111111111111",
    base: "2222222222222222222222222222222222222222",
    merge_base: "2222222222222222222222222222222222222222",
    merged_commit: null,
    evidence_refreshed_at: new Date().toISOString(),
    sources: { diff: true, sessions: 1, checks: 2, review_threads: true, agent_reviews: 1 },
    truncated: [],
  },
  summary: {
    title: "Parse the config",
    author: { login: "octocat", association: "MEMBER", bot: false },
    authorship: { observed_sessions: 1, observed_vendors: ["claude"], observed_models: [] },
    review_exchange: {
      reviews: 1,
      back_and_forths: 0,
      critical_issues_found: 1,
      changes_after_open: null,
    },
  },
  stats: {
    files: 2,
    hunks: 3,
    additions: 40,
    deletions: 4,
    role_loc: { code: 30, test: 14 },
    checks: { required_failed: 1, optional_failed: 0, pending: 0, skipped: 0 },
    agent_findings: { open: 1, resolved: 0, dismissed: 0 },
  },
  agent_reviews: [
    {
      id: "ar_cursor_1111111",
      tool: "cursor",
      assessed_head: "1111111111111111111111111111111111111111",
      stale: false,
      verdict: "comment",
      blocked_on: null,
      counts: { must_fix: 1, should_fix: 0, consider: 0 },
      anchors: [{ type: "pr_comment", id: 100 }],
      findings: [
        {
          id: "fnd_cursor_200",
          severity: "must_fix",
          text: "Guard the nil parser",
          section: null,
          anchors: [{ type: "review_thread", id: "PRRT_1" }],
          state: "open",
          addressed_by: null,
        },
      ],
    },
  ],
  unassigned_hunks: ["h_0123456789ab", "h_0123456789ac", "h_0123456789ad"],
  index: {
    hunks: [
      {
        id: "h_0123456789ab",
        path: "src/config.ts",
        new_range: [1, 20],
        additions: 20,
        deletions: 2,
        change: "modified",
        role: "code",
      },
      {
        id: "h_0123456789ac",
        path: "src/config.ts",
        new_range: [40, 10],
        additions: 10,
        deletions: 2,
        change: "modified",
        role: "code",
      },
      {
        id: "h_0123456789ad",
        path: "src/config.test.ts",
        new_range: [1, 14],
        additions: 14,
        deletions: 0,
        change: "added",
        role: "test",
      },
    ],
    checks: [
      {
        id: "ci",
        kind: "check_run",
        name: "CI / test",
        conclusion: "failure",
        required: true,
        head: "1111111111111111111111111111111111111111",
      },
      {
        id: "lint",
        kind: "check_run",
        name: "CI / lint",
        conclusion: "success",
        required: false,
        head: "1111111111111111111111111111111111111111",
      },
    ],
    reports: [],
    comments: [
      {
        id: 100,
        kind: "review",
        author: "cursor[bot]",
        bot: true,
        created_at: "2026-10-30T10:00:00Z",
      },
    ],
    threads: [{ id: "PRRT_1", path: "src/config.ts", line: 4, resolved: false, outdated: false }],
    sessions: [
      {
        id: "session-1",
        vendor: "claude",
        models: ["claude-sonnet-5"],
        title: "Parse the config file",
        restricted: false,
        prompt_count: 3,
        tool_calls: 12,
      },
    ],
    prs: [],
  },
};

const tab = (container: HTMLElement, name: string) =>
  [...container.querySelectorAll<HTMLButtonElement>('[role="tab"]')].find(
    (item) => item.textContent === name,
  )!;

describe("a pull request's facts", () => {
  it("are recognised as facts, never as a review document", () => {
    expect(parseReviewFacts(structuredClone(facts))).not.toBeNull();
    expect(parseReviewFacts(example)).toBeNull();
    expect(parseReviewFacts({ ...facts, schema: "baum.review/1" })).toBeNull();
    expect(checkStatus(facts).state).toBe("failing");
  });

  it("fill the Details and Activity tabs while there is no document", async () => {
    const container = await render(
      <ReviewViewer document={null} facts={facts} placeholder={<p>No review yet</p>} />,
    );
    expect(container.textContent).toContain("No review yet");
    // Nothing offers the facts as a version.
    expect(container.textContent).not.toMatch(/Review v\d/);
    await act(async () => tab(container, "Details").click());
    const details = container.querySelector<HTMLElement>("#conversation-panel-details")!;
    expect(details.textContent).toContain("Change size");
    expect(details.textContent).toContain("Files2");
    expect(details.textContent).toContain("Hunks3");
    expect(details.textContent).toContain("octocat · Member");
    expect(details.textContent).not.toContain("Sections");
    // The Activity tab leads with CI and the review tools' findings, and
    // is flagged for them.
    expect(tab(container, "Activity").querySelector("span[aria-hidden]")).not.toBeNull();
    await act(async () => tab(container, "Activity").click());
    const activity = container.querySelector<HTMLElement>("#conversation-panel-activity")!;
    expect(activity.textContent).toContain("CI failing");
    expect(activity.textContent).toContain("CI / test");
    expect(activity.textContent).toContain("Guard the nil parser");
    expect(activity.textContent).toContain("Linked sessions");
  });

  it("give way to the document once there is one", async () => {
    const container = await render(
      <ReviewViewer document={example as unknown as ReviewDocument} facts={facts} />,
    );
    await act(async () => tab(container, "Activity").click());
    expect(container.textContent).not.toContain("Guard the nil parser");
  });
});
