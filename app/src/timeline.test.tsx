import { mockStateFetch } from "./ui-state-test-helper";
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
import { forgetLastRows } from "./shortcuts";
import { PageRefreshProvider, usePageRefresh } from "./shell";

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
  localStorage.clear();
  requests.length = 0;
  clones = { "acme/demo": "clone-1" };
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
    mockStateFetch(async (path: string, init?: RequestInit) => {
      requests.push(path);
      if (init?.body) bodies.push(JSON.parse(String(init.body)));
      const key = path.replace("/s/test-ns/", "").split("?")[0]!;
      const answer = answers[key];
      if (typeof answer === "function")
        return (answer as (init?: RequestInit) => Promise<Response>)(init);
      return key in answers ? Response.json(answer) : new Response("missing", { status: 404 });
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});

let clones: Record<string, string> = {};
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
          cloneOf={(repository) => clones[repository] ?? null}
          jobsChanged={() => jobsChanged++}
        />
      </UrlStateProvider>
    </ViewerLinkProvider>
  );
}

/** Writes the Timeline's filter into the query box in the filter menu and
 *  applies it, as Enter does. */
async function filterTo(text: string) {
  if (!node.querySelector(".filter-menu-panel")) {
    const menu = node.querySelector<HTMLDetailsElement>("details.filter-menu")!;
    await act(async () => {
      menu.open = true;
      menu.dispatchEvent(new Event("toggle"));
    });
  }
  // An empty filter shows its query box once "Write a query…" asks for it.
  const write = node.querySelector<HTMLElement>(".filter-menu-write");
  if (!node.querySelector(".filter-menu-query textarea") && write)
    await act(async () => write.click());
  const box = node.querySelector<HTMLTextAreaElement>(".filter-menu-query textarea")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, text);
    box.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await act(async () => {
    box.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }),
    );
  });
  await act(async () => {});
}

async function show(initial = "page=timeline") {
  await act(async () => root.render(<Harness initial={initial} />));
  await act(async () => {});
  await act(async () => {});
}

it("moves through the features with the list keys and opens one", async () => {
  answers["api/timeline/features"] = {
    features: [feature, { ...feature, id: "F0", title: "Earlier work" }],
    next_cursor: null,
  };
  await show();
  const key = (k: string) =>
    act(async () => {
      (document.activeElement ?? window).dispatchEvent(
        new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }),
      );
    });
  const focusedFeature = () =>
    document.activeElement?.closest<HTMLElement>("[data-feature]")?.dataset.feature;
  await key("j");
  expect(focusedFeature()).toBe("F1");
  await key("j");
  expect(focusedFeature()).toBe("F0");
  await key("k");
  expect(focusedFeature()).toBe("F1");
  expect(node.querySelector("[data-focused]")?.getAttribute("data-row-id")).toBe("F1");
  await key("o");
  await act(async () => {});
  expect(current.get("feature")).toBe("F1");
  forgetLastRows();
});

it("goes on from a feature the pointer opened", async () => {
  answers["api/timeline/features"] = {
    features: [feature, { ...feature, id: "F0", title: "Earlier work" }],
    next_cursor: null,
  };
  await show();
  const earlier = [...node.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")].find((b) =>
    b.textContent?.includes("Earlier work"),
  )!;
  await act(async () => {
    earlier.focus();
    earlier.click();
  });
  await act(async () => {
    earlier.dispatchEvent(
      new KeyboardEvent("keydown", { key: "k", bubbles: true, cancelable: true }),
    );
  });
  expect(document.activeElement?.closest<HTMLElement>("[data-feature]")?.dataset.feature).toBe(
    "F1",
  );
  forgetLastRows();
});

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
  await filterTo("file=oss/app/src/timeline.tsx");
  expect(requests.at(-1)).toBe(
    "/s/test-ns/api/timeline/decisions?file=oss%2Fapp%2Fsrc%2Ftimeline.tsx",
  );
  // All decisions offers the filters that pick decisions.
  expect(node.querySelector(".filter-menu-rows")?.textContent).toContain("Decided by");
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
    repositories: [{ repository: "acme/demo" }, { repository: "acme/other" }],
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
  await filterTo("repo=acme/demo");
  expect(current.getAll("repo")).toEqual(["acme/demo"]);
  expect(requests).toContain("/s/test-ns/api/timeline/features?repository=acme%2Fdemo");
  expect(requests).toContain("/s/test-ns/api/timeline/build?repository=acme%2Fdemo");
  // A repository the Timeline does not serve is refused, not read as all.
  await filterTo("repo=acme/gone");
  expect(current.getAll("repo")).toEqual(["acme/demo"]);
  expect(node.querySelector('[role="status"].filter-bar-error')?.textContent).toContain(
    "repo takes acme/demo, acme/other.",
  );
});

it("filters with the Inbox's filter, offering the tab's people and the cited files", async () => {
  answers["api/timeline/features/people"] = {
    people: [{ ...me, name: "Ada", login: "ada", feature_count: 2 }],
  };
  answers["api/timeline/files"] = {
    files: [{ path: "oss/app/", kind: "folder", decision_count: 3 }],
  };
  await show("page=timeline&person=me%40example.com&from=2026-10-01&to=2026-10-31&file=oss/app");
  // Read once: the shared page's own person filter is not drawn here.
  expect(requests.filter((path) => path.endsWith("/features/people"))).toHaveLength(1);
  const query = new URLSearchParams({
    file: "oss/app",
    person: "me@example.com",
    from: localDayStart("2026-10-01"),
    to: localDayStart("2026-10-31", 1),
  });
  expect(requests).toContain(`/s/test-ns/api/timeline/features?${query}`);
  // The query reads back what the address holds.
  await filterTo("person=me@example.com file=oss/app date>=2026-10-01 date<=2026-10-31");
  const sorted = (params: URLSearchParams) => [...params].sort().map((one) => one.join("="));
  expect(sorted(current)).toEqual(
    sorted(
      new URLSearchParams(
        "page=timeline&file=oss/app&person=me@example.com&from=2026-10-01&to=2026-10-31",
      ),
    ),
  );
  // The menu offers the tab's people by name, and the cited files.
  const rows = node.querySelector(".filter-menu-rows")!;
  expect(rows.textContent).toContain("Author");
  expect(rows.textContent).toContain("File or folder");
  // Features offers no filter that picks decisions alone.
  expect(rows.textContent).not.toContain("Decided by");
  await filterTo("");
  expect(current.toString()).toBe("page=timeline");
  expect(requests.at(-1)).toBe("/s/test-ns/api/timeline/features");
});

it("explains a Timeline with no repositories", async () => {
  answers["api/timeline/repositories"] = { repositories: [] };
  await show();
  expect(node.textContent).toContain("No repositories with decisions");
});

it("says the Timeline could not be read, with one Retry that reads again", async () => {
  const good = answers["api/timeline/repositories"];
  answers["api/timeline/repositories"] = () =>
    Promise.resolve(Response.json({ message: "The daemon is not answering." }, { status: 500 }));
  await show();
  expect(node.textContent).toContain("Couldn't load the Timeline");
  expect(node.textContent).not.toContain("Loading the Timeline");
  expect(node.textContent).not.toContain("The Timeline is not available");
  const retry = [...node.querySelectorAll("button")].filter((b) => b.textContent === "Retry");
  expect(retry).toHaveLength(1);
  answers["api/timeline/repositories"] = good;
  await act(async () => retry[0]!.click());
  await act(async () => {});
  await act(async () => {});
  expect(node.textContent).not.toContain("Couldn't load the Timeline");
});

it("keeps the Timeline and says so when a later read of it fails", async () => {
  await show();
  expect(node.textContent).not.toContain("Couldn't");
  const good = answers["api/timeline/repositories"];
  answers["api/timeline/repositories"] = () =>
    Promise.resolve(Response.json({ message: "The daemon is not answering." }, { status: 500 }));
  await act(async () => root.render(<Harness initial="page=timeline" revision={1} />));
  await act(async () => {});
  await act(async () => {});
  expect(node.textContent).toContain("Couldn't read the Timeline again");
  expect(node.textContent).not.toContain("Couldn't load the Timeline");
  answers["api/timeline/repositories"] = good;
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

it("ends the refresh's loader once the open tab's list is read again, or fails to be", async () => {
  function Page() {
    const page = usePageRefresh("timeline", () => Promise.resolve());
    return (
      <PageRefreshProvider value={page}>
        <Harness initial="page=timeline" />
      </PageRefreshProvider>
    );
  }
  await act(async () => root.render(<Page />));
  await act(async () => {});
  const button = () => node.querySelector<HTMLButtonElement>('button[aria-label="Refresh"]')!;
  const refreshed = async () => {
    act(() => button().click());
    expect(button().getAttribute("aria-busy")).toBe("true");
    for (let i = 0; i < 4; i++) await act(async () => {});
    return button().getAttribute("aria-busy");
  };
  expect(await refreshed()).toBeNull();
  answers["api/timeline/features"] = () => Promise.resolve(new Response("down", { status: 500 }));
  expect(await refreshed()).toBeNull();
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
  await filterTo("status=shipped");
  await filterTo("");
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

it("starts a repository's feature history build at once from the empty state", async () => {
  answers["api/timeline/features"] = { features: [], next_cursor: null };
  answers["api/jobs"] = { jobs: [buildJob("queued", 0)] };
  bodies.length = 0;
  jobsChanged = 0;
  await show();
  const build = [...node.querySelectorAll("button")].find(
    (b) => b.textContent === "Build feature history",
  )!;
  await act(async () => build.click());
  await act(async () => {});
  // No estimate and no dialog: the job starts as every requested run does.
  expect(bodies).toEqual([
    { items: [{ clone: "clone-1", pr: 0, kind: "feature_build" }], mode: "single" },
  ]);
  expect(node.querySelector("dialog")).toBeNull();
  expect(jobsChanged).toBe(1);
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
  expect(node.textContent).toContain("30 of 40 pull requests placed, $1.50 at API prices so far");
  expect([...node.querySelectorAll("button")].some((b) => b.textContent === "Resume build")).toBe(
    true,
  );
  expect(node.textContent).toContain("Local timeline");
});

it("offers a quieter rebuild when the repository has features, whatever the filters", async () => {
  answers["api/timeline/build"] = { building: null, has_features: true };
  answers["api/timeline/features"] = { features: [], next_cursor: null };
  await show("page=timeline&status=abandoned");
  // In the filter bar's kebab menu, not the page bar.
  expect(
    node.querySelector('summary[aria-label="More"]')?.parentElement?.querySelector("button")
      ?.textContent,
  ).toBe("Rebuild feature history");
});

const rebuildItem = () =>
  node.querySelector('summary[aria-label="More"]')?.parentElement?.querySelector("button");
const twoRepositories = () => {
  answers["api/timeline/repositories"] = {
    repositories: [{ repository: "acme/demo" }, { repository: "acme/other" }],
  };
};

it("rebuilds every repository shown that has a clone, from the menu over several", async () => {
  twoRepositories();
  answers["api/jobs"] = { jobs: [buildJob("queued", 0)] };
  bodies.length = 0;
  jobsChanged = 0;
  await show();
  const item = rebuildItem()!;
  expect(item.disabled).toBe(false);
  expect(item.textContent).toContain("For 1 of 2 repositories; the others need a local clone");
  await act(async () => item.click());
  await act(async () => {});
  expect(bodies).toEqual([
    { items: [{ clone: "clone-1", pr: 0, kind: "feature_build" }], mode: "single" },
  ]);
  expect(jobsChanged).toBe(1);
});

it("starts the others when the daemon refuses one, naming the refused repository", async () => {
  twoRepositories();
  clones["acme/other"] = "clone-2";
  answers["api/jobs"] = async (init?: RequestInit) =>
    String(init?.body).includes("clone-2")
      ? Response.json({ blocked_reason: "backend_unavailable" }, { status: 409 })
      : Response.json({ jobs: [buildJob("queued", 0)] });
  bodies.length = 0;
  jobsChanged = 0;
  await show();
  expect(rebuildItem()!.textContent).toBe("Rebuild feature history");
  await act(async () => rebuildItem()!.click());
  await act(async () => {});
  expect(bodies).toHaveLength(2);
  expect(jobsChanged).toBe(1);
  expect(node.textContent).toContain(
    "Couldn't start the feature build: acme/other: The model backend is not available on this computer.",
  );
});

it("keeps the menu while a build runs, with the rebuild disabled and saying why", async () => {
  twoRepositories();
  await act(async () =>
    root.render(<Harness initial="page=timeline" jobs={[buildJob("running")]} />),
  );
  await act(async () => {});
  await act(async () => {});
  const item = rebuildItem()!;
  expect(item.disabled).toBe(true);
  expect(item.title).toBe("A feature build is running for acme/demo. Rebuild when it finishes.");
  expect(item.textContent).toContain("A feature build is running for acme/demo.");
});

it("keeps one repository's menu while its rebuild runs, disabled", async () => {
  answers["api/timeline/build"] = { building: null, has_features: true };
  await act(async () =>
    root.render(<Harness initial="page=timeline" jobs={[buildJob("running")]} />),
  );
  await act(async () => {});
  await act(async () => {});
  const item = rebuildItem()!;
  expect(item.disabled).toBe(true);
  expect(item.title).toBe("A feature build is running. Rebuild when it finishes.");
});

it("reads the lists again when any repository's build finishes over several", async () => {
  twoRepositories();
  clones["acme/other"] = "clone-2";
  const other = { ...buildJob("running"), id: "job-2", clone: "clone-2", repository: "acme/other" };
  await act(async () =>
    root.render(<Harness initial="page=timeline" jobs={[buildJob("running")]} />),
  );
  await act(async () => {});
  await act(async () => {});
  requests.length = 0;
  // acme/demo finishes as acme/other's starts: still one running.
  await act(async () =>
    root.render(
      <Harness
        initial="page=timeline"
        jobs={[buildJob("completed"), { ...other, state: "queued" }]}
      />,
    ),
  );
  await act(async () => {});
  expect(requests).toContain("/s/test-ns/api/timeline/features");
});

it("disables the rebuild over several repositories when none has a clone", async () => {
  twoRepositories();
  clones = {};
  await show();
  const item = rebuildItem()!;
  expect(item.disabled).toBe(true);
  expect(item.title).toBe("None of these repositories has a local clone");
});

const button = (label: string, within: ParentNode = node) =>
  [...within.querySelectorAll("button")].find((b) => b.textContent === label);

it("says why a build cannot start, in the build's words", async () => {
  answers["api/timeline/features"] = { features: [], next_cursor: null };
  answers["api/jobs"] = async () =>
    Response.json({ blocked_reason: "backend_unavailable" }, { status: 409 });
  await show();
  await act(async () => button("Build feature history")!.click());
  await act(async () => {});
  expect(node.textContent).toContain(
    "Couldn't start the feature build: The model backend is not available on this computer.",
  );
  // It can be asked for again.
  expect(button("Build feature history")).toBeDefined();
});

it("resumes a build that stopped, and says a failed or unexplained one in its own words", async () => {
  answers["api/timeline/build"] = {
    building: { placed: 30, total: 40, stopped: "backend_unavailable", spent_usd: 1.5 },
    has_features: true,
  };
  answers["api/jobs"] = { jobs: [buildJob("queued", 0)] };
  bodies.length = 0;
  await show();
  await act(async () => button("Resume build")!.click());
  await act(async () => {});
  // Resuming starts the same build again at once.
  expect(bodies).toEqual([
    { items: [{ clone: "clone-1", pr: 0, kind: "feature_build" }], mode: "single" },
  ]);
  expect(node.querySelector("dialog")).toBeNull();
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
  // Never a dead end: starting it again resumes it.
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
  // One whose fetch could not sign in says how to make the key available.
  const signIn = { ...failed, reason: "git_auth_ssh" };
  await render(signIn, { has_features: false, current_since: null }, 60);
  expect(node.textContent).toContain(
    `${lead} baum couldn't sign in over SSH to fetch pull requests.`,
  );
  expect(node.textContent).toContain("add your key to your SSH agent (ssh-add)");
  // A job with no time is shown without a current version, and not with one.
  const untimed = { ...buildJob("failed"), reason: "failed" };
  await render(untimed, { has_features: false, current_since: null }, 7);
  expect(node.textContent).toContain(`${lead} It failed before it finished.`);
  await render(untimed, { has_features: true, current_since: "2026-10-01T00:00:00Z" }, 8);
  expect(node.textContent).not.toContain(lead);
});

it("says nothing of a cancelled build, and lets a stopped one be closed until the next", async () => {
  const lead = "The last feature history build stopped before it made a version.";
  answers["api/timeline/build"] = { building: null, has_features: false, current_since: null };
  const render = async (job: Job, revision: number) => {
    await act(async () =>
      root.render(<Harness initial="page=timeline" revision={revision} jobs={[job]} />),
    );
    await act(async () => {});
    await act(async () => {});
  };
  // A running build the person cancels ends cancelled with the reason
  // interrupted; a daemon stop leaves it interrupted, which shows.
  await render({ ...buildJob("cancelled"), reason: "interrupted" }, 1);
  expect(node.textContent).not.toContain(lead);
  const failed = { ...buildJob("failed"), reason: "failed" };
  await render(failed, 2);
  expect(node.textContent).toContain(lead);
  await act(async () =>
    node.querySelector<HTMLButtonElement>('button[aria-label="Dismiss"]')!.click(),
  );
  expect(node.textContent).not.toContain(lead);
  await render(failed, 3);
  expect(node.textContent).not.toContain(lead);
  await render({ ...failed, id: "later" }, 4);
  expect(node.textContent).toContain(lead);
});

it("names a feature build's reasons in its own words, and others' as before", () => {
  expect(jobReasonText("failed", "feature_build")).toBe("It failed before it finished.");
  expect(jobReasonText("interrupted", "feature_build")).toBe(
    "baum stopped before the build finished.",
  );
  expect(jobReasonText("interrupted")).toBe("The run stopped before it finished.");
  expect(jobReasonText("daily_cap")).toBe("It stopped before it started.");
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

it("focuses the search box with /, which the shortcuts sheet lists", async () => {
  await show();
  await act(async () => {
    window.dispatchEvent(
      new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true }),
    );
  });
  expect(document.activeElement?.getAttribute("placeholder")).toBe("Search features");
  expect(node.querySelector(".timeline-search kbd.key-hint")?.textContent).toBe("/");
});

it("shows the open tab's own filter after the tab changes", async () => {
  await show();
  await filterTo("status=shipped");
  expect(current.get("status")).toBe("shipped");
  const all = [...node.querySelectorAll<HTMLElement>('[role="tab"]')].find(
    (tab) => tab.textContent === "All decisions",
  )!;
  await act(async () => all.click());
  await act(async () => {});
  const box = node.querySelector<HTMLTextAreaElement>(".filter-menu-query textarea");
  expect(box?.value ?? "").toBe("");
  forgetLastRows();
});
