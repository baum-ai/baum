import { expect, type Page, test } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

type Stored = { prs: Record<string, { unread: Record<string, number>; opened?: true }> };

/** Sets the unread activity of opened pull requests in the daemon's record,
 *  as reads of the app would have counted it. */
async function setUnread(page: Page, unread: Record<string, Record<string, number>>) {
  await page.evaluate(async (unread) => {
    // The app may save its own reads meanwhile: try again on a newer revision.
    for (let attempt = 0; attempt < 5; attempt++) {
      const read = await fetch("api/ui-state/activity", { cache: "no-store" });
      const { revision, value } = (await read.json()) as { revision: number; value: Stored };
      for (const [key, counts] of Object.entries(unread)) {
        const record = value.prs[key];
        if (!record) throw Error(`no activity record for ${key}`);
        record.unread = counts;
      }
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

/** Those of `keys` the record follows, with the conversations and GitHub's
 *  details read. */
const followed = (page: Page, keys: string[]) =>
  page.evaluate(async (keys) => {
    const read = await fetch("api/ui-state/activity", { cache: "no-store" });
    const { value } = (await read.json()) as { value: Stored | null };
    return keys.filter((key) => {
      const record = value?.prs[key];
      return record?.opened && !("partial" in record);
    });
  }, keys);

test("counts what happened on an opened pull request until its page is opened again", async ({
  page,
}) => {
  await page.goto(await launchURL());
  const rows = page.locator("li[data-row]:has(> button.upnext-row)");
  await expect(rows.nth(3)).toBeVisible();
  const keys = await rows.evaluateAll((items) =>
    items.map((item) => item.getAttribute("data-row")!),
  );
  // Only the pull requests opened before are followed: open three, of which
  // two have GitHub's details.
  const opened = keys.slice(0, 3);
  const unopened = keys[3];
  for (const key of opened) {
    await page.locator(`li[data-row="${key}"] > button.upnext-row`).click();
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/[?&]pr=/);
    await page.goBack();
    await expect(rows.first()).toBeVisible();
  }
  let followedKeys: string[] = [];
  await expect
    .poll(async () => {
      followedKeys = await followed(page, opened);
      return followedKeys.length;
    })
    .toBeGreaterThanOrEqual(2);
  const [first, second] = followedKeys;
  expect(await followed(page, [unopened!])).toEqual([]);
  await setUnread(page, { [first!]: { pushes: 2, answers: 1 }, [second!]: { reviews: 1 } });
  await page.reload();
  const badge = page.locator(`li[data-row="${first}"] .upnext-unread`);
  await expect(badge).toHaveText(/^3/);
  await expect(badge).toHaveAttribute(
    "title",
    "2 pushes and 1 agent answer since you last opened it",
  );
  await expect(page.locator(`li[data-row="${second}"] .upnext-unread`)).toHaveText(/^1/);
  await expect(page.locator(`li[data-row="${unopened}"] .upnext-unread`)).toHaveCount(0);
  // Opening the pull request reads it.
  await page.locator(`li[data-row="${first}"] > button.upnext-row`).click();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/[?&]pr=/);
  await page.goBack();
  await expect(page.locator(`li[data-row="${second}"] .upnext-unread`)).toBeVisible();
  await expect(page.locator(`li[data-row="${first}"] .upnext-unread`)).toHaveCount(0);
});
