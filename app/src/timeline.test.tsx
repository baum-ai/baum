import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type {
  DecisionDetail,
  DecisionSummary,
  FeatureDetail,
  FeatureSummary,
} from "@baum/viewer/decision-log";
import {
  UrlStateProvider,
  ViewerLinkProvider,
  type ViewerLinkProps,
} from "@baum/viewer/navigation";
import { daySections, pullRequestHref, sessionHref, TimelinePage } from "./timeline";

const Link = (props: ViewerLinkProps) => <a {...props} />;
const me = { email: "me@example.com", name: null, login: null, avatar_url: null };

const decision: DecisionSummary = {
  id: "d1",
  session_id: "claude-code:s1",
  turn: 2,
  decided_at: "2026-10-20T10:00:00Z",
  who: "human",
  person: me,
  vendor: "claude_code",
  category: "architecture",
  summary: "Keep the timeline local",
  superseded: false,
  file_count: 1,
};
const feature: FeatureSummary = {
  id: "F1",
  title: "Local timeline",
  description: "Shows features on this computer.",
  status: "in_progress",
  first_activity_at: "2026-10-19T10:00:00Z",
  last_activity_at: "2026-10-20T10:00:00Z",
  summary_pending: false,
  people: [me],
  pull_request_count: 1,
  session_count: 1,
  decision_count: 1,
  key_decisions: [
    {
      id: "d1",
      session_id: "claude-code:s1",
      turn: 2,
      who: "human",
      category: "architecture",
      summary: decision.summary,
    },
  ],
};
const featureDetail: FeatureDetail = {
  feature: { ...feature, digest: "The digest." },
  pull_requests: [
    {
      number: 7,
      title: "Add the timeline page",
      state: "open",
      draft: false,
      merged: false,
      review_state: "current",
      summary: "Adds the page.",
      decisions: [decision],
    },
  ],
  sessions: [],
};
const decisionDetail: DecisionDetail = {
  session: {
    id: "claude-code:s1",
    person: me,
    vendor: "claude_code",
    branch: "timeline",
    started_at: null,
    ended_at: null,
  },
  decisions: [
    {
      id: "d1",
      turn: 2,
      turns: [2],
      decided_at: decision.decided_at,
      who: "human",
      category: "architecture",
      summary: decision.summary,
      rejected_alternative: "A hosted-only page",
      superseded: false,
      files: ["oss/app/src/timeline.tsx"],
    },
  ],
  turns: [],
  prompts_withheld: true,
  pull_requests: [],
};

let root: Root, node: HTMLDivElement;
let answers: Record<string, unknown>;
const requests: string[] = [];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/?page=timeline");
  requests.length = 0;
  answers = {
    "api/timeline/repositories": { repositories: [{ repository: "acme/demo" }] },
    "api/timeline/features": { features: [feature], next_cursor: null, order: "newest" },
    "api/timeline/features/F1": featureDetail,
    "api/timeline/features/F0": featureDetail,
    "api/timeline/decisions": {
      decisions: [decision],
      next_cursor: null,
      order: "newest",
      coverage: { extracted: 3, pending: 1, skipped: 1, skipped_by_reason: { too_large: 1 } },
    },
    "api/timeline/decisions/d1": decisionDetail,
  };
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      requests.push(path);
      const key = path.replace("/s/test-ns/", "").split("?")[0]!;
      return key in answers
        ? Response.json(answers[key])
        : new Response("missing", { status: 404 });
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});

let current = new URLSearchParams();
function Harness({ initial }: { initial: string }) {
  const [search, setSearch] = useState(() => new URLSearchParams(initial));
  current = search;
  return (
    <ViewerLinkProvider link={Link}>
      <UrlStateProvider
        value={{ search, update: (patch) => setSearch((s) => patch(new URLSearchParams(s))) }}
      >
        <TimelinePage />
      </UrlStateProvider>
    </ViewerLinkProvider>
  );
}

async function show(initial = "page=timeline") {
  await act(async () => root.render(<Harness initial={initial} />));
  await act(async () => {});
  await act(async () => {});
}

it("lists a repository's features and opens one", async () => {
  await show();
  expect(requests).toContain("/s/test-ns/api/timeline/features?repository=acme%2Fdemo");
  expect(node.textContent).toContain("Local timeline");
  expect(node.textContent).toContain("1 PR · 1 session · 1 decision");
  const toggle = [...node.querySelectorAll("button[aria-expanded]")].find((b) =>
    b.textContent?.includes("Local timeline"),
  ) as HTMLButtonElement;
  await act(async () => toggle.click());
  await act(async () => {});
  expect(current.get("feature")).toBe("F1");
  expect(requests).toContain("/s/test-ns/api/timeline/features/F1");
  expect(node.textContent).toContain("Add the timeline page");
  const pull = [...node.querySelectorAll("a")].find((a) => a.textContent?.includes("#7"));
  expect(pull?.getAttribute("href")).toBe(pullRequestHref("acme/demo", 7));
});

it("shows all decisions with what capture covered and a decision without its prompts", async () => {
  await show("page=timeline&tab=all&open=d1");
  expect(requests).toContain("/s/test-ns/api/timeline/decisions?repository=acme%2Fdemo");
  expect(node.textContent).toContain("3 sessions read · 1 skipped (Too large: 1) · 1 being read");
  expect(node.textContent).toContain("Keep the timeline local");
  expect(node.textContent).toContain("A hosted-only page");
  expect(node.textContent).toContain("The Timeline does not show prompts.");
  const session = [...node.querySelectorAll("a")].find((a) => a.textContent === "Open the session");
  expect(session?.getAttribute("href")).toBe(sessionHref("claude-code:s1"));
  // A file narrows the list to the decisions citing it.
  const file = [...node.querySelectorAll("button")].find((b) =>
    b.textContent?.includes("oss/app/src/timeline.tsx"),
  )!;
  await act(async () => file.click());
  await act(async () => {});
  expect(requests.at(-1)).toBe(
    "/s/test-ns/api/timeline/decisions?repository=acme%2Fdemo&file=oss%2Fapp%2Fsrc%2Ftimeline.tsx",
  );
  const options = [...node.querySelectorAll("option")].map((o) => o.textContent);
  expect(options).toContain("All categories");
});

it("says how features appear before there are any", async () => {
  answers["api/timeline/features"] = { features: [], next_cursor: null };
  await show();
  expect(node.textContent).toContain("No features yet");
  expect(node.textContent).toContain("as baum places this repository's pull requests");
  const all = [...node.querySelectorAll("button")].find(
    (b) => b.textContent === "Show all decisions",
  )!;
  await act(async () => all.click());
  expect(current.get("tab")).toBe("all");
});

it("explains a Timeline with no repositories", async () => {
  answers["api/timeline/repositories"] = { repositories: [] };
  await show();
  expect(node.textContent).toContain("No repositories with decisions");
});

it("opens the feature a merged one went into", async () => {
  await show("page=timeline&feature=F0");
  await act(async () => {});
  expect(current.get("feature")).toBe("F1");
});

it("groups decisions by the day they were made", () => {
  const later = { ...decision, id: "d2", decided_at: "2026-10-21T10:00:00Z" };
  expect(daySections([later, decision]).map((s) => s.decisions.map((d) => d.id))).toEqual([
    ["d2"],
    ["d1"],
  ]);
});
