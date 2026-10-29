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

test("local inbox, reused PR viewer, versions, generation and settings", async ({ page }) => {
  // The longest story here waits on several real generations: about 10 s
  // locally, close to the default 30 s on a busy CI runner.
  test.slow();
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const evidence: string[] = [];
  const commits = new Map<string, number>();
  page.on("response", (response) => {
    if (new URL(response.url()).pathname.endsWith("/api/sessions"))
      // A reload can discard a body before it is read; that answer is not evidence.
      void response
        .json()
        .then((body) => {
          if (body.available)
            commits.set(
              new URL(response.url()).searchParams.get("version") ?? "current",
              body.commits.length,
            );
        })
        .catch(() => undefined);
  });
  page.on("request", (r) => {
    if (r.url().includes("/api/sessions?")) evidence.push(r.url());
  });
  const foreign: string[] = [];
  const local: string[] = [];
  page.on("request", (r) => {
    if (!r.url().startsWith("http://127.0.0.1:4167")) foreign.push(r.url());
    else local.push(new URL(r.url()).pathname);
  });
  const launch = await launchURL();
  await page.goto(launch);
  // The app opens on the Inbox; the pull request list is one click away.
  await expect(page.getByRole("heading", { name: "Up next", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Pull requests", exact: true })).toBeVisible();
  expect(page.url()).not.toContain("token");
  // The app is relaunched against a long-lived daemon: the same credential
  // authorizes another browser session.
  const relaunched = await page.context().browser()?.newPage();
  if (relaunched) {
    await relaunched.goto(launch);
    await expect(relaunched.getByRole("heading", { name: "Up next", exact: true })).toBeVisible();
    // The desktop app's first run opens the diagnostics page; the bootstrap
    // carries only that page over, and the address keeps no token.
    await relaunched.goto(`${launch}&page=diagnostics`);
    await expect(relaunched.getByRole("heading", { name: "Check this computer" })).toBeVisible();
    expect(new URL(relaunched.url()).search).toBe("?page=diagnostics");
    expect(relaunched.url()).not.toContain("token");
    await relaunched.close();
  }
  // The bootstrap replaces itself with the secret session namespace; the app,
  // its assets, API and event stream all live under it.
  const base = new URL(page.url()).pathname;
  expect(base).toMatch(/^\/s\/[A-Za-z0-9_-]{43}\/$/);
  expect((await page.request.get(`${base}api/repos`)).status()).toBe(200);
  expect((await page.request.get("/api/repos")).status()).toBe(401);
  expect((await page.request.get("/s/wrong/api/repos")).status()).toBe(404);
  await expect(page.getByRole("link", { name: /Reuse the existing PR page/ })).toBeVisible();
  expect(errors).toEqual([]);
  const inbox = page.getByRole("region", { name: "Pull request inbox" });
  const row = (title: string) => inbox.getByRole("listitem").filter({ hasText: title });
  const approved = row("Reuse the existing PR page");
  await expect(approved).toContainText("Review time: ~12 min");
  await expect(approved).toContainText("Review: Approved");
  await expect(approved).toContainText("Status: Ready");
  await expect(approved.locator(".inbox-threads")).toHaveCount(0);
  const changes = row("A pull request without a review");
  await expect(changes).toContainText("Review time: not estimated");
  await expect(changes).toContainText("Review: Changes requested3 unresolved");
  await expect(changes).toContainText("Status: Conflict");
  const unreviewed = row("An automatically reviewed change");
  await expect(unreviewed).toContainText("Review: Unreviewed");
  await expect(unreviewed).toContainText("Status: Draft");
  // The Inbox holds what is the person's to check; the rest is under Others.
  await expect(row("A fork requiring confirmation")).toHaveCount(0);
  await expect(row("Other repository change")).toHaveCount(0);
  await expect(inbox).not.toContainText(/Generated|Cost|Stale discovery/);
  await page.screenshot({
    path: "test-results/inbox-wide.png",
    fullPage: false,
    animations: "disabled",
  });
  // Peek opens the saved review under its row.
  await approved.hover();
  await approved.getByRole("button", { name: "Peek at acme/demo #7", exact: true }).click();
  const peek = approved.getByRole("region", { name: "Peek at #7" });
  await expect(peek).toContainText("Summary");
  await expect(peek.getByRole("link", { name: "Open the review", exact: true })).toBeVisible();
  // Selecting rows draws their actions in a bar over the list.
  await page.getByRole("checkbox", { name: "Select acme/demo #7", exact: true }).check();
  await page.getByRole("checkbox", { name: "Select acme/demo #8", exact: true }).check();
  const bar = page.getByRole("toolbar", { name: "Selected pull requests" });
  await expect(bar).toContainText("2 selected");
  await expect(bar.getByRole("button", { name: "Merge 1", exact: true })).toBeVisible();
  await page.screenshot({
    path: "test-results/inbox-peek.png",
    fullPage: false,
    animations: "disabled",
  });
  await bar.getByRole("button", { name: "Clear", exact: true }).click();
  await expect(bar).toHaveCount(0);
  await approved.getByRole("button", { name: "Peek at acme/demo #7", exact: true }).click();
  await expect(peek).toHaveCount(0);
  await page.setViewportSize({ width: 720, height: 900 });
  await expect(approved.getByText("Status:", { exact: true })).toBeVisible();
  await expect(
    approved.locator(".inbox-status").filter({ hasText: "Status: Ready" }),
  ).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(720);
  await page.screenshot({ path: "test-results/inbox-desktop-minimum.png", animations: "disabled" });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole("region", { name: "Pull request inbox" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: "test-results/inbox-narrow.png",
    fullPage: false,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 1280, height: 900 });
  const requested = page.getByRole("button", { name: /Review requested/ });
  await requested.click();
  await expect(requested).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByRole("link", { name: /Reuse the existing/ })).toHaveCount(0);
  await requested.click();
  const views = page.getByRole("group", { name: "Show" });
  await views.getByRole("button", { name: /^Mine/ }).click();
  await expect(page.getByRole("link", { name: /without a review/ })).toBeVisible();
  await expect(page.getByRole("link", { name: /Reuse the existing/ })).toHaveCount(0);
  await views.getByRole("button", { name: /^Others/ }).click();
  const fork = row("A fork requiring confirmation");
  await expect(fork).toContainText("Review: Commented6+ unresolved");
  await expect(fork.locator('[title*="only the first 100"]')).toBeVisible();
  await expect(fork).toContainText("Status: Checks running");
  const unknown = row("Other repository change");
  await expect(unknown.locator(".inbox-status")).toHaveCount(3);
  await expect(unknown.locator(".inbox-status-empty")).toHaveCount(3);
  await expect(unknown).not.toContainText("—");
  await page.screenshot({ path: "test-results/inbox-others.png", animations: "disabled" });
  await page.getByRole("checkbox", { name: "Select acme/demo #9", exact: true }).check();
  await page.getByRole("button", { name: "Generate reviews (1)", exact: true }).click();
  // Only a fork asks before generating, in a batch as on its own page.
  await expect(page.getByRole("dialog")).toContainText("Review code from outside your team?");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("checkbox", { name: "Select acme/demo #9", exact: true }).uncheck();
  await page.getByRole("link", { name: /A fork requiring confirmation/ }).click();
  await page.getByRole("button", { name: "Generate review", exact: true }).first().click();
  await expect(page.getByRole("dialog")).toContainText("Review code from outside your team?");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await expect(page.getByText("Local fixture review version 2", { exact: true })).toBeVisible();
  expect(new URL(page.url()).pathname).toBe(base);
  // A deep link reloads into the same PR page under the namespace.
  await page.reload();
  await expect(page.getByText("Local fixture review version 2", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Review version", exact: true }).click();
  await page.getByRole("button", { name: "View v1", exact: true }).click();
  await expect(page.getByText("Local fixture review version 1", { exact: true })).toBeVisible();
  await expect
    .poll(() => evidence.some((url) => new URL(url).searchParams.get("version") === "1"))
    .toBe(true);
  await expect.poll(() => commits.get("1")).toBe(0);
  await page.getByRole("button", { name: "Return to current version", exact: true }).click();
  await expect(page.getByText("Local fixture review version 2", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Docs and dependency bump", exact: true }).click();
  await page.getByRole("button", { name: "Review version", exact: true }).click();
  await page.getByRole("checkbox", { name: "Compare v1", exact: true }).check();
  await expect(page.locator('img[src*="/api/attachments/"]')).toHaveCount(2);
  const imageSources = await page
    .locator('img[src*="/api/attachments/"]')
    .evaluateAll((images) => images.map((img) => (img as HTMLImageElement).src));
  expect(imageSources.some((url) => new URL(url).searchParams.get("version") === "1")).toBe(true);
  expect(imageSources.some((url) => new URL(url).searchParams.get("version") === "2")).toBe(true);
  await page.getByRole("button", { name: "Close history comparison", exact: true }).click();
  const summary = page
    .getByRole("navigation", { name: "Review sections" })
    .getByRole("button", { name: "Summary", exact: true });
  await summary.click();
  await expect(summary).toHaveAttribute("aria-pressed", "true");
  await expect(
    page.locator("#review-section").getByRole("heading", { name: "Summary", exact: true }),
  ).toBeVisible();
  await expect(page.locator("#review-section .animate-pane-in")).toHaveCSS("opacity", "1");
  await page.screenshot({
    path: "test-results/review-wide.png",
    fullPage: false,
    animations: "disabled",
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: "test-results/review-narrow.png",
    fullPage: false,
    animations: "disabled",
  });

  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /A pull request without a review/ }).click();
  // Generate starts the review at once: no estimate, no confirmation.
  await page.getByRole("button", { name: "Generate review", exact: true }).first().click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  const progress = page.getByRole("region", { name: "Review generation", exact: true });
  await expect(progress).toBeVisible();
  await expect(page.getByText("No review yet", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Generate review", exact: true })).toHaveCount(0);
  await expect(progress.getByRole("button", { name: "Cancel job", exact: true })).toBeVisible();
  await page.screenshot({
    path: "test-results/review-generating.png",
    fullPage: false,
    animations: "disabled",
  });
  await expect(
    page.getByText("Adds notes, settings and a renamed guide.", { exact: true }),
  ).toBeVisible({ timeout: 30000 });
  // The review links to the job that generated it, where its usage is.
  await page.getByRole("link", { name: "View job", exact: true }).click();
  const job = page.getByRole("region", { name: "Generation jobs" }).getByRole("listitem").first();
  await expect(job.getByText("Completed")).toBeVisible();
  await expect(job.getByText("1 call", { exact: true })).toBeVisible();
  await page.screenshot({ path: "test-results/jobs.png", animations: "disabled" });
  await job.getByText("Usage", { exact: true }).click();
  await expect(
    page.getByText(
      /Provider-reported usage: Input 60 · Output 40 · Cache reads 900 · Cache creation 0/,
    ),
  ).toBeVisible();
  await expect(page.getByText(/Total processed tokens 1,000/)).toBeVisible();
  await expect(page.getByText(/Charged-token budget counter 100/)).toBeVisible();
  await expect(page.getByText(/provider did not report its pricing speed/)).toBeVisible();
  await page.screenshot({ path: "test-results/usage-wide.png", animations: "disabled" });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: "test-results/usage-narrow.png", animations: "disabled" });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /A pull request without a review/ }).click();
  await page.getByRole("button", { name: "Regenerate review", exact: true }).click();
  await expect(
    page.getByText("Adds notes, settings and a renamed guide.", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: /Generating review|Review queued/ }),
  ).toBeDisabled();
  await page.getByRole("button", { name: "Cancel job", exact: true }).click();
  await expect(page.getByRole("button", { name: "Regenerate review", exact: true })).toBeEnabled();
  await expect(page.getByText("Review generation was cancelled", { exact: true })).toBeVisible();
  await page.screenshot({ path: "test-results/review-cancelled.png", animations: "disabled" });
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  await expect(page.getByText(/cancelled/i).first()).toBeVisible();
  await page.screenshot({ path: "test-results/jobs-wide.png", animations: "disabled" });
  await page.setViewportSize({ width: 720, height: 900 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(720);
  await page.screenshot({ path: "test-results/jobs-narrow.png", animations: "disabled" });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.getByRole("link", { name: "Repositories", exact: true }).click();
  await page.getByRole("button", { name: "Settings for acme/demo" }).click();
  const settings = page.getByRole("dialog", { name: "Repository acme/demo", exact: true });
  // Automatic reviews are on by default: the repository follows the global
  // setting.
  await expect(settings.getByRole("combobox", { name: /Automatic reviews/ })).toHaveValue(
    "default",
  );
  // The repository picks its model with the dropdown the desktop setup uses.
  await settings
    .getByRole("combobox", { name: "Model for this repository" })
    .selectOption("Custom…");
  await settings.getByRole("textbox", { name: "Custom model" }).fill("claude-sonnet-4-5");
  await page.screenshot({ path: "test-results/repository-dialog.png", animations: "disabled" });
  await settings.getByRole("button", { name: "Save repository settings" }).click();
  await expect(settings).toBeHidden();
  await expect(page.getByText(/claude-sonnet-4-5/)).toBeVisible();
  await page.screenshot({ path: "test-results/repositories.png", animations: "disabled" });
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await expect(
    page.getByRole("region", { name: /things? needs? you|Everything is working/ }),
  ).toBeVisible();
  await page.screenshot({ path: "test-results/settings-overview.png", animations: "disabled" });
  await openSettings(page, "Reviews");
  await expect(
    page
      .getByRole("region", { name: "Reviews", exact: true })
      .getByRole("switch", { name: /Review pull requests automatically/ }),
  ).toBeChecked();
  await page.screenshot({ path: "test-results/settings.png", animations: "disabled" });
  await openSettings(page, "Fixes");
  await expect(
    page.getByRole("region", { name: "Fixes", exact: true }).getByLabel("Agent that fixes"),
  ).toBeVisible();
  await page.screenshot({ path: "test-results/settings-fixes.png", animations: "disabled" });
  await openSettings(page, "This computer");
  await page.getByRole("button", { name: "Check this computer", exact: true }).click();
  const diagnostics = page
    .getByRole("dialog")
    .getByRole("region", { name: "Diagnostics", exact: true });
  await expect(diagnostics.getByText(/Overall: Problem/)).toBeVisible();
  const problem = diagnostics.getByRole("listitem").filter({ hasText: "GitHub token" });
  await expect(problem.getByText("Problem", { exact: true })).toBeVisible();
  await expect(
    problem.getByText(/Connect GitHub in baum Settings, or set github_token/),
  ).toBeVisible();
  await expect(
    diagnostics.getByRole("listitem").filter({ hasText: "Model backend" }).getByText("Warning"),
  ).toBeVisible();
  await expect(
    diagnostics.getByRole("listitem").filter({ hasText: "git is available" }).getByText("OK"),
  ).toBeVisible();
  await page.screenshot({ path: "test-results/diagnostics-wide.png", animations: "disabled" });
  const again = page.waitForRequest((r) => new URL(r.url()).pathname === base + "api/diagnostics");
  await diagnostics.getByRole("button", { name: "Check again" }).click();
  await again;
  await expect(diagnostics.getByRole("button", { name: "Check again" })).toBeEnabled();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(problem.getByText(/for-narrow-layouts/)).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await diagnostics.scrollIntoViewIfNeeded();
  await page.screenshot({ path: "test-results/diagnostics-narrow.png", animations: "disabled" });
  expect(errors).toEqual([]);
  expect(foreign).toEqual([]);
  expect(
    local.filter(
      (path) => !["/", "/launch.js", "/api/auth/exchange"].includes(path) && !path.startsWith(base),
    ),
  ).toEqual([]);
});

// The root is the Inbox: what waits on the person, grouped beside the
// selected pull request's panel, with today's progress. j and a click move
// the selection, and the panel follows it.
test("the Inbox lists what is next beside the selected pull request", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
  await expect(page.getByRole("heading", { name: "Up next", exact: true })).toBeVisible();
  const panel = page.locator("article.upnext-panel");
  await expect(panel).toBeVisible({ timeout: 15000 });
  const title = panel.locator("h2.upnext-title");
  await expect(title).not.toHaveText("");
  await expect(page.getByRole("progressbar", { name: "Cleared today" })).toBeVisible();
  const rows = page.locator("button.upnext-row");
  expect(await rows.count()).toBeGreaterThan(1);
  await expect(rows.first()).toHaveAttribute("aria-current", "true");
  await page.screenshot({
    path: "test-results/up-next-wide.png",
    fullPage: false,
    animations: "disabled",
  });
  const first = (await title.textContent()) ?? "";
  await page.keyboard.press("j");
  await expect(rows.nth(1)).toHaveAttribute("aria-current", "true");
  await expect(title).not.toHaveText(first);
  await rows.first().click();
  await expect(title).toHaveText(first);
  await page.setViewportSize({ width: 720, height: 900 });
  await expect(panel).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(720);
  await page.screenshot({
    path: "test-results/up-next-desktop-minimum.png",
    animations: "disabled",
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(rows.first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.screenshot({
    path: "test-results/up-next-narrow.png",
    fullPage: false,
    animations: "disabled",
  });
  expect(errors).toEqual([]);
});

// The daemon generates a review by itself when a pull request the user wrote
// gets a new head after automatic generation was enabled, and says so on the
// PR page and in the jobs list. Turning it off is a setting the server keeps.
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
  await page.screenshot({ path: "test-results/automatic-review.png", animations: "disabled" });
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
  await page.screenshot({ path: "test-results/automatic-settings.png", animations: "disabled" });
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

test("a running agent session shows in the inbox and on the PR page until it ends", async ({
  page,
}) => {
  const marker = join(process.env.BAUM_APP_STATE_DIR ?? "", "live-session");
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  const row = page.getByRole("listitem").filter({ hasText: "Reuse the existing PR page" });
  // The first inbox load can take a while on a busy CI runner.
  await expect(row).toBeVisible({ timeout: 15000 });
  await expect(row.getByText("Agent running")).toHaveCount(0);
  // The fixture turns the marker into agent hook events of one session.
  await writeFile(marker, "running");
  await expect(row.getByText("Agent running")).toBeVisible({ timeout: 10000 });
  await page.screenshot({ path: "test-results/live-inbox.png", animations: "disabled" });
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  const live = page.getByRole("region", { name: "Sessions in progress", exact: true });
  await expect(live.getByText("Claude Code")).toBeVisible();
  await expect(live.getByText("Working")).toBeVisible();
  await expect(live.getByText("feature")).toBeVisible();
  await page.screenshot({ path: "test-results/live-pr.png", animations: "disabled" });
  await writeFile(marker, "idle");
  // A stop ends the agent's turn; the card says so, as Needs you does.
  await expect(live.getByText("Finished its turn")).toBeVisible({ timeout: 10000 });
  await writeFile(marker, "ended");
  await expect(live).toHaveCount(0, { timeout: 10000 });
  // The Sessions page keeps it under "Ended recently" and has none running.
  await page.getByRole("link", { name: "Sessions", exact: true }).first().click();
  const ended = page.getByRole("region", { name: "Ended recently", exact: true });
  await expect(ended.getByText("Claude Code")).toBeVisible();
  // Its title leads the compact row.
  await expect(
    ended.getByText("Reuse the PR page in the local app", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Running now", exact: true }).getByText("Claude Code"),
  ).toHaveCount(0);
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await expect(row).toBeVisible();
  await expect(row.getByText("Agent")).toHaveCount(0);
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
  const open = page
    .getByRole("region", { name: "Running now", exact: true })
    .getByRole("link", { name: /^Open transcript/ });
  await expect(open).toBeVisible({ timeout: 10000 });
  await open.click();
  // The session has no title of its own yet, so its first prompt names it.
  await expect(
    page.getByRole("heading", { name: "Fix the flaky retry test", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("region", { name: "Session", exact: true })).toContainText("Working");
  await expect(
    page.getByText("Local transcript; secrets and e-mail addresses masked."),
  ).toBeVisible();
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

test("a session asking for a permission shows in Needs you and leaves once it works again", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  // The fixture turns the marker into hook events of one Claude Code
  // session; "permission" is a permission prompt notification.
  const marker = join(process.env.BAUM_APP_STATE_DIR ?? "", "live-needs");
  await writeFile(marker, "running");
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Sessions", exact: true }).first().click();
  const running = page.getByRole("region", { name: "Running now", exact: true });
  const needs = page.getByRole("region", { name: "Needs you", exact: true });
  await expect(running.locator('li[title="needs-e2e"]')).toBeVisible({ timeout: 10000 });
  await expect(needs).toHaveCount(0);
  await writeFile(marker, "permission");
  const row = needs.locator('li[title="needs-e2e"]');
  await expect(row).toBeVisible({ timeout: 10000 });
  await expect(row.getByText("Asking permission")).toBeVisible();
  await expect(row.getByText("Claude needs your permission to use Bash")).toBeVisible();
  // The title Claude Code gave the session leads the row.
  await expect(row.getByText("Run the migration tests")).toBeVisible();
  await expect(row.getByText(/^waiting \d+s$/)).toBeVisible();
  await expect(row.getByText("feature")).toBeVisible();
  await expect(running.locator('li[title="needs-e2e"]')).toHaveCount(0);
  // The sidebar counts it, in the caution tone.
  const badge = page.locator('a[href$="?page=sessions"] .local-rail-badge.caution');
  await expect(badge).toHaveText("1");
  // Its transcript opens from the row.
  await row.getByRole("link", { name: /^Open transcript/ }).click();
  await expect(page).toHaveURL(/[?&]session=claude-code%3Aneeds-e2e/);
  await page.keyboard.press("u");
  // Given the permission, the agent works again: the row goes back.
  await writeFile(marker, "running");
  await expect(needs).toHaveCount(0, { timeout: 10000 });
  await expect(running.locator('li[title="needs-e2e"]')).toBeVisible();
  await expect(page.locator('a[href$="?page=sessions"] .local-rail-badge.caution')).toHaveCount(0);
  await writeFile(marker, "ended");
  await expect(running.locator('li[title="needs-e2e"]')).toHaveCount(0, { timeout: 10000 });
  expect(errors).toEqual([]);
});

test("desktop Settings has CLI and hook cards in their sections at desktop and narrow widths", async ({
  page,
}, testInfo) => {
  await page.addInitScript(() => {
    Object.defineProperty(window, "__TAURI_INTERNALS__", {
      value: {
        invoke: async () => ({
          first_run: false,
          home: "/home/user",
          command: {
            installed: true,
            record: {},
            found: { path: "/home/user/.local/bin/baum", version: "1.0" },
          },
          hooks: {
            agents: ["claude-code", "codex"].map((agent) => ({
              agent,
              name: agent === "codex" ? "Codex" : "Claude Code",
              detected: true,
              installed: true,
              enabled: true,
            })),
          },
        }),
      },
    });
  });
  await page.goto(await launchURL());
  const cli = page.getByRole("region", { name: "Command line", exact: true });
  const hooks = page.getByRole("region", { name: "Agent hooks", exact: true });
  const fits = () => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth);
  await openSettings(page, "Connections");
  await expect(hooks.getByText("Connected", { exact: true })).toHaveCount(2);
  await page.screenshot({ path: testInfo.outputPath("settings-connections.png"), fullPage: true });
  await openSettings(page, "This computer");
  await expect(cli.getByText("baum is installed at ~/.local/bin/baum.")).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("settings-desktop.png"), fullPage: true });
  await page.setViewportSize({ width: 600, height: 900 });
  await expect(cli).toBeVisible();
  expect(await fits()).toBe(true);
  await page.screenshot({ path: testInfo.outputPath("settings-narrow.png"), fullPage: true });
  await openSettings(page, "Reviews");
  await expect(page.getByLabel("Agent that writes reviews")).toBeVisible();
  expect(await fits()).toBe(true);
  await page.screenshot({
    path: testInfo.outputPath("settings-reviews-narrow.png"),
    fullPage: true,
  });
  await openSettings(page, "Connections");
  await expect(hooks).toBeVisible();
  expect(await fits()).toBe(true);
});

test("picks GitHub repositories and keeps clone-less PRs usable", async ({ page }) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  const missing = page.getByRole("link", {
    name: "A GitHub pull request waiting for a clone",
    exact: true,
  });
  await expect(missing).toBeVisible();
  await expect(missing.locator("xpath=ancestor::li")).toContainText("Needs a local clone");
  await missing.click();
  await expect(
    page.getByRole("heading", { name: "Needs a local clone", exact: true }),
  ).toBeVisible();
  expect(new URL(page.url()).searchParams.get("repository")).toBe("acme/missing");
  await page.reload();
  await expect(page.getByRole("button", { name: "Choose folder", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Generate review", exact: true })).toHaveCount(0);
  await page.screenshot({ path: "test-results/needs-clone-wide.png", animations: "disabled" });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.screenshot({ path: "test-results/needs-clone-narrow.png", animations: "disabled" });
  await page.getByRole("button", { name: "Choose folder", exact: true }).click();
  await page.getByRole("textbox", { name: "Local clone path", exact: true }).fill("/not/a/clone");
  await page.getByRole("button", { name: "Use folder", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("cannot register path");
  await page.getByRole("link", { name: "Repositories", exact: true }).click();
  await page.getByRole("button", { name: "Add repositories", exact: true }).click();
  const picker = page.getByRole("region", { name: "Choose GitHub repositories" });
  await expect(picker.getByRole("checkbox", { name: "Pick acme/demo", exact: true })).toBeChecked();
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.screenshot({ path: "test-results/repositories-wide.png", animations: "disabled" });
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  expect(
    await picker.evaluate((element) => element.getBoundingClientRect().right),
  ).toBeLessThanOrEqual(390);
  expect(
    await picker
      .locator("input[type=search]")
      .evaluate((element) => element.getBoundingClientRect().right),
  ).toBeLessThanOrEqual(390);
  await page.screenshot({ path: "test-results/repositories-narrow.png", animations: "disabled" });
  await picker.getByRole("searchbox", { name: "Search repositories" }).fill("clone-me");
  await picker.getByRole("checkbox", { name: "Pick acme/clone-me", exact: true }).click();
  await expect(
    picker.getByRole("checkbox", { name: "Pick acme/clone-me", exact: true }),
  ).toBeChecked();
  const row = picker.getByRole("listitem").filter({ hasText: "acme/clone-me" });
  await row.getByRole("button", { name: "Clone it", exact: true }).click();
  await expect(row.getByRole("textbox", { name: "Clone into" })).toHaveValue(
    /code\/acme\/clone-me/,
  );
  await row.getByRole("button", { name: "Clone repository", exact: true }).click();
  await expect(row).toContainText("Found at", { timeout: 15000 });
  await row.getByRole("checkbox", { name: "Pick acme/clone-me", exact: true }).uncheck();
  await picker.getByRole("searchbox", { name: "Search repositories" }).fill("");
});

// PR 8 is the viewer's own, with a conflict, failing checks and unresolved
// review threads. Fix starts at once, with no dialog and no estimate; the
// fixture's agent leaves a note, and the push fails because the fixture's
// clone has no reachable remote.
test("starts a fix from a pull request's page at once, with no dialog", async ({ page }) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  const inbox = page.getByRole("region", { name: "Pull request inbox" });
  const row = inbox.getByRole("listitem").filter({ hasText: "A pull request without a review" });
  await row.hover();
  await expect(
    row.getByRole("button", { name: "Fix conflicts, CI and review comments", exact: true }),
  ).toBeVisible();
  await page.getByRole("link", { name: /A pull request without a review/ }).click();
  const title = page.getByRole("heading", { name: /A pull request without a review/ });
  await expect(title).toHaveCount(1);
  await page
    .getByRole("button", { name: "Fix conflicts, CI and review comments", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByRole("region", { name: "Fix", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Jobs", exact: true })).toBeVisible();
  await expect(page.getByText(/Fix conflicts, CI and review comments/).first()).toBeVisible();
  await expect(title).toHaveCount(0);
});

// The fake GitHub drops PR 7 from the open list once merged; removing its
// marker afterwards gives it back to the specs that run after this one.
test("merges a ready pull request from the inbox after choosing a method", async ({ page }) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  const inbox = page.getByRole("region", { name: "Pull request inbox" });
  const row = inbox.getByRole("listitem").filter({ hasText: "Reuse the existing PR page" });
  await expect(row).toContainText("Status: Ready");
  // Only a ready pull request offers Merge.
  await expect(inbox.getByRole("button", { name: "Merge acme/demo #8", exact: true })).toHaveCount(
    0,
  );
  await row.hover();
  await row.getByRole("button", { name: "Merge acme/demo #7", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Merge pull request #7?" });
  // The fixture repository allows merge commits and squashing, not rebasing.
  await expect(dialog.getByRole("radio")).toHaveCount(2);
  await expect(dialog.getByRole("radio", { name: /Squash and merge/ })).toBeChecked();
  await dialog.getByRole("radio", { name: /Create a merge commit/ }).check();
  await dialog.getByRole("button", { name: "Merge pull request", exact: true }).click();
  await expect(dialog).toBeHidden();
  // The inbox refreshed: the merged pull request is no longer open or mergeable.
  await expect(inbox.getByRole("button", { name: /^Merge acme\/demo #7/ })).toHaveCount(0);
  await rm(join(process.env.BAUM_APP_STATE_DIR ?? "", "merged-7"), { force: true });
});

// The fake GitHub marks PR 8's checks as running once its failed run is
// re-run; removing the marker afterwards gives the specs after this one
// their failing checks back.
test("re-runs the failed checks of a pull request from its page", async ({ page }) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  const inbox = page.getByRole("region", { name: "Pull request inbox" });
  // The row's Status cell keeps its one action (Fix); Re-run is on the page.
  await expect(inbox.getByRole("button", { name: /Re-run/ })).toHaveCount(0);
  await page.getByRole("link", { name: /A pull request without a review/ }).click();
  await page.getByRole("button", { name: "Re-run checks", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Re-run failed checks of pull request #8?" });
  await expect(dialog.getByRole("listitem")).toHaveText(["CI", "test"]);
  await expect(dialog).toContainText("Not re-runnable from baum");
  await dialog.screenshot({ path: "test-results/rerun-dialog.png", animations: "disabled" });
  await dialog.getByRole("button", { name: "Re-run failed checks", exact: true }).click();
  await expect(dialog).toBeHidden();
  // The page refreshed: the checks run again, so there is nothing to re-run.
  await expect(page.getByRole("button", { name: "Re-run checks", exact: true })).toHaveCount(0);
  await rm(join(process.env.BAUM_APP_STATE_DIR ?? "", "rerun-8"), { force: true });
});

test("updates the branch of a pull request that is behind its base from its page", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  const inbox = page.getByRole("region", { name: "Pull request inbox" });
  await expect(inbox.getByRole("button", { name: /Update/ })).toHaveCount(0);
  await page.getByRole("link", { name: /A branch behind its base/ }).click();
  await page.getByRole("button", { name: "Update branch", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Update branch of pull request #11?" });
  await expect(dialog).toContainText("merge main into feature");
  await dialog.screenshot({ path: "test-results/update-dialog.png", animations: "disabled" });
  await dialog.getByRole("button", { name: "Update branch", exact: true }).click();
  await expect(dialog).toContainText("GitHub accepted the update");
  await dialog.getByRole("button", { name: "Close", exact: true }).click();
  await expect(dialog).toBeHidden();
  // The page refreshed: GitHub no longer reports the branch as behind.
  await expect(page.getByRole("button", { name: "Update branch", exact: true })).toHaveCount(0);
  await rm(join(process.env.BAUM_APP_STATE_DIR ?? "", "updated-11"), { force: true });
});

// The fixture writes two Claude Code sessions in the demo clone (13 minutes of
// work and two prompts each; one on a model the price list does not know),
// two commits of the person's in the other clone (one co-authored by Claude),
// plan samples and GitHub's answer to the delivery search. The specs before
// this one merged, updated and re-ran from the app.
test("Analytics shows what the agents did, cost and delivered", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Analytics", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Analytics", exact: true })).toBeVisible();
  expect(new URL(page.url()).search).toBe("?page=analytics");
  const tile = (label: string) =>
    page
      .getByLabel("Summary")
      .locator(".analytics-tile")
      .filter({ has: page.locator("dt", { hasText: new RegExp(`^${label}$`) }) });
  await expect(tile("Sessions")).toContainText("2");
  await expect(tile("Human prompts")).toContainText("4");
  await expect(tile("Active agent time")).toContainText("26 min");
  // One model has no price: the estimate is a floor, never a full figure.
  await expect(tile("At API prices")).toContainText("≥ $");
  await expect(tile("At API prices")).toContainText("Incomplete");
  await expect(tile("Agent-written")).toContainText("50%");
  await expect(tile("Agent-written")).toContainText("1 of 2 of your commits");
  const delivery = page.getByRole("region", { name: "Delivery" });
  await expect(delivery).toContainText("Median time to first review");
  await expect(delivery.locator("dd").first()).toHaveText("2");
  await expect(delivery).toContainText("6 h");
  await expect(delivery).toContainText("1 (50%)");
  const baum = page.getByRole("region", { name: "baum's work" });
  await expect(baum.locator("div").filter({ hasText: /^Merges from baum/ })).toContainText("1");
  await expect(page.getByRole("region", { name: "Plan usage" })).toContainText(
    "5-hour: peak 100%, at its limit 1 time",
  );
  const breakdown = page.getByRole("region", { name: "Breakdown" });
  await expect(breakdown.getByRole("row", { name: /Claude Code/ })).toContainText("50%");
  await breakdown.getByRole("button", { name: "Model", exact: true }).click();
  await expect(breakdown).toContainText("claude-sonnet-4-5");
  await expect(breakdown.getByRole("row", { name: /fixture-model-x/ })).toContainText("—");
  // The window and filters are in the address and change the figures.
  await page.getByRole("button", { name: "90 days", exact: true }).click();
  await expect(page).toHaveURL(/days=90/);
  await page.getByLabel("Repository").selectOption("acme/other");
  await expect(page).toHaveURL(/repo=acme%2Fother/);
  await expect(tile("Sessions")).toContainText("0");
  await expect(page.getByRole("region", { name: "Agent activity" })).toContainText(
    "No agent sessions in this window",
  );
  await expect(tile("Agent-written")).toContainText("50%");
  await page.reload();
  await expect(page.getByLabel("Repository")).toHaveValue("acme/other");
  // Narrow: the page folds without scrolling sideways.
  await page.setViewportSize({ width: 720, height: 900 });
  await page.getByLabel("Repository").selectOption("");
  await expect(tile("Sessions")).toContainText("2");
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  expect(errors).toEqual([]);
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
  const walk = page.getByRole("region", { name: "Walkthrough" });
  await expect(walk.getByText("The route · riskiest first")).toBeVisible();
  expect(page.url()).toContain("walk=tour");
  await walk.screenshot({ path: "test-results/walkthrough-intro.png", animations: "disabled" });
  await walk.getByRole("button", { name: "Start the tour", exact: true }).click();
  await expect(walk.getByRole("article")).toContainText(
    "Chapter 1 · Operator-only dead-letter endpoints",
  );
  await walk.getByRole("button", { name: /Something looks off/ }).click();
  await page.keyboard.type("Trusts a client-supplied header");
  await walk.getByRole("button", { name: "Save comment", exact: true }).click();
  await expect(walk.getByText("1 comment", { exact: true })).toBeVisible();
  await walk.screenshot({ path: "test-results/walkthrough-stop.png", animations: "disabled" });
  // A reload keeps the view, what was looked at and the comment.
  await page.reload();
  await expect(walk.getByText("1 comment", { exact: true })).toBeVisible();
  await page.keyboard.press("e");
  await expect(page).toHaveURL(/walk=all/);
  await expect(walk.getByRole("button", { name: /^With comments/ })).toContainText("1");
  await walk.getByRole("radio", { name: /Request changes/ }).click();
  await walk.screenshot({
    path: "test-results/walkthrough-everything.png",
    animations: "disabled",
  });
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
  await walk.getByRole("button", { name: "Guided tour", exact: true }).click();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(
    true,
  );
  await page.screenshot({ path: "test-results/walkthrough-narrow.png", fullPage: true });
  await walk.getByRole("button", { name: "Full review", exact: true }).click();
  await expect(page).not.toHaveURL(/walk=/);
  expect(errors).toEqual([]);
});

// Every page from the keyboard alone: the command key with a number (Ctrl
// here, which is not a Mac), Go to, each list's keys, a pull request's
// section keys, back and forward, and the sheet that lists them.
test("the whole app can be used from the keyboard", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  const marker = join(process.env.BAUM_APP_STATE_DIR ?? "", "live-transcript");
  await writeFile(marker, "running\nWalk the app by keyboard\n");
  await page.goto(await launchURL());
  const heading = (name: string) => page.getByRole("heading", { level: 1, name, exact: true });
  const search = () => new URL(page.url()).searchParams;
  await expect(heading("Up next")).toBeVisible();

  await page.keyboard.press("Control+2");
  await expect(heading("Pull requests")).toBeVisible();
  await expect(page.getByRole("link", { name: /Reuse the existing PR page/ })).toBeVisible();
  // l and h move along the views.
  await page.keyboard.press("l");
  await expect.poll(() => search().get("filter")).toBe("requested");
  await page.keyboard.press("h");
  await expect.poll(() => search().get("filter")).toBe("inbox");
  await page.keyboard.press("j");
  await expect(page.locator(".inbox-row[data-focused]")).toHaveCount(1);
  await page.keyboard.press("Escape");
  await expect(page.locator(".inbox-row[data-focused]")).toHaveCount(0);

  // Go to finds a pull request by its words and opens it.
  await page.keyboard.press("Control+k");
  const goTo = page.getByRole("dialog", { name: "Go to" });
  await expect(goTo).toBeVisible();
  await page.keyboard.type("existing pr page");
  await expect(goTo.getByRole("option", { selected: true })).toContainText(
    "Reuse the existing PR page",
  );
  await page.keyboard.press("Enter");
  await expect(goTo).toHaveCount(0);
  await expect(page.locator("[data-review-page]")).toBeVisible();
  await expect.poll(() => search().get("pr")).toBe("7");
  // j and k step through the review in reading order, from the Summary, once
  // the review has loaded its sections.
  const rail = page.getByRole("navigation", { name: "Review sections" });
  await expect(rail.getByRole("button", { name: "Summary", pressed: true })).toBeVisible();
  await expect(rail.getByRole("button", { name: "Retry policy and backoff" })).toBeVisible();
  await expect.poll(() => search().get("pane")).toBeNull();
  await page.keyboard.press("j");
  await expect.poll(() => search().get("pane")).not.toBeNull();
  await page.keyboard.press("k");
  await expect.poll(() => search().get("pane")).toBeNull();
  await page.keyboard.press("u");
  await expect(heading("Up next")).toBeVisible();

  // Sessions: j focuses the row, Enter opens its transcript, u comes back.
  await page.keyboard.press("Control+4");
  await expect(heading("Sessions")).toBeVisible();
  // The fixture's one session keeps the title an earlier story gave it, so
  // the row is found by its link, not its words.
  await expect(
    page
      .getByRole("region", { name: "Running now", exact: true })
      .getByRole("link", { name: /^Open transcript/ }),
  ).toBeVisible({ timeout: 10000 });
  await page.keyboard.press("j");
  await expect(page.locator(".local-feed-card[data-focused]")).toHaveCount(1);
  await page.keyboard.press("Enter");
  await expect.poll(() => search().get("session")).not.toBeNull();
  await expect(page.getByRole("region", { name: "Transcript", exact: true })).toBeVisible();
  await page.keyboard.press("u");
  await expect(heading("Sessions")).toBeVisible();

  await page.keyboard.press("Control+7");
  await expect(heading("Jobs")).toBeVisible();
  await page.keyboard.press("Control+5");
  await expect(heading("Analytics")).toBeVisible();
  // Repositories: a opens the picker with its search focused.
  await page.keyboard.press("Control+6");
  await expect(heading("Repositories")).toBeVisible();
  await page.keyboard.press("a");
  await expect(page.getByRole("searchbox")).toBeFocused();
  // The app's keys still work in a text field; Settings is ⌘, too.
  await page.keyboard.press("Control+,");
  await expect(page).toHaveURL(/\?page=settings$/);
  // Back and forward walk the history the keys made.
  await page.keyboard.press("Alt+ArrowLeft");
  await expect(heading("Repositories")).toBeVisible();
  await page.keyboard.press("Alt+ArrowLeft");
  await expect(heading("Analytics")).toBeVisible();
  await page.keyboard.press("Alt+ArrowRight");
  await expect(heading("Repositories")).toBeVisible();

  // The sheet lists the page's own keys first; Escape closes it.
  await page.keyboard.press("Control+4");
  await expect(heading("Sessions")).toBeVisible();
  await page.keyboard.press("?");
  const sheet = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await expect(sheet.locator("h3").first()).toHaveText("Sessions");
  // It opens at its top, on the page's own keys.
  await expect(sheet.locator("h3").first()).toBeInViewport();
  expect(await sheet.evaluate((dialog) => dialog.scrollTop)).toBe(0);
  await expect(sheet).toContainText("Ctrl+K");
  await page.screenshot({ path: "test-results/shortcuts-sheet.png", animations: "disabled" });
  await page.keyboard.press("Escape");
  await expect(sheet).toHaveCount(0);
  await page.keyboard.press("Control+k");
  await expect(goTo).toBeVisible();
  await page.screenshot({ path: "test-results/go-to.png", animations: "disabled" });
  await page.keyboard.press("Escape");
  await expect(goTo).toHaveCount(0);
  await page.keyboard.down("Alt");
  await expect(page.locator(".local-rail-key").first()).toBeVisible();
  await page.screenshot({ path: "test-results/sidebar-keys.png", animations: "disabled" });
  await page.keyboard.up("Alt");
  await writeFile(marker, "ended\nWalk the app by keyboard\n");
  expect(errors).toEqual([]);
});
