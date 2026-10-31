import { readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";

/** Opens Settings from the sidebar, then one of its sections. */
async function openSettings(page: Page, section: string) {
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await page
    .getByRole("navigation", { name: "Settings sections" })
    .getByRole("button", { name: new RegExp(`^${section}`) })
    .click();
}

// The fixture runs the real daemon, which publishes its launch URL in a private
// credential file once it serves; the desktop app reads the same file.
async function launchURL(): Promise<string> {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      const path = join(process.env.BAUM_APP_STATE_DIR ?? "", "credential.json");
      const { url } = JSON.parse(await readFile(path, "utf8"));
      if (typeof url === "string") return url;
    } catch {
      // Not written yet.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("the daemon did not publish its credential");
}

test("daemon bootstrap keeps credentials private and opens saved review versions", async ({
  page,
  browser,
}) => {
  const foreign: string[] = [];
  const local: string[] = [];
  page.on("request", (request) => {
    if (!request.url().startsWith("http://127.0.0.1:4167")) foreign.push(request.url());
    else local.push(new URL(request.url()).pathname);
  });
  const launch = await launchURL();
  await page.goto(launch);
  await expect(page.getByRole("heading", { name: "Up next", exact: true })).toBeVisible();
  expect(page.url()).not.toContain("token");
  const base = new URL(page.url()).pathname;
  expect(base).toMatch(/^\/s\/[A-Za-z0-9_-]{43}\/$/);
  expect((await page.request.get(`${base}api/repos`)).status()).toBe(200);
  expect((await page.request.get("/api/repos")).status()).toBe(401);

  // The same launch credential must bootstrap a fresh desktop window.
  const relaunched = await browser.newPage();
  try {
    await relaunched.goto(launch);
    await expect(relaunched.getByRole("heading", { name: "Up next", exact: true })).toBeVisible();
    expect(relaunched.url()).not.toContain("token");
  } finally {
    await relaunched.close();
  }

  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await expect(page.getByText("Local fixture review version 2", { exact: true })).toBeVisible();
  await page.reload();
  await expect(page.getByText("Local fixture review version 2", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Review version", exact: true }).click();
  await page.getByRole("button", { name: "View v1", exact: true }).click();
  await expect(page.getByText("Local fixture review version 1", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Return to current version", exact: true }).click();
  await expect(page.getByText("Local fixture review version 2", { exact: true })).toBeVisible();
  expect(new URL(page.url()).pathname).toBe(base);
  expect(foreign).toEqual([]);
  expect(
    local.filter(
      (path) => !["/", "/launch.js", "/api/auth/exchange"].includes(path) && !path.startsWith(base),
    ),
  ).toEqual([]);
});

test("automatic review of a new head, shown as started automatically", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  const pr = page.getByRole("link", { name: /An automatically reviewed change/ });
  await expect(pr).toBeVisible();
  await pr.click();
  await expect(page.getByText("No review yet", { exact: true })).toBeVisible();
  // The fixture's discovery reports PR 10's new head once this marker exists.
  await writeFile(join(process.env.BAUM_APP_STATE_DIR ?? "", "push-10"), "");
  const progress = page.getByRole("region", { name: "Review generation", exact: true });
  await expect(
    progress.getByText(/Started automatically because new commits were pushed/),
  ).toBeVisible({ timeout: 15000 });
  await expect(
    page.getByText("Adds notes, settings and a renamed guide.", { exact: true }),
  ).toBeVisible({ timeout: 30000 });
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  const jobs = page.getByRole("region", { name: "Generation jobs", exact: true });
  await expect(
    jobs.getByText("Started automatically because new commits were pushed", { exact: true }),
  ).toHaveCount(1);
  await openSettings(page, "Reviews");
  const automatic = page.getByRole("region", { name: "Reviews", exact: true });
  const toggle = automatic.getByRole("switch", { name: /Review pull requests automatically/ });
  // The switch saves as it changes.
  await toggle.click();
  await expect(automatic.getByRole("status")).toHaveText("Saved");
  await expect(toggle).not.toBeChecked();
  // The section is in the address, so a reload stays on it.
  await page.reload();
  // After a reload the section renders once the settings requests answer,
  // which can take a while on a busy CI runner.
  await expect(automatic).toBeVisible({ timeout: 15000 });
  await expect(toggle).not.toBeChecked();
  await page.getByRole("link", { name: "Repositories", exact: true }).click();
  await page.getByRole("button", { name: "Settings for acme/demo" }).click();
  const repository = page.getByRole("dialog", { name: "Repository acme/demo", exact: true });
  await expect(
    repository.getByText(/Automatic reviews are paused by the global switch/),
  ).toBeVisible();
  await repository.getByRole("button", { name: "Cancel", exact: true }).click();
  await openSettings(page, "Reviews");
  await toggle.click();
  await expect(automatic.getByRole("status")).toHaveText("Saved");
  await expect(toggle).toBeChecked();
  expect(errors).toEqual([]);
});

test("a session's transcript opens from the Sessions page and grows without a reload", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  // The fixture turns the marker's first line into hook events of one Claude
  // Code session and its other lines into the transcript its hooks name.
  const marker = join(process.env.BAUM_APP_STATE_DIR ?? "", "live-transcript");
  await writeFile(marker, "running\nFix the flaky retry test\n");
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Sessions", exact: true }).first().click();
  const runningNow = page.getByRole("region", { name: "Running now", exact: true });
  const title = runningNow.getByRole("link", { name: /^Open transcript/ });
  await expect(title).toBeVisible({ timeout: 10000 });
  // A click on the card's title peeks, as a click anywhere on it does; the
  // transcript opens from Open transcript beside the reply.
  await title.click();
  await expect(page.getByRole("region", { name: /^Preview of/ })).toBeVisible();
  await runningNow.getByRole("link", { name: "Open transcript", exact: true }).first().click();
  // The session has no title of its own yet, so its first prompt names it.
  await expect(
    page.getByRole("heading", { name: "Fix the flaky retry test", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("region", { name: "Session", exact: true })).toContainText("Working");
  const transcript = page.getByRole("region", { name: "Transcript", exact: true });
  // The card's title is the session's first prompt too; the prompts are the
  // conversation's list items below it.
  const prompts = transcript.getByRole("listitem");
  await expect(prompts.getByText("Fix the flaky retry test", { exact: true })).toBeVisible();
  await expect(transcript.getByText("Done: Fix the flaky retry test")).toBeVisible();
  // The agent writes another turn without a hook event: the open view shows
  // it without a reload, and masks the address in it.
  let loads = 0;
  page.on("load", () => loads++);
  await writeFile(
    marker,
    "running\nFix the flaky retry test\nMail ada.lovelace@example.com the summary\n",
  );
  await expect(transcript.getByText(/^Mail .* the summary$/)).toBeVisible({ timeout: 15000 });
  await expect(transcript.getByText("ada.lovelace@example.com")).toHaveCount(0);
  await expect(prompts.getByText("Fix the flaky retry test", { exact: true })).toBeVisible();
  expect(loads).toBe(0);
  // Ended, the view says so; u goes back to the list, which keeps it.
  await writeFile(
    marker,
    "ended\nFix the flaky retry test\nMail ada.lovelace@example.com the summary\n",
  );
  await expect(page.getByRole("region", { name: "Session", exact: true })).toContainText("Ended", {
    timeout: 10000,
  });
  await page.keyboard.press("u");
  await expect(page.getByRole("region", { name: "Ended recently", exact: true })).toBeVisible();
  expect(errors).toEqual([]);
});

// The fake GitHub drops PR 7 from the open list once merged; removing its
// marker afterwards gives it back to the specs that run after this one.
test("merges a ready pull request from the inbox after choosing a method", async ({ page }) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  const inbox = page.getByRole("region", { name: "Pull request inbox" });
  const row = inbox.getByRole("listitem").filter({ hasText: "Reuse the existing PR page" });
  await expect(row).toContainText("Status: Ready");
  // Only a ready pull request offers Merge, from its row's menu.
  const broken = inbox.getByRole("listitem").filter({ hasText: "A pull request without a review" });
  await broken.hover();
  await broken.getByRole("button", { name: "Actions for acme/demo #8", exact: true }).click();
  await expect(page.getByRole("menuitem", { name: "Merge…", exact: true })).toHaveCount(0);
  await page.keyboard.press("Escape");
  await row.hover();
  await row.getByRole("button", { name: "Actions for acme/demo #7", exact: true }).click();
  await page.getByRole("menuitem", { name: "Merge…", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Merge pull request #7?" });
  // The fixture repository allows merge commits and squashing, not rebasing.
  await expect(dialog.getByRole("radio")).toHaveCount(2);
  await expect(dialog.getByRole("radio", { name: /Squash and merge/ })).toBeChecked();
  await dialog.getByRole("radio", { name: /Create a merge commit/ }).check();
  await dialog.getByRole("button", { name: "Merge pull request", exact: true }).click();
  await expect(dialog).toBeHidden();
  // The inbox refreshed: the merged pull request is no longer open.
  await expect(row).toHaveCount(0);
  await rm(join(process.env.BAUM_APP_STATE_DIR ?? "", "merged-7"), { force: true });
});

test("walks through a review, comments on a behaviour and submits the review", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  // A render failure is caught by the page's boundary and only logged.
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("Failed to load resource"))
      errors.push(m.text());
  });
  const submitted: unknown[] = [];
  // The fixture daemon has no GitHub to post to; the request is the evidence.
  await page.route("**/api/prs/review", async (route) => {
    submitted.push(route.request().postDataJSON());
    await route.fulfill({
      json: { url: "https://github.com/acme/demo/pull/7#pullrequestreview-1" },
    });
  });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await page.getByRole("button", { name: "Walk through", exact: true }).click();
  await expect(page).toHaveURL(/walk=landing/);
  const walk = page.getByRole("region", { name: "Walkthrough" });
  await expect(walk.getByText("The tour · riskiest first")).toBeVisible();
  await walk.getByRole("button", { name: "Start the review", exact: true }).click();
  await expect(page).toHaveURL(/walk=tour/);
  await expect(walk.getByRole("article").getByRole("heading", { level: 2 })).toHaveText(
    "Operator-only dead-letter endpoints",
  );
  await walk.getByRole("button", { name: /Something looks off/ }).click();
  await page.keyboard.type("Trusts a client-supplied header");
  await walk.getByRole("button", { name: "Save comment", exact: true }).click();
  await expect(walk.getByText("1 comment", { exact: true })).toBeVisible();
  // A reload keeps the walkthrough, what was looked at and the comment.
  await page.reload();
  await expect(walk.getByText("1 comment", { exact: true })).toBeVisible();
  await expect(page).toHaveURL(/walk=tour/);
  await walk.getByRole("button", { name: "Send review", exact: true }).click();
  await walk.getByRole("radio", { name: /Request changes/ }).click();
  await walk.getByRole("button", { name: "Request changes", exact: true }).click();
  await expect(walk.getByText("Your review is on GitHub.")).toBeVisible();
  expect(submitted).toEqual([
    expect.objectContaining({
      repository: "acme/demo",
      pr: 7,
      event: "REQUEST_CHANGES",
      comments: [
        expect.objectContaining({ body: "Trusts a client-supplied header", side: "RIGHT" }),
      ],
    }),
  ]);
  // Narrow: no sideways scroll.
  await page.setViewportSize({ width: 390, height: 844 });
  await walk.getByRole("button", { name: /^Retry policy and backoff,/ }).click();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  await walk.getByRole("button", { name: "Full review", exact: true }).click();
  await expect(page).not.toHaveURL(/walk=/);
  expect(errors).toEqual([]);
});

test("asks the review's agent a question and reads the lines its answer cites", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  page.on("console", (m) => {
    if (m.type() === "error" && !m.text().includes("Failed to load resource"))
      errors.push(m.text());
  });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await expect(page.getByRole("button", { name: "Ask", exact: true })).toBeVisible();
  // The key opens the panel, with the cursor in its box.
  await page.keyboard.press("a");
  const panel = page.getByRole("complementary", { name: "Ask about this review" });
  await expect(panel).toBeVisible();
  expect(page.url()).toContain("ask=1");
  await expect(panel.getByText("answers questions about the code")).toBeVisible();
  await page.keyboard.type("Why is the delay capped?");
  await page.keyboard.press("Enter");
  await expect(panel.getByLabel("Question 1").getByText("Why is the delay capped?")).toBeVisible();
  await expect(panel.getByText("so a long outage cannot push retries out forever")).toBeVisible();
  await expect(panel.locator("strong", { hasText: "capped" })).toBeVisible();
  // Started from a saved review whose agent session was not kept.
  await expect(panel.getByText("Started without the review's own context.")).toBeVisible();
  // The citation opens the lines it names, from the review's own hunk.
  await panel.getByRole("button", { name: "docs/guide.md:3–6" }).click();
  await expect(panel.getByText("A job that fails more often than its retry budget")).toBeVisible();
  // A reload keeps the panel open and the conversation.
  await page.reload();
  await expect(panel.getByLabel("Question 1").getByText("Why is the delay capped?")).toBeVisible();
  // Asking about a section puts it above the box, and a second question
  // goes to the same conversation.
  await page
    .getByRole("navigation", { name: "Review sections" })
    .getByRole("button", { name: "Retry policy and backoff" })
    .click();
  await page.getByRole("button", { name: "Ask about this section", exact: true }).click();
  await expect(panel.locator(".ask-focus")).toContainText("About");
  await page.keyboard.type("Is this safe on retry?");
  await page.keyboard.press("Enter");
  await expect(panel.getByText("Is this safe on retry?")).toBeVisible();
  await expect(panel.locator(".ask-answer")).toHaveCount(2);
  // Escape in the panel closes it.
  await panel.locator("textarea").focus();
  await page.keyboard.press("Escape");
  await expect(panel).toBeHidden();
  expect(page.url()).not.toContain("ask=");
  expect(errors).toEqual([]);
});
