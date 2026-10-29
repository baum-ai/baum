import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, type Locator, type Page, test } from "@playwright/test";

// The fixture daemon publishes its launch URL in its credential file.
async function launchURL(): Promise<string> {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      const path = join(process.env.BAUM_APP_STATE_DIR ?? "", "credential.json");
      const { url } = JSON.parse(await readFile(path, "utf8"));
      if (typeof url === "string") return url;
    } catch {
      // Not written yet.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("the daemon did not publish its credential");
}

// Opens the shared Filters popover, as the hosted browser tests do.
async function openFilters(page: Page) {
  const summary = page.locator("summary").filter({ hasText: /^Filters/ });
  const details = page.locator("details").filter({ has: summary });
  if (!(await details.evaluate((element) => (element as HTMLDetailsElement).open)))
    await summary.click();
}

// With TIMELINE_SHOTS set, the views are also saved there for a look: the
// whole page, or with `at` the window scrolled to that element (a full-page
// shot resizes the window, which moves the opened feature out of its side
// panel).
async function shot(page: Page, name: string, at?: Locator) {
  const dir = process.env.TIMELINE_SHOTS;
  if (!dir) return;
  if (at) {
    await at.scrollIntoViewIfNeeded();
    await page.screenshot({ path: join(dir, `${name}.png`) });
  } else await page.screenshot({ path: join(dir, `${name}.png`), fullPage: true });
}

for (const scheme of ["light", "dark"] as const) {
  test(`the Timeline lists features and decisions from the local store (${scheme})`, async ({
    page,
  }) => {
    const errors: string[] = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.emulateMedia({ colorScheme: scheme });
    await page.setViewportSize({ width: 1600, height: 1000 });
    await page.goto(await launchURL());
    await page.getByRole("link", { name: "Timeline", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Timeline", exact: true })).toBeVisible();
    await expect(page.locator('summary[aria-label^="Repositories:"]')).toHaveText(
      "All repositories",
    );
    await expect(page.getByLabel("Repositories without feature history")).toContainText(
      "acme/other has no feature history yet.",
    );
    const features = page.getByRole("list", { name: "Features" });
    await expect(features).toContainText("Local decision log");
    await expect(features).toContainText("Keyword search over decisions");
    await expect(features).toContainText("Summary being updated");
    await expect(features).toContainText("2 PRs · 1 session · 2 decisions");
    await shot(page, `features-${scheme}`);

    // A wide screen opens the feature beside the timeline.
    await features
      .getByRole("button", { name: /Local decision log/ })
      .first()
      .click();
    await expect(page).toHaveURL(/feature=feature-local/);
    const panel = page.locator("#opened-feature-panel");
    await expect(panel).toContainText("Serve the decision log locally");
    await panel.getByRole("button", { name: /#12 Serve the decision log locally/ }).click();
    await expect(panel).toContainText("Adds the local Timeline API and page.");
    await panel.getByRole("button", { name: /Serve the Timeline from the daemon/ }).click();
    await expect(panel).toContainText("Sync the hosted log down");
    // The prompt it cites, from the transcript on this computer, masked.
    await expect(panel).toContainText("Serve the Timeline from the daemon's own store; ask");
    await expect(panel).not.toContainText("ada.lovelace@example.com");
    await shot(page, `decision-prompts-${scheme}`);
    // The session opens at the cited turn, though no tracker lists it.
    await panel.getByRole("button", { name: "Open session at turn 1" }).click();
    const card = panel.getByRole("region", { name: "Transcript" });
    await expect(card).toContainText("Draw it with the review viewer's decision log components.");
    await expect(card).not.toContainText("ada.lovelace@example.com");
    await shot(
      page,
      `session-at-turn-${scheme}`,
      card.getByText("Draw it with the review viewer").first(),
    );

    // The shared filter bar: the user is one person, their sessions and the
    // pull request their login opened.
    await shot(page, `filter-bar-${scheme}`, page.locator(".timeline-filters"));
    await openFilters(page);
    const author = page.getByRole("combobox", { name: "Author", exact: true });
    await expect(author.locator("option")).toHaveText([
      "Any author",
      "Ada Lovelace (3)",
      "author (2)",
    ]);
    await author.selectOption({ label: "author (2)" });
    await expect(page).toHaveURL(/person=author%40users.noreply.github.com/);
    await expect(features).toContainText("Hosted-only Timeline");
    await expect(features).not.toContainText("Keyword search over decisions");
    await author.selectOption({ label: "Ada Lovelace (3)" });
    await expect(features).toContainText("Keyword search over decisions");
    if (process.env.TIMELINE_SHOTS) {
      // The list the select offers, drawn open for the shot.
      // Drawn as a list it is a listbox while the shot is taken.
      const handle = await author.elementHandle();
      await handle?.evaluate((select: HTMLSelectElement) => {
        select.size = 3;
      });
      await shot(page, `people-${scheme}`, page.locator(".timeline-filters"));
      await handle?.evaluate((select: HTMLSelectElement) => {
        select.size = 0;
      });
    }
    await author.selectOption("");

    // Status and keywords narrow the features.
    await page
      .getByRole("group", { name: "Feature status" })
      .getByRole("button", { name: "Abandoned" })
      .click();
    await expect(page).toHaveURL(/status=abandoned/);
    await expect(features).toContainText("Hosted-only Timeline");
    await expect(features).not.toContainText("Local decision log");
    await page
      .getByRole("group", { name: "Feature status" })
      .getByRole("button", { name: "Abandoned" })
      .click();
    await expect(page).not.toHaveURL(/status=/);
    // A file, suggested from the cited ones, narrows the features to those
    // touching it.
    const box = page.getByRole("combobox", { name: "File or folder" });
    await box.fill("internal/decisionlog/feat");
    await page
      .getByRole("listbox", { name: "File or folder" })
      .getByRole("option", { name: /featurestore\.go/ })
      .click();
    await expect(page).toHaveURL(/file=internal%2Fdecisionlog%2Ffeaturestore.go/);
    await expect(features).toContainText("Keyword search over decisions");
    await expect(features).not.toContainText("Local decision log");
    await shot(page, `filters-open-${scheme}`, page.locator(".timeline-filters"));
    // A range after everything leaves nothing.
    await page.getByLabel("From", { exact: true }).fill("2099-01-01");
    await expect(page.getByText("No features match these filters")).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Clear filters" }).first().click();
    await expect(page).not.toHaveURL(/file=|from=/);
    await expect(features).toContainText("Local decision log");
    await page.getByLabel("Search features").fill("keyword");
    await expect(page).toHaveURL(/q=keyword/);
    await expect(features).toContainText("Keyword search over decisions");
    await expect(features).not.toContainText("Local decision log");
    await page.getByLabel("Search features").fill("");
    await expect(page).not.toHaveURL(/q=/);

    // All decisions, with one open.
    await page.getByRole("tab", { name: "All decisions" }).click();
    await expect(page).toHaveURL(/tab=all/);
    // Every repository's decisions, each row naming its own.
    await expect(page.getByText("4 sessions read")).toBeVisible();
    await expect(
      page.locator("li", { hasText: "Keep the other repository's checks in one workflow" }),
    ).toContainText("acme/other");
    await page.getByRole("button", { name: /Match keywords with SQLite FTS5/ }).click();
    await expect(
      page.getByRole("button", { name: "internal/decisionlog/featurestore.go", exact: true }),
    ).toBeVisible();
    // Its session's transcript is gone from this computer.
    await expect(
      page.getByText("The session's transcript is no longer on this computer."),
    ).toBeVisible();
    await shot(page, `decisions-${scheme}`);
    await openFilters(page);
    await page.locator("summary", { hasText: "All categories" }).click();
    await page.getByRole("checkbox", { name: "Scope" }).check();
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");
    const listed = page.locator("main section > ol");
    await expect(listed).toContainText("Try a hosted-only Timeline first");
    await expect(listed).not.toContainText("Match keywords with SQLite FTS5");
    // The open decision stays open above the list, as on the hosted Timeline.
    await expect(page.getByLabel("Opened decision")).toContainText(
      "Match keywords with SQLite FTS5",
    );

    // Another repository with nothing placed yet explains how features
    // appear.
    await page.getByRole("tab", { name: "Features" }).click();
    await page
      .getByLabel("Repositories without feature history")
      .getByRole("button", { name: "Open acme/other" })
      .click();
    await expect(page).toHaveURL(/repo=acme%2Fother/);
    await expect(page.getByText("No features yet")).toBeVisible();
    await shot(page, `empty-${scheme}`);
    // Its history is built by a confirmed job, from its estimate.
    await page.getByRole("button", { name: "Build feature history", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Build feature history" });
    await expect(dialog).toContainText("42 to place");
    await expect(dialog).toContainText("About $0.84");
    await expect(dialog).toContainText("baum first discovers this repository's features");
    await expect(dialog).toContainText("its least recently updated pull requests are left out");
    await expect(dialog).toContainText("a token budget of what about $5.00 buys");
    await shot(page, `build-confirm-${scheme}`);
    await dialog.getByRole("button", { name: "Build feature history", exact: true }).click();
    await expect(dialog.getByRole("link", { name: "View it in Jobs" })).toHaveAttribute(
      "href",
      /page=jobs&job=/,
    );
    await dialog.getByRole("button", { name: "Close", exact: true }).click();
    await expect(dialog).toBeHidden();
    // The fixture's build stops at once; the empty state stays, with the
    // failed build above it.
    await expect(page.getByRole("region", { name: "Feature build" })).toContainText(
      "The last feature history build stopped before it made a version",
    );
    await expect(page.getByText("No features yet")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Build feature history", exact: true }),
    ).toBeVisible();

    // Narrow: the page folds without scrolling sideways, the feature opens
    // in place.
    await page.setViewportSize({ width: 420, height: 900 });
    await page.locator('summary[aria-label^="Repositories:"]').click();
    await page.getByRole("button", { name: "All repositories" }).click();
    await features
      .getByRole("button", { name: /Local decision log/ })
      .first()
      .click();
    await expect(features).toContainText("Serve the decision log locally");
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    ).toBe(true);
    await shot(page, `narrow-${scheme}`);
    await page.evaluate(() => window.scrollTo(0, 0));
    await shot(page, `filter-bar-narrow-${scheme}`, page.locator(".timeline-filters"));
    expect(errors).toEqual([]);
  });
}
