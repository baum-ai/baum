import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { formatDateTime } from "@baum/viewer/components/format";
import type { Automatic, Entry, Job, Repository } from "./api";
import { RepositoriesPage, RepositoryPage } from "./settings";

let root: Root;
let node: HTMLDivElement;
beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  localStorage.clear();
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
});
afterEach(() => {
  act(() => root.unmount());
  node.remove();
});

const repository = (name: string, automatic: boolean | null = null): Repository => ({
  repository: name,
  settings: { automatic },
  clones: [
    {
      id: name,
      path: `/home/dev/work/${name}`,
      repository: name,
      available: true,
      settings: { automatic },
    },
  ],
});
const entry = (repository: string, number: number, over: Partial<Entry> = {}): Entry => ({
  pull_request: {
    repository,
    number,
    head_sha: "head",
    title: `Change ${number}`,
    url: `https://github.com/${repository}/pull/${number}`,
    state: "OPEN",
    draft: false,
    fork: false,
    authored_by_me: false,
    review_requested_from_me: false,
    author_has_write: true,
    team_membership_known: true,
    review: "",
    checks: "",
    merge: "",
    unresolved: null,
    unresolved_capped: false,
  },
  clone_id: repository,
  status: "no_review",
  version_count: 0,
  latest: null,
  risk_score: null,
  historical: false,
  stale: false,
  review: "",
  checks: "",
  merge: "",
  unresolved: null,
  unresolved_capped: false,
  review_minutes: null,
  ...over,
});
const reviewed = (at: string) => ({
  version: 1,
  generated_at: at,
  backend: "claude-code",
  model: "",
  recorded_cost_usd: null,
});

it("groups repositories by owner and shows what each has going on", async () => {
  const automatic: Automatic = {
    enabled: true,
    repositories: [
      { repository: "acme/api", enabled: true, pending: 0 },
      { repository: "acme/web", enabled: true, pending: 2 },
      { repository: "solo/tool", enabled: false, pending: 0 },
    ],
  };
  const jobs = [
    { id: "1", repository: "acme/api", pr: 1, state: "running" },
    { id: "2", repository: "acme/api", pr: 2, state: "done" },
  ] as Job[];
  await act(async () =>
    root.render(
      <RepositoriesPage
        Link={(props) => <a {...props} />}
        repositories={[
          repository("acme/api"),
          repository("solo/tool", false),
          repository("acme/web"),
        ]}
        automatic={automatic}
        entries={[
          entry("acme/api", 1, { latest: reviewed("2026-01-01T00:00:00Z") }),
          entry("acme/api", 2, { latest: reviewed("2026-02-01T00:00:00Z") }),
          entry("acme/api", 3, { historical: true }),
          entry("solo/tool", 4),
        ]}
        jobs={jobs}
        changed={async () => {}}
      />,
    ),
  );
  const sections = [...node.querySelectorAll("section[aria-label]")];
  expect(sections[0]!.querySelector(".list-heading img")!.getAttribute("src")).toBe(
    "https://github.com/acme.png?size=32",
  );
  expect(sections.map((s) => s.getAttribute("aria-label"))).toEqual(["acme", "solo"]);
  expect([...sections[0]!.querySelectorAll(".local-repo-name")].map((n) => n.textContent)).toEqual([
    "api",
    "web",
  ]);
  const row = (name: string) => node.querySelector(`[data-row-id="${name}"]`)!;
  // Two open pull requests (the third is closed), one reviewed most recently.
  expect(row("acme/api").textContent).toContain("Reviewing");
  expect(row("acme/api").textContent).toContain("2 open PRs");
  expect(row("acme/api").querySelectorAll(".local-repo-figure")[1]!.getAttribute("title")).toBe(
    formatDateTime("2026-02-01T00:00:00Z"),
  );
  expect(row("acme/web").textContent).toContain("2 waiting");
  expect(row("acme/web").textContent).toContain("No open PRs");
  expect(row("acme/web").textContent).toContain("Not reviewed yet");
  // Automatic reviews are marked only where they are off.
  expect(row("acme/api").textContent).not.toContain("Automatic reviews");
  expect(row("solo/tool").textContent).toContain("Automatic reviews off");
  expect(row("solo/tool").textContent).toContain("~/work/solo/tool");
});

const withTiles = (tiles?: { types?: string[] }): Repository => {
  const base = repository("acme/api");
  const settings = { ...base.settings, tiles };
  return { ...base, settings, clones: base.clones.map((c) => ({ ...c, settings })) };
};

function stubSettingsApi() {
  const puts: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_url: string, init?: RequestInit) => {
      if (init?.method === "PUT") puts.push(JSON.parse(String(init.body)));
      return Response.json({});
    }),
  );
  return puts;
}

const renderPage = (repositories: Repository[]) =>
  act(async () =>
    root.render(
      <RepositoryPage
        name="acme/api"
        repositories={repositories}
        loading={false}
        automatic={null}
        changed={async () => {}}
        section="reviews"
        setSection={() => {}}
        Link={(props) => <a {...props} />}
      />,
    ),
  );

it("saves its review settings without the tile types pinned to it, which the daemon keeps", async () => {
  const puts = stubSettingsApi();
  await renderPage([withTiles({ types: ["schema_change"] })]);
  // No layout preferences: the page arranges every review the same way.
  expect(node.querySelector("#repository-tile-text")).toBeNull();
  expect(node.querySelector("#repository-tile-pin")).toBeNull();
  const decisions = node.querySelector<HTMLInputElement>("#repository-decisions")!;
  await act(async () => decisions.click());
  expect(puts).toEqual([{ automatic: null, decisions: false }]);
});
