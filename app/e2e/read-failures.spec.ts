import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

/** Makes the daemon refuse the reads of `path` (a segment after the session's
 *  namespace, as `api/jobs`), until `heal` is called. */
async function refuse(page: Page, path: string) {
  const pattern = (url: URL) => url.pathname.endsWith(`/${path}`);
  await page.route(pattern, (route) =>
    route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ message: "The daemon is not answering." }),
    }),
  );
  return () => page.unroute(pattern);
}

const retry = (page: Page) => page.getByRole("button", { name: "Retry", exact: true });

test("a failed first read says so on Inbox and Jobs, once, with one Retry", async ({ page }) => {
  const heal = await refuse(page, "api/jobs");
  await page.goto(await launchURL());
  await expect(page.getByRole("heading", { name: "Couldn't load your inbox" })).toBeVisible();
  await expect(page.getByText("The daemon is not answering.")).toHaveCount(1);
  await expect(page.getByText("Loading your inbox")).toHaveCount(0);
  await expect(page.getByText("Inbox zero")).toHaveCount(0);
  await expect(retry(page)).toHaveCount(1);

  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Couldn't load jobs" })).toBeVisible();
  await expect(page.getByText("No jobs yet")).toHaveCount(0);
  await expect(retry(page)).toHaveCount(1);

  await heal();
  await retry(page).click();
  await expect(page.getByRole("heading", { name: "Couldn't load jobs" })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "Generation jobs" })).toBeVisible();
});

test("a failed later read keeps the jobs shown and says so", async ({ page }) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  const jobs = page.getByRole("region", { name: "Generation jobs" });
  await expect(jobs).toBeVisible();
  await refuse(page, "api/jobs");
  await page.keyboard.press("Control+r");
  await expect(page.getByText("The daemon is not answering.")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Couldn't load jobs" })).toHaveCount(0);
  await expect(jobs).toBeVisible();
});

test("a review group is not called missing when the groups could not be read", async ({ page }) => {
  const heal = await refuse(page, "api/groups");
  await page.goto(await launchURL());
  await expect(page.getByRole("heading", { name: "Up next", exact: true })).toBeVisible();
  await page.goto(new URL("?page=pulls&group=unknown", page.url()).toString());
  await expect(page.getByRole("heading", { name: "Couldn't load the review group" })).toBeVisible();
  await expect(page.getByText("does not exist any more")).toHaveCount(0);
  await heal();
  await retry(page).click();
  await expect(page.getByText("does not exist any more")).toBeVisible();
});

test("Go to and the shortcuts sheet give the focus back, and the page keys keep working", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page.getByRole("heading", { name: "Up next", exact: true })).toBeVisible();
  const link = page.getByRole("link", { name: "Jobs", exact: true });
  await link.focus();

  await page.keyboard.press("Control+k");
  await expect(page.getByRole("dialog", { name: "Go to" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(link).toBeFocused();

  await page.keyboard.press("?");
  await expect(page.getByRole("dialog", { name: "Keyboard shortcuts" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(link).toBeFocused();

  // The page's own keys still act: Escape lets go of the focus, then j moves
  // the row selection.
  await page.keyboard.press("Escape");
  await expect(link).not.toBeFocused();
  await page.keyboard.press("j");
  await expect(page.locator("[data-focused]").first()).toBeVisible();
});
