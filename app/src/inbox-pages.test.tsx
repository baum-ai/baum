import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { App } from "./app";
import type { Entry } from "./api";
import { MAX_INBOXES, type Inbox } from "./inboxes";

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

// Change 1: a review of acme's, risk 4. 2: acme's, failing checks (an agent
// can fix it). 3: ready to merge in acme. 4: ready to merge in other/thing,
// by a bot. The signed-in login is acme, so "other" is the only other owner.
const fixture = () => [
  entry(
    1,
    "acme/demo",
    { review_requested_from_me: true, author: "sam" },
    { status: "current", version_count: 1, risk_score: { score: 4, status: "assessed" } },
  ),
  entry(2, "acme/demo", { authored_by_me: true, checks: "failing" }, { checks: "failing" }),
  entry(3, "acme/demo", { authored_by_me: true }, { merge: "ready", checks: "passing" }),
  entry(
    4,
    "other/thing",
    { authored_by_me: true, author_bot: true },
    { merge: "ready", checks: "passing" },
  ),
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
const acme = inbox("Acme", { owners: ["acme"], color: "grey" });

let root: Root;
let node: HTMLDivElement;
let entries: Entry[] = [];
let daemon: { revision: number; inboxes: Inbox[] } | null = null;
let puts: { revision: number; inboxes: Inbox[] }[] = [];
// What the first read of the inboxes waits for, and a status that replaces its answer.
let inboxesHold: Promise<void> | null = null;
let inboxesFail: number | null = null;

beforeEach(() => {
  Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
  history.replaceState(null, "", "/s/test-ns/");
  localStorage.clear();
  // jsdom has no modal dialogs.
  HTMLDialogElement.prototype.showModal = function () {
    this.open = true;
  };
  entries = fixture();
  daemon = { revision: 1, inboxes: [reviews, acme] };
  puts = [];
  inboxesHold = null;
  inboxesFail = null;
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
        if (inboxesHold) await inboxesHold;
        if (inboxesFail) return new Response("no", { status: inboxesFail });
        if (init?.method === "PUT") {
          const body = JSON.parse(init.body as string) as { revision: number; inboxes: Inbox[] };
          puts.push(body);
          daemon = { revision: body.revision + 1, inboxes: body.inboxes };
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
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
const render = async (search = "") => {
  await act(async () => root.unmount());
  root = createRoot(node);
  history.replaceState(null, "", `/s/test-ns/${search}`);
  await act(async () => root.render(<App />));
};
// The real timer, so a test that fakes the clock can still let requests finish.
const realTimeout = setTimeout;
const settle = () => new Promise((resolve) => realTimeout(resolve, 0));
const click = (element: Element | undefined | null) =>
  act(async () => {
    (element as HTMLElement).click();
    await settle();
  });
const byText = <T extends Element = HTMLElement>(selector: string, text: string | RegExp) =>
  Array.from(node.querySelectorAll<HTMLElement>(selector)).find((element) =>
    typeof text === "string" ? element.textContent === text : text.test(element.textContent ?? ""),
  ) as T | undefined;
const button = (name: string | RegExp) => byText<HTMLButtonElement>("button", name)!;
const openActions = (name: string) =>
  click(node.querySelector(`button[aria-label="Actions for ${name}"]`));
const choice = (label: string) =>
  Array.from(node.querySelectorAll<HTMLElement>('[role="menu"] [role^=menuitem]')).find(
    (item) => item.querySelector(".context-menu-label")?.textContent === label,
  );
const choices = () =>
  Array.from(node.querySelectorAll<HTMLElement>('[role="menu"] [role^=menuitem]')).map(
    (item) => item.querySelector(".context-menu-label")!.textContent,
  );
const link = (name: string | RegExp) => byText<HTMLAnchorElement>("a", name)!;
const search = () => new URLSearchParams(location.search);
const template = (name: string) =>
  Array.from(node.querySelectorAll<HTMLButtonElement>(".inbox-template")).find(
    (element) => element.querySelector(".inbox-template-name")?.textContent === name,
  )!;
// Types into an input as a person would, then lets React see it.
const type = (input: HTMLInputElement, value: string) =>
  act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
const blur = (input: HTMLInputElement) =>
  act(async () => {
    input.focus();
    input.blur();
    await settle();
  });
const nameInput = () => node.querySelector<HTMLInputElement>("input.inbox-name")!;
const previewTitles = () =>
  Array.from(node.querySelectorAll(".inbox-preview-title")).map((title) => title.textContent);
const railLink = (name: string) =>
  Array.from(node.querySelectorAll<HTMLAnchorElement>(".local-rail a")).find((a) =>
    a.textContent?.includes(name),
  )!;

// --- New inbox -------------------------------------------------------------

it("lists the six templates with their sentence and how many pull requests each would show", async () => {
  await render("?page=inbox-new");
  expect(node.querySelector("h1")!.textContent).toBe("What should this inbox catch?");
  expect(node.textContent).toContain(
    "Start from one of these. You can change anything afterwards.",
  );
  expect(Array.from(node.querySelectorAll(".inbox-template")).map((t) => t.textContent)).toEqual([
    "Urgent1 nowAnything waiting on me in any repository, when risk is 4 or 5.",
    "Work1 nowAnything waiting on me in other, whatever the risk.",
    "Personal3 nowAnything waiting on me in my own repositories, whatever the risk.",
    "Needs my review1 nowReviews I’m asked for in any repository, whatever the risk.",
    "Quick merges2 nowThings ready to merge in any repository, whatever the risk.",
    "For an agent1 nowThings an agent can fix in any repository, whatever the risk.",
  ]);
  expect(template("Urgent").className).toContain("inbox-red");
  expect(node.textContent).toContain(
    "A pull request can sit in more than one inbox. Inbox at the top always shows everything.",
  );
  // The sidebar keeps Inbox as its section; only New inbox is the current page.
  expect(railLink("Inbox").getAttribute("aria-current")).toBeNull();
  expect(railLink("New inbox").getAttribute("aria-current")).toBe("page");
  expect(node.querySelectorAll('.local-rail [aria-current="page"]')).toHaveLength(1);
});

it("focuses the name once a template's page shows", async () => {
  await render("?page=inbox-new");
  await click(template("Urgent"));
  expect(search().get("page")).toBe("inbox-edit");
  expect(document.activeElement).toBe(nameInput());
});

it("makes Work from the owners of the person's repositories except their login", async () => {
  await render("?page=inbox-new");
  await click(template("Work"));
  expect(puts).toHaveLength(1);
  const made = puts[0]!.inboxes;
  // Appended at the end, with an id of the daemon's shape.
  expect(made.map((one) => one.name)).toEqual(["Reviews", "Acme", "Work"]);
  expect(made[2]).toMatchObject({
    name: "Work",
    color: "blue",
    what: "any",
    owners: ["other"],
    risk: "any",
    repositories: [],
    no_bots: false,
    badge: true,
  });
  expect(made[2]!.id).toMatch(/^[0-9a-f]{32}$/);
  // It opens its page, says so, and is in the sidebar.
  expect(search().get("page")).toBe("inbox-edit");
  expect(search().get("inbox")).toBe(made[2]!.id);
  expect(node.querySelector(".inbox-fresh")!.textContent).toBe(
    "Created and added to the sidebar. Adjust anything below.",
  );
  expect(railLink("Work")).toBeDefined();
});

it("makes Personal from the login with the badge off, and the others as their sentence says", async () => {
  await render("?page=inbox-new");
  await click(template("Personal"));
  expect(puts[0]!.inboxes[2]).toMatchObject({
    name: "Personal",
    color: "green",
    owners: ["acme"],
    badge: false,
  });
  await render("?page=inbox-new");
  await click(template("Urgent"));
  expect(puts[1]!.inboxes.at(-1)).toMatchObject({
    color: "red",
    what: "any",
    owners: [],
    risk: "4",
  });
  await render("?page=inbox-new");
  await click(template("Needs my review"));
  expect(puts[2]!.inboxes.at(-1)).toMatchObject({ color: "purple", what: "review", badge: true });
  await render("?page=inbox-new");
  await click(template("Quick merges"));
  expect(puts[3]!.inboxes.at(-1)).toMatchObject({ color: "amber", what: "merge" });
  await render("?page=inbox-new");
  await click(template("For an agent"));
  expect(puts[4]!.inboxes.at(-1)).toMatchObject({ color: "grey", what: "agent" });
});

it("numbers the name of a template another inbox already has", async () => {
  daemon = { revision: 1, inboxes: [inbox("Urgent", {}), inbox("urgent 2", {})] };
  await render("?page=inbox-new");
  expect(template("Urgent 3")).toBeDefined();
  await click(template("Urgent 3"));
  expect(puts[0]!.inboxes.map((one) => one.name)).toEqual(["Urgent", "urgent 2", "Urgent 3"]);
});

it("disables the templates at twenty inboxes and says so", async () => {
  daemon = {
    revision: 1,
    inboxes: Array.from({ length: MAX_INBOXES }, (_, n) => inbox(`Inbox ${n}`, {})),
  };
  await render("?page=inbox-new");
  expect(node.querySelector('[role="status"]')!.textContent).toContain("You have 20 inboxes");
  const all = Array.from(node.querySelectorAll<HTMLButtonElement>(".inbox-template"));
  expect(all).toHaveLength(6);
  expect(all.every((one) => one.disabled)).toBe(true);
  await click(all[0]);
  expect(puts).toHaveLength(0);
});

// --- Edit ------------------------------------------------------------------

it("edits the name in place and refuses a blank, too long or taken name without saving", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  expect(link(/← Acme/).getAttribute("href")).toBe("/s/test-ns/?inbox=Acme");
  expect(node.textContent).toContain("Changes save as you make them");
  expect(node.querySelector("label[for]")!.textContent).toBe("Name");
  await type(nameInput(), "  ");
  expect(puts).toHaveLength(0);
  await blur(nameInput());
  expect(node.querySelector(".inbox-name-error")!.textContent).toBe("Give the inbox a name.");
  expect(nameInput().getAttribute("aria-invalid")).toBe("true");
  await type(nameInput(), "REVIEWS");
  // Typing clears the message; it is not checked on every key.
  expect(node.querySelector(".inbox-name-error")).toBeNull();
  await blur(nameInput());
  expect(node.querySelector(".inbox-name-error")!.textContent).toBe(
    "Another inbox already has that name.",
  );
  await type(nameInput(), "x".repeat(41));
  await blur(nameInput());
  expect(node.querySelector(".inbox-name-error")!.textContent).toContain("40 characters");
  expect(puts).toHaveLength(0);

  // Its own name in another case is fine; Enter saves too.
  await type(nameInput(), "  ACME  ");
  await act(async () => {
    nameInput().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    await settle();
  });
  expect(puts).toHaveLength(1);
  expect(puts[0]!.inboxes.map((one) => one.name)).toEqual(["Reviews", "ACME"]);
  expect(node.querySelector(".inbox-name-error")).toBeNull();
  // Blurring afterwards saves nothing more.
  await blur(nameInput());
  expect(puts).toHaveLength(1);
});

it("shows Saved after a save and the colour as a radio group", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  const group = node.querySelector('[role="radiogroup"][aria-label="Colour"]')!;
  expect(
    Array.from(group.querySelectorAll('[role="radio"]')).map((radio) => [
      radio.getAttribute("aria-label"),
      radio.getAttribute("aria-checked"),
    ]),
  ).toEqual([
    ["Blue", "false"],
    ["Green", "false"],
    ["Red", "false"],
    ["Amber", "false"],
    ["Purple", "false"],
    ["Grey", "true"],
  ]);
  expect(node.querySelector(".settings-saved")!.textContent).toBe("");
  await click(group.querySelector('[aria-label="Red"]'));
  expect(puts).toHaveLength(1);
  expect(puts[0]!.inboxes[1]!.color).toBe("red");
  expect(node.querySelector(".settings-saved")!.textContent).toBe("Saved");
  expect(group.querySelector('[aria-label="Red"]')!.getAttribute("aria-checked")).toBe("true");
});

it("changes a part of the sentence with one save and shows what it added and removed", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  expect(node.querySelector(".inbox-preview-count")!.textContent).toBe("3");
  expect(previewTitles()).toEqual(["Change 1", "Change 3", "Change 2"]);
  const parts = Array.from(node.querySelectorAll<HTMLButtonElement>(".inbox-part"));
  expect(parts.map((part) => part.textContent)).toEqual([
    "Anything waiting on me",
    "in my own repositories",
    "whatever the risk",
  ]);
  await click(parts[0]);
  expect(parts[0]!.getAttribute("aria-expanded")).toBe("true");
  const options = Array.from(
    node.querySelectorAll<HTMLButtonElement>('[role="group"][aria-label="What it holds"] button'),
  );
  // Each option says how many pull requests it would show.
  expect(options.map((option) => option.textContent)).toEqual([
    "Anything waiting on me3 (3 now)",
    "Reviews I’m asked for1 (1 now)",
    "Things ready to merge1 (1 now)",
    "Things an agent can fix1 (1 now)",
    "My own pull requests2 (2 now)",
  ]);
  await click(options[1]);
  expect(puts).toHaveLength(1);
  expect(puts[0]!.inboxes[1]).toMatchObject({ id: "Acme", what: "review", owners: ["acme"] });
  expect(node.querySelector(".inbox-diff")!.textContent).toBe("2 removed");
  // The preview keeps the removed rows, struck through, until they fade.
  expect(node.querySelector(".inbox-preview-count")!.textContent).toBe("1");
  expect(
    Array.from(node.querySelectorAll(".inbox-preview-row.gone .inbox-preview-title")).map(
      (title) => title.textContent,
    ),
  ).toEqual(["Change 3", "Change 2"]);
  expect(
    Array.from(node.querySelectorAll(".inbox-preview-group h3")).map((h) => h.textContent),
  ).toEqual(["Needs your eyes · 1", "Ready to merge · 0", "Hand to an agent · 0"]);
  expect(node.querySelector('[aria-expanded="true"].inbox-part')).toBeNull();
  // Changing back adds them again.
  await click(node.querySelectorAll<HTMLButtonElement>(".inbox-part")[0]);
  await click(
    node.querySelectorAll<HTMLButtonElement>(
      '[role="group"][aria-label="What it holds"] button',
    )[0],
  );
  expect(puts).toHaveLength(2);
  expect(node.querySelector(".inbox-diff")!.textContent).toBe("2 added");
});

it("draws the diff's live region before there is a diff, empty", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  const region = node.querySelector(".inbox-diff")!;
  expect(region.getAttribute("role")).toBe("status");
  expect(region.textContent).toBe("");
  await click(node.querySelectorAll<HTMLButtonElement>(".inbox-part")[0]);
  await click(
    node.querySelectorAll<HTMLButtonElement>(
      '[role="group"][aria-label="What it holds"] button',
    )[1],
  );
  // The same element is filled, which is what a screen reader announces.
  expect(node.querySelector(".inbox-diff")).toBe(region);
  expect(region.textContent).toBe("2 removed");
});

it("lets the diff fade after a few seconds, the removed rows leaving with it", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  vi.useFakeTimers();
  await click(node.querySelectorAll<HTMLButtonElement>(".inbox-part")[2]);
  await click(
    node.querySelectorAll<HTMLButtonElement>('[role="group"][aria-label="Risk"] button')[2],
  );
  expect(node.querySelector(".inbox-diff")!.textContent).not.toBe("");
  // The rows fade over the diff's last moments (a CSS animation), so they are
  // still there until it ends.
  await act(async () => {
    vi.advanceTimersByTime(3900);
  });
  expect(node.querySelector(".inbox-preview-row.gone")).not.toBeNull();
  await act(async () => {
    vi.advanceTimersByTime(200);
  });
  expect(node.querySelector(".inbox-diff")!.textContent).toBe("");
  expect(node.querySelector(".inbox-preview-row.gone")).toBeNull();
});

it("offers where from the person's owners, and the set an inbox already holds", async () => {
  daemon = { revision: 1, inboxes: [inbox("Many", { owners: ["acme", "other"] })] };
  await render("?page=inbox-edit&inbox=Many");
  expect(node.querySelectorAll(".inbox-part")[1]!.textContent).toBe("in acme or other");
  await click(node.querySelectorAll<HTMLButtonElement>(".inbox-part")[1]);
  const group = node.querySelector('[role="group"][aria-label="Where"]')!;
  expect(
    Array.from(group.querySelectorAll("button")).map((option) => [
      option.firstChild?.textContent,
      option.getAttribute("aria-pressed"),
    ]),
  ).toEqual([
    ["Any repository", "false"],
    ["My own repositories", "false"],
    ["other", "false"],
    ["acme or other", "true"],
  ]);
  await click(group.querySelectorAll("button")[1]);
  expect(puts[0]!.inboxes[0]!.owners).toEqual(["acme"]);
  expect(node.querySelectorAll(".inbox-part")[1]!.textContent).toBe("in my own repositories");
});

it("toggles repositories and leaving out bots, and the badge", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  // Closed until asked for, as nothing is fine-tuned.
  expect(node.querySelector(".inbox-fine")).toBeNull();
  await click(button("Fine-tune"));
  const chip = (name: string) =>
    Array.from(node.querySelectorAll<HTMLButtonElement>(".inbox-fine .inbox-chip")).find(
      (c) => c.textContent === name,
    )!;
  expect(node.querySelector(".inbox-fine")!.textContent).toContain(
    "Only these repositories · none picked means all that match above",
  );
  await click(chip("acme/demo"));
  expect(puts[0]!.inboxes[1]!.repositories).toEqual(["acme/demo"]);
  expect(chip("acme/demo").getAttribute("aria-pressed")).toBe("true");
  await click(chip("acme/demo"));
  expect(puts[1]!.inboxes[1]!.repositories).toEqual([]);

  const bots = node.querySelector<HTMLInputElement>('.inbox-leave-out input[type="checkbox"]')!;
  expect(node.querySelector(".inbox-leave-out")!.textContent).toBe(
    "Leave outBots· Renovate, Dependabot",
  );
  await click(bots);
  expect(puts[2]!.inboxes[1]!.no_bots).toBe(true);
  await click(bots);
  expect(puts[3]!.inboxes[1]!.no_bots).toBe(false);

  const badge = node.querySelector<HTMLInputElement>('input[role="switch"]')!;
  expect(badge.labels![0]!.textContent).toBe("Count it in the Inbox badge");
  expect(node.textContent).toContain("Its pull requests add to the number on Inbox.");
  await click(badge);
  expect(puts[4]!.inboxes[1]!.badge).toBe(false);
  expect(node.textContent).toContain(
    "Left out of the badge, so it never nags. It still shows here and in Inbox.",
  );
});

it("opens fine-tune with what is already set, and a bot filter changes the preview", async () => {
  daemon = { revision: 1, inboxes: [inbox("Merges", { what: "merge" })] };
  await render("?page=inbox-edit&inbox=Merges");
  expect(previewTitles()).toEqual(["Change 3", "Change 4"]);
  await click(button("Fine-tune"));
  await click(node.querySelector('.inbox-leave-out input[type="checkbox"]'));
  expect(node.querySelector(".inbox-diff")!.textContent).toBe("1 removed");
  await render("?page=inbox-edit&inbox=Merges");
  expect(node.querySelector(".inbox-fine")!.textContent).toContain("Leave out");
});

it("tags the preview's rows with the other inboxes and says what an empty one waits for", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  const rows = Array.from(node.querySelectorAll(".inbox-preview-row"));
  // Change 1 is also in Reviews; the inbox being edited is not tagged.
  expect(rows[0]!.textContent).toBe("Change 1acme/demo #1Reviews");
  expect(rows[1]!.textContent).toBe("Change 3acme/demo #3");
  expect(node.textContent).toContain("Tags show the other inboxes a pull request is also in.");
  daemon = { revision: 1, inboxes: [inbox("Quiet", { owners: ["nobody"] })] };
  await render("?page=inbox-edit&inbox=Quiet");
  expect(node.querySelector(".inbox-preview-count")!.textContent).toBe("0");
  expect(node.querySelector(".inbox-preview")!.textContent).toContain(
    "Nothing yet. It fills up as pull requests come in.",
  );
});

it("leaves snoozed pull requests out of the preview, as the queue does", async () => {
  const day = new Date();
  localStorage.setItem(
    "baum.inbox.v1",
    JSON.stringify({
      day: `${day.getFullYear()}-${day.getMonth() + 1}-${day.getDate()}`,
      seen: {},
      snoozed: { "acme/demo#3": Date.now() + 3600_000 },
      held: {},
    }),
  );
  await render("?page=inbox-edit&inbox=Acme");
  expect(previewTitles()).toEqual(["Change 1", "Change 2"]);
});

it("deletes the inbox from its page with Undo and goes to Inbox", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  expect(node.textContent).toContain("Deleting an inbox leaves its pull requests in Inbox.");
  await click(button("Delete inbox"));
  expect(puts).toHaveLength(1);
  expect(puts[0]!.inboxes.map((one) => one.id)).toEqual(["Reviews"]);
  expect(location.search).toBe("");
  expect(node.querySelector(".upnext-list-head h1")!.textContent).toBe("Up next");
  // Focus goes to Inbox in the sidebar, not to the page.
  expect(document.activeElement).toBe(railLink("Inbox"));
  expect(node.querySelector(".inbox-toast")!.textContent).toContain(
    "Deleted Acme. Its pull requests stay in Inbox.",
  );
  await click(node.querySelector(".inbox-toast button"));
  expect(puts[1]!.inboxes.map((one) => one.id)).toEqual(["Reviews", "Acme"]);
});

it("says an inbox that is gone no longer exists", async () => {
  await render("?page=inbox-edit&inbox=gone");
  expect(node.textContent).toContain("This inbox no longer exists");
  expect(link("Go to Inbox").getAttribute("href")).toBe("/s/test-ns/");
});

// --- Manage ----------------------------------------------------------------

it("lists the inboxes in order with their counts, sentences and controls", async () => {
  await render("?page=inboxes");
  expect(node.querySelector("h1")!.textContent).toBe("Inboxes");
  expect(node.textContent).toContain(
    "Inbox shows everything. These sit under it in the sidebar, in this order. A pull request can be in several.",
  );
  const rows = Array.from(node.querySelectorAll(".inbox-manage-row"));
  expect(rows.map((row) => row.querySelector(".inbox-manage-text")!.textContent)).toEqual([
    "Reviews1 nowReviews I’m asked for in any repository, whatever the risk.",
    "Acme3 nowAnything waiting on me in my own repositories, whatever the risk.",
  ]);
  // The first has nothing to move up to, the last nothing to move down to.
  await openActions("Reviews");
  expect(choices()).toEqual(["Edit…", "Move down", "Count in the Inbox badge"]);
  await openActions("Acme");
  expect(choices()).toEqual(["Edit…", "Move up", "Count in the Inbox badge"]);
  expect(node.querySelector('[aria-label="Edit Reviews"]')!.getAttribute("href")).toBe(
    "/s/test-ns/?page=inbox-edit&inbox=Reviews",
  );
  expect(link("New inbox").getAttribute("href")).toBe("/s/test-ns/?page=inbox-new");
  expect(railLink("Manage inboxes").getAttribute("aria-current")).toBe("page");
  expect(railLink("Inbox").getAttribute("aria-current")).toBeNull();
  expect(node.querySelectorAll('.local-rail [aria-current="page"]')).toHaveLength(1);
});

it("reorders inboxes, saving the new order, and switches the badge", async () => {
  await render("?page=inboxes");
  await openActions("Acme");
  await click(choice("Move up"));
  expect(puts).toHaveLength(1);
  expect(puts[0]!.inboxes.map((one) => one.id)).toEqual(["Acme", "Reviews"]);
  expect(
    Array.from(node.querySelectorAll(".inbox-manage-name strong")).map((n) => n.textContent),
  ).toEqual(["Acme", "Reviews"]);
  expect(node.querySelector(".settings-saved")!.textContent).toBe("Saved");
  // The sidebar follows.
  expect(
    Array.from(node.querySelectorAll(".local-rail-inbox .local-rail-label")).map(
      (n) => n.textContent,
    ),
  ).toEqual(["Acme", "Reviews"]);
  await openActions("Acme");
  await click(choice("Move down"));
  expect(puts[1]!.inboxes.map((one) => one.id)).toEqual(["Reviews", "Acme"]);

  await openActions("Reviews");
  expect(choice("Count in the Inbox badge")!.getAttribute("aria-checked")).toBe("true");
  await click(choice("Count in the Inbox badge"));
  expect(puts[2]!.inboxes[0]).toMatchObject({ id: "Reviews", badge: false });
  expect(node.querySelector(".inbox-manage-row")!.textContent).toContain(
    "· not counted in the badge",
  );
});

it("says how many pull requests are in no inbox, or that every one is in some", async () => {
  daemon = { revision: 1, inboxes: [reviews] };
  await render("?page=inboxes");
  expect(node.querySelector(".inbox-coverage")!.textContent).toBe(
    "3 pull requests aren’t in any inbox right now. They show only under Inbox and always count in its badge.",
  );
  expect(node.querySelector(".inbox-coverage")!.classList.contains("caution")).toBe(true);
  daemon = { revision: 1, inboxes: [reviews, inbox("Rest", {})] };
  await render("?page=inboxes");
  expect(node.querySelector(".inbox-coverage")!.textContent).toBe(
    "Every open pull request is in at least one inbox.",
  );
  expect(node.querySelector(".inbox-coverage")!.classList.contains("ok")).toBe(true);
});

it("leads to New inbox when there are no inboxes", async () => {
  daemon = { revision: 1, inboxes: [] };
  await render("?page=inboxes");
  expect(node.querySelector(".local-empty h2")!.textContent).toBe("No inboxes yet");
  await click(link("New inbox"));
  expect(search().get("page")).toBe("inbox-new");
});

// --- Links, Go to and an older daemon ----------------------------------------

it("adds New inbox and Manage inboxes to the sidebar even with none, and Edit to the menu and queue", async () => {
  daemon = { revision: 1, inboxes: [] };
  await render();
  expect(railLink("New inbox").getAttribute("href")).toBe("/s/test-ns/?page=inbox-new");
  expect(railLink("Manage inboxes").getAttribute("href")).toBe("/s/test-ns/?page=inboxes");
  daemon = { revision: 1, inboxes: [acme] };
  await render();
  const first = node.querySelector<HTMLButtonElement>('[aria-label="More for Acme"]')!;
  await click(first);
  expect(node.querySelector(".local-rail-menu button")!.textContent).toBe("Edit inbox");
  await click(node.querySelector(".local-rail-menu button"));
  expect(search().get("page")).toBe("inbox-edit");
  expect(search().get("inbox")).toBe("Acme");
  // The inbox's queue has an Edit link in its header.
  await render("?inbox=Acme");
  expect(node.querySelector(".upnext-list-title a")!.getAttribute("href")).toBe(
    "/s/test-ns/?page=inbox-edit&inbox=Acme",
  );
  // The Inbox itself has none.
  await render();
  expect(node.querySelector(".upnext-list-title")).toBeNull();
});

it("lists the inboxes, New inbox and Manage inboxes in Go to", async () => {
  await render();
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }));
  });
  const options = () =>
    Array.from(node.querySelectorAll('[data-go-to] [role="option"]')).map((o) => o.textContent);
  expect(options()).toContain("AcmeInbox");
  expect(options()).toContain("ReviewsInbox");
  expect(options()).toContain("New inboxPage");
  expect(options()).toContain("Manage inboxesPage");
  // Found by name; opening one shows its queue.
  const input = node.querySelector<HTMLInputElement>("[data-go-to] input")!;
  await type(input, "acme");
  expect(options()[0]).toBe("AcmeInbox");
  await click(node.querySelector('[data-go-to] [role="option"]'));
  expect(search().get("inbox")).toBe("Acme");
  expect(node.querySelector(".upnext-list-head h1")!.textContent).toBe("Acme");
  // ⌘1 still opens Inbox.
  await act(async () => {
    window.dispatchEvent(
      new KeyboardEvent("keydown", { key: "1", code: "Digit1", ctrlKey: true, bubbles: true }),
    );
  });
  expect(location.search).toBe("");
});

it("shows none of it with an older daemon, and Inbox for a direct address", async () => {
  daemon = null;
  for (const page of ["inbox-new", "inbox-edit&inbox=Acme", "inboxes"]) {
    await render(`?page=${page}`);
    expect(node.querySelector(".upnext-list-head h1")!.textContent).toBe("Up next");
    expect(node.querySelector(".inbox-page")).toBeNull();
  }
  expect(node.textContent).not.toContain("New inbox");
  expect(node.textContent).not.toContain("Manage inboxes");
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }));
  });
  const options = Array.from(node.querySelectorAll('[data-go-to] [role="option"]')).map(
    (o) => o.textContent,
  );
  expect(options.some((o) => /inbox/i.test(o ?? "") && o !== "InboxPage")).toBe(false);
});

it("shows loading, not Inbox, for an inbox page until the first read answers", async () => {
  let release!: () => void;
  inboxesHold = new Promise<void>((resolve) => (release = resolve));
  await render("?page=inboxes");
  expect(node.querySelector(".upnext-list-head h1")).toBeNull();
  expect(node.querySelector(".inbox-page")).toBeNull();
  expect(node.textContent).toContain("Loading inboxes…");
  await act(async () => {
    release();
    await settle();
  });
  expect(node.textContent).not.toContain("Loading inboxes…");
  expect(node.querySelector(".inbox-manage")).not.toBeNull();
});

it("says the read failed on an inbox page when the first read fails, and recovers", async () => {
  inboxesFail = 500;
  await render("?page=inbox-edit&inbox=Acme");
  expect(node.querySelector(".upnext-list-head h1")).toBeNull();
  const alerts = Array.from(node.querySelectorAll('[role="alert"]'));
  expect(alerts).toHaveLength(1);
  expect(alerts[0]!.textContent).toContain("Couldn't read your inboxes");
  inboxesFail = null;
  await click(button("Retry"));
  expect(node.querySelector('[role="alert"]')).toBeNull();
  expect(nameInput().value).toBe("Acme");
});

it("falls back to Inbox for a daemon that says it has no inboxes", async () => {
  daemon = null;
  await render("?page=inboxes");
  expect(node.querySelector(".upnext-list-head h1")!.textContent).toBe("Up next");
  expect(node.querySelector('[role="alert"]')).toBeNull();
});
