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

test("a section link opens that section on load, and a stale one falls back to the summary", async ({
  page,
}) => {
  await page.goto("/?pane=section&section=sec_ackfix");
  await expect(
    page.getByRole("heading", { level: 2, name: "Second ack is ignored" }),
  ).toBeVisible();

  await page.goto("/?pane=section&section=sec_that_does_not_exist");
  await expect(page.getByText("This section did not exist in review v3.")).toBeVisible();
  // The address keeps what it asked for.
  expect(new URL(page.url()).search).toBe("?pane=section&section=sec_that_does_not_exist");

  await page.goto("/?pane=nonsense");
  await expect(page.getByRole("heading", { level: 2, name: "Summary" })).toBeVisible();
});

test("the summary's jump links open the section they name", async ({ page }) => {
  await page.goto("/");
  const intent = page.getByRole("region", { name: "Intent and instructions" });

  await intent.getByRole("button", { name: "Second ack is ignored →" }).click();

  await expect(page).toHaveURL(/section=sec_ackfix/);
  await expect(
    page.getByRole("heading", { level: 2, name: "Second ack is ignored" }),
  ).toBeVisible();
});

test("the Changes and Risks overviews open from the summary", async ({ page }) => {
  await page.goto("/");

  await page.getByRole("button", { name: "View all risks →" }).click();
  await expect(page).toHaveURL(/pane=risks/);
  await expect(page.getByRole("heading", { level: 2, name: "Risks" })).toBeVisible();

  await page.goBack();
  await page
    .getByRole("navigation", { name: "Review sections" })
    .getByRole("button", { name: "Changes", exact: true })
    .click();
  await expect(page).toHaveURL(/pane=changes/);
});

test("a risk opens by its link, from the rail or the address", async ({ page }) => {
  await page.goto("/?pane=risk&risk_section=sec_api&risk_id=rsk_api_header");
  await expect(page.getByText("Critical risk").first()).toBeVisible();
  await expect(page.getByText("RoleFromRequest trusts X-Taskline-Role").first()).toBeVisible();

  await page.goto("/");
  await page
    .getByRole("navigation", { name: "Review sections" })
    .getByRole("button", { name: "Concurrency · Retry policy and backoff" })
    .click();
  await expect(page).toHaveURL(/pane=risk&risk_section=sec_retry&risk_id=rsk_retry_herd/);
  await expect(page.getByText("All workers share one policy").first()).toBeVisible();
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
