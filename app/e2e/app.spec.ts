import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test } from "@playwright/test";

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
  await expect(page.getByRole("heading", { name: "Pull requests", exact: true })).toBeVisible();
  expect(page.url()).not.toContain("token");
  // The app is relaunched against a long-lived daemon: the same credential
  // authorizes another browser session.
  const relaunched = await page.context().browser()?.newPage();
  if (relaunched) {
    await relaunched.goto(launch);
    await expect(
      relaunched.getByRole("heading", { name: "Pull requests", exact: true }),
    ).toBeVisible();
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
  await page.setViewportSize({ width: 720, height: 900 });
  await expect(approved.getByText("Status:", { exact: true })).toBeVisible();
  // The cell also holds the Merge button of a ready pull request.
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
  await page.getByRole("button", { name: "Generate selected (1)", exact: true }).click();
  // Only a fork asks before generating, in a batch as on its own page.
  await expect(page.getByRole("dialog")).toContainText("Review code from outside your team?");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("checkbox", { name: "Select acme/demo #9", exact: true }).uncheck();
  await page.getByRole("link", { name: /A fork requiring confirmation/ }).click();
  await page.getByRole("button", { name: "Generate review", exact: true }).first().click();
  await expect(page.getByRole("dialog")).toContainText("Review code from outside your team?");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("link", { name: "Inbox", exact: true }).first().click();
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
  await page.getByRole("link", { name: "Inbox", exact: true }).first().click();
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
  await page.getByRole("link", { name: "Inbox", exact: true }).first().click();
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
    page
      .getByRole("region", { name: "Reviews", exact: true })
      .getByRole("switch", { name: /Review pull requests automatically/ }),
  ).toBeChecked();
  await page.screenshot({ path: "test-results/settings.png", animations: "disabled" });
  await page.getByRole("button", { name: /Check this computer/ }).click();
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

// The daemon generates a review by itself when a pull request the user wrote
// gets a new head after automatic generation was enabled, and says so on the
// PR page and in the jobs list. Turning it off is a setting the server keeps.
test("automatic review of a new head, shown as started automatically", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
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
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  const automatic = page.getByRole("region", { name: "Reviews", exact: true });
  const toggle = automatic.getByRole("switch", { name: /Review pull requests automatically/ });
  const save = automatic.getByRole("button", { name: "Save review settings" });
  // The switch shows the saved preference once saving answers.
  await toggle.click();
  await save.click();
  await expect(save).toBeDisabled();
  await expect(toggle).not.toBeChecked();
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
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await toggle.click();
  await save.click();
  await expect(toggle).toBeChecked();
  expect(errors).toEqual([]);
});

test("a running agent session shows in the inbox and on the PR page until it ends", async ({
  page,
}) => {
  const marker = join(process.env.BAUM_APP_STATE_DIR ?? "", "live-session");
  await page.goto(await launchURL());
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
  await expect(live.getByText("Waiting for you")).toBeVisible({ timeout: 10000 });
  await writeFile(marker, "ended");
  await expect(live).toHaveCount(0, { timeout: 10000 });
  // The Sessions page keeps it under "Ended recently" and has none running.
  await page.getByRole("link", { name: "Sessions", exact: true }).first().click();
  const ended = page.getByRole("region", { name: "Ended recently", exact: true });
  await expect(ended.getByText("Claude Code")).toBeVisible();
  await expect(ended.getByText("feature")).toBeVisible();
  await expect(
    page.getByRole("region", { name: "Running now", exact: true }).getByText("Claude Code"),
  ).toHaveCount(0);
  await page.getByRole("link", { name: "Inbox", exact: true }).first().click();
  await expect(row).toBeVisible();
  await expect(row.getByText("Agent")).toHaveCount(0);
});

test("desktop Settings has separate CLI and hook cards at desktop and narrow widths", async ({
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
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  const cli = page.getByRole("region", { name: "Command line", exact: true });
  const hooks = page.getByRole("region", { name: "Agent hooks", exact: true });
  await expect(cli.getByText("baum is installed at ~/.local/bin/baum.")).toBeVisible();
  await expect(hooks.getByText("Connected", { exact: true })).toHaveCount(2);
  await cli.scrollIntoViewIfNeeded();
  await page.screenshot({ path: testInfo.outputPath("settings-desktop.png"), fullPage: true });
  await page.setViewportSize({ width: 600, height: 900 });
  await expect(cli).toBeVisible();
  await expect(hooks).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath("settings-narrow.png"), fullPage: true });
});

test("picks GitHub repositories and keeps clone-less PRs usable", async ({ page }) => {
  await page.goto(await launchURL());
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

// Last in the file: the fake GitHub drops PR 7 from the open list once merged,
// and earlier tests read it.
test("merges a ready pull request from the inbox after choosing a method", async ({ page }) => {
  await page.goto(await launchURL());
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
});
