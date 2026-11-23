import { randomUUID } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";
import { launchURL, resetInboxes } from "./inbox-helpers";

/** Clears Pull requests' sort and saved tabs through the daemon. */
async function clearFilters(page: Page) {
  if (!page.url().includes("/s/")) return;
  const endpoint = new URL("api/ui-state/filters", page.url()).href;
  const origin = new URL(page.url()).origin;
  await page.goto("about:blank");
  for (let attempt = 0; attempt < 3; attempt++) {
    const read = await page.request.get(endpoint);
    const { revision } = (await read.json()) as { revision: number };
    const written = await page.request.put(endpoint, {
      headers: { Origin: origin },
      data: { revision, mutation_id: `${randomUUID()}:1`, value: { pages: {}, tabs: [] } },
    });
    if (written.status() !== 409) return;
  }
}

test.afterEach(async ({ page }) => {
  await resetInboxes(page);
  await clearFilters(page);
});

/** Each section's rows, by their keys, in the order drawn. */
const sectionRows = (page: Page) =>
  page
    .locator(".upnext-list section")
    .evaluateAll((sections) =>
      sections.map((section) =>
        [...section.querySelectorAll(":scope > ul > li[data-row]")].map(
          (row) => (row as HTMLElement).dataset.row!,
        ),
      ),
    );
/** Whether each section's pull requests run by repository, A to Z or back. */
const byRepository = (sections: string[][], reversed = false) =>
  sections.every((rows) => {
    const names = rows.filter((key) => !key.startsWith("group:")).map((key) => key.split("#")[0]!);
    return names.every((name, i) => {
      if (i === 0) return true;
      const order = names[i - 1]!.localeCompare(name);
      return reversed ? order >= 0 : order <= 0;
    });
  });

test("sorts an inbox from the keyboard, within its sections, and saves it with the inbox", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
  await expect(page.locator(".upnext-list button.upnext-row").first()).toBeVisible({
    timeout: 15000,
  });
  const before = await sectionRows(page);
  // The fixture has a section with two repositories, A to Z, so Z to A
  // moves its rows.
  expect(byRepository(before, true)).toBe(false);
  const button = page.locator("summary.sort-menu-button");
  await expect(button).toHaveAttribute("aria-label", "Sort");

  // Shift+S opens the menu with the keys on it; 6 sorts by Repository.
  await page.keyboard.press("Shift+S");
  const menu = page.getByRole("menu", { name: "Sort by" });
  await expect(menu).toBeFocused();
  await page.keyboard.press("6");
  await expect(menu.getByRole("menuitemcheckbox", { name: /Repository/ })).toHaveAttribute(
    "aria-checked",
    "true",
  );
  await expect(button).toHaveAttribute("aria-label", "Sort, Repository, A to Z");
  await expect.poll(async () => byRepository(await sectionRows(page))).toBe(true);
  // The sections stay, each with the same rows.
  const sorted = await sectionRows(page);
  expect(sorted.map((rows) => [...rows].sort())).toEqual(before.map((rows) => [...rows].sort()));
  // 6 again turns it round.
  await page.keyboard.press("6");
  await expect(button).toHaveAttribute("aria-label", "Sort, Repository, Z to A");
  await expect.poll(async () => byRepository(await sectionRows(page), true)).toBe(true);

  // A second key follows the first; Backspace drops it.
  await page.keyboard.press("3");
  await expect(button).toHaveAttribute(
    "aria-label",
    "Sort, Repository, Z to A, then Risk, highest first",
  );
  await page.keyboard.press("Backspace");
  await expect(button).toHaveAttribute("aria-label", "Sort, Repository, Z to A");
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);

  // The sort is the inbox's own, saved with it, and back after a reload.
  await expect
    .poll(() =>
      page.evaluate(async () => {
        const read = await fetch("api/inboxes", { cache: "no-store" });
        const { inboxes } = (await read.json()) as { inboxes: { sort?: string }[] };
        return inboxes[0]?.sort;
      }),
    )
    .toBe("-repo");
  await page.reload();
  await expect(button).toHaveAttribute("aria-label", "Sort, Repository, Z to A");
  await expect.poll(async () => byRepository(await sectionRows(page), true)).toBe(true);

  await page.keyboard.press("Shift+S");
  await page.keyboard.press("Backspace");
  await expect(button).toHaveAttribute("aria-label", "Sort");
  await expect.poll(() => sectionRows(page)).toEqual(before);
  expect(errors).toEqual([]);
});

test("sorts Pull requests by risk within each bucket and keeps it across a reload", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page.locator(".upnext-list button.upnext-row").first()).toBeVisible({
    timeout: 15000,
  });
  await page.goto(page.url().replace(/\?.*$/, "") + "?page=pulls");
  await expect(page.locator("[data-row-id]").first()).toBeVisible();
  const button = page.locator("summary.sort-menu-button");
  await button.click();
  await page.getByRole("menuitemcheckbox", { name: /Risk/ }).click();
  await expect(button).toHaveAttribute("aria-label", "Sort, Risk, highest first");
  await page.keyboard.press("Escape");

  // Within each bucket, the scored rows run from the highest risk down and
  // the unscored come last.
  const risks = () =>
    page.locator(".local-inbox section").evaluateAll((sections) =>
      sections.map((section) =>
        [...section.querySelectorAll(':scope > ul > [data-row-id^="pr:"] .inbox-risk')].map(
          (cell) => {
            const score = /Risk (?:at least )?(\d)/.exec(cell.getAttribute("aria-label") ?? "");
            return score ? Number(score[1]) : 0;
          },
        ),
      ),
    );
  const ordered = (lists: number[][]) =>
    lists.every((list) => list.every((risk, i) => i === 0 || list[i - 1]! >= risk));
  await expect.poll(async () => ordered(await risks())).toBe(true);

  await page.reload();
  await expect(button).toHaveAttribute("aria-label", "Sort, Risk, highest first");
  await expect.poll(async () => ordered(await risks())).toBe(true);
});
