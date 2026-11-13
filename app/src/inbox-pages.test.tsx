import { mockStateFetch, seedUIState } from "./ui-state-test-helper";
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
  query: "",
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
    mockStateFetch(async (path: string, init?: RequestInit) => {
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
              : path.endsWith("api/prs?view=health") || path.endsWith("api/background")
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
// Inbox in the sidebar: a heading over the saved inboxes, not a link.
const railHeading = () => node.querySelector<HTMLElement>(".local-rail-heading")!;

// --- New inbox -------------------------------------------------------------

it("lists the seven templates with their sentence and how many pull requests each would show", async () => {
  await render("?page=inbox-new");
  expect(node.querySelector("h1")!.textContent).toBe("What should this inbox catch?");
  expect(node.textContent).not.toContain("Start from one of these");
  expect(Array.from(node.querySelectorAll(".inbox-template")).map((t) => t.textContent)).toEqual([
    "Custom4 nowEvery open pull request, until you add filters.",
    "Urgent1 nowMine, assigned to me or asking for my review, at risk 4 or 5.",
    "Work1 nowMine, assigned to me or asking for my review, in other.",
    "Personal3 nowEvery open pull request in my own repositories, whatever the risk.",
    "Quick merges2 nowThings ready to merge in any repository, whatever the risk.",
    "For an agent1 nowThings an agent can fix in any repository, whatever the risk.",
    "Unclaimed0 nowOthers' pull requests with no one assigned and no review requested.",
  ]);
  expect(template("Urgent").className).toContain("inbox-red");
  expect(node.textContent).toContain("A pull request can sit in more than one inbox.");
  // The sidebar keeps Inbox as its section, and has no New inbox entry.
  expect(railHeading().getAttribute("aria-current")).toBeNull();
  expect(railLink("New inbox")).toBeUndefined();
});

it("focuses the name once a template's page shows", async () => {
  await render("?page=inbox-new");
  await click(template("Urgent"));
  expect(search().get("page")).toBe("inbox-edit");
  expect(document.activeElement).toBe(nameInput());
});

// Opens a template and saves it, as its page's Save does.
const saveTemplate = async (name: string) => {
  await render("?page=inbox-new");
  await click(template(name));
  await click(button("Save"));
};

it("opens a template unsaved, and Save adds it, from the owners of the person's repositories except their login", async () => {
  await render("?page=inbox-new");
  await click(template("Work"));
  // Nothing is saved yet: the page says so and offers Save, not Delete.
  expect(puts).toHaveLength(0);
  expect(search().get("page")).toBe("inbox-edit");
  expect(node.querySelector("h1")!.textContent).toBe("New inbox");
  expect(node.querySelector(".inbox-fresh")!.textContent).toBe(
    "Not saved yet. Adjust anything below, then Save.",
  );
  expect(byText("button", "Delete inbox")).toBeUndefined();
  expect(railLink("Work")).toBeUndefined();
  expect(node.querySelector<HTMLTextAreaElement>(".inbox-query textarea")!.value).toBe(
    "org=other (author=@me OR assignee=@me OR review-requested=@me)",
  );
  await click(button("Save"));
  expect(puts).toHaveLength(1);
  const made = puts[0]!.inboxes;
  // Appended at the end, with an id of the daemon's shape, its parts one query.
  expect(made.map((one) => one.name)).toEqual(["Reviews", "Acme", "Work"]);
  expect(made[2]).toMatchObject({
    name: "Work",
    color: "blue",
    what: "any",
    owners: [],
    query: "org=other (author=@me OR assignee=@me OR review-requested=@me)",
    badge: true,
  });
  expect(made[2]!.id).toMatch(/^[0-9a-f]{32}$/);
  // Saved, it opens its queue and is in the sidebar.
  expect(search().get("inbox")).toBe(made[2]!.id);
  expect(railLink("Work")).toBeDefined();
});

it("keeps changes made before Save, and leaving drops the unsaved inbox", async () => {
  await render("?page=inbox-new");
  await click(template("Custom"));
  await type(nameInput(), "Mine only");
  await blur(nameInput());
  expect(puts).toHaveLength(0);
  await click(button("Save"));
  expect(puts[0]!.inboxes.at(-1)).toMatchObject({ name: "Mine only", query: "" });
  await render("?page=inbox-new");
  await click(template("Urgent"));
  await click(link("← New inbox"));
  expect(search().get("page")).toBe("inbox-new");
  expect(puts).toHaveLength(1);
});

it("checks the name as typed on Save, and keeps the unsaved inbox when the save is refused", async () => {
  await render("?page=inbox-new");
  await click(template("Urgent"));
  await type(nameInput(), "Acme");
  await click(button("Save"));
  // Taken: nothing is saved and it says why.
  expect(puts).toHaveLength(0);
  expect(node.querySelector(".inbox-name-error")!.textContent).toBe(
    "Another inbox already has that name.",
  );
  await type(nameInput(), "Hot");
  inboxesFail = 500;
  await click(button("Save"));
  // Refused: still the unsaved inbox, to save again.
  expect(search().get("page")).toBe("inbox-edit");
  expect(node.querySelector("h1")!.textContent).toBe("New inbox");
  expect(nameInput().value).toBe("Hot");
  inboxesFail = null;
  await click(button("Save"));
  expect(puts.at(-1)!.inboxes.at(-1)).toMatchObject({
    name: "Hot",
    query: "risk>=4 (author=@me OR assignee=@me OR review-requested=@me)",
  });
  expect(search().get("inbox")).toBe(puts.at(-1)!.inboxes.at(-1)!.id);
});

it("makes Personal from the login with the badge off, and the others as their query says", async () => {
  await saveTemplate("Personal");
  expect(puts[0]!.inboxes[2]).toMatchObject({
    name: "Personal",
    color: "green",
    query: "org=acme",
    badge: false,
  });
  await saveTemplate("Urgent");
  expect(puts[1]!.inboxes.at(-1)).toMatchObject({
    color: "red",
    query: "risk>=4 (author=@me OR assignee=@me OR review-requested=@me)",
  });
  await saveTemplate("Quick merges");
  expect(puts[2]!.inboxes.at(-1)).toMatchObject({ color: "amber", query: "waiting=merge" });
  await saveTemplate("For an agent");
  expect(puts[3]!.inboxes.at(-1)).toMatchObject({ color: "blue", query: "waiting=agent" });
  await saveTemplate("Custom");
  expect(puts[4]!.inboxes.at(-1)).toMatchObject({ name: "Custom", query: "" });
});

it("numbers the name of a template another inbox already has", async () => {
  daemon = { revision: 1, inboxes: [inbox("Urgent", {}), inbox("urgent 2", {})] };
  await render("?page=inbox-new");
  expect(template("Urgent 3")).toBeDefined();
  await click(template("Urgent 3"));
  await click(button("Save"));
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
  expect(all).toHaveLength(7);
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

// Types a query into the edit page's filter, which applies it on leaving.
const typeQuery = (value: string) =>
  act(async () => {
    const box = node.querySelector<HTMLTextAreaElement>(".inbox-query textarea")!;
    box.focus();
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    setter.call(box, value);
    box.dispatchEvent(new Event("input", { bubbles: true }));
    box.blur();
    await settle();
  });
const queryBox = () => node.querySelector<HTMLTextAreaElement>(".inbox-query textarea")!;

it("shows the inbox as one query, saving each change once and saying what it added and removed", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  expect(queryBox().value).toBe("org=acme");
  expect(node.querySelector(".inbox-query summary.filter-menu-lead")).not.toBeNull();
  expect(node.querySelector(".inbox-preview-count")!.textContent).toBe("3");
  expect(previewTitles()).toEqual(["Change 1", "Change 3", "Change 2"]);
  await typeQuery("org=acme waiting=review");
  expect(puts).toHaveLength(1);
  expect(puts[0]!.inboxes[1]).toMatchObject({
    id: "Acme",
    query: "org=acme waiting=review",
    what: "any",
    owners: [],
  });
  expect(node.querySelector(".inbox-diff")!.textContent).toBe("2 removed");
  // The preview keeps the removed rows, struck through, until they fade.
  expect(node.querySelector(".inbox-preview-count")!.textContent).toBe("1");
  expect(
    Array.from(node.querySelectorAll(".inbox-preview-row.gone .inbox-preview-title")).map(
      (title) => title.textContent,
    ),
  ).toEqual(["Change 3", "Change 2"]);
  // Changing back adds them again.
  await typeQuery("org=acme");
  expect(puts).toHaveLength(2);
  expect(node.querySelector(".inbox-diff")!.textContent).toBe("2 added");
});

it("draws the diff's live region before there is a diff, empty", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  const region = node.querySelector(".inbox-diff")!;
  expect(region.getAttribute("role")).toBe("status");
  expect(region.textContent).toBe("");
  await typeQuery("org=acme waiting=review");
  // The same element is filled, which is what a screen reader announces.
  expect(node.querySelector(".inbox-diff")).toBe(region);
  expect(region.textContent).toBe("2 removed");
});

it("lets the diff fade after a few seconds, the removed rows leaving with it", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  vi.useFakeTimers();
  await typeQuery("org=acme risk>=4");
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

it("folds what an inbox holds, its owners, risk, repositories and bots into its query", async () => {
  daemon = {
    revision: 1,
    inboxes: [
      inbox("Many", {
        what: "merge",
        owners: ["acme", "other"],
        risk: "3",
        repositories: ["acme/demo"],
        no_bots: true,
        query: "checks=passing OR checks=none",
      }),
    ],
  };
  await render("?page=inbox-edit&inbox=Many");
  expect(queryBox().value).toBe(
    "waiting=merge org=acme,other risk>=3 repo=acme/demo is!=bot (checks=passing OR checks=none)",
  );
  daemon = { revision: 1, inboxes: [inbox("Mine", { what: "mine" })] };
  await render("?page=inbox-edit&inbox=Mine");
  expect(queryBox().value).toBe("author=@me");
});

it("counts the inbox in the badge or not", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  const badge = node.querySelector<HTMLInputElement>('input[role="switch"]')!;
  expect(badge.labels![0]!.textContent).toBe("Show its count in the sidebar");
  expect(node.textContent).toContain("The sidebar shows how many of its pull requests wait.");
  await click(badge);
  expect(puts[0]!.inboxes[1]!.badge).toBe(false);
  expect(node.textContent).toContain(
    "No count beside it, so it never nags. Its pull requests still add to the Inbox total.",
  );
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
  seedUIState("api/ui-state/inbox", {
    day: `${day.getFullYear()}-${day.getMonth() + 1}-${day.getDate()}`,
    seen: {},
    snoozed: { "acme/demo#3": Date.now() + 3600_000 },
    held: {},
  });
  await render("?page=inbox-edit&inbox=Acme");
  expect(previewTitles()).toEqual(["Change 1", "Change 2"]);
});

it("deletes the inbox from its page once confirmed, with Undo, and goes to the first inbox left", async () => {
  await render("?page=inbox-edit&inbox=Acme");
  expect(node.textContent).toContain(
    "Deleting an inbox keeps its pull requests, reviews and snoozes.",
  );
  await click(button("Delete inbox"));
  // It asks first, saying only the view goes.
  expect(puts).toHaveLength(0);
  expect(node.querySelector("dialog")!.textContent).toContain(
    "Only this view goes. Its pull requests, reviews and snoozes stay.",
  );
  await click(byText("dialog button", "Cancel"));
  expect(node.querySelector("dialog")).toBeNull();
  expect(puts).toHaveLength(0);
  await click(button("Delete inbox"));
  await click(byText("dialog button", "Delete inbox"));
  expect(puts).toHaveLength(1);
  expect(puts[0]!.inboxes.map((one) => one.id)).toEqual(["Reviews"]);
  // The edit page's entry becomes the first inbox left.
  expect(location.search).toBe("?inbox=Reviews");
  expect(node.querySelector(".upnext-list-head h1")!.textContent).toBe("Reviews");
  // Focus goes to that inbox in the sidebar, not to the page.
  expect(document.activeElement).toBe(node.querySelector('.local-rail a[href$="?inbox=Reviews"]'));
  expect(node.querySelector(".inbox-toast")!.textContent).toContain("Deleted Acme.");
  await click(node.querySelector(".inbox-toast button"));
  expect(puts[1]!.inboxes.map((one) => one.id)).toEqual(["Reviews", "Acme"]);
});

it("cannot delete the last inbox from its edit page", async () => {
  daemon = { revision: 1, inboxes: [acme] };
  await render("?page=inbox-edit&inbox=Acme");
  const del = button("Delete inbox");
  expect(del.disabled).toBe(true);
  expect(node.querySelector(".inbox-delete")!.textContent).toContain("Keep at least one inbox");
  expect(del.getAttribute("aria-describedby")).toBe(
    node.querySelector(".inbox-delete p")!.getAttribute("id"),
  );
  await click(del);
  expect(node.querySelector("dialog")).toBeNull();
  expect(puts).toHaveLength(0);
});

it("says an inbox that is gone no longer exists", async () => {
  await render("?page=inbox-edit&inbox=gone");
  expect(node.textContent).toContain("This inbox no longer exists");
  expect(link("Go to Inbox").getAttribute("href")).toBe("/s/test-ns/");
});

// --- Manage ----------------------------------------------------------------

it("lists the inboxes in order with their counts and controls", async () => {
  await render("?page=inboxes");
  expect(node.querySelector("h1")!.textContent).toBe("Inboxes");
  expect(node.textContent).toContain(
    "These are your inboxes, in their order in the sidebar. A pull request can be in several.",
  );
  // Nothing here deletes an inbox, so the last one cannot go from this page.
  expect(button(/^Delete/)).toBeUndefined();
  const rows = Array.from(node.querySelectorAll(".inbox-manage-row"));
  expect(rows.map((row) => row.querySelector(".inbox-manage-text")!.textContent)).toEqual([
    "Reviews1 now",
    "Acme3 now",
  ]);
  // The first has nothing to move up to, the last nothing to move down to.
  await openActions("Reviews");
  expect(choices()).toEqual(["Edit…", "Move down", "Show count in the sidebar"]);
  await openActions("Acme");
  expect(choices()).toEqual(["Edit…", "Move up", "Show count in the sidebar"]);
  expect(node.querySelector('[aria-label="Edit Reviews"]')!.getAttribute("href")).toBe(
    "/s/test-ns/?page=inbox-edit&inbox=Reviews",
  );
  expect(link("New inbox").getAttribute("href")).toBe("/s/test-ns/?page=inbox-new");
  expect(railLink("Manage inboxes")).toBeUndefined();
  expect(railHeading().getAttribute("aria-current")).toBeNull();
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
  expect(choice("Show count in the sidebar")!.getAttribute("aria-checked")).toBe("true");
  await click(choice("Show count in the sidebar"));
  expect(puts[2]!.inboxes[0]).toMatchObject({ id: "Reviews", badge: false });
  expect(node.querySelector(".inbox-manage-row")!.textContent).toContain("· count hidden");
});

it("leads to New inbox from Manage inboxes", async () => {
  await render("?page=inboxes");
  await click(link("New inbox"));
  expect(search().get("page")).toBe("inbox-new");
});

// --- Links, Go to and an older daemon ----------------------------------------

it("puts New inbox and Manage inboxes in Inbox's right-click menu, and Edit in an inbox's and its queue head's", async () => {
  await render();
  expect(railLink("New inbox")).toBeUndefined();
  expect(railLink("Manage inboxes")).toBeUndefined();
  await act(async () => {
    railHeading().dispatchEvent(
      new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 9, clientY: 9 }),
    );
  });
  expect(choices()).toEqual(["New inbox…", "Manage inboxes…"]);
  await click(choice("Manage inboxes…"));
  expect(search().get("page")).toBe("inboxes");
  daemon = { revision: 1, inboxes: [acme] };
  await render();
  const first = node.querySelector<HTMLButtonElement>('[aria-label="More for Acme"]')!;
  await click(first);
  expect(node.querySelector(".local-rail-menu button")!.textContent).toBe("Edit inbox");
  await click(node.querySelector(".local-rail-menu button"));
  expect(search().get("page")).toBe("inbox-edit");
  expect(search().get("inbox")).toBe("Acme");
  // The inbox's queue has Edit in its head's right-click menu, not a link.
  await render("?inbox=Acme");
  expect(node.querySelector(".upnext-list-title a")).toBeNull();
  await act(async () => {
    node
      .querySelector(".upnext-list-title h1")!
      .dispatchEvent(
        new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 9, clientY: 9 }),
      );
  });
  const edit = [...node.querySelectorAll<HTMLElement>('[role="menu"] [role="menuitem"]')];
  expect(edit.map((one) => one.textContent)).toEqual(["Edit…"]);
  await click(edit[0]!);
  expect(search().get("page")).toBe("inbox-edit");
  expect(search().get("inbox")).toBe("Acme");
});

it("lists each saved inbox, New inbox and Manage inboxes in Go to, with no Inbox page", async () => {
  await render("?page=pulls");
  await act(async () => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true }));
  });
  const options = () =>
    Array.from(node.querySelectorAll('[data-go-to] [role="option"]')).map((o) => o.textContent);
  expect(options()).toContain("AcmeInbox");
  expect(options()).toContain("ReviewsInbox");
  expect(options()).not.toContain("InboxPage");
  expect(options()).toContain("New inboxPage");
  expect(options()).toContain("Manage inboxesPage");
  // Found by name; opening one shows its queue.
  const input = node.querySelector<HTMLInputElement>("[data-go-to] input")!;
  await type(input, "acme");
  expect(options()[0]).toBe("AcmeInbox");
  await click(node.querySelector('[data-go-to] [role="option"]'));
  expect(search().get("inbox")).toBe("Acme");
  expect(node.querySelector(".upnext-list-head h1")!.textContent).toBe("Acme");
  // ⌘1 opens the first saved inbox.
  await act(async () => {
    window.dispatchEvent(
      new KeyboardEvent("keydown", { key: "1", code: "Digit1", ctrlKey: true, bubbles: true }),
    );
  });
  expect(location.search).toBe("?inbox=Reviews");
  expect(node.querySelector(".upnext-list-head h1")!.textContent).toBe("Reviews");
});

it("shows none of it with an older daemon, and says it needs a newer baum for a direct address", async () => {
  daemon = null;
  for (const page of ["inbox-new", "inbox-edit&inbox=Acme", "inboxes"]) {
    await render(`?page=${page}`);
    expect(node.querySelector(".upnext-list-head")).toBeNull();
    expect(node.querySelector(".upnext-row")).toBeNull();
    expect(node.textContent).toContain("Inboxes need a newer baum");
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

it("says it needs a newer baum for a daemon that says it has no inboxes", async () => {
  daemon = null;
  await render("?page=inboxes");
  expect(node.querySelector(".upnext-list-head")).toBeNull();
  expect(node.textContent).toContain("Inboxes need a newer baum");
  expect(node.querySelector('[role="alert"]')).toBeNull();
});

for (const modifier of ["metaKey", "ctrlKey"]) {
  it(`creates an inbox with ${modifier} and Enter`, async () => {
    await render("?page=inbox-new");
    await click(template("Custom"));
    await type(nameInput(), "Keyboard inbox");
    await act(async () => {
      nameInput().dispatchEvent(
        new KeyboardEvent("keydown", {
          key: "Enter",
          [modifier]: true,
          bubbles: true,
          cancelable: true,
        }),
      );
    });
    expect(puts.at(-1)?.inboxes.at(-1)).toMatchObject({ name: "Keyboard inbox" });
  });
}

// --- Unclaimed template and the condition chips ------------------------------

it("opens the Unclaimed template on others' pull requests that nobody holds, and Save adds it", async () => {
  await render("?page=inbox-new");
  await click(template("Unclaimed"));
  expect(search().get("page")).toBe("inbox-edit");
  expect(nameInput().value).toBe("Unclaimed");
  expect(queryBox().value).toBe(
    "waiting=other assignee=none review-requested=none is!=draft is!=bot",
  );
  // The fixture has no one else's pull request: none to show yet.
  expect(node.querySelector(".inbox-preview-count")!.textContent).toBe("0");
  await click(button("Save"));
  expect(puts).toHaveLength(1);
  expect(puts[0]!.inboxes.at(-1)).toMatchObject({
    name: "Unclaimed",
    query: "waiting=other assignee=none review-requested=none is!=draft is!=bot",
    badge: true,
  });
});

it("catches an unclaimed pull request in the Unclaimed inbox, and not a claimed one", async () => {
  entries = [
    ...fixture(),
    entry(5, "acme/demo", { author: "sam" }),
    entry(6, "acme/demo", { author: "sam", assigned: true }),
    entry(7, "acme/demo", { author: "sam", review_requested: true }),
    entry(8, "acme/demo", { author: "sam", draft: true }, { merge: "draft" }),
    entry(9, "acme/demo", { author: "dependabot", author_bot: true }),
  ];
  await render("?page=inbox-new");
  expect(template("Unclaimed").textContent).toContain("1 now");
  await click(template("Unclaimed"));
  expect(previewTitles()).toEqual(["Change 5"]);
});

/** The query box's terms as its tokens show them, away from the box. */
const tokens = () =>
  Array.from(node.querySelectorAll(".inbox-query .filter-token")).map(
    (token) => token.firstElementChild!.textContent,
  );
const remove = (label: string) =>
  node.querySelector<HTMLButtonElement>(`.inbox-query button[aria-label="Remove ${label}"]`)!;

it("shows the query's terms as tokens in the box, and a token's × saves the query without it", async () => {
  daemon = {
    revision: 1,
    inboxes: [
      inbox("Hot", {
        query: "org=baum-ai (author=@me OR assignee=@me OR review-requested=@me) review=none",
      }),
    ],
  };
  await render("?page=inbox-edit&inbox=Hot");
  expect(tokens()).toEqual([
    "org=baum-ai",
    "author=@me",
    "assignee=@me",
    "review-requested=@me",
    "review=none",
  ]);
  // The operators and parentheses stay as text between them.
  expect(node.querySelector(".inbox-query .filter-bar-tokens")!.textContent).toContain(" OR ");
  await click(remove("Assignee is Me"));
  expect(puts).toHaveLength(1);
  expect(puts[0]!.inboxes[0]!.query).toBe(
    "org=baum-ai (author=@me OR review-requested=@me) review=none",
  );
  expect(queryBox().value).toBe("org=baum-ai (author=@me OR review-requested=@me) review=none");
  expect(tokens()).toEqual(["org=baum-ai", "author=@me", "review-requested=@me", "review=none"]);
  // A group left with one choice loses its parentheses.
  await click(remove("Author is Me"));
  expect(puts.at(-1)!.inboxes[0]!.query).toBe("org=baum-ai review-requested=@me review=none");
});

it("shows the text while the box has the focus, and the tokens again once it leaves", async () => {
  daemon = { revision: 1, inboxes: [inbox("Hot", { query: "org=acme risk>=4" })] };
  await render("?page=inbox-edit&inbox=Hot");
  expect(queryBox().classList.contains("filter-bar-input-under")).toBe(true);
  await act(async () => queryBox().focus());
  expect(tokens()).toEqual([]);
  expect(queryBox().classList.contains("filter-bar-input-under")).toBe(false);
  await act(async () => queryBox().blur());
  expect(tokens()).toEqual(["org=acme", "risk>=4"]);
});

it("offers what may follow the query once the box has the focus, the operators first", async () => {
  daemon = { revision: 1, inboxes: [inbox("Hot", { query: "org=acme review=none" })] };
  await render("?page=inbox-edit&inbox=Hot");
  await act(async () => {
    queryBox().dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
  });
  const offered = () =>
    Array.from(node.querySelectorAll(".inbox-query [role='option'] code")).map(
      (one) => one.textContent,
    );
  expect(offered().slice(0, 2)).toEqual(["AND", "OR"]);
  expect(offered()).toContain("author");
  // Tab moves on rather than joining an operator onto a whole filter.
  expect(queryBox().hasAttribute("data-own-tab")).toBe(false);
  // Taken, the operator comes after a space.
  await act(async () => {
    node
      .querySelectorAll<HTMLElement>(".inbox-query [role='option']")[1]!
      .dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true }));
  });
  expect(queryBox().value).toBe("org=acme review=none OR ");
  // An empty box offers the fields alone.
  daemon = { revision: 1, inboxes: [inbox("Open", { query: "" })] };
  await render("?page=inbox-edit&inbox=Open");
  await act(async () => queryBox().focus());
  expect(offered()).toContain("author");
  expect(offered()).not.toContain("AND");
});

it("keeps the box with no query, saying what no filter means, also once the last token goes", async () => {
  daemon = {
    revision: 1,
    inboxes: [inbox("Open", { query: "" }), inbox("One", { query: "org=acme" })],
  };
  await render("?page=inbox-edit&inbox=Open");
  expect(queryBox().value).toBe("");
  expect(queryBox().placeholder).toBe("No filter: every open pull request");
  // The labelled button opens the menu, which has no box of its own to write in.
  await click(node.querySelector<HTMLElement>(".inbox-query summary.filter-menu-lead")!);
  expect(node.querySelector(".inbox-query .filter-menu-write")).toBeNull();
  await render("?page=inbox-edit&inbox=One");
  await click(remove("Organization is acme"));
  expect(puts.at(-1)!.inboxes[1]!.query).toBe("");
  expect(queryBox().value).toBe("");
  expect(queryBox().placeholder).toContain("No filter");
});

it("puts a quick filter's query into the box, and takes it out when pressed again", async () => {
  daemon = { revision: 1, inboxes: [inbox("Hot", { query: "org=acme" })] };
  await render("?page=inbox-edit&inbox=Hot");
  const quick = (label: string) =>
    Array.from(
      node.querySelectorAll<HTMLButtonElement>('[role="group"][aria-label="Quick filters"] button'),
    ).find((one) => one.textContent === label)!;
  expect(quick("Risky").getAttribute("aria-pressed")).toBe("false");
  await click(quick("Risky"));
  expect(puts.at(-1)!.inboxes[0]!.query).toBe("org=acme risk>=4");
  expect(queryBox().value).toBe("org=acme risk>=4");
  expect(quick("Risky").getAttribute("aria-pressed")).toBe("true");
  await click(quick("For me"));
  expect(puts.at(-1)!.inboxes[0]!.query).toBe(
    "org=acme risk>=4 (author=@me OR assignee=@me OR review-requested=@me)",
  );
  await click(quick("Risky"));
  expect(puts.at(-1)!.inboxes[0]!.query).toBe(
    "org=acme (author=@me OR assignee=@me OR review-requested=@me)",
  );
  expect(quick("Risky").getAttribute("aria-pressed")).toBe("false");
  // The filter's syntax is a link away, in the docs.
  const docs = node.querySelector<HTMLAnchorElement>(".inbox-docs-link")!;
  expect(docs.textContent).toBe("Filter syntax");
  expect(docs.href).toBe("https://docs.usebaum.com/inbox/#filter-what-you-see");
  expect(docs.target).toBe("_blank");
});

it("shows no tokens for a query that does not read", async () => {
  daemon = { revision: 1, inboxes: [inbox("Bad", { query: "colour:red" })] };
  await render("?page=inbox-edit&inbox=Bad");
  expect(tokens()).toEqual([]);
  expect(queryBox().value).toBe("colour:red");
});
