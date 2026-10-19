import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach, expect, it, vi } from "vitest";
import { inboxGroups } from "./inbox";
import { App } from "./app";
import { appBase, cost, filtered, type Entry } from "./api";
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
  },
  clone_id: "clone",
  status,
  version_count: status === "no_review" ? 0 : 1,
  latest: null,
  risk_score: { score: 5, status: "assessed" },
  historical,
  stale: false,
});
let root: Root;
let node: HTMLDivElement;
let fail = false;
let entries: Entry[] = [];
let streams: string[] = [];
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
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
        path.includes("/s/test-ns/api/index")
          ? { entries, problems: [] }
          : path === "/s/test-ns/api/repos"
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
it("shows first-run registration rather than a blank inbox", async () => {
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Register a local clone");
  expect(node.textContent).toContain("Repositories & settings");
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
  expect(node.textContent).toContain("No generation jobs yet.");
  await act(async () => {
    Array.from(node.querySelectorAll("a"))
      .find((a) => a.textContent === "Inbox")!
      .click();
  });
  await act(async () => {
    const select = node.querySelector<HTMLSelectElement>("select")!;
    select.value = "mine";
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  expect(location.pathname).toBe("/s/test-ns/");
  expect(location.search).toBe("?filter=mine");
});
it("shows actionable launch authorization failure", async () => {
  fail = true;
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Open the URL printed by baum ui");
  expect(node.textContent).not.toContain("Generate selected");
});
it("renders review states and unknown costs without inventing zero", async () => {
  entries = [entry(1, "outdated"), entry(2, "failed"), entry(3, "current"), entry(4, "generating")];
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("outdated");
  expect(node.textContent).toContain("failed");
  expect(node.textContent).toContain("current");
  expect(node.textContent).toContain("generating");
  expect(node.textContent).toContain("—");
  expect(node.textContent).not.toContain("$0.0000");
});
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

it("shows an unpriced model exclusion and keeps generation disabled", async () => {
  entries = [entry(1)];
  const original = globalThis.fetch;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, ...args: unknown[]) =>
      path === "/s/test-ns/api/jobs/estimate"
        ? Response.json({
            id: "quote",
            estimate_usd: null,
            price_version: "test",
            expires_at: "2026-10-15T15:00:00Z",
            items: [
              {
                clone: "clone",
                pr: 1,
                head_sha: "head",
                backend: "claude",
                model: "",
                estimate_usd: null,
                requires_isolation_confirmation: false,
                blocked_reason: "unknown_model_price",
              },
            ],
          })
        : original(path, ...(args as [])),
    ),
  );
  Object.defineProperty(HTMLDialogElement.prototype, "showModal", {
    configurable: true,
    value() {
      this.open = true;
    },
  });
  await act(async () => root.render(<App />));
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
  expect(node.textContent).toContain("Choose a priced model in repository settings.");
  expect(
    Array.from(node.querySelectorAll("button")).find(
      (b) => b.textContent === "Confirm estimate and generate",
    )!.disabled,
  ).toBe(true);
});

it("groups requested PRs before authored PRs once and keeps saved history separate", () => {
  const requested = entry(1);
  requested.pull_request.authored_by_me = true;
  const mine = entry(2);
  const other = entry(3);
  const history = entry(4, "current", true);
  const groups = inboxGroups([history, other, mine, requested]);
  expect(
    groups.map((group) => [group.id, group.rows.map((row) => row.pull_request.number)]),
  ).toEqual([
    ["requested", [1]],
    ["mine", [2]],
    ["other", [3]],
    ["history", [4]],
  ]);
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
  expect(risks[0]!.textContent).toContain("1+/5");
  expect(risks[0]!.getAttribute("aria-label")).toContain("at least 1 of 5");
  expect(risks[0]!.title).toContain("incomplete coverage");
  expect(risks[0]!.className).not.toContain("status-positive");
  expect(risks[1]!.textContent).toContain("1/5");
  expect(risks[1]!.textContent).not.toContain("+");
  expect(risks[1]!.getAttribute("aria-label")).toBe("Risk 1 of 5");
  expect(risks[2]!.textContent).toContain("—");
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
  expect(node.textContent).toContain(
    "background metadata refresh is unavailable (metadata_unavailable)",
  );
  expect(node.textContent).toContain("Cached reviews remain readable");
  expect(node.querySelector('[aria-label="Select acme/demo #1"]')).not.toBeNull();
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
      fix: "Run gh auth login.",
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
  history.replaceState(null, "", "/s/test-ns/?page=settings");
  stubDiagnostics(async () => Response.json({ local: true, report: diagnosticsReport }));
  await act(async () => root.render(<App />));
  const text = node.textContent ?? "";
  expect(text).toContain("Diagnostics");
  expect(text).toContain("Overall: Problem. 1 problem found.");
  for (const word of ["OK", "Warning", "Problem"]) expect(text).toContain(word);
  expect(text).toContain("Fix: Run gh auth login.");
  expect(text).toContain("Fix: Upgrade gh.");
  expect(text).toContain("/usr/bin/git");
  expect(text).toContain("2.40.0");
  const checks = [...node.querySelectorAll("li h3")].map((h) => h.textContent);
  expect(checks).toEqual(["GitHub token", "GitHub CLI", "git"]);
});
it("shows diagnostics loading, then re-runs on Check again and disables it meanwhile", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=settings");
  let resolve: (r: Response) => void = () => {};
  const calls = stubDiagnostics(() => new Promise<Response>((r) => (resolve = r)));
  await act(async () => root.render(<App />));
  expect(node.textContent).toContain("Checking this machine…");
  const button = () =>
    [...node.querySelectorAll("button")].find((b) => /Check/.test(b.textContent ?? ""))!;
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
/** Serves api/repos and api/automatic, and records PUT bodies. */
function stubAutomatic() {
  const original = vi.mocked(fetch).getMockImplementation()!;
  const puts: { path: string; body: unknown }[] = [];
  vi.mocked(fetch).mockImplementation((...args) => {
    const path = String(args[0]);
    const init = args[1] as RequestInit | undefined;
    if (init?.method === "PUT") {
      puts.push({ path, body: JSON.parse(String(init.body)) });
      return Promise.resolve(
        Response.json(path.endsWith("api/automatic") ? automaticOverview : {}),
      );
    }
    if (path === "/s/test-ns/api/repos") return Promise.resolve(Response.json([demoClone]));
    if (path === "/s/test-ns/api/automatic")
      return Promise.resolve(Response.json(automaticOverview));
    return original(...args);
  });
  return puts;
}
it("sets automatic reviews globally and per repository and shows a daily cap pause", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=settings");
  stubDiagnostics(async () => Response.json({ local: true, report: diagnosticsReport }));
  const puts = stubAutomatic();
  await act(async () => root.render(<App />));
  const text = node.textContent ?? "";
  expect(text).toContain("Automatic reviews");
  expect(text).toContain("acme/demo: Automatic generation is paused");
  expect(text).toContain("Spent automatically today: $5.00 of $5.00. 1 waiting to start.");
  const global = node.querySelector<HTMLInputElement>(
    'section[aria-label="Automatic reviews"] input[type="checkbox"]',
  )!;
  expect(global.checked).toBe(true);
  await act(async () => global.click());
  expect(puts).toContainEqual({ path: "/s/test-ns/api/automatic", body: { enabled: false } });
  const acknowledge = node.querySelectorAll<HTMLInputElement>(
    'section[aria-label="Automatic reviews"] input[type="checkbox"]',
  )[1]!;
  await act(async () => acknowledge.click());
  expect(puts).toContainEqual({
    path: "/s/test-ns/api/automatic",
    body: { cursor_agent_acknowledged: true },
  });
  const region = node.querySelector('[aria-label="Repository acme/demo"]')!;
  const mode = region.querySelector<HTMLSelectElement>("select")!;
  expect(mode.value).toBe("default");
  expect(mode.options[0]!.textContent).toBe("Global setting (on)");
  await act(async () => {
    mode.value = "off";
    mode.dispatchEvent(new Event("change", { bubbles: true }));
  });
  const cap = region.querySelector<HTMLInputElement>('input[type="number"]')!;
  expect(cap.value).toBe("5");
  await act(async () =>
    Array.from(region.querySelectorAll("button"))
      .find((b) => b.textContent === "Save repository settings")!
      .click(),
  );
  expect(puts).toContainEqual({
    path: "/s/test-ns/api/repos/clone/settings",
    body: { automatic: false, daily_cap: 5, backend: null, model: null },
  });
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
            jobs: [job("a", true, "new_head"), job("b", true, "review_requested"), job("c", false)],
          }),
        )
      : original(...args),
  );
  await act(async () => root.render(<App />));
  const text = node.textContent ?? "";
  expect(text).toContain("Started automatically because new commits were pushed");
  expect(text).toContain("Started automatically because your review was requested");
  expect(text.match(/Started automatically/g)).toHaveLength(2);
});
it("shows a diagnostics error with a retry that recovers", async () => {
  history.replaceState(null, "", "/s/test-ns/?page=settings");
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
  expect(node.textContent).toContain("Fix: Run gh auth login.");
});
