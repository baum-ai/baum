import { expect, test } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

test("a running session is one of the pull request's sessions, counted and listed", async ({
  page,
}) => {
  const now = new Date().toISOString();
  await page.route("**/api/live-sessions", async (route) => {
    await route.fulfill({
      json: {
        sessions: [
          {
            agent: "claude-code",
            session_id: "running-on-7",
            state: "idle",
            waiting_reason: "turn_finished",
            started_at: now,
            last_activity_at: now,
            last_event: "stop",
            repository: "acme/demo",
            branch: "feature",
            pr: 7,
            title: "Resolve the conflict with the base",
          },
        ],
      },
    });
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();

  const group = page.getByRole("button", { name: /^Sessions, \d+$/ });
  await expect(group).toBeVisible();
  const count = Number((await group.getAttribute("aria-label"))!.split(", ")[1]);
  expect(count).toBeGreaterThan(0);
  await group.click();

  // The running session is the first card of the pane (the rail's Sessions
  // row lists nothing under it), counted with the linked ones in the pane's
  // heading.
  const card = page.locator("li#session-running-on-7");
  await expect(card).toContainText("Finished its turn");
  await expect(card).toContainText("feature");
  await expect(page.getByText(`${count} session`, { exact: false }).first()).toBeVisible();
  await expect(page.getByText("No agent sessions")).toHaveCount(0);
  await expect(page.getByRole("region", { name: "Sessions in progress" })).toHaveCount(0);
  await page.screenshot({ path: "test-results/pr-sessions-list.png" });

  // Opened, it leaves the review for its own page on Sessions, where it can
  // be continued, and Esc comes back to the review's list.
  await card.getByRole("button", { name: "Resolve the conflict with the base" }).click();
  await expect(page).toHaveURL(/page=sessions&session=claude-code%3Arunning-on-7/);
  await expect(
    page.getByRole("heading", { name: "Resolve the conflict with the base" }),
  ).toBeVisible();
  await page.screenshot({ path: "test-results/pr-sessions-open.png" });
  await page.keyboard.press("Escape");
  await expect(page).toHaveURL(/pane=sessions/);
  await expect(card).toBeVisible();
});
