import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// The session panel over the fixture daemon's real conversations service,
// whose agent is scripted (`panelAgent` in the uiserver fixture): it reads one
// file and answers in a few steps, writing its transcript and hook events as
// Claude Code does.

const panel = (page: Page) => page.locator("section.session-panel");

/** The page context bodies the app sends to `PUT api/console/page`. */
function reportedPages(page: Page) {
  const bodies: Record<string, unknown>[] = [];
  page.on("request", (request) => {
    if (request.method() === "PUT" && request.url().endsWith("/api/console/page"))
      bodies.push(request.postDataJSON() as Record<string, unknown>);
  });
  return bodies;
}

test("opens the session panel on a pull request and starts a session there", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  const reported = reportedPages(page);
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await expect(page.getByRole("heading", { name: "Reuse the existing PR page" })).toBeVisible();

  // The key opens it from anywhere, its composer taking the keys.
  await page.keyboard.press("`");
  await expect(panel(page)).toBeVisible();
  await expect(panel(page).getByRole("heading", { name: "New session" })).toBeVisible();
  await expect(page.getByTestId("session-panel-page")).toHaveText(
    "Reuse the existing PR page · acme/demo #7",
  );
  await expect(panel(page).getByText("A checkout of acme/demo #7")).toBeVisible();
  await expect(panel(page).getByRole("button", { name: "Auto", pressed: true })).toBeVisible();
  await expect(panel(page).getByText("Start a session")).toBeVisible();
  // The daemon knows the page the panel was opened on.
  await expect
    .poll(() => reported.at(-1))
    .toMatchObject({ page: "pull_request", repository: "acme/demo", pull_request: 7 });

  const composer = panel(page).getByRole("textbox", { name: "Message to the agent" });
  await expect(composer).toBeFocused();
  await composer.fill("Why is the retry delay capped?");
  await composer.press("Enter");
  // The prompt shows at once, before the daemon answers.
  await expect(panel(page).getByText("Why is the retry delay capped?").first()).toBeVisible();
  await expect(composer).toHaveValue("");

  // While the agent works: its tool, then its answer as it streams.
  await expect(panel(page).getByText("Read docs/guide.md")).toBeVisible();
  await expect(panel(page).getByText("Working", { exact: true })).toBeVisible();
  await expect(panel(page).getByRole("button", { name: "Stop" })).toBeVisible();
  await page.screenshot({ path: "test-results/session-panel-running.png", animations: "disabled" });

  // The Sessions menu stays open while the turn goes on under it.
  await panel(page).getByRole("button", { name: "Sessions" }).click();
  await expect(page.getByRole("menu")).toBeVisible();
  // Once it finished, the session's transcript holds the turn.
  await expect(panel(page).getByRole("region", { name: "Transcript" })).toContainText(
    "the retry delay is capped",
  );
  await expect(page.getByRole("menu")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menu")).toHaveCount(0);
  await expect(panel(page)).toBeVisible();
  await expect(panel(page).getByText("Working", { exact: true })).toHaveCount(0);
  await expect(panel(page).getByRole("button", { name: "Send" })).toBeVisible();
  await expect(
    panel(page).getByRole("heading", { name: "Why is the retry delay capped?" }),
  ).toBeVisible();
  await expect(panel(page).locator(".session-panel-directory code")).toContainText("console");
  await expect(panel(page).getByText("Auto", { exact: true })).toBeVisible();
  await page.screenshot({
    path: "test-results/session-panel-answered.png",
    animations: "disabled",
  });

  // Escape in the empty composer hides it; the key brings it back on the
  // same session.
  await composer.press("Escape");
  await expect(panel(page)).toHaveCount(0);
  await page.keyboard.press("`");
  await expect(
    panel(page).getByRole("heading", { name: "Why is the retry delay capped?" }),
  ).toBeVisible();

  // With something typed, the first Escape gives the keys to the panel and
  // the second hides it; the pull request's page under it keeps both, so it
  // does not go back to its list.
  await composer.fill("A half-written question");
  await composer.press("Escape");
  await expect(panel(page)).toBeVisible();
  await expect(panel(page)).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(panel(page)).toHaveCount(0);
  await expect(page.getByRole("heading", { name: "Reuse the existing PR page" })).toBeVisible();
  await page.keyboard.press("`");
  await expect(composer).toHaveValue("A half-written question");
  await composer.fill("");

  // The Sessions page lists it as the panel's, to open in the panel.
  await page.getByRole("link", { name: "Sessions", exact: true }).first().click();
  const card = page.locator("[data-row-id]", { hasText: "Panel" }).first();
  await expect(card).toBeVisible();
  await card.getByRole("button", { name: /^Actions for / }).click();
  const menu = page.getByRole("menu");
  await expect(menu.getByRole("menuitem", { name: "Open in panel" })).toBeVisible();
  await expect(menu.getByRole("menuitem", { name: /Reply|End session|Focus session/ })).toHaveCount(
    0,
  );
  await page.screenshot({
    path: "test-results/session-panel-sessions.png",
    animations: "disabled",
  });
});

test("the panel follows the page the person has open", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  const reported = reportedPages(page);
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("button", { name: "Session", exact: true }).click();
  await expect(panel(page)).toBeVisible();
  await expect(page.getByTestId("session-panel-page")).toHaveText("Pull requests");
  await expect.poll(() => reported.at(-1)).toMatchObject({ page: "pulls" });

  // Open across navigation, it names the new page and tells the daemon.
  await page.getByRole("link", { name: "Settings", exact: true }).first().click();
  await expect(panel(page)).toBeVisible();
  await expect(page.getByTestId("session-panel-page")).toHaveText("Settings · General");
  await expect.poll(() => reported.at(-1)).toEqual({ page: "settings", section: "general" });

  await page.getByRole("button", { name: /^Fixes/ }).click();
  await expect(page.getByTestId("session-panel-page")).toHaveText("Settings · Fixes");
  await expect.poll(() => reported.at(-1)).toEqual({ page: "settings", section: "fixes" });
  // The page scrolls clear of the panel: its last section comes into view
  // above it.
  const section = page.getByRole("heading", { name: "Session panel", exact: true });
  await section.scrollIntoViewIfNeeded();
  const footnote = page.getByText("Changes apply to the next session you start");
  await footnote.scrollIntoViewIfNeeded();
  const panelTop = (await panel(page).boundingBox())!.y;
  expect((await footnote.boundingBox())!.y + 10).toBeLessThan(panelTop);
  await page.screenshot({
    path: "test-results/session-panel-settings.png",
    animations: "disabled",
  });

  // ⌘K (Ctrl+K here) hides it as the key does.
  await page.keyboard.press("Control+k");
  await page.getByRole("combobox", { name: "Go to" }).fill("session panel");
  await page.keyboard.press("Enter");
  await expect(panel(page)).toHaveCount(0);
});

test("from the top, the panel leaves the pull request's bar in view", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Settings", exact: true }).first().click();
  await page.getByRole("button", { name: /^Fixes/ }).click();
  await page.getByRole("button", { name: "Top", exact: true }).click();
  await expect(page.getByRole("button", { name: "Top", pressed: true })).toBeVisible();
  try {
    await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
    await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
    await expect(page.getByRole("heading", { name: "Reuse the existing PR page" })).toBeVisible();
    await page.keyboard.press("`");
    await expect(panel(page)).toBeVisible();
    await expect(panel(page)).toHaveAttribute("data-position", "top");
    const box = (await panel(page).boundingBox())!;
    expect(box.y).toBeLessThan(20);
    // The page starts under it: the pull request's title and its actions
    // stay in view, not under the panel.
    const merge = page.getByRole("button", { name: /^Merge/ }).first();
    await expect(merge).toBeVisible();
    expect((await merge.boundingBox())!.y).toBeGreaterThan(box.y + box.height);
    const title = page.getByRole("heading", { name: "Reuse the existing PR page" });
    expect((await title.boundingBox())!.y).toBeGreaterThan(box.y + box.height);
    await page.screenshot({ path: "test-results/session-panel-top.png", animations: "disabled" });
  } finally {
    await page.evaluate(() =>
      fetch("api/settings/panel", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ position: "bottom" }),
      }),
    );
  }
});

test("a comment the agent proposes joins the pending review only after Save", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await expect(page.getByRole("heading", { name: "Reuse the existing PR page" })).toBeVisible();
  const count = page.locator("[data-review-button] .review-count").first();
  const before = (await count.count()) ? Number(await count.innerText()) : 0;

  // Asked for a comment, the scripted agent calls propose_review_comment:
  // a diff proposal the person saves into their pending review.
  await page.getByRole("button", { name: "Ask", exact: true }).click();
  const composer = panel(page).getByRole("textbox", { name: "Message to the agent" });
  await expect(composer).toBeFocused();
  await composer.fill("Propose a comment about the retry cap");
  await composer.press("Enter");
  const proposal = panel(page).getByRole("article", { name: "Proposal" });
  await expect(proposal).toContainText("Proposes a change");
  await expect(proposal).toContainText("acme/demo#7:review_comment");
  await expect(proposal).toContainText(
    "Cap the retry delay at five minutes, as docs/guide.md says.",
  );
  // Nothing joined the review before Save.
  if (before) await expect(count).toHaveText(String(before));
  else await expect(count).toHaveCount(0);
  await page.screenshot({ path: "test-results/session-panel-comment.png", animations: "disabled" });
  await proposal.getByRole("button", { name: "Save", exact: true }).click();
  await expect(proposal).toHaveCount(0);
  await expect(count).toHaveText(String(before + 1));
});
