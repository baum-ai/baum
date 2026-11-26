import { expect, type Page, test } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

type Stored = { prs: Record<string, { unread: Record<string, number>; opened?: true }> };

/** Sets the unread activity of pull requests in the daemon's record, as
 *  reads of the app would have counted it for pull requests opened before. */
async function setUnread(page: Page, unread: Record<string, Record<string, number>>) {
  await page.evaluate(async (unread) => {
    // The app may save its own reads meanwhile: try again on a newer revision.
    for (let attempt = 0; attempt < 5; attempt++) {
      const read = await fetch("api/ui-state/activity", { cache: "no-store" });
      const { revision, value } = (await read.json()) as { revision: number; value: Stored };
      for (const [key, counts] of Object.entries(unread))
        value.prs[key] = { ...value.prs[key]!, unread: counts, opened: true };
      const saved = await fetch("api/ui-state/activity", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          revision,
          mutation_id: `e2e-activity-test:${Date.now()}`,
          value,
        }),
      });
      if (saved.ok) return;
      if (saved.status !== 409) throw Error(`saving the activity failed: ${saved.status}`);
    }
    throw Error("saving the activity kept conflicting");
  }, unread);
}

test("counts what happened on a pull request until its page is opened", async ({ page }) => {
  await page.goto(await launchURL());
  const rows = page.locator("li[data-row]:has(> button.upnext-row)");
  await expect(rows.first()).toBeVisible();
  // Two listed pull requests the record follows: those with GitHub's details.
  let followed: string[] = [];
  await expect
    .poll(async () => {
      const keys = await rows.evaluateAll((items) =>
        items.map((item) => item.getAttribute("data-row")!),
      );
      followed = await page.evaluate(async (keys) => {
        const read = await fetch("api/ui-state/activity", { cache: "no-store" });
        const { value } = (await read.json()) as { value: Stored | null };
        return keys.filter((key) => value?.prs[key]);
      }, keys);
      return followed.length;
    })
    .toBeGreaterThanOrEqual(2);
  const [first, second] = followed;
  await setUnread(page, { [first!]: { pushes: 2, answers: 1 }, [second!]: { reviews: 1 } });
  await page.reload();
  const badge = page.locator(`li[data-row="${first}"] .upnext-unread`);
  await expect(badge).toHaveText(/^3/);
  await expect(badge).toHaveAttribute(
    "title",
    "2 pushes and 1 agent answer since you last opened it",
  );
  await expect(page.locator(`li[data-row="${second}"] .upnext-unread`)).toHaveText(/^1/);
  // Opening the pull request reads it.
  await page.locator(`li[data-row="${first}"] > button.upnext-row`).click();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/[?&]pr=/);
  await page.goBack();
  await expect(page.locator(`li[data-row="${second}"] .upnext-unread`)).toBeVisible();
  await expect(page.locator(`li[data-row="${first}"] .upnext-unread`)).toHaveCount(0);
});
