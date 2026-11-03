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
  const choices = page.locator('.upnext-menu [role="menuitem"]');
  await expect(choices.first()).toBeFocused();
  for (const [key, index] of [
    ["ArrowDown", 1],
    ["ArrowUp", 0],
    ["j", 1],
    ["k", 0],
    ["Control+n", 1],
    ["Control+p", 0],
  ] as const) {
    await page.keyboard.press(key);
    await expect(choices.nth(index)).toBeFocused();
    await expect(row).toHaveAttribute("aria-current", "true");
  }
  const saved = page.waitForResponse(
    (response) =>
      response.url().includes("api/ui-state/inbox") &&
      response.request().method() === "PUT" &&
      response.status() === 200,
  );
  await page.getByRole("menuitem", { name: /Tomorrow morning/ }).focus();
  await page.keyboard.press("Enter");
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
  } finally {
    await context.close();
  }
});
