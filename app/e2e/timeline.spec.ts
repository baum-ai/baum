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
    await expect(page.locator(".timeline-repos > summary")).toHaveText("All repositories");
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
    const panel = page.locator("#timeline-feature-panel");
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

    // The user is one person: their sessions and the pull request their
    // login opened.
    const person = page.getByLabel("Person");
    await expect(person.locator("option")).toHaveText([
      "Everyone",
      "Ada Lovelace (3)",
      "author (2)",
    ]);
    await person.selectOption({ label: "author (2)" });
    await expect(page).toHaveURL(/person=author%40users.noreply.github.com/);
    await expect(features).toContainText("Hosted-only Timeline");
    await expect(features).not.toContainText("Keyword search over decisions");
    await person.selectOption({ label: "Ada Lovelace (3)" });
    await expect(features).toContainText("Keyword search over decisions");
    if (process.env.TIMELINE_SHOTS) {
      // The list the select offers, drawn open for the shot.
      await person.evaluate((select: HTMLSelectElement) => {
        select.size = 3;
      });
      await shot(page, `people-${scheme}`, person);
      await person.evaluate((select: HTMLSelectElement) => {
        select.size = 0;
      });
    }
    await person.selectOption("");

    // Filters: status and keywords narrow the features.
    await page.getByLabel("Status").selectOption("abandoned");
    await expect(page).toHaveURL(/status=abandoned/);
    await expect(features).toContainText("Hosted-only Timeline");
    await expect(features).not.toContainText("Local decision log");
    await page.getByLabel("Status").selectOption("");
    await page.getByLabel("Search features").fill("keyword");
    await expect(page).toHaveURL(/q=keyword/);
    await expect(features).toContainText("Keyword search over decisions");
    await expect(features).not.toContainText("Local decision log");
    await page.getByLabel("Search features").fill("");
    await expect(page).not.toHaveURL(/q=/);
    // A file narrows the features to those touching it.
    await page.getByLabel("Filter by file").fill("internal/decisionlog/featurestore.go");
    await page.getByLabel("Filter by file").press("Enter");
    await expect(page).toHaveURL(/file=internal%2Fdecisionlog%2Ffeaturestore.go/);
    await expect(features).toContainText("Keyword search over decisions");
    await expect(features).not.toContainText("Local decision log");
    await page.getByRole("button", { name: /Remove filter: File/ }).click();
    // A range after everything leaves nothing.
    await page.getByLabel("From", { exact: true }).fill("2099-01-01");
    await expect(page.getByText("No features match these filters")).toBeVisible();
    await page.getByLabel("From", { exact: true }).fill("");
    await expect(features).toContainText("Local decision log");

    // All decisions, with one open.
    await page.getByRole("tab", { name: "All decisions" }).click();
    await expect(page).toHaveURL(/tab=all/);
    // Every repository's decisions, each row naming its own.
    await expect(page.getByText("4 sessions read")).toBeVisible();
    await expect(
      page.locator("li", { hasText: "Keep the other repository's checks in one workflow" }),
    ).toContainText("acme/other");
    await page.getByRole("button", { name: /Match keywords with SQLite FTS5/ }).click();
    await expect(page.getByText("internal/decisionlog/featurestore.go")).toBeVisible();
    // Its session's transcript is gone from this computer.
    await expect(
      page.getByText("The session's transcript is no longer on this computer."),
    ).toBeVisible();
    await shot(page, `decisions-${scheme}`);
    await page.getByLabel("Category").selectOption("scope");
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
    await page.locator(".timeline-repos > summary").click();
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
    expect(errors).toEqual([]);
  });
}
