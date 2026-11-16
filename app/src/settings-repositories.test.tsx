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

const withTiles = (tiles?: { text?: string; pins?: string[] }): Repository => {
  const base = repository("acme/api");
  const settings = { ...base.settings, tiles };
  return { ...base, settings, clones: base.clones.map((c) => ({ ...c, settings })) };
};

function stubTileApi() {
  const puts: unknown[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT") {
        puts.push(JSON.parse(String(init.body)));
        return Response.json({});
      }
      if (String(url).endsWith("api/tile-library"))
        return Response.json({
          single: [{ name: "risks", title: "Risks" }],
          types: [{ name: "schema_change", title: "Schema changes" }],
        });
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

it("offers the tiles the daemon lists to pin, and saves the tile layout text after typing stops", async () => {
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  try {
    const puts = stubTileApi();
    await renderPage([withTiles()]);
    await act(async () => void (await vi.advanceTimersByTimeAsync(0)));
    const pin = node.querySelector<HTMLSelectElement>("#repository-tile-pin")!;
    expect([...pin.options].map((o) => o.value)).toEqual(["", "risks", "schema_change"]);
    const text = node.querySelector<HTMLTextAreaElement>("#repository-tile-text")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(
        text,
        "Lead with the schema.",
      );
      text.dispatchEvent(new Event("input", { bubbles: true }));
    });
    // Not yet: the person may still be typing.
    await act(async () => void (await vi.advanceTimersByTimeAsync(700)));
    expect(puts).toEqual([]);
    await act(async () => void (await vi.advanceTimersByTimeAsync(200)));
    expect(puts).toEqual([
      { automatic: null, decisions: null, tiles: { text: "Lead with the schema.", pins: [] } },
    ]);
  } finally {
    vi.useRealTimers();
  }
});

it("saves typed text when the page closes", async () => {
  const puts = stubTileApi();
  await renderPage([withTiles()]);
  const text = node.querySelector<HTMLTextAreaElement>("#repository-tile-text")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(
      text,
      "Unsaved words.",
    );
    text.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(puts).toEqual([]);
  await act(async () => root.unmount());
  expect(puts).toEqual([
    { automatic: null, decisions: null, tiles: { text: "Unsaved words.", pins: [] } },
  ]);
  root = createRoot(node);
});

it("follows tile preferences saved elsewhere, and labels the pin list without a select", async () => {
  stubTileApi();
  await renderPage([withTiles()]);
  expect(node.querySelector<HTMLTextAreaElement>("#repository-tile-text")!.value).toBe("");
  const eight = ["a", "b", "c", "d", "e", "f", "g", "h"];
  await renderPage([withTiles({ text: "From a saved proposal.", pins: eight })]);
  expect(node.querySelector<HTMLTextAreaElement>("#repository-tile-text")!.value).toBe(
    "From a saved proposal.",
  );
  expect(node.querySelectorAll('ul[aria-label="Pinned tiles"] li')).toHaveLength(8);
  // At the limit the select is gone, and the label points at nothing.
  expect(node.querySelector("#repository-tile-pin")).toBeNull();
  expect(node.querySelector('label[for="repository-tile-pin"]')).toBeNull();
  expect([...node.querySelectorAll("span.settings-row-label")].map((n) => n.textContent)).toContain(
    "Pinned tiles",
  );
});

it("keeps typed text when the stored settings change under it", async () => {
  stubTileApi();
  await renderPage([withTiles()]);
  const text = node.querySelector<HTMLTextAreaElement>("#repository-tile-text")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(
      text,
      "Mine.",
    );
    text.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await renderPage([withTiles({ text: "Theirs.", pins: [] })]);
  expect(node.querySelector<HTMLTextAreaElement>("#repository-tile-text")!.value).toBe("Mine.");
});
