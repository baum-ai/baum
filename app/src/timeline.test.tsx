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
import { jobReasonText, type Job } from "./api";
import { decisionSections, localDayStart } from "@baum/viewer/decision-log";
import { pullRequestHref, TimelinePage } from "./timeline";

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
const bodies: unknown[] = [];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  // The page brings an opened row into view, which jsdom does not draw.
  Element.prototype.scrollIntoView ??= () => {};
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
    vi.fn(async (path: string, init?: RequestInit) => {
      requests.push(path);
      if (init?.body) bodies.push(JSON.parse(String(init.body)));
      const key = path.replace("/s/test-ns/", "").split("?")[0]!;
      const answer = answers[key];
      if (typeof answer === "function") return (answer as () => Promise<Response>)();
      return key in answers ? Response.json(answer) : new Response("missing", { status: 404 });
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});

let current = new URLSearchParams();
let jobsChanged = 0;
function Harness({
  initial,
  revision = 0,
  jobs = [],
}: {
  initial: string;
  revision?: number;
  jobs?: Job[];
}) {
  const [search, setSearch] = useState(() => new URLSearchParams(initial));
  current = search;
  return (
    <ViewerLinkProvider link={Link}>
      <UrlStateProvider
        value={{ search, update: (patch) => setSearch((s) => patch(new URLSearchParams(s))) }}
      >
        <TimelinePage
          revision={revision}
          jobs={jobs}
          cloneOf={(repository) => (repository === "acme/demo" ? "clone-1" : null)}
          jobsChanged={() => jobsChanged++}
        />
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
  expect(requests).toContain("/s/test-ns/api/timeline/features");
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
  expect(requests).toContain("/s/test-ns/api/timeline/decisions");
  expect(node.textContent).toContain("3 sessions read · 1 skipped (Too large: 1) · 1 being read");
  expect(node.textContent).toContain("Keep the timeline local");
  expect(node.textContent).toContain("A hosted-only page");
  expect(node.textContent).toContain("The session's transcript is no longer on this computer.");
  // A file narrows the list to the decisions citing it.
  const file = [...node.querySelectorAll("button")].find((b) =>
    b.textContent?.includes("oss/app/src/timeline.tsx"),
  )!;
  await act(async () => file.click());
  await act(async () => {});
  expect(requests.at(-1)).toBe(
    "/s/test-ns/api/timeline/decisions?file=oss%2Fapp%2Fsrc%2Ftimeline.tsx",
  );
  // All decisions offers the filters that pick decisions.
  expect(node.textContent).toContain("Decided by");
});

it("shows the prompts a decision cites", async () => {
  answers["api/timeline/decisions/d1"] = {
    ...decisionDetail,
    prompts_withheld: false,
    turns: [
      { ordinal: 2, triggered_at: "2026-10-20T09:58:00Z", text: "Keep it on this computer." },
    ],
  };
  await show("page=timeline&tab=all&open=d1");
  expect(node.textContent).toContain("Keep it on this computer.");
  expect(node.textContent).not.toContain("no longer on this computer");
});

it("opens a decision's session from the Timeline's own route", async () => {
  answers["api/timeline/decisions/d1"] = { ...decisionDetail, prompts_withheld: false };
  answers["api/timeline/sessions/claude-code%3As1/transcript"] = {
    available: false,
    live: false,
    code: "transcript_unavailable",
    message: "The transcript file is not there any more.",
  };
  await show("page=timeline&tab=all&open=d1");
  const open = [...node.querySelectorAll("button")].find(
    (b) => b.textContent === "Open session at turn 2",
  )!;
  await act(async () => open.click());
  await act(async () => {});
  expect(requests).toContain("/s/test-ns/api/timeline/sessions/claude-code%3As1/transcript");
  expect(node.textContent).toContain("The transcript file is not there any more.");
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

it("shows every repository at once, or those ticked, each row naming its own", async () => {
  answers["api/timeline/repositories"] = {
    repositories: [
      { repository: "acme/demo", has_features: true },
      { repository: "acme/other", has_features: false },
    ],
  };
  const other = {
    ...feature,
    id: "F2",
    title: "Other work",
    repository: { id: "acme/other", full_name: "acme/other" },
  };
  answers["api/timeline/features"] = {
    features: [{ ...feature, repository: { id: "acme/demo", full_name: "acme/demo" } }, other],
    next_cursor: null,
  };
  await show();
  expect(requests).toContain("/s/test-ns/api/timeline/features");
  expect(requests.some((path) => path.includes("api/timeline/build"))).toBe(false);
  const list = node.querySelector('ol[aria-label="Features"]')!;
  expect(list.textContent).toContain("acme/other");
  expect(
    node.querySelector('[aria-label="Repositories without feature history"]')?.textContent,
  ).toContain("acme/other has no feature history yet.");
  expect(node.querySelector("summary")?.textContent).toBe("All repositories");
  const box = [...node.querySelectorAll("label")]
    .find((l) => l.textContent === "acme/other")!
    .querySelector("input")!;
  await act(async () => box.click());
  await act(async () => {});
  expect(current.getAll("repo")).toEqual(["acme/demo"]);
  expect(requests).toContain("/s/test-ns/api/timeline/features?repository=acme%2Fdemo");
  expect(requests).toContain("/s/test-ns/api/timeline/build?repository=acme%2Fdemo");
  // The one box left ticked cannot be cleared.
  const last = [...node.querySelectorAll("label")]
    .find((l) => l.textContent === "acme/demo")!
    .querySelector("input")!;
  expect(last.disabled).toBe(true);
});

it("filters with the shared controls, offering the tab's people and the cited files", async () => {
  answers["api/timeline/features/people"] = {
    people: [{ ...me, name: "Ada", login: "ada", feature_count: 2 }],
  };
  answers["api/timeline/files"] = {
    files: [{ path: "oss/app/", kind: "folder", decision_count: 3 }],
  };
  await show("page=timeline&person=me%40example.com&from=2026-10-01&to=2026-10-31&file=oss/app");
  expect(requests).toContain("/s/test-ns/api/timeline/features/people");
  const query = new URLSearchParams({
    file: "oss/app",
    person: "me@example.com",
    from: localDayStart("2026-10-01"),
    to: localDayStart("2026-10-31", 1),
  });
  expect(requests).toContain(`/s/test-ns/api/timeline/features?${query}`);
  // A pill per filter in force; the person's reads as their name.
  expect(node.textContent).toContain("Ada");
  const clear = [...node.querySelectorAll("button")].find(
    (b) => b.textContent === "Clear filters",
  )!;
  await act(async () => clear.click());
  await act(async () => {});
  expect(current.toString()).toBe("page=timeline");
  expect(requests.at(-1)).toBe("/s/test-ns/api/timeline/features");
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
  expect(
    decisionSections([later, decision], "newest").map((s) => s.decisions.map((d) => d.id)),
  ).toEqual([["d2"], ["d1"]]);
});

it("reads only the tab shown, and again with the rows it had when the log changes", async () => {
  await show();
  expect(requests.some((path) => path.includes("api/timeline/decisions"))).toBe(false);
  requests.length = 0;
  await act(async () => root.render(<Harness initial="page=timeline" revision={1} />));
  await act(async () => {});
  expect(requests).toContain("/s/test-ns/api/timeline/features");
  expect(requests.some((path) => path.includes("api/timeline/decisions"))).toBe(false);
});

it("says when the decision store or the decisions switch is off", async () => {
  answers["api/timeline/repositories"] = { available: false, code: "timeline_unavailable" };
  await show();
  expect(node.textContent).toContain("The Timeline is not available");
  await act(async () => root.unmount());
  root = createRoot(node);
  answers["api/timeline/repositories"] = {
    decisions_enabled: false,
    repositories: [],
    turned_off: [],
  };
  await show();
  expect(node.textContent).toContain("Decision capture is off");
  await act(async () => root.unmount());
  root = createRoot(node);
  answers["api/timeline/repositories"] = {
    decisions_enabled: true,
    repositories: [],
    turned_off: ["acme/demo"],
  };
  await show();
  expect(node.textContent).toContain("Decisions are turned off for acme/demo.");
});

it("reads again as many rows as were loaded when the log changes", async () => {
  const many = (from: number, n: number) =>
    Array.from({ length: n }, (_, i) => ({ ...feature, id: `F${from + i}`, key_decisions: [] }));
  answers["api/timeline/features"] = { features: many(0, 50), next_cursor: "1|F49" };
  await show();
  answers["api/timeline/features"] = { features: many(50, 20), next_cursor: null };
  const more = [...node.querySelectorAll("button")].find(
    (b) => b.textContent === "Load more features",
  )!;
  await act(async () => more.click());
  await act(async () => {});
  expect(node.querySelectorAll("li[data-feature]").length).toBe(70);
  requests.length = 0;
  answers["api/timeline/features"] = { features: many(0, 70), next_cursor: null };
  await act(async () => root.render(<Harness initial="page=timeline" revision={1} />));
  await act(async () => {});
  expect(requests.filter((path) => path.includes("features") && !path.includes("people"))).toEqual([
    "/s/test-ns/api/timeline/features?limit=70",
  ]);
  expect(node.querySelectorAll("li[data-feature]").length).toBe(70);
});

/** A response the test sends when it chooses. */
function deferred() {
  let send: (body: unknown) => void = () => {};
  const promise = new Promise<Response>((resolve) => {
    send = (body) => resolve(Response.json(body));
  });
  return { promise, send };
}

it("keeps the newest detail when an older one answers last", async () => {
  const old = deferred();
  answers["api/timeline/decisions/d1"] = () => old.promise;
  await show("page=timeline&tab=all&open=d1");
  // The log changes while the first read is under way; the second answers
  // first, with the newer detail.
  answers["api/timeline/decisions/d1"] = {
    ...decisionDetail,
    decisions: [{ ...decisionDetail.decisions[0]!, rejected_alternative: "The newer alternative" }],
  };
  await act(async () =>
    root.render(<Harness initial="page=timeline&tab=all&open=d1" revision={1} />),
  );
  await act(async () => {});
  expect(node.textContent).toContain("The newer alternative");
  await act(async () => old.send(decisionDetail));
  await act(async () => {});
  expect(node.textContent).toContain("The newer alternative");
  expect(node.textContent).not.toContain("A hosted-only page");
  expect(node.textContent).not.toContain("Loading this decision");
});

it("shows an opened feature the list does not hold, and one that is gone", async () => {
  answers["api/timeline/features/F9"] = {
    ...featureDetail,
    feature: { ...featureDetail.feature, id: "F9", title: "An older feature" },
  };
  await show("page=timeline&feature=F9");
  const opened = node.querySelector('section[aria-label="Opened feature"]');
  expect(opened?.textContent).toContain("An older feature");
  expect(opened?.textContent).toContain("Add the timeline page");
  await act(async () => root.unmount());
  root = createRoot(node);
  await show("page=timeline&feature=F404");
  expect(node.querySelector('section[aria-label="Opened feature"]')?.textContent).toContain(
    "This feature no longer exists.",
  );
  await act(async () => root.unmount());
  root = createRoot(node);
  answers["api/timeline/features/F8"] = { archived: true };
  await show("page=timeline&feature=F8");
  expect(node.querySelector('section[aria-label="Opened feature"]')?.textContent).toContain(
    "This feature is archived",
  );
});

it("drops rows a load of more brings after the list was read again", async () => {
  const many = (from: number, n: number) =>
    Array.from({ length: n }, (_, i) => ({ ...feature, id: `F${from + i}`, key_decisions: [] }));
  answers["api/timeline/features"] = { features: many(0, 50), next_cursor: "1|F49" };
  await show();
  const later = deferred();
  answers["api/timeline/features"] = () => later.promise;
  const more = [...node.querySelectorAll("button")].find(
    (b) => b.textContent === "Load more features",
  )!;
  await act(async () => more.click());
  // The filter changes and back while it is under way: the list is read
  // twice for the same query and revision it had, and those answers land
  // first.
  answers["api/timeline/features"] = { features: many(0, 50), next_cursor: "1|F49" };
  for (let i = 0; i < 2; i++) {
    const shipped = [...node.querySelectorAll("button[aria-pressed]")].find(
      (b) => b.textContent === "Shipped",
    ) as HTMLButtonElement;
    await act(async () => shipped.click());
    await act(async () => {});
  }
  expect(node.querySelectorAll("li[data-feature]").length).toBe(50);
  // Then the old load of more answers: only the read count tells it is
  // stale, and its rows are not appended.
  await act(async () => later.send({ features: many(50, 20), next_cursor: null }));
  await act(async () => {});
  expect(node.querySelectorAll("li[data-feature]").length).toBe(50);
  expect(node.textContent).toContain("Load more features");
});

it("reads more than 500 rows again in requests that follow the cursor", async () => {
  const many = (from: number, n: number) =>
    Array.from({ length: n }, (_, i) => ({ ...feature, id: `F${from + i}`, key_decisions: [] }));
  answers["api/timeline/features"] = { features: many(0, 50), next_cursor: "1|F49" };
  await show();
  answers["api/timeline/features"] = { features: many(50, 550), next_cursor: null };
  const more = [...node.querySelectorAll("button")].find(
    (b) => b.textContent === "Load more features",
  )!;
  await act(async () => more.click());
  await act(async () => {});
  expect(node.querySelectorAll("li[data-feature]").length).toBe(600);
  requests.length = 0;
  let page = 0;
  answers["api/timeline/features"] = async () =>
    Response.json(
      page++ === 0
        ? { features: many(0, 500), next_cursor: "1|F499" }
        : { features: many(500, 100), next_cursor: null },
    );
  await act(async () => root.render(<Harness initial="page=timeline" revision={1} />));
  await act(async () => {});
  await act(async () => {});
  expect(requests.filter((path) => path.includes("features") && !path.includes("people"))).toEqual([
    "/s/test-ns/api/timeline/features?limit=500",
    "/s/test-ns/api/timeline/features?limit=100&cursor=1%7CF499",
  ]);
  expect(node.querySelectorAll("li[data-feature]").length).toBe(600);
});

const buildJob = (state: string, calls = 3): Job => ({
  id: "job-1",
  clone: "clone-1",
  repository: "acme/demo",
  pr: 0,
  kind: "feature_build",
  state,
  phase: "placing",
  calls,
  tokens: 0,
  repairs: 0,
  actual_cost_usd: null,
});

it("builds a repository's feature history from the empty state after its estimate", async () => {
  answers["api/timeline/features"] = { features: [], next_cursor: null };
  answers["api/jobs/estimate"] = {
    id: "est-1",
    estimate_usd: 1.2,
    price_version: "v1",
    expires_at: "2026-10-28T12:00:00Z",
    items: [
      {
        clone: "clone-1",
        pr: 0,
        kind: "feature_build",
        build_pull_requests: 40,
        build_discovery_usd: 5,
        build_history_truncated: true,
        head_sha: "",
        backend: "claude",
        model: "claude-haiku-5-5",
        estimate_usd: 1.2,
        max_tokens: 0,
        max_calls: 0,
        requires_isolation_confirmation: false,
      },
    ],
  };
  answers["api/jobs"] = { jobs: [buildJob("queued", 0)] };
  bodies.length = 0;
  jobsChanged = 0;
  await show();
  const build = [...node.querySelectorAll("button")].find(
    (b) => b.textContent === "Build feature history",
  )!;
  await act(async () => build.click());
  await act(async () => {});
  expect(bodies[0]).toEqual({
    items: [{ clone: "clone-1", pr: 0, kind: "feature_build" }],
    mode: "single",
  });
  const dialog = node.querySelector("dialog")!;
  expect(dialog.textContent).toContain("40 to place (the most recently updated)");
  expect(dialog.textContent).toContain("About $1.20");
  // The whole history, with discovery first on a token budget.
  expect(dialog.textContent).toContain("baum first discovers this repository's features");
  expect(dialog.textContent).toContain("pull requests of its whole history");
  expect(dialog.textContent).toContain("its least recently updated pull requests are left out");
  expect(dialog.textContent).toContain(
    "feature discovery runs first with a token budget of what about $5.00 buys",
  );
  const confirm = [...dialog.querySelectorAll("button")].find(
    (b) => b.textContent === "Build feature history",
  )!;
  await act(async () => confirm.click());
  await act(async () => {});
  expect(bodies[1]).toEqual({ estimate_id: "est-1", confirm_estimate: true });
  expect(jobsChanged).toBe(1);
  const link = [...dialog.querySelectorAll("a")].find((a) => a.textContent === "View it in Jobs");
  expect(link?.getAttribute("href")).toBe("/s/test-ns/?page=jobs&job=job-1");
});

it("shows a build in progress above the features it keeps, and offers to resume one that stopped", async () => {
  answers["api/timeline/build"] = {
    building: { placed: 12, total: 40, stopped: "", spent_usd: 0.4 },
  };
  await act(async () =>
    root.render(<Harness initial="page=timeline" jobs={[buildJob("running")]} />),
  );
  await act(async () => {});
  await act(async () => {});
  expect(node.textContent).toContain("Building feature history… 12 of 40 pull requests placed");
  expect(node.textContent).toContain("Local timeline");
  expect(node.textContent).not.toContain("Rebuild feature history");
  answers["api/timeline/build"] = {
    building: { placed: 30, total: 40, stopped: "backend_unavailable", spent_usd: 1.5 },
  };
  await act(async () =>
    root.render(
      <Harness
        initial="page=timeline"
        jobs={[{ ...buildJob("failed"), reason: "backend_unavailable" }]}
      />,
    ),
  );
  await act(async () => {});
  await act(async () => {});
  expect(node.textContent).toContain(
    "The feature history build stopped. The model backend is not available on this computer.",
  );
  expect(node.textContent).toContain("30 of 40 pull requests placed, $1.50 spent");
  expect([...node.querySelectorAll("button")].some((b) => b.textContent === "Resume build")).toBe(
    true,
  );
  expect(node.textContent).toContain("Local timeline");
});

it("offers a quieter rebuild when the repository has features, whatever the filters", async () => {
  answers["api/timeline/build"] = { building: null, has_features: true };
  answers["api/timeline/features"] = { features: [], next_cursor: null };
  await show("page=timeline&status=abandoned");
  expect(
    [...node.querySelectorAll("button")].some((b) => b.textContent === "Rebuild feature history"),
  ).toBe(true);
});

const estimateOf = (item: Record<string, unknown>) => ({
  id: "est-1",
  estimate_usd: 1.2,
  price_version: "v1",
  expires_at: "2026-10-28T12:00:00Z",
  items: [
    {
      clone: "clone-1",
      pr: 0,
      kind: "feature_build",
      build_pull_requests: 40,
      head_sha: "",
      backend: "claude",
      model: "claude-haiku-5-5",
      estimate_usd: 1.2,
      max_tokens: 0,
      max_calls: 0,
      requires_isolation_confirmation: false,
      ...item,
    },
  ],
});

const button = (label: string, within: ParentNode = node) =>
  [...within.querySelectorAll("button")].find((b) => b.textContent === label);

it("says why a build cannot run, in the build's words", async () => {
  answers["api/timeline/features"] = { features: [], next_cursor: null };
  answers["api/jobs/estimate"] = estimateOf({
    blocked_reason: "unknown_model_price",
    estimate_usd: null,
  });
  await show();
  await act(async () => button("Build feature history")!.click());
  await act(async () => {});
  const dialog = node.querySelector("dialog")!;
  expect(dialog.textContent).toContain("so the build's cost cannot be estimated");
  expect(button("Build feature history", dialog)).toBeUndefined();
  expect(button("Close", dialog)).toBeDefined();
});

it("estimates again when starting a build is refused", async () => {
  answers["api/timeline/features"] = { features: [], next_cursor: null };
  answers["api/jobs/estimate"] = estimateOf({});
  answers["api/jobs"] = async () => new Response("estimate expired", { status: 409 });
  bodies.length = 0;
  await show();
  await act(async () => button("Build feature history")!.click());
  await act(async () => {});
  const dialog = node.querySelector("dialog")!;
  await act(async () => button("Build feature history", dialog)!.click());
  await act(async () => {});
  expect(dialog.textContent).toContain("estimate expired");
  expect(button("Build feature history", dialog)).toBeUndefined();
  await act(async () => button("Estimate again", dialog)!.click());
  await act(async () => {});
  expect(bodies.filter((body) => JSON.stringify(body).includes("feature_build"))).toHaveLength(2);
  expect(button("Build feature history", dialog)).toBeDefined();
  expect(dialog.textContent).not.toContain("estimate expired");
});

it("resumes a build that stopped, and says a failed or unexplained one in its own words", async () => {
  answers["api/timeline/build"] = {
    building: { placed: 30, total: 40, stopped: "backend_unavailable", spent_usd: 1.5 },
    has_features: true,
  };
  answers["api/jobs/estimate"] = estimateOf({ build_pull_requests: 10 });
  await show();
  await act(async () => button("Resume build")!.click());
  await act(async () => {});
  const dialog = node.querySelector("dialog")!;
  expect(dialog.textContent).toContain("Resume the feature history build");
  expect(button("Resume build", dialog)).toBeDefined();
  await act(async () => root.unmount());
  root = createRoot(node);
  // Its version records no reason: the failed job's is read.
  answers["api/timeline/build"] = {
    building: { placed: 30, total: 40, stopped: "", spent_usd: 1.5 },
    has_features: true,
  };
  await act(async () =>
    root.render(
      <Harness
        initial="page=timeline"
        jobs={[{ ...buildJob("failed"), reason: "backend_unsupported" }]}
      />,
    ),
  );
  await act(async () => {});
  await act(async () => {});
  expect(node.textContent).toContain("The configured backend cannot make the feature log's calls.");
  expect(button("Resume build")).toBeDefined();
  // A building version with no job claims neither.
  await act(async () => root.render(<Harness initial="page=timeline" />));
  await act(async () => {});
  await act(async () => {});
  expect(node.textContent).toContain("A feature history build is in progress or stopped");
  // Never a dead end: confirming estimates the rest and resumes it.
  expect(button("Resume build")).toBeDefined();
  // A cancelled build whose version records no reason reads as stopped.
  await act(async () =>
    root.render(<Harness initial="page=timeline" revision={1} jobs={[buildJob("cancelled")]} />),
  );
  await act(async () => {});
  await act(async () => {});
  expect(node.textContent).toContain(
    "The feature history build stopped. It was cancelled before it finished.",
  );
  expect(button("Resume build")).toBeDefined();
  // A build that ended with no version is shown until a newer version is
  // current, features or not.
  const lead = "The last feature history build stopped before it made a version.";
  const render = async (job: Job, build: Record<string, unknown>, revision: number) => {
    answers["api/timeline/build"] = { building: null, ...build };
    await act(async () =>
      root.render(<Harness initial="page=timeline" revision={revision} jobs={[job]} />),
    );
    await act(async () => {});
    await act(async () => {});
  };
  const failed = { ...buildJob("failed"), reason: "disabled", updated_at: "2026-10-28T10:00:00Z" };
  await render(failed, { has_features: false, current_since: null }, 2);
  expect(node.textContent).toContain(
    `${lead} Decisions are turned off for this repository, so it cannot be built.`,
  );
  expect(button("Try again")).toBeDefined();
  // A Rebuild that stopped shows beside the features it kept.
  await render(failed, { has_features: true, current_since: "2026-10-01T00:00:00Z" }, 3);
  expect(node.textContent).toContain(lead);
  // At the very time the version became current, it is not older.
  await render(failed, { has_features: true, current_since: "2026-10-28T10:00:00Z" }, 4);
  expect(node.textContent).toContain(lead);
  await render(failed, { has_features: true, current_since: "2026-10-28T11:00:00Z" }, 5);
  expect(node.textContent).not.toContain(lead);
  // An interrupted one, in its words.
  const interrupted = {
    ...buildJob("interrupted"),
    reason: "interrupted",
    updated_at: failed.updated_at,
  };
  await render(interrupted, { has_features: false, current_since: null }, 6);
  expect(node.textContent).toContain(`${lead} baum stopped before the build finished.`);
  // A job with no time is shown without a current version, and not with one.
  const untimed = { ...buildJob("failed"), reason: "failed" };
  await render(untimed, { has_features: false, current_since: null }, 7);
  expect(node.textContent).toContain(`${lead} It failed before it finished.`);
  await render(untimed, { has_features: true, current_since: "2026-10-01T00:00:00Z" }, 8);
  expect(node.textContent).not.toContain(lead);
});

it("names a feature build's reasons in its own words, and others' as before", () => {
  expect(jobReasonText("failed", "feature_build")).toBe("It failed before it finished.");
  expect(jobReasonText("interrupted", "feature_build")).toBe(
    "baum stopped before the build finished.",
  );
  expect(jobReasonText("interrupted")).toBe("The run stopped before it finished.");
  expect(jobReasonText("daily_cap")).toBe("daily cap");
});

it("keeps the build state shown when a read of it fails", async () => {
  answers["api/timeline/build"] = {
    building: { placed: 30, total: 40, stopped: "backend_unavailable", spent_usd: 1.5 },
    has_features: true,
  };
  await show();
  expect(node.textContent).toContain("The model backend is not available on this computer.");
  answers["api/timeline/build"] = async () => new Response("busy", { status: 500 });
  await act(async () => root.render(<Harness initial="page=timeline" revision={1} />));
  await act(async () => {});
  expect(node.textContent).toContain("The model backend is not available on this computer.");
});
