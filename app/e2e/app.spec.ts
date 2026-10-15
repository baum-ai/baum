import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
test("local inbox, reused PR viewer, versions, generation and settings", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const evidence: string[] = [];
  const commits = new Map<string, number>();
  page.on("response", (response) => {
    if (new URL(response.url()).pathname === "/api/sessions")
      void response.json().then((body) => {
        if (body.available)
          commits.set(
            new URL(response.url()).searchParams.get("version") ?? "current",
            body.commits.length,
          );
      });
  });
  page.on("request", (r) => {
    if (r.url().includes("/api/sessions?")) evidence.push(r.url());
  });
  const foreign: string[] = [];
  page.on("request", (r) => {
    if (!r.url().startsWith("http://127.0.0.1:4167")) foreign.push(r.url());
  });
  await page.goto((await readFile("e2e/launch-url", "utf8")).trim());
  await expect(page.getByRole("heading", { name: "Pull requests", exact: true })).toBeVisible();
  expect(page.url()).not.toContain("token");
  await expect(page.getByRole("link", { name: /Reuse the existing PR page/ })).toBeVisible();
  expect(errors).toEqual([]);
  await page.screenshot({
    path: "test-results/inbox-wide.png",
    fullPage: false,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("region", { name: "Pull request inbox" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: "test-results/inbox-narrow.png",
    fullPage: false,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 1280, height: 900 });
  const requested = page.getByRole("button", { name: /Review requested/ });
  await requested.click();
  await expect(requested).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByRole("link", { name: /Reuse the existing/ })).toHaveCount(0);
  await requested.click();
  await page.getByRole("combobox").first().selectOption("mine");
  await expect(page.getByRole("link", { name: /without a review/ })).toBeVisible();
  await expect(page.getByRole("link", { name: /Reuse the existing/ })).toHaveCount(0);
  await page.getByRole("combobox").first().selectOption("all");
  await page.getByRole("checkbox", { name: "Select acme/demo #9", exact: true }).check();
  await page.getByRole("button", { name: "Generate selected (1)", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("isolation_confirmation_required");
  await expect(
    page.getByRole("button", { name: "Confirm estimate and generate", exact: true }),
  ).toBeDisabled();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("checkbox", { name: "Select acme/demo #9", exact: true }).uncheck();
  await page.getByRole("link", { name: /A fork requiring confirmation/ }).click();
  await page.getByRole("button", { name: "Generate review", exact: true }).first().click();
  await expect(
    page.getByRole("button", { name: "Confirm estimate and generate", exact: true }),
  ).toBeDisabled();
  await page.getByRole("dialog").getByRole("checkbox").check();
  await expect(
    page.getByRole("button", { name: "Confirm estimate and generate", exact: true }),
  ).toBeEnabled();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("link", { name: "Inbox", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await expect(page.getByText("Local fixture review version 2", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Review version", exact: true }).click();
  await page.getByRole("button", { name: "View v1", exact: true }).click();
  await expect(page.getByText("Local fixture review version 1", { exact: true })).toBeVisible();
  await expect
    .poll(() => evidence.some((url) => new URL(url).searchParams.get("version") === "1"))
    .toBe(true);
  await expect.poll(() => commits.get("1")).toBe(0);
  await page.getByRole("button", { name: "Return to current version", exact: true }).click();
  await expect(page.getByText("Local fixture review version 2", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Docs and dependency bump", exact: true }).click();
  await page.getByRole("button", { name: "Review version", exact: true }).click();
  await page.getByRole("checkbox", { name: "Compare v1", exact: true }).check();
  await expect(page.locator('img[src*="/api/attachments/"]')).toHaveCount(2);
  const imageSources = await page
    .locator('img[src*="/api/attachments/"]')
    .evaluateAll((images) => images.map((img) => (img as HTMLImageElement).src));
  expect(imageSources.some((url) => new URL(url).searchParams.get("version") === "1")).toBe(true);
  expect(imageSources.some((url) => new URL(url).searchParams.get("version") === "2")).toBe(true);
  await page.getByRole("button", { name: "Close history comparison", exact: true }).click();
  const summary = page
    .getByRole("navigation", { name: "Review sections" })
    .getByRole("button", { name: "Summary", exact: true });
  await summary.click();
  await expect(summary).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.locator("#review-section").getByRole("heading", { name: "Summary", exact: true }),
  ).toBeVisible();
  await expect(page.locator("#review-section .animate-pane-in")).toHaveCSS("opacity", "1");
  await page.screenshot({
    path: "test-results/review-wide.png",
    fullPage: false,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: "test-results/review-narrow.png",
    fullPage: false,
    animations: "disabled",
  });

  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole("link", { name: "Inbox", exact: true }).first().click();
  await page.getByRole("link", { name: /A pull request without a review/ }).click();
  await page.getByRole("button", { name: "Generate review", exact: true }).first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByText(/Estimated cost:/)).toBeVisible();
  await page.getByRole("button", { name: "Confirm estimate and generate" }).click();
  await expect(
    page.getByText("Adds notes, settings and a renamed guide.", { exact: true }),
  ).toBeVisible({ timeout: 30000 });
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  await expect(page.getByText(/completed/)).toBeVisible();
  await expect(page.getByText(/Calls 1 · Tokens 100/)).toBeVisible();
  await page.getByRole("link", { name: "Inbox", exact: true }).first().click();
  await page.getByRole("link", { name: /A pull request without a review/ }).click();
  await page.getByRole("button", { name: "Regenerate review", exact: true }).click();
  await page.getByRole("button", { name: "Confirm estimate and generate", exact: true }).click();
  await page.getByRole("button", { name: "Cancel job", exact: true }).click();
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  await expect(page.getByText(/cancelled/).first()).toBeVisible();
  await page.getByRole("link", { name: "Repositories & settings" }).click();
  const settings = page.getByRole("region", { name: "Repository acme/demo", exact: true });
  await settings.getByRole("textbox", { name: "Model override" }).fill("claude-sonnet-4-5");
  await settings.getByRole("button", { name: "Save repository settings" }).click();
  await expect(settings.getByRole("textbox", { name: "Model override" })).toHaveValue(
    "claude-sonnet-4-5",
  );
  expect(errors).toEqual([]);
  expect(foreign).toEqual([]);
});
