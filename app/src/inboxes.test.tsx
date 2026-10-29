import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./app";
import type { Entry } from "./api";
import type { Inbox } from "./inboxes";

const entry = (
  number: number,
  repository: string,
  flags: Partial<Entry["pull_request"]>,
  rest: Partial<Entry> = {},
): Entry =>
  ({
    pull_request: {
      repository,
      number,
      title: `Change ${number}`,
      url: `https://github.com/${repository}/pull/${number}`,
      head_sha: "head",
      state: "OPEN",
      draft: false,
      fork: false,
      authored_by_me: false,
      review_requested_from_me: false,
      author_has_write: true,
      team_membership_known: true,
      viewer_can_push: true,
      review: "",
      checks: "",
      merge: "",
      unresolved: null,
      unresolved_capped: false,
      ...flags,
    },
    clone_id: "clone",
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
    ...rest,
  }) as Entry;

// Change 1: a review of acme's, risk 4. 2: theirs, failing checks. 3: ready
// to merge in acme. 4: ready to merge in another owner's repository.
const fixture = () => [
  entry(
    1,
    "acme/demo",
    { review_requested_from_me: true, author: "sam" },
    {
      status: "current",
      version_count: 1,
      risk_score: { score: 4, status: "assessed" },
    },
  ),
  entry(2, "acme/demo", { authored_by_me: true, checks: "failing" }, { checks: "failing" }),
  entry(3, "acme/demo", { authored_by_me: true }, { merge: "ready", checks: "passing" }),
  entry(4, "other/thing", { authored_by_me: true }, { merge: "ready", checks: "passing" }),
];

const inbox = (id: string, overrides: Partial<Inbox>): Inbox => ({
  id,
  name: id,
  color: "blue",
  what: "any",
  owners: [],
  risk: "any",
  repositories: [],
  no_bots: false,
  badge: true,
  ...overrides,
});
const reviews = inbox("Reviews", { what: "review", color: "purple" });
const merges = inbox("Merges", { what: "merge", color: "amber", badge: false });
const acme = inbox("Acme", { owners: ["acme"], color: "grey" });
const quiet = inbox("Quiet", { owners: ["nobody"], color: "red" });

let root: Root;
let node: HTMLDivElement;
let entries: Entry[] = [];
// What GET api/inboxes answers; null is an older daemon's 404.
let daemon: { revision: number; inboxes: Inbox[] } | null = null;
let puts: { revision: number; inboxes: Inbox[] }[] = [];

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  localStorage.clear();
  entries = fixture();
  daemon = { revision: 1, inboxes: [reviews, merges, acme, quiet] };
  puts = [];
  node = document.createElement("div");
  document.body.append(node);
  root = createRoot(node);
  vi.stubGlobal(
    "EventSource",
    class {
      addEventListener() {}
      close() {}
    },
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, init?: RequestInit) => {
      if (path.endsWith("api/inboxes")) {
        if (init?.method === "PUT") {
          const body = JSON.parse(init.body as string) as { revision: number; inboxes: Inbox[] };
          puts.push(body);
          daemon = {
            revision: body.revision + 1,
            inboxes: body.inboxes.map((one, i) => ({ ...one, id: one.id ?? `new-${i}` })),
          };
        }
        return daemon
          ? Response.json({ available: true, ...daemon })
          : new Response("not found", { status: 404 });
      }
      return Response.json(
        path.includes("api/index")
          ? { entries, problems: [] }
          : path.endsWith("api/repositories")
            ? [{ repository: "acme/demo", settings: {}, clones: [] }]
            : path.endsWith("api/repos")
              ? [{ id: "clone", repository: "acme/demo", path: "/c", available: true }]
              : path.endsWith("api/prs") || path.endsWith("api/background")
                ? []
                : path.endsWith("api/jobs")
                  ? { jobs: [] }
                  : path.endsWith("api/github-auth")
                    ? {
                        state: "connected",
                        host: "github.com",
                        login: "acme",
                        override: false,
                        saved: true,
                      }
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
const render = async (search = "") => {
  await act(async () => root.unmount());
  root = createRoot(node);
  history.replaceState(null, "", `/s/test-ns/${search}`);
  await act(async () => root.render(<App />));
};
const rail = () => node.querySelector(".local-rail")!;
const subRows = () => Array.from(node.querySelectorAll<HTMLElement>(".local-rail-inbox"));
const subLink = (name: string) =>
  Array.from(node.querySelectorAll<HTMLAnchorElement>("a.local-rail-sub")).find((a) =>
    a.textContent?.startsWith(name),
  )!;
const inboxLink = () =>
  Array.from(rail().querySelectorAll<HTMLAnchorElement>("a.local-rail-link")).find((a) =>
    a.textContent?.startsWith("Inbox"),
  )!;
const titles = () =>
  Array.from(node.querySelectorAll(".upnext-row-title")).map((title) => title.textContent);
// Lets a save's request and its answer finish.
const click = (element: Element | undefined | null) =>
  act(async () => {
    (element as HTMLElement).click();
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
const menuItem = (label: string) =>
  Array.from(node.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')).find(
    (item) => item.textContent === label,
  )!;

it("nests the inboxes under Inbox with their counts and the current one", async () => {
  await render();
  expect(subRows().map((row) => row.textContent?.replace("⋯", ""))).toEqual([
    "Reviews1",
    "Merges2",
    "Acme3",
    // Nothing matches: no count.
    "Quiet",
  ]);
  // The badge counts what is in no inbox or in one that counts: all but 4.
  expect(inboxLink().querySelector(".local-rail-badge")?.textContent).toBe("3");
  expect(subLink("Merges").querySelector(".local-rail-sub-count")?.className).toContain("muted");
  expect(subLink("Acme").querySelector(".local-rail-sub-count")?.className).not.toContain("muted");
  expect(subLink("Acme").getAttribute("href")).toBe("/s/test-ns/?inbox=Acme");
  expect(inboxLink().getAttribute("aria-current")).toBe("page");
  expect(subLink("Acme").getAttribute("aria-current")).toBeNull();

  await click(subLink("Acme"));
  expect(subLink("Acme").getAttribute("aria-current")).toBe("page");
  expect(inboxLink().getAttribute("aria-current")).toBeNull();
});

it("folds the nested list with its chevron and remembers it", async () => {
  await render();
  const fold = () => rail().querySelector<HTMLButtonElement>(".local-rail-fold")!;
  expect(fold().getAttribute("aria-expanded")).toBe("true");
  expect(fold().getAttribute("aria-label")).toBe("Hide inboxes");
  await click(fold());
  expect(subRows()).toHaveLength(0);
  expect(fold().getAttribute("aria-label")).toBe("Show inboxes");
  expect(fold().getAttribute("aria-expanded")).toBe("false");
  await render();
  expect(subRows()).toHaveLength(0);
  await click(fold());
  expect(subRows()).toHaveLength(4);
});

it("shows only an inbox's own queue under its name and sentence", async () => {
  await render("?inbox=Merges");
  expect(titles()).toEqual(["Change 3", "Change 4"]);
  const head = node.querySelector(".upnext-list-head")!;
  expect(head.querySelector("h1")!.textContent).toBe("Merges");
  expect(head.textContent).toContain("Things ready to merge in any repository, whatever the risk.");
  // Done today, its bar and the person's pull requests waiting on others are the Inbox's.
  expect(node.querySelector('[role="progressbar"]')).toBeNull();
  expect(node.querySelector('[aria-label="Done today"]')).toBeNull();
  expect(
    Array.from(node.querySelectorAll(".upnext-group-head h2")).map((h) => h.textContent),
  ).toEqual(["Ready to merge · 2"]);
  expect(subLink("Merges").getAttribute("aria-current")).toBe("page");
  // Change 3 is also in Acme.
  expect(node.querySelector(".upnext-panel")!.textContent).toContain("Change 3");
  expect(
    Array.from(node.querySelectorAll(".upnext-tags .upnext-tag")).map((t) => t.textContent),
  ).toEqual(["Acme"]);
  // Merge all counts only this inbox's pull requests.
  expect(node.querySelector(".upnext-batch")!.textContent).toBe("Merge all 2");
  await render("?inbox=Acme");
  expect(titles()).toEqual(["Change 1", "Change 3", "Change 2"]);
  await render("?inbox=Reviews");
  expect(titles()).toEqual(["Change 1"]);
});

it("says an inbox with nothing in it fills up, and falls back to Inbox for an unknown one", async () => {
  await render("?inbox=Quiet");
  expect(node.querySelector(".upnext-zero")!.textContent).toBe(
    "Nothing here right now.It fills up as matching pull requests come in.",
  );
  await render("?inbox=gone");
  expect(titles()).toEqual(["Change 1", "Change 3", "Change 4", "Change 2"]);
  expect(node.querySelector(".upnext-list-head h1")!.textContent).toBe("Up next");
  expect(inboxLink().getAttribute("aria-current")).toBe("page");
});

it("tags rows with their first inbox and the panel with all of them in Inbox", async () => {
  await render();
  const tagOf = (title: string) =>
    Array.from(node.querySelectorAll(".upnext-row"))
      .find((row) => row.textContent?.includes(title))
      ?.querySelector(".upnext-tag")?.textContent;
  expect(tagOf("Change 1")).toBe("Reviews");
  expect(tagOf("Change 3")).toBe("Merges");
  expect(tagOf("Change 4")).toBe("Merges");
  expect(tagOf("Change 2")).toBe("Acme");
  // Change 1 is in Reviews and Acme.
  expect(
    Array.from(node.querySelectorAll(".upnext-panel .upnext-tag")).map((t) => t.textContent),
  ).toEqual(["Reviews", "Acme"]);
});

it("deletes an inbox with Undo", async () => {
  await render();
  await click(node.querySelector('[aria-label="More for Acme"]'));
  expect(node.querySelector('[aria-label="More for Acme"]')!.getAttribute("aria-expanded")).toBe(
    "true",
  );
  await click(menuItem("Delete inbox"));
  expect(puts).toHaveLength(1);
  expect(puts[0]!.revision).toBe(1);
  expect(puts[0]!.inboxes.map((one) => one.id)).toEqual(["Reviews", "Merges", "Quiet"]);
  expect(subRows().map((row) => row.textContent)).not.toContain("Acme3⋯");
  expect(node.querySelector(".inbox-toast")!.textContent).toContain(
    "Deleted Acme. Its pull requests stay in Inbox.",
  );
  await click(node.querySelector(".inbox-toast button"));
  expect(puts).toHaveLength(2);
  // The daemon no longer stores it, so it goes back without its id, at its place.
  expect(puts[1]!.revision).toBe(2);
  expect(puts[1]!.inboxes.map((one) => one.name)).toEqual(["Reviews", "Merges", "Acme", "Quiet"]);
  expect("id" in puts[1]!.inboxes[2]!).toBe(false);
  expect(subRows().map((row) => row.querySelector(".local-rail-label")?.textContent)).toEqual([
    "Reviews",
    "Merges",
    "Acme",
    "Quiet",
  ]);
  expect(node.querySelector(".inbox-toast")).toBeNull();
});

it("switches an inbox in and out of the badge, and closes its menu with Esc", async () => {
  await render();
  await click(node.querySelector('[aria-label="More for Merges"]'));
  expect(menuItem("Count in the Inbox badge")).toBeTruthy();
  await act(async () => {
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  });
  expect(node.querySelector('[role="menu"]')).toBeNull();
  await click(node.querySelector('[aria-label="More for Merges"]'));
  await click(menuItem("Count in the Inbox badge"));
  expect(puts[0]!.inboxes.find((one) => one.id === "Merges")!.badge).toBe(true);
  // Change 4 now counts too.
  expect(inboxLink().querySelector(".local-rail-badge")?.textContent).toBe("4");
  await click(node.querySelector('[aria-label="More for Acme"]'));
  await act(async () => {
    document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
  });
  expect(node.querySelector('[role="menu"]')).toBeNull();
});

it("shows no inbox controls and today's badge with an older daemon", async () => {
  daemon = null;
  await render();
  expect(rail().querySelector(".local-rail-fold")).toBeNull();
  expect(subRows()).toHaveLength(0);
  expect(inboxLink().querySelector(".local-rail-badge")?.textContent).toBe("4");
  expect(node.querySelector(".upnext-tag")).toBeNull();
  expect(node.querySelector(".upnext-list-head h1")!.textContent).toBe("Up next");
  expect(titles()).toEqual(["Change 1", "Change 3", "Change 4", "Change 2"]);
});
