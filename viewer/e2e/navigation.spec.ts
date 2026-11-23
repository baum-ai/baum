import { expect, test } from "./fixtures.ts";

test("the rail walks from the summary to a section and keeps the place in the URL", async ({
  page,
}) => {
  await page.goto("/");
  const rail = page.getByRole("navigation", { name: "Review sections" });
  await expect(rail.getByRole("button", { name: "Summary" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );

  await rail.getByRole("button", { name: "Retry policy and backoff", exact: true }).click();

  await expect(page).toHaveURL(/\?pane=section&section=sec_retry$/);
  await expect(
    page.getByRole("heading", { level: 2, name: "Retry policy and backoff" }),
  ).toBeVisible();
  await expect(rail.getByRole("button", { name: "Summary" })).toHaveAttribute(
    "aria-pressed",
    "false",
  );

  await page.goBack();
  await expect(page).not.toHaveURL(/pane=/);
  await expect(page.getByRole("heading", { level: 2, name: "Summary" })).toBeVisible();
  await page.goForward();
  await expect(
    page.getByRole("heading", { level: 2, name: "Retry policy and backoff" }),
  ).toBeVisible();
});

test("on a phone the rail is a sheet behind a bar that steps through the review", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await page.goto("/");

  const rail = page.getByRole("navigation", { name: "Review sections" });
  await expect(rail).toBeHidden();
  await expect(page.getByRole("button", { name: "Previous" })).toBeDisabled();

  await page.getByRole("button", { name: /^Next: / }).click();
  await expect(page).toHaveURL(/pane=section&section=sec_api$/);
  await expect(
    page.getByRole("heading", { level: 2, name: "Operator-only dead-letter endpoints" }),
  ).toBeVisible();

  // The sheet lists the same places and closes when one is chosen.
  await page
    .getByRole("button", { name: /^Operator-only dead-letter endpoints/ })
    .first()
    .click();
  await expect(rail).toBeVisible();
  await rail.getByRole("button", { name: "Retry policy and backoff", exact: true }).click();
  await expect(page).toHaveURL(/section=sec_retry/);
  await expect(rail).toBeHidden();
});

for (const width of [1440, 390]) {
  test(`] toggles the right sidebar at width ${width}`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/");
    await expect(page.getByRole("heading", { level: 2, name: "Summary" })).toBeVisible();
    if (width >= 1024) {
      await page.keyboard.press("]");
      await expect(
        page.getByRole("button", { name: "Expand side pane", exact: true }),
      ).toBeVisible();
      await page.reload();
      await expect(
        page.getByRole("button", { name: "Expand side pane", exact: true }),
      ).toBeVisible();
      await page.keyboard.press("]");
      await expect(
        page.getByRole("button", { name: "Collapse side pane", exact: true }),
      ).toBeVisible();
    } else {
      const aside = page.getByRole("region", { name: "Side pane", exact: true });
      await expect(aside).toBeHidden();
      await page.keyboard.press("]");
      await expect(aside).toBeVisible();
      await page.keyboard.press("]");
      await expect(aside).toBeHidden();
    }
  });
}
