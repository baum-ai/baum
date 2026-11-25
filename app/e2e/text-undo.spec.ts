import { expect, test } from "@playwright/test";
import { launchURL, resetInboxes } from "./inbox-helpers";

// Undo and redo in a React-controlled text field (src/text-history.ts); the
// prompt editor's are in prompts.spec.ts.

test.afterEach(async ({ page }) => {
  await resetInboxes(page);
});

test("undoes and redoes the inbox name", async ({ page }) => {
  await page.goto(await launchURL());
  await page.locator(".upnext-list-title h1").click({ button: "right" });
  await page.getByRole("menuitem", { name: "Edit…" }).click();
  const name = page.getByLabel("Name", { exact: true });
  await expect(name).toHaveValue("Default");
  await name.focus();
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.type("Renamed");
  await expect(name).toHaveValue("Renamed");
  await page.keyboard.press("ControlOrMeta+z");
  await expect(name).toHaveValue("Default");
  await page.keyboard.press("ControlOrMeta+Shift+z");
  await expect(name).toHaveValue("Renamed");
});
