import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { inboxGroups } from "./inbox";
import { App } from "./app";
import { appBase, cost, filtered, type Entry, type Clone } from "./api";
const entry = (number: number, status = "no_review", historical = false): Entry => ({
  pull_request: {
    repository: "acme/demo",
    number,
    title: `Change ${number}`,
    url: `https://github.com/acme/demo/pull/${number}`,
    head_sha: "head",
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: number === 2,
    review_requested_from_me: number === 1,
    author_has_write: true,
    team_membership_known: true,
    review: "",
    checks: "",
    merge: "",
    unresolved: null,
    unresolved_capped: false,
  },
  clone_id: "clone",
  status,
  version_count: status === "no_review" ? 0 : 1,
  latest: null,
  risk_score: { score: 5, status: "assessed" },
  historical,
  stale: false,
  review: "",
  checks: "",
  merge: "",
  unresolved: null,
  unresolved_capped: false,
  review_minutes: null,
});
let root: Root;
let node: HTMLDivElement;
let fail = false;
let entries: Entry[] = [];
let streams: string[] = [];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  // Every open pull request; the default Inbox view has its own test.
  history.replaceState(null, "", "/s/test-ns/?page=pulls&filter=all");
  localStorage.clear();
  fail = false;
  entries = [];
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  streams = [];
  vi.stubGlobal(
    "EventSource",
    class {
      constructor(url: string) {
        streams.push(url);
      }
      addEventListener() {}
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      // As on the server, the session authorizes nothing outside its namespace.
      if (fail || !path.startsWith("/s/test-ns/"))
        return new Response("unauthorized", { status: 401 });
      return Response.json(
        path === "/s/test-ns/api/github-auth"
          ? { state: "disconnected", host: "github.com", saved: false, override: false }
          : path.includes("/s/test-ns/api/index")
            ? { entries, problems: [] }
            : path === "/s/test-ns/api/repos" || path === "/s/test-ns/api/repositories"
              ? []
              : path === "/s/test-ns/api/prs"
                ? []
                : path === "/s/test-ns/api/jobs"
                  ? { jobs: [] }
                  : {},
      );
    }),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  node.remove();
  vi.unstubAllGlobals();
});
it("shows a slow inbox response while coalescing refresh events into a follow-up read", async () => {
  const listeners = new Map<string, () => void>();
  vi.stubGlobal(
    "EventSource",
    class {
      addEventListener(name: string, listener: () => void) {
        listeners.set(name, listener);
      }
      close() {}
    },
  );
  const original = vi.mocked(fetch).getMockImplementation()!;
  const responses: ((value: Response) => void)[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn((path: string) =>
      path.includes("api/index")
        ? new Promise<Response>((resolve) => responses.push(resolve))
        : original(path),
    ),
  );
  const missing = { ...entry(42), clone_id: undefined, needs_clone: true };
  await act(async () => root.render(<App />));
  expect(responses).toHaveLength(1);
  await act(async () => {
    for (let i = 0; i < 5; i++) listeners.get("index")!();
  });
  expect(responses).toHaveLength(1);
  await act(async () => responses[0]!(Response.json({ entries: [missing], problems: [] })));
  expect(node.textContent).toContain("Change 42");
  expect(node.textContent).toContain("Needs a local clone");
  expect(responses).toHaveLength(2);
  await act(async () => responses[1]!(Response.json({ entries: [], problems: [] })));
  expect(node.textContent).not.toContain("Change 42");
});
it("keeps API, event and navigation URLs under the session namespace", async () => {
  entries = [entry(1)];
  await act(async () => root.render(<App />));
  expect(appBase("/s/abc_-123/")).toBe("/s/abc_-123/");
  expect(appBase("/s/abc/?page=jobs")).toBe("/s/abc/");
  expect(appBase("/")).toBe("/");
  expect(vi.mocked(fetch).mock.calls.map(([url]) => url)).not.toHaveLength(0);
  for (const [url] of vi.mocked(fetch).mock.calls) expect(url).toMatch(/^\/s\/test-ns\/api\//);
  expect(streams).toEqual(["/s/test-ns/api/events"]);
  const hrefs = Array.from(node.querySelectorAll("a"))
    .map((a) => a.getAttribute("href")!)
    .filter((href) => !href.startsWith("https:"));
  expect(hrefs).toContain("/s/test-ns/?page=jobs");
  for (const href of hrefs) expect(href.startsWith("/s/test-ns/")).toBe(true);
  const jobs = Array.from(node.querySelectorAll("a")).find((a) => a.textContent === "Jobs")!;
  await act(async () => jobs.click());
  expect(location.pathname).toBe("/s/test-ns/");
  expect(location.search).toBe("?page=jobs");
  expect(node.textContent).toContain("No jobs yet");
  await act(async () => {
    Array.from(node.querySelectorAll("a"))
      .find((a) => a.textContent === "Pull requests")!
      .click();
  });
  await act(async () => {
    [...node.querySelectorAll<HTMLButtonElement>('[aria-label="Show"] button')]
      .find((button) => button.textContent?.startsWith("Mine"))!
      .click();
  });
  expect(location.pathname).toBe("/s/test-ns/");
  expect(location.search).toBe("?page=pulls&filter=mine");
});
it("shows actionable launch authorization failure", async () => {
  fail = true;
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Open this page from the baum desktop app");
  expect(node.textContent).not.toContain("Generate selected");
});
it("keeps generating and failed indications without old generation columns", async () => {
  entries = [entry(1, "outdated"), entry(2, "failed"), entry(3, "current"), entry(4, "generating")];
  await act(async () => root.render(<App />));
  const inbox = node.querySelector(".local-inbox")!;
  expect(inbox.textContent).toContain("Review failed");
  expect(inbox.textContent).toContain("Generating review");
  expect(inbox.textContent).not.toMatch(/outdated|current|Generated|Cost|v1|—/);
});
it("shows GitHub states that ask for something, one merge status, and nothing for normal states", async () => {
  entries = [entry(1), entry(2), entry(3), entry(4), entry(5), entry(6)];
  const values = [
    { review: "approved", checks: "passing", merge: "ready", unresolved: 0, review_minutes: 1 },
    {
      review: "changes_requested",
      checks: "failing",
      merge: "conflict",
      unresolved: 3,
      review_minutes: 240,
    },
    {
      review: "commented",
      checks: "running",
      merge: "blocked",
      unresolved: 6,
      unresolved_capped: true,
    },
    { review: "unreviewed", checks: "none", merge: "draft" },
    { review: "unreviewed", checks: "failing", merge: "blocked" },
  ] as const;
  values.forEach((value, index) => Object.assign(entries[index]!, value));
  await act(async () => root.render(<App />));
  const rows = Array.from(node.querySelectorAll<HTMLElement>(".inbox-row"));
  expect(node.querySelector(".inbox-columns-head")!.textContent).toContain(
    "RiskPull requestReview timeReviewStatus",
  );
  const states = rows.map((row) =>
    Array.from(row.querySelectorAll<HTMLElement>(".inbox-status")).map((cell) => cell.textContent),
  );
  // A ready pull request also carries its Merge button.
  // Its key hint is drawn inside it, shown only while Alt is held.
  expect(states[0]).toEqual(["Review time: ~1 min", "Review: Approved", "Status: ReadyMergem"]);
  expect(states[1]).toEqual([
    "Review time: ~240 min",
    "Review: Changes requested3 unresolved",
    "Status: Conflict",
  ]);
  expect(states[2]).toEqual([
    "Review time: not estimated",
    "Review: Commented6+ unresolved",
    "Status: Checks running",
  ]);
  expect(states[3]).toEqual(["Review time: not estimated", "Review: Unreviewed", "Status: Draft"]);
  expect(states[4]![2]).toBe("Status: Checks failing");
  expect(states[5]).toEqual(["Review time: not estimated", "Review: unknown", "Status: unknown"]);
  const cells = rows.map((row) => Array.from(row.querySelectorAll<HTMLElement>(".inbox-status")));
  // Normal states draw nothing; their words are for screen readers only.
  expect(cells[3]![1]!.className).toContain("inbox-status-empty");
  expect(cells[1]![2]!.className).toContain("status-critical");
  expect(cells[1]![2]!.title).toBe("Checks failing, merge conflict");
  expect(cells[2]![2]!.className).toContain("status-caution");
  expect(cells[0]![2]!.className).toContain("status-positive");
  expect(rows[0]!.querySelector(".inbox-threads")).toBeNull();
  expect(rows[2]!.querySelector<HTMLElement>(".inbox-threads")!.title).toContain(
    "At least 6 unresolved threads; only the first 100",
  );
});
it.each([
  { stale: true, error: undefined, remaining: 5000, note: null },
  {
    stale: true,
    error: "offline",
    remaining: 4999,
    note: "GitHub is not connected, so baum cannot refresh pull requests.",
  },
  {
    stale: false,
    error: undefined,
    remaining: 0,
    note: "GitHub's request limit for acme/demo is used up until",
  },
])(
  "shows refresh notes only after failure or allowance exhaustion: %j",
  async ({ stale, error, remaining, note }) => {
    entries = [{ ...entry(1), stale }];
    const original = vi.mocked(fetch).getMockImplementation()!;
    vi.mocked(fetch).mockImplementation((...args) =>
      String(args[0]) === "/s/test-ns/api/prs"
        ? Promise.resolve(
            Response.json([
              {
                repository: "acme/demo",
                stale,
                error,
                rate: { remaining, reset: "2026-10-23T00:00:00Z" },
              },
            ]),
          )
        : original(...args),
    );
    await act(async () => root.render(<App />));
    expect(node.textContent).toContain("Change 1");
    expect(node.textContent).not.toContain("Stale discovery");
    expect(node.textContent).not.toContain("Cached discovery is stale");
    if (note) expect(node.textContent).toContain(note);
    else expect(node.textContent).not.toContain("showing cached data");
  },
);
it("filters by requested, authored, review state, repository and saved history", () => {
  const rows = [entry(1, "outdated"), entry(2), entry(3, "current", true)];
  expect(filtered(rows, "requested", "").map((e) => e.pull_request.number)).toEqual([1]);
  expect(filtered(rows, "mine", "").map((e) => e.pull_request.number)).toEqual([2]);
  expect(filtered(rows, "outdated", "").length).toBe(1);
  expect(filtered(rows, "no_review", "").length).toBe(1);
  expect(filtered(rows, "all", "other/repo")).toEqual([]);
  expect(filtered(rows, "history", "").map((e) => e.pull_request.number)).toEqual([3]);
  expect(cost(null)).toBe("—");
  expect(cost(0)).toBe("$0.0000");
});
it("selects multiple rows before generation", async () => {
  entries = [entry(1), entry(2)];
  await act(async () => root.render(<App />));
  const box = node.querySelector<HTMLInputElement>(
    'input[aria-label="Select all visible pull requests"]',
  )!;
  await act(async () => box.click());
  expect(node.textContent).toContain("Generate selected (2)");
});

/** Answers POST api/jobs as the daemon would, recording each request. */
function stubGenerate(needsIsolation: boolean) {
  const original = vi.mocked(fetch).getMockImplementation()!;
  const posts: unknown[] = [];
  vi.mocked(fetch).mockImplementation((...args) => {
    const init = args[1] as RequestInit | undefined;
    if (String(args[0]) === "/s/test-ns/api/jobs" && init?.method === "POST") {
      const body = JSON.parse(String(init.body));
      posts.push(body);
      return Promise.resolve(
        needsIsolation && !body.confirm_isolation
          ? Response.json({ isolation_required: true }, { status: 409 })
          : Response.json({ jobs: [] }),
      );
    }
    return original(...args);
  });
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.open = true;
    },
  });
  return posts;
}
const generateSelected = async () => {
  await act(async () =>
    node
      .querySelector<HTMLInputElement>('input[aria-label="Select all visible pull requests"]')!
      .click(),
  );
  await act(async () =>
    Array.from(node.querySelectorAll("button"))
      .find((b) => b.textContent?.includes("Generate selected"))!
      .click(),
  );
};
it("generates the selected reviews at once, without a confirmation", async () => {
  entries = [entry(1), entry(2)];
  const posts = stubGenerate(false);
  await act(async () => root.render(<App />));
  await generateSelected();
  expect(posts).toEqual([
    {
      items: [
        { clone: "clone", pr: 1 },
        { clone: "clone", pr: 2 },
      ],
      mode: "batch",
      confirm_isolation: false,
    },
  ]);
  expect(node.querySelector("dialog")).toBeNull();
});
it("asks before running an agent on a fork, and generates once confirmed", async () => {
  entries = [entry(1)];
  const posts = stubGenerate(true);
  await act(async () => root.render(<App />));
  await generateSelected();
  expect(node.querySelector("dialog")?.textContent).toContain(
    "Review code from outside your team?",
  );
  await act(async () =>
    Array.from(node.querySelectorAll<HTMLButtonElement>("dialog button"))
      .find((b) => b.textContent === "Generate anyway")!
      .click(),
  );
  expect(posts.map((p) => (p as { confirm_isolation: boolean }).confirm_isolation)).toEqual([
    false,
    true,
  ]);
  expect(node.querySelector("dialog")).toBeNull();
});
it("groups requested PRs first, splits the person's own by what they need, and keeps saved history separate", () => {
  const requested = entry(1);
  requested.pull_request.authored_by_me = true;
  requested.merge = "ready";
  const mine = entry(2);
  mine.merge = "blocked";
  const other = entry(3);
  const history = entry(4, "current", true);
  const older = entry(5, "current", true);
  const ready = entry(6);
  ready.merge = "ready";
  const failing = entry(7);
  failing.checks = "failing";
  const conflict = entry(8);
  conflict.merge = "conflict";
  const changes = entry(9);
  changes.review = "changes_requested";
  changes.merge = "ready";
  for (const row of [mine, ready, failing, conflict, changes])
    row.pull_request.authored_by_me = true;
  const at = (time: string) => ({
    version: 1,
    generated_at: time,
    backend: "claude",
    model: "m",
    recorded_cost_usd: null,
  });
  history.latest = at("2026-10-22T10:00:00+01:00");
  older.latest = at("2026-10-22T09:30:00Z");
  const groups = inboxGroups([
    older,
    history,
    other,
    mine,
    requested,
    ready,
    failing,
    conflict,
    changes,
  ]);
  expect(
    groups.map((group) => [group.id, group.rows.map((row) => row.pull_request.number)]),
  ).toEqual([
    ["requested", [1]],
    ["changes", [7, 8, 9]],
    ["ready", [6]],
    ["mine", [2]],
    ["other", [3]],
    // Most recently reviewed first, whatever the offset.
    ["history", [5, 4]],
  ]);
});
it("opens Pull requests on For you, with only review requests and the person's own, and the rest under Others", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=pulls");
  entries = [entry(1), entry(2), entry(3)];
  await act(async () => root.render(<App />));
  const titles = () =>
    Array.from(node.querySelectorAll(".inbox-pr-title")).map((link) => link.textContent);
  expect(titles()).toEqual(["Change 1", "Change 2"]);
  const tab = (name: string) =>
    Array.from(node.querySelectorAll<HTMLButtonElement>('[aria-label="Show"] button')).find(
      (button) => button.textContent?.startsWith(name),
    )!;
  expect(tab("For you").getAttribute("aria-pressed")).toBe("true");
  expect(tab("Others").textContent).toBe("Others1");
  await act(async () => tab("Others").click());
  expect(titles()).toEqual(["Change 3"]);
  entries = [entry(3)];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation((...args) =>
    String(args[0]) === "/s/test-ns/api/repos"
      ? Promise.resolve(Response.json([{ id: "clone", repository: "acme/demo", path: "/c" }]))
      : original(...args),
  );
  history.replaceState(null, "", "/s/test-ns/?page=pulls");
  await act(async () => root.unmount());
  root = createRoot(node);
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Nothing is waiting on you");
  expect(tab("For you").className).toContain("empty");
});
it("keeps collapsed selections and selects only expanded visible rows", async () => {
  entries = [entry(1), entry(2), entry(3)];
  await act(async () => root.render(<App />));
  const box = node.querySelector<HTMLInputElement>(
    'input[aria-label="Select all visible pull requests"]',
  )!;
  await act(async () => box.click());
  expect(node.textContent).toContain("Generate selected (3)");
  const requested = Array.from(node.querySelectorAll("button")).find((button) =>
    button.textContent?.includes("Review requested"),
  )!;
  await act(async () => requested.click());
  expect(requested.getAttribute("aria-expanded")).toBe("false");
  await act(async () => box.click());
  expect(node.textContent).toContain("Generate selected (1)");
  await act(async () => requested.click());
  expect(
    node.querySelector<HTMLInputElement>('input[aria-label="Select acme/demo #1"]')!.checked,
  ).toBe(true);
  expect(
    node.querySelector<HTMLInputElement>('input[aria-label="Select acme/demo #2"]')!.checked,
  ).toBe(false);
});

it("distinguishes lower-bound risk from exact and unscored reviews", async () => {
  const bound = entry(1);
  bound.risk_score = { score: 1, status: "lower_bound" };
  const exact = entry(2);
  exact.risk_score = { score: 1, status: "assessed" };
  const unknown = entry(3);
  unknown.risk_score = null;
  entries = [bound, exact, unknown];
  await act(async () => root.render(<App />));
  const risks = Array.from(node.querySelectorAll<HTMLElement>(".inbox-risk"));
  expect(risks[0]!.textContent).toContain("≥1/5");
  expect(risks[0]!.getAttribute("aria-label")).toContain("at least 1 of 5");
  expect(risks[0]!.title).toContain("incomplete coverage");
  expect(risks[0]!.innerHTML).not.toContain("status-positive");
  expect(risks[1]!.textContent).toContain("1/5");
  expect(risks[1]!.textContent).not.toContain("≥");
  expect(risks[1]!.innerHTML).toContain("status-positive");
  expect(risks[1]!.getAttribute("aria-label")).toBe("Risk 1 of 5");
  expect(risks[2]!.textContent).toBe("");
  expect(risks[2]!.getAttribute("aria-label")).toBe("Risk not scored");
});

it("shows failed background refresh without hiding cached reviews", async () => {
  entries = [entry(1)];
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation((...args) =>
    String(args[0]) === "/s/test-ns/api/background"
      ? Promise.resolve(
          Response.json([
            {
              repository: "acme/demo",
              error: "metadata_unavailable",
              next_refresh: "2026-10-15T12:01:00Z",
            },
          ]),
        )
      : original(...args),
  );
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("GitHub is not connected");
  expect(node.textContent).toContain("Saved reviews are still available");
  expect(node.querySelector('[aria-label="Select acme/demo #1"]')).not.toBeNull();
});

it("folds every repository GitHub could not refresh into one notice with one action", async () => {
  entries = [entry(1)];
  const original = vi.mocked(fetch).getMockImplementation()!;
  const failed = (repository: string) => ({
    repository,
    error: "discovery_unavailable",
    next_refresh: "2026-10-15T12:01:00Z",
  });
  vi.mocked(fetch).mockImplementation((...args) =>
    String(args[0]) === "/s/test-ns/api/background"
      ? Promise.resolve(Response.json([failed("acme/demo"), failed("acme/other")]))
      : String(args[0]) === "/s/test-ns/api/github-auth"
        ? Promise.resolve(
            Response.json({
              state: "connected",
              host: "github.com",
              saved: true,
              login: "octocat",
              override: false,
            }),
          )
        : original(...args),
  );
  await act(async () => root.render(<App />));
  const notices = [...node.querySelectorAll(".local-notice")];
  expect(notices).toHaveLength(1);
  expect(notices[0]!.textContent).toContain("could not refresh 2 repositories from GitHub");
  expect(notices[0]!.querySelector("a")!.textContent).toBe("Open settings");
  expect(notices[0]!.querySelector("a")!.getAttribute("href")).toContain("page=settings");
});

const diagnosticsReport = {
  status: "problem",
  checks: [
    { id: "git", label: "git", status: "ok", detail: "git is available.", path: "/usr/bin/git" },
    {
      id: "gh",
      label: "GitHub CLI",
      status: "warning",
      detail: "gh is old.",
      version: "2.40.0",
      fix: "Upgrade gh.",
    },
    {
      id: "token",
      label: "GitHub token",
      status: "problem",
      detail: "No token found.",
      source: "none",
      fix: "Connect GitHub in baum Settings.",
    },
  ],
};
function stubDiagnostics(respond: () => Promise<Response>) {
  const original = vi.mocked(fetch).getMockImplementation()!;
  const calls = { count: 0 };
  vi.mocked(fetch).mockImplementation((...args) => {
    if (String(args[0]) !== "/s/test-ns/api/diagnostics") return original(...args);
    calls.count++;
    return respond();
  });
  return calls;
}
it("lists diagnostics problems first with status text and fixes", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=diagnostics");
  stubDiagnostics(async () => Response.json({ local: true, report: diagnosticsReport }));
  await act(async () => root.render(<App />));
  const text = node.textContent ?? "";
  expect(text).toContain("Diagnostics");
  expect(text).toContain("Overall: Problem. 1 problem found.");
  for (const word of ["OK", "Warning", "Problem"]) expect(text).toContain(word);
  expect(text).toContain("Fix: Connect GitHub in baum Settings.");
  expect(text).toContain("Fix: Upgrade gh.");
  expect(text).toContain("/usr/bin/git");
  expect(text).toContain("2.40.0");
  const checks = [...node.querySelectorAll("li h3")].map((h) => h.textContent);
  expect(checks).toEqual(["GitHub token", "GitHub CLI", "git"]);
});
it("shows diagnostics loading, then re-runs on Check again and disables it meanwhile", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=diagnostics");
  let resolve: (r: Response) => void = () => {};
  const calls = stubDiagnostics(() => new Promise<Response>((r) => (resolve = r)));
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Checking this machine…");
  const button = () =>
    [
      ...node.querySelectorAll<HTMLButtonElement>("[aria-labelledby=diagnostics-heading] button"),
    ].find((b) => /Check/.test(b.textContent ?? ""))!;
  expect(button().disabled).toBe(true);
  await act(async () => resolve(Response.json({ local: true, report: diagnosticsReport })));
  expect(button().textContent).toBe("Check again");
  expect(button().disabled).toBe(false);
  expect(calls.count).toBe(1);
  await act(async () => button().click());
  expect(calls.count).toBe(2);
  expect(button().disabled).toBe(true);
  await act(async () => resolve(Response.json({ local: true, report: diagnosticsReport })));
  expect(button().disabled).toBe(false);
});
it("points every page at the diagnostics when they find a problem", async () => {
  const calls = stubDiagnostics(async () =>
    Response.json({ local: true, report: diagnosticsReport }),
  );
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain(
    "baum cannot fully work on this computer: diagnostics found 1 problem. Show the fixes",
  );
  expect(calls.count).toBe(1);
  const link = [...node.querySelectorAll("a")].find((a) => a.textContent === "Show the fixes")!;
  expect(link.getAttribute("href")).toBe("/s/test-ns/?page=diagnostics");
  await act(async () => link.click());
  expect(location.search).toBe("?page=diagnostics");
  const text = node.textContent ?? "";
  expect(text).toContain("Check this computer");
  expect(text).toContain("Fix: Connect GitHub in baum Settings.");
  expect(text).not.toContain("Show the fixes");
  // The page ran the checks again; the inbox does not rerun them.
  expect(calls.count).toBe(2);
});
it("shows no diagnostics notice when nothing is a problem", async () => {
  const calls = stubDiagnostics(async () =>
    Response.json({
      local: true,
      report: {
        status: "warning",
        checks: diagnosticsReport.checks.filter((c) => c.status !== "problem"),
      },
    }),
  );
  await act(async () => root.render(<App />));
  expect(calls.count).toBe(1);
  expect(node.textContent).not.toContain("diagnostics found");
});
it("opens the diagnostics page from its address", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=diagnostics");
  const calls = stubDiagnostics(async () =>
    Response.json({ local: true, report: diagnosticsReport }),
  );
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Check this computer");
  expect(node.textContent).toContain("Overall: Problem. 1 problem found.");
  expect(node.textContent).not.toContain("Show the fixes");
  expect(calls.count).toBe(1);
});
const demoClone = {
  id: "clone",
  path: "/work/demo",
  repository: "acme/demo",
  available: true,
  settings: { automatic: null, daily_cap: 5, backend: null, model: null },
};
const automaticOverview = {
  enabled: true,
  cursor_agent_acknowledged: false,
  repositories: [
    {
      repository: "acme/demo",
      enabled: true,
      daily_cap: 5,
      spent_today: 5,
      spent_known: true,
      paused_until: "2026-10-20T00:00:00+02:00",
      notice: "daily_cap",
      pending: 1,
    },
  ],
};
/** Serves api/repos, api/automatic and api/settings, and records PUT bodies. */
function stubAutomatic(
  clone: Omit<Clone, "settings"> & { settings: Partial<Clone["settings"]> } = demoClone,
  overview: object = automaticOverview,
  settings: object = { backend: "auto", model: "" },
) {
  const original = vi.mocked(fetch).getMockImplementation()!;
  const puts: { path: string; body: unknown }[] = [];
  vi.mocked(fetch).mockImplementation((...args) => {
    const path = String(args[0]);
    const init = args[1] as RequestInit | undefined;
    if (init?.method === "PUT") {
      puts.push({ path, body: JSON.parse(String(init.body)) });
      if (path.endsWith("api/settings/agent-env"))
        return Promise.resolve(Response.json(puts.at(-1)!.body));
      return Promise.resolve(Response.json(path.endsWith("api/automatic") ? overview : {}));
    }
    if (path === "/s/test-ns/api/repos") return Promise.resolve(Response.json([clone]));
    if (path === "/s/test-ns/api/repositories")
      return Promise.resolve(
        Response.json([
          { repository: clone.repository, settings: clone.settings, clones: [clone] },
        ]),
      );
    if (path === "/s/test-ns/api/automatic") return Promise.resolve(Response.json(overview));
    if (path === "/s/test-ns/api/settings") return Promise.resolve(Response.json(settings));
    return original(...args);
  });
  return puts;
}
it("saves the global review settings from the form the desktop setup uses", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=settings");
  stubDiagnostics(async () => Response.json({ local: true, report: diagnosticsReport }));
  const puts = stubAutomatic();
  await act(async () => root.render(<App />));
  const save = () =>
    [...node.querySelectorAll("button")].find((b) => b.textContent === "Save review settings")!;
  // "auto" shows as the harness it runs first, with that harness's default model.
  expect(node.querySelector<HTMLInputElement>('input[value="claude"]')!.checked).toBe(true);
  expect(node.querySelector<HTMLSelectElement>("#review-model")!.value).toBe("claude-haiku-5-5");
  const global = node.querySelector<HTMLInputElement>("#review-automatic")!;
  expect(global.checked).toBe(true);
  await act(async () => global.click());
  await act(async () => save().click());
  expect(puts).toContainEqual({
    path: "/s/test-ns/api/automatic",
    body: { enabled: false, backend: "claude", model: "claude-haiku-5-5", pull_requests: "both" },
  });
});
it("edits the environment agents run with and shows server errors inline", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=settings");
  stubDiagnostics(async () => Response.json({ local: true, report: diagnosticsReport }));
  const puts = stubAutomatic(demoClone, automaticOverview, {
    backend: "auto",
    model: "",
    agent_env: { GIT_CONFIG_GLOBAL: "~/.gitconfig-agent" },
    agent_env_source: "file",
  });
  await act(async () => root.render(<App />));
  const section = () => node.querySelector('[aria-labelledby="agent-env-heading"]')!;
  const save = () =>
    [...section().querySelectorAll("button")].find((b) => b.textContent === "Save environment")!;
  const add = () =>
    [...section().querySelectorAll("button")].find((b) => b.textContent === "Add variable")!;
  const type = async (input: HTMLInputElement, value: string) =>
    act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
  const names = () =>
    section().querySelectorAll<HTMLInputElement>('input[aria-label="Variable name"]');
  const inputs = () => section().querySelectorAll<HTMLInputElement>("input");
  expect(section().textContent).toContain("[agent_env]");
  expect(names()[0]!.value).toBe("GIT_CONFIG_GLOBAL");
  expect(save().disabled).toBe(true);
  await act(async () => add().click());
  await type(names()[1]!, "1BAD");
  expect(section().textContent).toContain("not starting with a digit");
  expect(save().disabled).toBe(true);
  await type(names()[1]!, "GIT_CONFIG_GLOBAL");
  expect(section().textContent).toContain("Another row has this name");
  await type(names()[1]!, "EDITOR");
  await type(inputs()[3]!, "$HOME/bin/edit");
  expect(save().disabled).toBe(false);
  await act(async () => save().click());
  expect(puts).toContainEqual({
    path: "/s/test-ns/api/settings/agent-env",
    body: { agent_env: { GIT_CONFIG_GLOBAL: "~/.gitconfig-agent", EDITOR: "$HOME/bin/edit" } },
  });
  expect(section().textContent).toContain("Saved.");
  expect(save().disabled).toBe(true);
  // A refusal from the server stays by the form.
  const accepted = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation((...args) =>
    String(args[0]).endsWith("api/settings/agent-env")
      ? Promise.resolve(Response.json({ message: "GIT_DIR cannot be set" }, { status: 400 }))
      : accepted(...args),
  );
  await act(async () => add().click());
  await type(names()[2]!, "GIT_DIR");
  await act(async () => save().click());
  expect(section().textContent).toContain("GIT_DIR cannot be set");
});
it("sets automatic reviews per repository and clears an old daily cap", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=repositories");
  stubDiagnostics(async () => Response.json({ local: true, report: diagnosticsReport }));
  const puts = stubAutomatic();
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("acme/demo: Automatic generation is paused");
  const region = await openRepository();
  const mode = region.querySelector<HTMLSelectElement>("select")!;
  expect(mode.value).toBe("default");
  expect(mode.options[0]!.textContent).toBe("Global setting (on)");
  await act(async () => {
    mode.value = "off";
    mode.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await act(async () =>
    Array.from(region.querySelectorAll("button"))
      .find((b) => b.textContent === "Save repository settings")!
      .click(),
  );
  expect(puts).toContainEqual({
    path: "/s/test-ns/api/repositories/acme/demo/settings",
    body: { automatic: false, daily_cap: null, backend: null, model: null },
  });
});
/** Opens a repository's settings dialog and returns it. */
async function openRepository(name = "acme/demo") {
  await act(async () =>
    node.querySelector<HTMLButtonElement>(`button[aria-label="Settings for ${name}"]`)!.click(),
  );
  return node.querySelector(`[aria-label="Repository ${name}"]`)!;
}
it("offers GitHub repositories only, with no local folder fallback", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=repositories");
  stubDiagnostics(async () => Response.json({ local: true, report: diagnosticsReport }));
  stubAutomatic();
  const original = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation((...args) =>
    String(args[0]).startsWith("/s/test-ns/api/github/repositories")
      ? Promise.resolve(Response.json({ repositories: [] }))
      : original(...args),
  );
  await act(async () => root.render(<App />));
  await act(async () =>
    [...node.querySelectorAll("button")].find((b) => b.textContent === "Add repositories")!.click(),
  );
  expect(node.querySelector(".repository-picker")).not.toBeNull();
  expect(node.textContent).not.toContain("local folder");
  expect(node.querySelector('input[placeholder="/path/to/a/local/clone"]')).toBeNull();
});
it("saves a repository without a cap as explicit null when automatic settings are unavailable", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=repositories");
  stubDiagnostics(async () => Response.json({ local: true, report: diagnosticsReport }));
  // The server omits daily_cap when there is none.
  const puts = stubAutomatic({
    ...demoClone,
    settings: { automatic: null, backend: null, model: null },
  });
  const stubbed = vi.mocked(fetch).getMockImplementation()!;
  vi.mocked(fetch).mockImplementation((...args) =>
    String(args[0]) === "/s/test-ns/api/automatic"
      ? Promise.resolve(new Response("{}", { status: 500 }))
      : stubbed(...args),
  );
  await act(async () => root.render(<App />));
  const region = await openRepository();
  await act(async () =>
    Array.from(region.querySelectorAll("button"))
      .find((b) => b.textContent === "Save repository settings")!
      .click(),
  );
  expect(puts).toContainEqual({
    path: "/s/test-ns/api/repositories/acme/demo/settings",
    body: { automatic: null, daily_cap: null, backend: null, model: null },
  });
});
it("shows a repository set to On as paused while the global switch is off", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=repositories");
  stubDiagnostics(async () => Response.json({ local: true, report: diagnosticsReport }));
  stubAutomatic(
    { ...demoClone, settings: { ...demoClone.settings, automatic: true } },
    {
      enabled: false,
      cursor_agent_acknowledged: false,
      repositories: [
        {
          repository: "acme/demo",
          enabled: false,
          daily_cap: 5,
          spent_today: 0,
          spent_known: true,
          pending: 0,
        },
      ],
    },
  );
  await act(async () => root.render(<App />));
  const region = await openRepository();
  const mode = region.querySelector<HTMLSelectElement>("select")!;
  expect(mode.value).toBe("on");
  expect(mode.selectedOptions[0]!.textContent).toBe(
    "On for this repository (paused by the global switch)",
  );
  expect(region.textContent).toContain(
    "Automatic reviews are paused by the global switch, although this repository is set to On.",
  );
});
it("marks automatically started jobs in the jobs list", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=jobs");
  const original = vi.mocked(fetch).getMockImplementation()!;
  const job = (id: string, automatic: boolean, trigger?: string) => ({
    id,
    clone: "clone",
    repository: "acme/demo",
    pr: 1,
    state: "queued",
    phase: "queued",
    calls: 0,
    tokens: 0,
    repairs: 0,
    actual_cost_usd: null,
    automatic,
    trigger,
  });
  vi.mocked(fetch).mockImplementation((...args) =>
    String(args[0]) === "/s/test-ns/api/jobs"
      ? Promise.resolve(
          Response.json({
            jobs: [
              job("a", true, "new_head"),
              job("b", true, "review_requested"),
              job("d", true, "ready_for_review"),
              job("e", true, "retry"),
              job("c", false),
            ],
          }),
        )
      : original(...args),
  );
  await act(async () => root.render(<App />));
  const text = node.textContent ?? "";
  expect(text).toContain("Started automatically because new commits were pushed");
  expect(text).toContain("Started automatically because your review was requested");
  expect(text).toContain(
    "Started automatically because the pull request was marked ready for review",
  );
  expect(text).toContain(
    "Started automatically again: the daemon stopped during the previous automatic run",
  );
  expect(text.match(/Started automatically/g)).toHaveLength(4);
  expect([...node.querySelectorAll(".local-job-row")].map((row) => row.id)).toEqual([
    "job-c",
    "job-e",
    "job-d",
    "job-b",
    "job-a",
  ]);
});
it("shows a diagnostics error with a retry that recovers", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=diagnostics");
  let ok = false;
  const calls = stubDiagnostics(async () =>
    ok
      ? Response.json({ local: true, report: diagnosticsReport })
      : new Response("boom", { status: 504 }),
  );
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Local request failed (504): boom");
  ok = true;
  const retry = [...node.querySelectorAll("button")].find((b) => b.textContent === "Retry")!;
  await act(async () => retry.click());
  expect(calls.count).toBe(2);
  expect(node.textContent).not.toContain("boom");
  expect(node.textContent).toContain("Fix: Connect GitHub in baum Settings.");
});

it("manages CLI and hooks inline in desktop Settings", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=settings");
  const view = {
    first_run: false,
    command: {
      installed: false,
      target: "/app/baum",
      plan: { link: "/home/user/bin/baum", on_path: true, admin: false },
    },
    hooks: {
      agents: [
        {
          agent: "codex",
          name: "Codex",
          detected: true,
          installed: false,
          enabled: false,
          plugin: "baum@baum",
        },
      ],
    },
  };
  const invoke = vi.fn(async (command: string) =>
    command === "setup_state" ? view : { ok: true, lines: ["Installed"] },
  );
  Object.defineProperty(window, "__TAURI_INTERNALS__", { configurable: true, value: { invoke } });
  try {
    await act(async () => root.render(<App />));
    const click = async (label: string) => {
      const button = [...node.querySelectorAll("button")].find((b) => b.textContent === label);
      expect(button).toBeTruthy();
      await act(async () => button!.click());
    };
    await click("Install baum CLI");
    expect(invoke).toHaveBeenCalledWith("install_command", {});
    await click("Install Codex hooks");
    expect(invoke).toHaveBeenCalledWith("install_hooks", { agent: "codex" });
    expect(location.search).toBe("?page=settings");
  } finally {
    Reflect.deleteProperty(window, "__TAURI_INTERNALS__");
  }
});

it("opens clone-less PR deep links by repository and refreshes picked repositories", async () => {
  const row = { ...entry(7), clone_id: undefined, needs_clone: true };
  entries = [row];
  const original = vi.mocked(fetch).getMockImplementation()!;
  const posts: unknown[] = [];
  vi.mocked(fetch).mockImplementation((...args) => {
    const path = String(args[0]);
    const init = args[1] as RequestInit | undefined;
    if (path === "/s/test-ns/api/repositories")
      return Promise.resolve(
        Response.json([
          {
            repository: "acme/demo",
            settings: { automatic: null, daily_cap: null, backend: null, model: null },
            clones: [],
          },
        ]),
      );
    if (path === "/s/test-ns/api/prs/refresh") {
      posts.push(JSON.parse(String(init?.body)));
      return Promise.resolve(Response.json({}));
    }
    return original(...args);
  });
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Needs a local clone");
  expect(
    node.querySelector<HTMLInputElement>('input[aria-label="Select acme/demo #7"]')!.disabled,
  ).toBe(true);
  await act(async () =>
    node.querySelector<HTMLButtonElement>('button[aria-label="Refresh from GitHub"]')!.click(),
  );
  expect(posts).toEqual([{ repository: "acme/demo" }]);
  const link = [...node.querySelectorAll("a")].find((a) => a.textContent === "Change 7")!;
  expect(link.search).toContain("repository=acme%2Fdemo");
  await act(async () => link.click());
  expect(node.querySelector("h2")!.textContent).toBe("Needs a local clone");
  expect(node.textContent).toContain("Choose folder");
  expect(node.textContent).toContain("Clone it");
  expect(vi.mocked(fetch).mock.calls.some(([path]) => String(path).includes("api/document"))).toBe(
    false,
  );
});
const press = (key: string, init: KeyboardEventInit = {}) =>
  act(async () => {
    document.body.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, ...init }));
  });
it("toggles the shortcuts sheet with ? and from its button", async () => {
  entries = [entry(1)];
  // jsdom has no modal dialogs.
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value(this: HTMLDialogElement) {
      this.open = true;
    },
  });
  await act(async () => root.render(<App />));
  await press("?");
  expect(node.querySelector("dialog")?.textContent).toContain("Keyboard shortcuts");
  await press("?");
  expect(node.querySelector("dialog")).toBeNull();
  await act(async () =>
    node.querySelector<HTMLElement>('button[aria-label="Keyboard shortcuts"]')!.click(),
  );
  expect(node.querySelector("dialog")?.textContent).toContain("Refresh from GitHub");
});
it("goes back to the inbox from a pull request page with u or Escape, keeping the filter", async () => {
  entries = [{ ...entry(42), clone_id: undefined, needs_clone: true }];
  const page = "/s/test-ns/?filter=requested&repository=acme%2Fdemo&pr=42";
  history.replaceState(null, "", page);
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Needs a local clone");
  await press("u", { ctrlKey: true });
  expect(location.search).toContain("pr=42");
  await press("u");
  expect(location.search).toBe("?filter=requested");
  expect(node.textContent).not.toContain("Needs a local clone");
  history.pushState(null, "", page);
  await act(async () => dispatchEvent(new PopStateEvent("popstate")));
  expect(node.textContent).toContain("Needs a local clone");
  await press("Escape");
  expect(location.search).toBe("?filter=requested");
});
