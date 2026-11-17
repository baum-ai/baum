import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { REVIEW_1303, VIEW_1303 } from "../src/review-view/fixture-1303";
import { launchURL } from "./inbox-helpers";

// The review page's overview tabs (`?view=tiles`) on the fixture's acme/demo#7,
// whose tile route answers with the review of #1303 and its view, as the
// daemon serves them. REVIEW_VIEW_SHOTS=<dir> also writes a picture of each
// tab at each width, light and dark.

const PR7 = "repository=acme%2Fdemo&pr=7&view=tiles";
const SHOTS = process.env.REVIEW_VIEW_SHOTS ?? "";

async function open(
  page: Page,
  search: string,
  answer: Record<string, unknown> = { document: REVIEW_1303, view: VIEW_1303 },
) {
  await page.route(/\/api\/tiles\?/, (route) => route.fulfill({ json: answer }));
  await signedIn(page, search);
}

/** Opens the app's address with `search`, signed in, as the daemon serves it. */
async function signedIn(page: Page, search: string) {
  if (!page.url().includes("/s/")) {
    await page.goto(await launchURL());
    await page.waitForURL(/\/s\//);
  }
  await page.goto(`${new URL(page.url()).pathname}?${search}`);
}

const overflows = (page: Page) =>
  page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth);

function watchErrors(page: Page) {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("Failed to load resource"))
      errors.push(m.text());
  });
  return errors;
}

const PLACES = [
  ["overview", ""],
  ["sections", "&pane=section&section=sec_allowlist"],
  ["context", "&pane=section&section=context"],
  ["encryption", "&pane=section&section=sec_encryption"],
  ["section-code", "&pane=section&section=sec_allowlist&section_view=code"],
  ["section-session", "&pane=section&section=sec_relay&section_view=session"],
  ["code", "&pane=tile&tile=code&tile_item=oss%2Finternal%2Fuiserver%2Fremote.go"],
] as const;

test("the tabs switch, keep their place in the address and survive a reload", async ({ page }) => {
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR7);
  const tabs = page.getByRole("tablist", { name: "Review" });
  await expect(tabs.getByRole("tab", { name: "Overview" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page.getByRole("heading", { name: "How a phone reaches the daemon" })).toBeVisible();
  await tabs.getByRole("tab", { name: "How it works" }).click();
  await expect(page).toHaveURL(/section=sec_pairing/);
  await page.getByRole("button", { name: "What a phone can do" }).first().click();
  await expect(page.getByRole("heading", { name: "What a phone can reach" })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "What a phone can reach" })).toBeVisible();
  await tabs.getByRole("tab", { name: "Code" }).click();
  await expect(page).toHaveURL(/tile=code/);
  await page.goBack();
  await expect(page).toHaveURL(/section=sec_allowlist/);
  expect(errors).toEqual([]);
});

test("a section's views switch, keep their place in the address and reset per section", async ({
  page,
}) => {
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, `${PR7}&pane=section&section=sec_allowlist`);
  const views = page.getByRole("tablist", { name: "Section views" });
  await expect(views.getByRole("tab")).toHaveText([
    "Overview",
    "Code 1",
    "Tests 1",
    "Session 0",
    "Docs 1",
  ]);
  await expect(views.getByRole("tab", { name: "Session" })).toBeDisabled();
  await views.getByRole("tab", { name: "Code" }).click();
  await expect(page).toHaveURL(/section_view=code/);
  await expect(
    page.getByRole("article", { name: "oss/internal/uiserver/remote.go diff" }),
  ).toBeVisible();
  // The arrows pass over the disabled Session.
  await page.keyboard.press("ArrowRight");
  await expect(page).toHaveURL(/section_view=tests/);
  await page.keyboard.press("ArrowRight");
  await expect(page).toHaveURL(/section_view=docs/);
  await expect(page.getByRole("article", { name: "oss/SECURITY.md diff" })).toBeVisible();
  await page.reload();
  await expect(views.getByRole("tab", { name: "Docs" })).toHaveAttribute("aria-selected", "true");
  await page.getByRole("button", { name: "Relay connection" }).first().click();
  await expect(page).toHaveURL(/section=sec_relay/);
  await expect(page).not.toHaveURL(/section_view/);
  await expect(views.getByRole("tab", { name: "Overview" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await views.getByRole("tab", { name: "Session" }).click();
  await expect(page.getByRole("blockquote").first()).toHaveText(
    "use an encrypted relay that we run",
  );
  await page.goBack();
  await expect(page).not.toHaveURL(/section_view/);
  expect(errors).toEqual([]);
});

for (const width of [390, 900, 1440]) {
  test(`at ${width}px no tab scrolls the page sideways`, async ({ page }) => {
    // A picture shows more of the page than the check needs.
    if (SHOTS) test.setTimeout(120_000);
    await page.setViewportSize({ width, height: SHOTS ? 1800 : 900 });
    for (const [name, place] of PLACES) {
      await open(page, `${PR7}${place}`);
      await expect(page.getByRole("tablist", { name: "Review" })).toBeVisible();
      await expect(
        page.locator('#review-section [role="tabpanel"]:not([hidden])').first(),
      ).toBeVisible();
      expect(await overflows(page), `${name} at ${width}px`).toBe(false);
      if (!SHOTS) continue;
      mkdirSync(SHOTS, { recursive: true });
      for (const scheme of ["light", "dark"] as const) {
        await page.emulateMedia({ colorScheme: scheme });
        // The pane fades in as it arrives.
        await page.waitForTimeout(400);
        await page.screenshot({
          path: join(SHOTS, `${name}-${width}-${scheme}.png`),
          fullPage: true,
        });
      }
      await page.emulateMedia({ colorScheme: "light" });
    }
  });
}

test("a tile the person added is removed from its menu, and Undo puts it back", async ({
  page,
}) => {
  const added = {
    id: "lib_retry_notes",
    type: "prose",
    title: "Retry notes",
    state: "ready",
    data: { text: "Retries double their delay.", anchors: [] },
    added: true,
    from: 1,
  };
  const view = {
    ...VIEW_1303,
    tiles: [...VIEW_1303.tiles, added],
    sections: VIEW_1303.sections.map((section) =>
      section.id === "sec_allowlist"
        ? { ...section, tiles: [...section.tiles, added.id] }
        : section,
    ),
  };
  const sent: { tile?: string; action?: string; pr?: number }[] = [];
  await page.route(/\/api\/tiles\/added$/, async (route) => {
    const body = route.request().postDataJSON();
    sent.push(body);
    await route.fulfill({ json: { tile: body.tile, action: body.action } });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, `${PR7}&pane=section&section=sec_allowlist`, { document: REVIEW_1303, view });
  const tile = page.locator('[role="figure"][data-tile="lib_retry_notes"]');
  await expect(tile.getByRole("heading", { name: "Retry notes" })).toBeVisible();
  await expect(tile.locator("[data-added-note]")).toHaveText("From version 1");
  // A tile the model placed has no Remove. A menu closes as the page
  // scrolls, so its button is scrolled to (and the scroll's event has
  // passed) before it is clicked.
  const openMenu = (title: string) =>
    expect(async () => {
      const button = page.getByRole("button", { name: `Actions for ${title}` });
      await button.scrollIntoViewIfNeeded();
      await page.evaluate(
        () => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))),
      );
      await button.click();
      await expect(page.getByRole("menuitem", { name: "Ask about this tile" }).first()).toBeVisible(
        {
          timeout: 1000,
        },
      );
    }).toPass({ timeout: 10000 });
  await openMenu("What a phone can reach");
  await expect(page.getByRole("menuitem", { name: "Remove" })).toHaveCount(0);
  // Opening another tile's menu closes this one.
  await openMenu("Retry notes");
  await page.getByRole("menuitem", { name: "Remove" }).click();
  await expect.poll(() => sent.map((each) => each.action)).toEqual(["remove"]);
  expect(sent[0]).toMatchObject({ tile: "lib_retry_notes", pr: 7 });
  const toast = page.locator(".local-toast");
  await expect(toast).toContainText("Removed Retry notes");
  await toast.getByRole("button", { name: "Undo" }).click();
  await expect.poll(() => sent.map((each) => each.action)).toEqual(["remove", "restore"]);
  expect(sent[1]).toMatchObject({ tile: "lib_retry_notes", pr: 7 });
});

test("tiles being made stand on their sections as pending cards", async ({ page }) => {
  const drafts = [
    {
      tile: "draft_1",
      title: "Route table",
      phase: "checking",
      round: 2,
      section: "sec_allowlist",
    },
    { tile: "draft_2", title: "Where it runs", phase: "writing", section: "context" },
  ];
  const removed: string[] = [];
  await page.route(/\/api\/tiles\/drafts\//, async (route) => {
    removed.push(new URL(route.request().url()).pathname.split("/").at(-1)!);
    await route.fulfill({ json: {} });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  const answer = { document: REVIEW_1303, view: VIEW_1303, drafts };
  await open(page, `${PR7}&pane=section&section=sec_allowlist`, answer);
  const first = page.locator('[data-tile-draft="draft_1"]');
  await expect(first).toContainText("Route table");
  await expect(first).toContainText("Checking (round 2)");
  await expect(page.locator('[data-tile-draft="draft_2"]')).toHaveCount(0);
  await first.getByRole("button", { name: "Remove Route table" }).click();
  await expect.poll(() => removed).toEqual(["draft_1"]);
  await open(page, `${PR7}&pane=section&section=context`, answer);
  await expect(page.locator('[data-tile-draft="draft_2"]')).toContainText("Where it runs");
  await expect(page.locator('[data-tile-draft="draft_1"]')).toHaveCount(0);
});

/** A session at work on #7 and two decisions it made, as the daemon lists
 *  them, so the side pane has Sessions, Commits and Decisions. */
async function withSessionWork(page: Page) {
  const now = new Date().toISOString();
  await page.route("**/api/live-sessions", (route) =>
    route.fulfill({
      json: {
        sessions: [
          {
            agent: "claude-code",
            session_id: "running-on-7",
            state: "idle",
            waiting_reason: "turn_finished",
            started_at: now,
            last_activity_at: now,
            last_event: "stop",
            repository: "acme/demo",
            branch: "feature",
            pr: 7,
            title: "Resolve the conflict with the base",
          },
        ],
      },
    }),
  );
  const decision = (id: string, title: string, who: string) => ({
    decision_id: id,
    title,
    summary: `${title}, in short.`,
    category: "architecture",
    who,
    superseded: false,
    files: [],
    decided_at: now,
  });
  await page.route(/\/api\/decisions\?/, (route) =>
    route.fulfill({
      json: {
        review_generated_at: now,
        sessions: [
          {
            agent: "claude-code",
            session_id: "running-on-7",
            title: "Resolve the conflict with the base",
            live: true,
            coverage: "extracted",
            updated_at: now,
            capturing: false,
            decisions: [
              decision("dec_1", "Keep the relay opt-in", "user"),
              decision("dec_2", "Deny by default in the allowlist", "agent"),
            ],
          },
        ],
      },
    }),
  );
}

/** Opens a tab of the side pane, from under its ⋯ when it did not fit, once
 *  the pull request's lists have arrived. */
async function sideTab(page: Page, name: string) {
  const tab = page.locator(`#conversation-tab-${name.toLowerCase()}`);
  await expect(async () => {
    if (await tab.count()) await tab.click({ timeout: 1000 });
    else {
      const more = page.getByRole("group", { name: "More tabs" });
      if (!(await more.isVisible())) await page.locator("[data-pane-tabs-more]").click();
      await more.getByRole("button", { name }).click({ timeout: 1000 });
    }
    await expect(tab).toHaveAttribute("aria-selected", "true", { timeout: 1000 });
  }).toPass({ timeout: 15_000 });
}

test("the side pane lists the sessions and decisions, which open under the tabs", async ({
  page,
}) => {
  const errors = watchErrors(page);
  await withSessionWork(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR7);
  // No rail: the tabs are the way through the page.
  await expect(page.locator("#review-rail")).toHaveCount(0);
  // The side pane starts folded; ] unfolds it.
  await expect(page.getByRole("button", { name: "Expand side pane" })).toBeVisible();
  await page.keyboard.press("]");
  await expect(page.getByRole("button", { name: "Collapse side pane" })).toBeVisible();
  await sideTab(page, "Sessions");
  const aside = page.locator("#review-aside");
  await aside.getByRole("button", { name: /Resolve the conflict with the base/ }).click();
  await expect(page).toHaveURL(/pane=session&session=running-on-7/);
  // The side pane stays open, on the session's row.
  await expect(
    aside.getByRole("button", { name: /Resolve the conflict with the base/ }),
  ).toHaveAttribute("aria-pressed", "true");
  // The tabs stand above the session, none of them current, and lead back.
  const tabs = page.locator("#review-section").getByRole("group", { name: "Review" });
  await expect(tabs.getByRole("button", { name: "Overview" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );
  await sideTab(page, "Decisions");
  await aside.getByRole("button", { name: /Deny by default in the allowlist/ }).click();
  await expect(page).toHaveURL(/pane=decisions&decision=dec_2/);
  await expect(
    page
      .locator("#review-section")
      .getByRole("button", { name: /Deny by default in the allowlist/ }),
  ).toHaveAttribute("aria-expanded", "true");
  await tabs.getByRole("button", { name: "Overview" }).click();
  await expect(
    page.getByRole("tablist", { name: "Review" }).getByRole("tab", { name: "Overview" }),
  ).toHaveAttribute("aria-selected", "true");
  expect(errors).toEqual([]);
});

test("below the side by side width the tabs open the side pane, with no navigator bar", async ({
  page,
}) => {
  await page.setViewportSize({ width: 900, height: 900 });
  await open(page, PR7);
  await expect(page.locator('button[aria-controls="review-rail"]')).toHaveCount(0);
  await page.locator('#review-section button[aria-controls="review-aside"]').click();
  const aside = page.locator("#review-aside");
  await expect(aside).toBeVisible();
  await sideTab(page, "Commits");
  await page.locator("#conversation-panel-commits button").first().click();
  await expect(page).toHaveURL(/pane=commit&commit=/);
  // Picking it closes the sheet; the commit sits under the tabs.
  await expect(aside).toBeHidden();
  await expect(
    page.locator("#review-section").getByRole("group", { name: "Review" }),
  ).toBeVisible();
  expect(await overflows(page)).toBe(false);
});

test("the Code tab names the open file once, in its diff's header", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, `${PR7}${PLACES.find(([name]) => name === "code")![1]}`);
  const file = page.locator("#review-view-file");
  await expect(
    file.getByRole("article", { name: "oss/internal/uiserver/remote.go diff" }),
  ).toContainText("Explained in");
  await expect(file.getByText("oss/internal/uiserver/remote.go", { exact: true })).toHaveCount(1);
});

test("the setting chooses the review page", async ({ page }) => {
  await page.route(/\/api\/tiles\?/, (route) =>
    route.fulfill({ json: { document: REVIEW_1303, view: VIEW_1303 } }),
  );
  await page.setViewportSize({ width: 1440, height: 900 });
  await signedIn(page, "page=settings");
  const select = page.locator("#review-page");
  await expect(select).toHaveValue("full");
  await select.selectOption("tiles");
  try {
    await signedIn(page, "repository=acme%2Fdemo&pr=7");
    await expect(page.getByRole("tablist", { name: "Review" })).toBeVisible();
    // An address the full review wrote lands on the same place: a risk opens
    // How it works on its section.
    await signedIn(
      page,
      "repository=acme%2Fdemo&pr=7&pane=risk&risk_section=sec_allowlist&risk_id=rsk_always_allow",
    );
    await expect(
      page.getByRole("tablist", { name: "Review" }).getByRole("tab", { name: "How it works" }),
    ).toHaveAttribute("aria-selected", "true");
  } finally {
    await signedIn(page, "page=settings");
    await page.locator("#review-page").selectOption("full");
  }
  await signedIn(page, "repository=acme%2Fdemo&pr=7");
  await expect(page.getByRole("heading", { name: "Summary", level: 2 })).toBeVisible();
  await expect(page.getByRole("tablist", { name: "Review" })).toHaveCount(0);
});

test("with tile reviews on, an arranged version opens on its tabs", async ({ page }) => {
  // The fixture's acme/demo#12 was generated with a composition; #7 was not.
  await signedIn(page, "repository=acme%2Fdemo&pr=12");
  const labs = (on: boolean) =>
    page.evaluate(
      (tile_reviews) =>
        fetch("api/settings/labs", {
          method: "PUT",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ tile_reviews }),
        }).then((r) => r.status),
      on,
    );
  const tabs = page.getByRole("tablist", { name: "Review" });
  // Off, it opens on the full review, as the setting says.
  await expect(page.getByText("An arranged fixture review").first()).toBeVisible();
  await expect(tabs).toHaveCount(0);
  expect(await labs(true)).toBe(200);
  try {
    await signedIn(page, "repository=acme%2Fdemo&pr=12");
    await expect(tabs).toBeVisible();
    // The address still picks the full review.
    await signedIn(page, "repository=acme%2Fdemo&pr=12&view=full");
    await expect(page.getByText("An arranged fixture review").first()).toBeVisible();
    await expect(tabs).toHaveCount(0);
    // A version that was not arranged opens on the full review.
    await signedIn(page, "repository=acme%2Fdemo&pr=7");
    await expect(page.getByText("Local fixture review version 2").first()).toBeVisible();
    await expect(tabs).toHaveCount(0);
  } finally {
    expect(await labs(false)).toBe(200);
  }
});
