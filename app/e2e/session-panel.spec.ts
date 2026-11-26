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
  await page.keyboard.press("Control+`");
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
  // It grows with a longer message, its text never spilling out of it, and
  // shrinks back once it is short again.
  const oneLine = (await composer.boundingBox())!.height;
  await composer.fill("first line\nsecond line\nthird line");
  await expect
    .poll(async () => (await composer.boundingBox())!.height)
    .toBeGreaterThan(oneLine * 2);
  expect(await composer.evaluate((box) => box.scrollHeight <= box.clientHeight)).toBe(true);
  await composer.fill("Why is the retry delay capped?");
  await expect.poll(async () => (await composer.boundingBox())!.height).toBe(oneLine);
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

  // The key in the composer hides it, and brings it back on the same
  // session.
  await composer.press("Control+`");
  await expect(panel(page)).toHaveCount(0);
  await page.keyboard.press("Control+`");
  await expect(
    panel(page).getByRole("heading", { name: "Why is the retry delay capped?" }),
  ).toBeVisible();

  // From the composer, the first Escape gives the keys to the panel and
  // the second gives them back to the page, the panel staying open; the pull
  // request's page under it gets neither, so it does not go back to its
  // list. The key takes them back to the composer, where it hides the panel.
  await composer.fill("A half-written question");
  await composer.press("Escape");
  await expect(panel(page)).toBeVisible();
  await expect(panel(page)).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(panel(page)).toBeVisible();
  await expect(panel(page)).not.toBeFocused();
  await expect(page.getByRole("heading", { name: "Reuse the existing PR page" })).toBeVisible();
  await page.keyboard.press("Control+`");
  await expect(composer).toBeFocused();
  await composer.press("Control+`");
  await expect(panel(page)).toHaveCount(0);
  await page.keyboard.press("Control+`");
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
    await page.keyboard.press("Control+`");
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

test("a repository's prompt the agent proposes from a review is saved from the review", async ({
  page,
}) => {
  test.setTimeout(90000);
  await page.setViewportSize({ width: 1440, height: 1000 });
  const id = "review-v2-shared";
  const line = "Flag every database migration that can lose data, and say how to roll it back.";
  try {
    await page.goto(await launchURL());
    await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
    await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
    await expect(page.getByRole("heading", { name: "Reuse the existing PR page" })).toBeVisible();

    // Asked while reading the review, the scripted agent calls propose_prompt
    // for the pull request's repository: no Settings or repository page.
    await page.getByRole("button", { name: "Ask", exact: true }).click();
    const composer = panel(page).getByRole("textbox", { name: "Message to the agent" });
    await expect(composer).toBeFocused();
    await composer.fill("Change this repository's review prompt to flag risky migrations");
    await composer.press("Enter");
    const proposal = panel(page).getByRole("article", { name: "Proposal" });
    await expect(proposal.locator(".session-panel-proposal-where")).toHaveText(
      `acme/demo's prompt · ${id}`,
    );
    await expect(proposal).toContainText(
      "Gives acme/demo its own prompt in place of the text it inherits.",
    );
    // Against the text in use: the one line added, not the whole prompt.
    await expect(proposal.locator('.prompt-diff span[data-kind="add"]')).toContainText([line]);
    await expect(proposal).not.toContainText("open it to save");
    await proposal.scrollIntoViewIfNeeded();
    await page.screenshot({
      path: "test-results/session-panel-prompt.png",
      animations: "disabled",
    });
    await proposal.screenshot({
      path: "test-results/session-panel-prompt-card.png",
      animations: "disabled",
    });
    await proposal.getByRole("button", { name: "Save", exact: true }).click();
    await expect(proposal).toHaveCount(0);

    // The repository has its own prompt now; the global one is unchanged.
    const prompts = await page.evaluate(async () => {
      const read = async (path: string) =>
        (
          (await (await fetch(new URL(path, location.href))).json()) as {
            prompts: { id: string; custom_text: string | null }[];
          }
        ).prompts;
      return {
        own: await read("api/repositories/acme/demo/prompts"),
        global: await read("api/settings/prompts"),
      };
    });
    expect(prompts.own.find((p) => p.id === id)?.custom_text).toContain(line);
    expect(prompts.global.find((p) => p.id === id)?.custom_text ?? null).toBeNull();
  } finally {
    // Leave the fixture as it was, whatever failed above.
    await page.evaluate(async (prompt) => {
      await fetch(new URL(`api/repositories/acme/demo/prompts/${prompt}`, location.href), {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text: null }),
      });
    }, id);
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

test("a menu opened beside the panel draws over it, and the panel stays open", async ({ page }) => {
  await page.setViewportSize({ width: 1100, height: 800 });
  await page.goto(await launchURL());
  const rows = page.locator(".upnext-list button.upnext-row");
  await expect(rows.first()).toBeVisible({ timeout: 15000 });
  await page.evaluate(() =>
    fetch("api/settings/panel", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ position: "right" }),
    }),
  );
  try {
    await page.reload();
    await expect(rows.first()).toBeVisible({ timeout: 15000 });
    await page.keyboard.press("Control+`");
    await expect(panel(page)).toHaveAttribute("data-position", "right");
    await page.locator("summary.filter-menu-button").click();
    const menu = page.locator(".filter-menu-panel");
    await expect(page.getByRole("combobox", { name: "Add filter", exact: true })).toBeFocused();
    const sheet = (await panel(page).boundingBox())!;
    const box = (await menu.boundingBox())!;
    // Where the menu and the panel overlap, the menu is in front.
    const left = Math.max(sheet.x, box.x);
    const right = Math.min(sheet.x + sheet.width, box.x + box.width);
    const top = Math.max(sheet.y, box.y);
    const bottom = Math.min(sheet.y + sheet.height, box.y + box.height);
    expect(right).toBeGreaterThan(left);
    expect(bottom).toBeGreaterThan(top);
    const front = await page.evaluate(
      ([x, y]) => !!document.elementFromPoint(x!, y!)?.closest(".filter-menu-panel"),
      [(left + right) / 2, (top + bottom) / 2],
    );
    expect(front).toBe(true);
    await page.screenshot({ path: "test-results/menu-over-session-panel.png" });
    await page.keyboard.press("Escape");
    await expect(menu).toHaveCount(0);
    await expect(panel(page)).toBeVisible();
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
