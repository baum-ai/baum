import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, type Page, test } from "@playwright/test";

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

// With TIMELINE_SHOTS set, the views are also saved there for a look.
async function shot(page: Page, name: string) {
  const dir = process.env.TIMELINE_SHOTS;
  if (dir) await page.screenshot({ path: join(dir, `${name}.png`), fullPage: true });
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
    await expect(page.getByLabel("Repository")).toHaveValue("acme/demo");
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
    await expect(panel).toContainText("The Timeline does not show prompts.");
    await shot(page, `feature-panel-${scheme}`);

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

    // All decisions, with one open.
    await page.getByRole("tab", { name: "All decisions" }).click();
    await expect(page).toHaveURL(/tab=all/);
    await expect(page.getByText("3 sessions read")).toBeVisible();
    await page.getByRole("button", { name: /Match keywords with SQLite FTS5/ }).click();
    await expect(page.getByText("internal/decisionlog/featurestore.go")).toBeVisible();
    await expect(page.getByRole("link", { name: "Open the session" })).toHaveAttribute(
      "href",
      /page=sessions&session=claude-code%3Atimeline-2/,
    );
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
    await page.getByLabel("Repository").selectOption("acme/other");
    await expect(page.getByText("No features yet")).toBeVisible();
    await shot(page, `empty-${scheme}`);
    // Its history is built by a confirmed job, from its estimate and cap.
    await page.getByRole("button", { name: "Build feature history", exact: true }).click();
    const dialog = page.getByRole("dialog", { name: "Build feature history" });
    await expect(dialog).toContainText("42 to place");
    await expect(dialog).toContainText("About $0.84");
    await expect(dialog).toContainText("$1.05 over all its runs");
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
      "The last feature history build failed",
    );
    await expect(page.getByText("No features yet")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Build feature history", exact: true }),
    ).toBeVisible();

    // Narrow: the page folds without scrolling sideways, the feature opens
    // in place.
    await page.setViewportSize({ width: 420, height: 900 });
    await page.getByLabel("Repository").selectOption("acme/demo");
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
