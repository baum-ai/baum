import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { REVIEW_1303 } from "../src/review-view/fixture-1303";
import { reviewDocumentFor } from "../../viewer/test/review-document";
import { launchURL } from "./inbox-helpers";

// The review page's rail and places on the fixture's acme/demo#7,
// whose tile route answers with #1303's baum.review/2 document, as the daemon
// serves it. REVIEW_VIEW_SHOTS=<dir> also writes a picture of each
// place at each width, light and dark.

const PR7 = "repository=acme%2Fdemo&pr=7";
const SHOTS = process.env.REVIEW_VIEW_SHOTS ?? "";

async function open(
  page: Page,
  search: string,
  answer: Record<string, unknown> = { document: REVIEW_1303 },
) {
  await page.route(/\/api\/tiles\?/, (route) => route.fulfill({ json: answer }));
  // The page reads a version as stored first: a baum.review/2 document goes
  // on to the tile route, a baum.review/1 one is drawn as it is.
  await page.route(/\/api\/document\?/, (route) => route.fulfill({ json: answer.document }));
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

/** A row of the rail, which on the tiles page is the way through the page. */
const railRow = (page: Page, name: string) =>
  page.locator("#review-rail").getByRole("button", { name: new RegExp(`^${name}(, \\d+)?$`) });
/** The rail's own mark of the tiles page: its How it works group. */
const tilesRail = (page: Page) => railRow(page, "How it works");

const PLACES = [
  ["overview", ""],
  ["sections", "&pane=section&section=sec_allowlist"],
  ["context", "&pane=section&section=context"],
  ["encryption", "&pane=section&section=sec_encryption"],
  ["section-code", "&pane=section&section=sec_allowlist&section_view=code"],
  ["section-tests", "&pane=section&section=sec_allowlist&section_view=tests"],
  ["section-session", "&pane=section&section=sec_relay&section_view=session"],
  ["code", "&pane=tile&tile=code&tile_item=oss%2Finternal%2Fuiserver%2Fremote.go"],
] as const;

test("the rail switches places, keeps them in the address and survives a reload", async ({
  page,
}) => {
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR7);
  await expect(railRow(page, "Overview")).toHaveAttribute("aria-pressed", "true");
  await expect(page.locator('[role="tablist"][aria-label="Review"]')).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "How a phone reaches the daemon" })).toBeVisible();
  await railRow(page, "How it works").click();
  await expect(page).toHaveURL(/section=sec_pairing/);
  await railRow(page, "What a phone can do").click();
  await expect(page.getByRole("heading", { name: "What a phone can reach" })).toBeVisible();
  await page.reload();
  await expect(page.getByRole("heading", { name: "What a phone can reach" })).toBeVisible();
  await expect(railRow(page, "What a phone can do")).toHaveAttribute("aria-pressed", "true");
  await railRow(page, "Files changed").click();
  await expect(page).toHaveURL(/tile=code/);
  await page.goBack();
  await expect(page).toHaveURL(/section=sec_allowlist/);
  expect(errors).toEqual([]);
});

test("Files changed groups by type, folder or feature, and remembers it", async ({ page }) => {
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, `${PR7}${PLACES.find(([name]) => name === "code")![1]}`);
  const files = page.getByRole("navigation", { name: "Changed files" });
  const by = page.getByRole("group", { name: "Group files by" });
  await expect(by.getByRole("button", { name: "Type" })).toHaveAttribute("aria-pressed", "true");
  await expect(files.getByText("Tests", { exact: true })).toBeVisible();
  await by.getByRole("button", { name: "Folder" }).click();
  await expect(files.getByText("oss", { exact: true })).toBeVisible();
  await expect(files.getByText("Tests", { exact: true })).toHaveCount(0);
  await page.reload();
  await expect(by.getByRole("button", { name: "Folder" })).toHaveAttribute("aria-pressed", "true");
  await by.getByRole("button", { name: "Feature" }).click();
  await expect(files.getByText("Pairing", { exact: true })).toBeVisible();
  await files
    .getByRole("button", { name: /relay\.go/ })
    .first()
    .click();
  await expect(page).toHaveURL(/tile_item=oss%2Finternal%2Fremote%2Frelay\.go/);
  expect(await overflows(page)).toBe(false);
  expect(errors).toEqual([]);
});

test("a section's views switch, keep their place in the address and reset per section", async ({
  page,
}) => {
  const errors = watchErrors(page);
  // Narrow enough that the section's views share one column.
  await page.setViewportSize({ width: 1280, height: 900 });
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
  await railRow(page, "Relay connection").click();
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

test("a wide section shows its Overview beside its other views, which stay in sight", async ({
  page,
}) => {
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 1920, height: 900 });
  await open(page, `${PR7}&pane=section&section=sec_allowlist`);
  // The column starts collapsed, the Overview taking the section, until opened.
  await expect(page.locator("[data-section-body]")).toBeVisible();
  await expect(page.locator("[data-section-side]")).toHaveCount(0);
  await page.getByRole("button", { name: "Expand section views" }).click();
  const views = page.getByRole("tablist", { name: "Section views" });
  // The Overview has its own column, so it leaves the row; Code is first.
  await expect(views.getByRole("tab")).toHaveText(["Code 1", "Tests 1", "Session 0", "Docs 1"]);
  await expect(views.getByRole("tab", { name: "Code" })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("[data-section-body]")).toBeVisible();
  const diff = page.getByRole("article", { name: "oss/internal/uiserver/remote.go diff" });
  await expect(diff).toBeVisible();
  const side = page.locator("[data-section-side]");
  const overview = await page.locator("[data-section-body]").boundingBox();
  const beside = await side.boundingBox();
  expect(beside!.x).toBeGreaterThan(overview!.x + overview!.width);
  await views.getByRole("tab", { name: "Tests" }).click();
  await expect(page).toHaveURL(/section_view=tests/);
  await expect(page.locator("[data-section-body]")).toBeVisible();
  // The views' column stays put while the Overview scrolls under it.
  const top = (await side.boundingBox())!.y;
  await page.mouse.wheel(0, 600);
  await expect.poll(async () => (await side.boundingBox())!.y).toBeLessThanOrEqual(top);
  await expect(views).toBeInViewport();
  expect(await overflows(page)).toBe(false);
  if (SHOTS) {
    mkdirSync(SHOTS, { recursive: true });
    await page.evaluate(() => document.querySelector(".local-main")!.scrollTo({ top: 0 }));
    await views.getByRole("tab", { name: "Code" }).click();
    for (const scheme of ["light", "dark"] as const) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.waitForTimeout(400);
      await page.screenshot({ path: join(SHOTS, `section-split-1920-${scheme}.png`) });
    }
  }
  expect(errors).toEqual([]);
});

test("a wide section's excerpts open in its side column, which collapses and resizes", async ({
  page,
}) => {
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 1920, height: 900 });
  await open(page, `${PR7}&pane=section&section=sec_allowlist&section_view=tests`);
  await page.getByRole("button", { name: "Expand section views" }).click();
  const views = page.getByRole("tablist", { name: "Section views" });
  await expect(views.getByRole("tab", { name: "Tests" })).toHaveAttribute("aria-selected", "true");
  const side = page.locator("[data-section-side]");
  const overview = page.locator("[data-section-body]");
  const pageTop = () => page.evaluate(() => document.querySelector(".local-main")!.scrollTop);
  const diff = side.getByRole("article", { name: "oss/internal/uiserver/remote.go diff" });
  // Opening a row shows its first excerpt beside the Overview, which stays
  // where it was. (In sight first: Playwright scrolls to what it clicks.)
  await overview.getByRole("button", { name: /Deny by default/ }).scrollIntoViewIfNeeded();
  const before = await pageTop();
  await overview.getByRole("button", { name: /Deny by default/ }).click();
  await expect(page).toHaveURL(/section_view=code/);
  await expect(views.getByRole("tab", { name: "Code" })).toHaveAttribute("aria-selected", "true");
  await expect(diff).toBeInViewport();
  await expect(overview).toBeVisible();
  expect(await pageTop()).toBe(before);
  // Closing it leaves the column as it is.
  await views.getByRole("tab", { name: "Tests" }).click();
  await overview.getByRole("button", { name: /Deny by default/ }).click();
  await expect(views.getByRole("tab", { name: "Tests" })).toHaveAttribute("aria-selected", "true");
  // An excerpt pressed opens Code there too.
  await overview.getByRole("button", { name: /Deny by default/ }).click();
  await views.getByRole("tab", { name: "Tests" }).click();
  await overview
    .getByRole("button", { name: "oss/internal/uiserver/remote.go", exact: true })
    .click();
  await expect(page).toHaveURL(/section_view=code/);
  await expect(diff).toBeInViewport();
  expect(await pageTop()).toBe(before);
  // The views' row stays at the top of the column, above what scrolls.
  const scroller = page.locator("[data-section-side-body]");
  await scroller.evaluate((element) => {
    element.scrollTop = element.scrollHeight;
  });
  await expect.poll(() => scroller.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
  const row = (await page.locator("[data-section-side-tabs]").boundingBox())!;
  expect(Math.abs(row.y - (await side.boundingBox())!.y)).toBeLessThan(2);
  await expect(views).toBeInViewport();
  // The column collapses to a strip, which the Overview widens into, and
  // stays collapsed; an excerpt opens it again.
  const narrow = (await overview.boundingBox())!.width;
  await side.getByRole("button", { name: "Collapse section views" }).click();
  await expect(side).toHaveCount(0);
  await expect.poll(async () => (await overview.boundingBox())!.width).toBeGreaterThan(narrow);
  await page.reload();
  const expand = page.getByRole("button", { name: "Expand section views" });
  await expect(expand).toBeVisible();
  await overview.getByRole("button", { name: /Deny by default/ }).click();
  await overview
    .getByRole("button", { name: "oss/internal/uiserver/remote.go", exact: true })
    .click();
  await expect(
    side.getByRole("article", { name: "oss/internal/uiserver/remote.go diff" }),
  ).toBeInViewport();
  await side.getByRole("button", { name: "Collapse section views" }).click();
  await expand.click();
  await expect(views).toBeVisible();
  // `]` folds it and opens it again; Control+] is the right-hand sidebar's.
  await page.keyboard.press("]");
  await expect(side).toHaveCount(0);
  await page.keyboard.press("]");
  await expect(views).toBeVisible();
  const sidebarFolded = page.getByRole("button", { name: /^Expand (conversation|side pane)$/ });
  const folded = await sidebarFolded.count();
  await page.keyboard.press("Control+]");
  await expect(sidebarFolded).toHaveCount(1 - folded);
  await expect(views).toBeVisible();
  await page.keyboard.press("Control+]");
  await expect(sidebarFolded).toHaveCount(folded);
  // Its divider resizes it from the keyboard too, and the width is kept.
  const divider = page.getByRole("separator", { name: "Resize section views" });
  const width = Number(await divider.getAttribute("aria-valuenow"));
  await divider.focus();
  await page.keyboard.press("Shift+ArrowLeft");
  await expect(divider).toHaveAttribute("aria-valuenow", String(width + 64));
  await page.reload();
  await expect(divider).toHaveAttribute("aria-valuenow", String(width + 64));
  expect(await overflows(page)).toBe(false);
  expect(errors).toEqual([]);
});

test("a flow tile lays out across, then with its labels, as the page widens", async ({ page }) => {
  await page.setViewportSize({ width: 1800, height: 900 });
  await open(page, PR7);
  const flow = page.locator(".flow").first();
  await expect(flow).toBeVisible();
  // The Overview's flow has 5 columns and lane labels: lanes as rows from
  // 8.5rem a column (42.5rem), the labels in a column of their own from
  // 9rem a column and 6rem (51rem), at the default 16px root font size.
  const seen = new Set<string>();
  for (const width of [390, 900, 1100, 1300, 1500, 1800]) {
    await page.setViewportSize({ width, height: 900 });
    // The attributes follow the width the flow is given, as it settles.
    const state = () =>
      flow.evaluate((el) => ({
        px: el.clientWidth,
        across: el.hasAttribute("data-across"),
        labels: el.hasAttribute("data-labels"),
      }));
    await expect
      .poll(async () => {
        const now = await state();
        return now.across === now.px >= 680 && now.labels === now.px >= 816;
      })
      .toBe(true);
    const now = await state();
    seen.add(`${now.across}/${now.labels}`);
  }
  // It was a list, then across, then with labels.
  expect(seen).toContain("false/false");
  expect(seen).toContain("true/false");
  expect(seen).toContain("true/true");
});

for (const width of [390, 900, 1440]) {
  test(`at ${width}px no place scrolls the page sideways`, async ({ page }) => {
    // A picture shows more of the page than the check needs.
    if (SHOTS) test.setTimeout(120_000);
    await page.setViewportSize({ width, height: SHOTS ? 1800 : 900 });
    for (const [name, place] of PLACES) {
      await open(page, `${PR7}${place}`);
      await expect(page.locator("#review-section")).toBeVisible();
      await expect(page.locator('[role="tablist"][aria-label="Review"]')).toHaveCount(0);
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
    owner: "sec_allowlist",
    title: "Retry notes",
    state: "ready" as const,
    cites: [],
    data: { text: "Retries double their delay.", anchors: [] },
    added: true,
    from: 1,
  };
  const document = { ...REVIEW_1303, tiles: [...REVIEW_1303.tiles, added] };
  const sent: { tile?: string; action?: string; pr?: number }[] = [];
  await page.route(/\/api\/tiles\/added$/, async (route) => {
    const body = route.request().postDataJSON();
    sent.push(body);
    await route.fulfill({ json: { tile: body.tile, action: body.action } });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, `${PR7}&pane=section&section=sec_allowlist`, { document });
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
  const answer = { document: REVIEW_1303, drafts };
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

test("the Overview shows the tiles added to it and the cards of those being made", async ({
  page,
}) => {
  const added = {
    id: "added_images_0a1b2c3d4e",
    type: "prose",
    owner: "overview",
    title: "Screens after the change",
    state: "ready" as const,
    cites: [],
    data: { text: "The Tests view lists each test by name.", anchors: [] },
    added: true,
    from: 1,
  };
  const document = { ...REVIEW_1303, tiles: [...REVIEW_1303.tiles, added] };
  const drafts = [{ tile: "draft_1", title: "Retry timeline", phase: "writing" }];
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR7, { document, drafts });
  const tile = page.locator(`[role="figure"][data-tile="${added.id}"]`);
  await expect(tile.getByRole("heading", { name: "Screens after the change" })).toBeVisible();
  await expect(page.locator('[data-tile-draft="draft_1"]')).toContainText("Retry timeline");
  // Neither stands on a section of How it works.
  await open(page, `${PR7}&pane=section&section=context`, { document, drafts });
  await expect(page.locator('[data-tile-draft="draft_1"]')).toHaveCount(0);
  await expect(page.locator(`[data-tile="${added.id}"]`)).toHaveCount(0);
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

test("the rail's Sessions, Commits and Decisions are plain rows that open their panes", async ({
  page,
}) => {
  const errors = watchErrors(page);
  await withSessionWork(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, PR7);
  const rail = page.locator("#review-rail");
  // Plain rows: nothing folds open under them, Files changed included.
  for (const name of ["Files changed", "Decisions", "Sessions", "Commits"])
    await expect(rail.getByRole("button", { name: new RegExp(`^${name}`) })).not.toHaveAttribute(
      "aria-expanded",
      /.*/,
    );
  const sessions = rail.getByRole("button", { name: /^Sessions/ });
  await sessions.click();
  await expect(page).toHaveURL(/pane=sessions/);
  const pane = page.locator("#review-section");
  await pane
    .getByRole("button", { name: /Resolve the conflict with the base/ })
    .first()
    .click();
  await expect(page).toHaveURL(/pane=session&session=running-on-7/);
  // The group's row stays current on its item.
  await expect(sessions).toHaveAttribute("aria-pressed", "true");
  await rail.getByRole("button", { name: /^Decisions/ }).click();
  await expect(page).toHaveURL(/pane=decisions/);
  await expect(
    pane.getByRole("button", { name: /Deny by default in the allowlist/ }).first(),
  ).toBeVisible();
  await railRow(page, "Overview").click();
  await expect(railRow(page, "Overview")).toHaveAttribute("aria-pressed", "true");
  expect(errors).toEqual([]);
});

test("below the side by side width the rail is a sheet, and the commits open from it", async ({
  page,
}) => {
  await page.setViewportSize({ width: 900, height: 900 });
  await open(page, PR7);
  await expect(page.locator("#review-rail")).toBeHidden();
  await page.locator('button[aria-controls="review-rail"]').first().click();
  await expect(page.locator("#review-rail")).toBeVisible();
  await page
    .locator("#review-rail")
    .getByRole("button", { name: /^Commits/ })
    .click();
  await expect(page).toHaveURL(/pane=commits/);
  expect(await overflows(page)).toBe(false);
});

test("Files changed names the open file once, in its diff's header", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, `${PR7}${PLACES.find(([name]) => name === "code")![1]}`);
  const file = page.locator("#review-view-file");
  await expect(
    file.getByRole("article", { name: "oss/internal/uiserver/remote.go diff" }),
  ).toContainText("Explained in");
  await expect(file.getByText("oss/internal/uiserver/remote.go", { exact: true })).toHaveCount(1);
});

for (const [width, height] of [
  [1280, 860],
  [720, 480],
] as const) {
  test(`Files changed reveals the selected diff at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height });
    await page.emulateMedia({ colorScheme: width === 720 ? "dark" : "light" });
    await open(page, `${PR7}&pane=tile&tile=code`);
    const files = page.getByRole("navigation", { name: "Changed files" });
    const path = "oss/internal/uiserver/remote.go";
    await files
      .getByRole("button", { name: /^remote\.go/ })
      .first()
      .click();
    const diff = page.getByRole("article", { name: `${path} diff` });
    const header = diff.getByText(path, { exact: true });
    await expect(header).toBeVisible();
    if (SHOTS) {
      mkdirSync(SHOTS, { recursive: true });
      await page.screenshot({
        path: join(SHOTS, `file-selected-${width}.png`),
        animations: "disabled",
      });
    }
    await expect(header).toBeInViewport();
    await page.keyboard.press("j");
    await expect(diff).toHaveCount(0);
    await page.keyboard.press("k");
    await expect(header).toBeInViewport();
    await expect(page).toHaveURL(/tile_item=oss%2Finternal%2Fuiserver%2Fremote\.go/);

    // A row lower in the scrollable list still opens a diff in view.
    await files
      .getByRole("button", { name: /^remote_test\.go/ })
      .first()
      .click();
    const laterPath = "oss/internal/uiserver/remote_test.go";
    const laterDiff = page.getByRole("article", { name: `${laterPath} diff` });
    await expect(laterDiff.getByText(laterPath, { exact: true })).toBeInViewport();
  });
}

test("every pull request opens on the rail, with no setting to choose a page", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, "repository=acme%2Fdemo&pr=7");
  await expect(tilesRail(page)).toBeVisible();
  // An address a risk's link wrote lands on its section in How it works.
  await signedIn(
    page,
    "repository=acme%2Fdemo&pr=7&pane=risk&risk_section=sec_allowlist&risk_id=rsk_always_allow",
  );
  await expect(railRow(page, "What a phone can do")).toHaveAttribute("aria-pressed", "true");
  await signedIn(page, "page=settings");
  await expect(page.locator("#review-page")).toHaveCount(0);
});

test("a version that was not arranged opens on the rail too", async ({ page }) => {
  // The fixture's acme/demo#12 was generated with a composition; #7 was not.
  await signedIn(page, "repository=acme%2Fdemo&pr=12");
  await expect(tilesRail(page)).toBeVisible();
  await signedIn(page, "repository=acme%2Fdemo&pr=7");
  await expect(tilesRail(page)).toBeVisible();
});

test("a baum.review/1 version draws its own Summary and sections in the rail", async ({ page }) => {
  const errors = watchErrors(page);
  await page.setViewportSize({ width: 1440, height: 900 });
  // Any stored baum.review/1 document will do: the routes below answer with it.
  const stored = reviewDocumentFor("acme/widgets", 42)!;
  const section = stored.sections[0]!;
  await open(page, PR7, { document: stored });
  await expect(tilesRail(page)).toBeVisible();
  await expect(page.getByRole("heading", { name: "Summary", level: 2 })).toBeVisible();
  await open(page, `${PR7}&pane=section&section=${section.id}`, { document: stored });
  await expect(page.getByRole("heading", { name: section.title })).toBeVisible();
  expect(errors).toEqual([]);
});

test("j and k go through a points block row by row, and Enter opens the row", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await open(page, `${PR7}&pane=section&section=sec_allowlist`);
  const points = page.locator('[data-block="points"] [data-point]');
  await expect(points.first()).toBeVisible();
  const active = page.locator("[data-review-block-active]");
  // To the first row: the list itself is not a stop.
  for (let i = 0; i < 20; i++) {
    await page.keyboard.press("j");
    if ((await active.getAttribute("data-point")) !== null) break;
  }
  await expect(points.nth(0)).toHaveAttribute("data-review-block-active", "");
  await page.keyboard.press("j");
  await expect(points.nth(1)).toHaveAttribute("data-review-block-active", "");
  await expect(page.locator('[data-block="points"][data-review-block-active]')).toHaveCount(0);
  await page.keyboard.press("Enter");
  await expect(points.nth(1).getByRole("button").first()).toHaveAttribute("aria-expanded", "true");
  await page.keyboard.press("k");
  await expect(points.nth(0)).toHaveAttribute("data-review-block-active", "");
});

test("k back to the first block, and to the first place, shows the review's header again", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1400, height: 480 });
  await open(page, `${PR7}&pane=section&section=sec_allowlist`);
  const blocks = page.locator("[data-review-block]");
  await expect(blocks.first()).toBeVisible();
  const top = () => page.evaluate(() => document.querySelector(".local-main")!.scrollTop);
  const count = await blocks.count();
  for (let i = 0; i < count; i++) await page.keyboard.press("j");
  await expect.poll(top).toBeGreaterThan(0);
  for (let i = 0; i < count; i++) await page.keyboard.press("k");
  await expect.poll(top).toBe(0);
  // Back through the places to the first, the page starts at its top too.
  for (let i = 0; i < count; i++) await page.keyboard.press("j");
  await expect.poll(top).toBeGreaterThan(0);
  for (let i = 0; i < 6; i++) await page.keyboard.press("Shift+K");
  await expect.poll(top).toBe(0);
  // On the first place already, Shift+K still takes a scrolled page back up.
  await page.evaluate(() => document.querySelector(".local-main")!.scrollTo({ top: 200 }));
  await expect.poll(top).toBeGreaterThan(0);
  await page.keyboard.press("Shift+K");
  await expect.poll(top).toBe(0);
});
