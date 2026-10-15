import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
test("local inbox, reused PR viewer, versions, generation and settings", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const foreign: string[] = [];
  page.on("request", (r) => {
    if (!r.url().startsWith("http://127.0.0.1:4167")) foreign.push(r.url());
  });
  await page.goto((await readFile("e2e/launch-url", "utf8")).trim());
  await expect(page.getByRole("heading", { name: "Pull requests", exact: true })).toBeVisible();
  expect(page.url()).not.toContain("token");
  await expect(page.getByRole("link", { name: /Reuse the existing PR page/ })).toBeVisible();
  expect(errors).toEqual([]);
  await page.screenshot({ path: "test-results/inbox-wide.png", fullPage: true });
  await page.getByRole("combobox").first().selectOption("mine");
  await expect(page.getByRole("link", { name: /without a review/ })).toBeVisible();
  await expect(page.getByRole("link", { name: /Reuse the existing/ })).toHaveCount(0);
  await page.getByRole("combobox").first().selectOption("all");
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await expect(page.getByText("Local fixture review version 2", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Review version", exact: true }).click();
  await page.getByRole("button", { name: "View v1", exact: true }).click();
  await expect(page.getByText("Local fixture review version 1", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Return to current version", exact: true }).click();
  await expect(page.getByText("Local fixture review version 2", { exact: true })).toBeVisible();
  await page.screenshot({ path: "test-results/review-wide.png", fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ path: "test-results/review-narrow.png", fullPage: true });

  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole("button", { name: "Regenerate review", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByText(/Estimated cost:/)).toBeVisible();
  await page.getByRole("button", { name: "Confirm estimate and generate" }).click();
  await expect(
    page.getByText("Adds notes, settings and a renamed guide.", { exact: true }),
  ).toBeVisible({ timeout: 30000 });
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  await expect(page.getByText(/completed/)).toBeVisible();
  await expect(page.getByText(/Calls 1 · Tokens 100/)).toBeVisible();
  await page.getByRole("link", { name: "Repositories & settings" }).click();
  await page.getByRole("textbox", { name: "Model override" }).fill("claude-sonnet-4-5");
  await page.getByRole("button", { name: "Save repository settings" }).click();
  await expect(page.getByRole("textbox", { name: "Model override" })).toHaveValue(
    "claude-sonnet-4-5",
  );
  expect(errors).toEqual([]);
  expect(foreign).toEqual([]);
});
