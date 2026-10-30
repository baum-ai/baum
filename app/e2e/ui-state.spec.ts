import { expect, test } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

test("keeps inbox snoozes after clearing browser storage and opening a fresh context", async ({
  page,
  browser,
}) => {
  const launch = await launchURL();
  await page.goto(launch);
  const row = page.locator("button.upnext-row").first();
  await expect(row).toBeVisible();
  const title = (await row.locator(".upnext-row-title").textContent())!;
  await row.click();
  await page.getByRole("button", { name: /^Snooze/ }).click();
  const saved = page.waitForResponse(
    (response) =>
      response.url().includes("api/ui-state/inbox") &&
      response.request().method() === "PUT" &&
      response.status() === 200,
  );
  await page.getByRole("menuitem", { name: /Tomorrow morning/ }).click();
  await saved;
  await page.evaluate(() => localStorage.clear());
  await page.reload();
  await expect(page.getByRole("region", { name: "Snoozed" })).toBeVisible();
  const context = await browser.newContext();
  try {
    const reopened = await context.newPage();
    await reopened.goto(launch);
    const snoozed = reopened.getByRole("region", { name: "Snoozed" });
    await expect(snoozed).toBeVisible();
    await snoozed.getByRole("button", { name: /Snoozed/ }).click();
    const retained = snoozed.getByRole("listitem").filter({ hasText: title });
    await expect(retained).toBeVisible();
    const restored = reopened.waitForResponse(
      (response) =>
        response.url().includes("api/ui-state/inbox") &&
        response.request().method() === "PUT" &&
        response.status() === 200,
    );
    await retained.getByRole("button", { name: "Bring back" }).click();
    await restored;
    await expect(retained).toHaveCount(0);
  } finally {
    await context.close();
  }
});
