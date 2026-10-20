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
  await page.screenshot({
    path: "test-results/inbox-wide.png",
    fullPage: false,
    animations: "disabled",
  });
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
  await page.getByRole("combobox").first().selectOption("mine");
  await expect(page.getByRole("link", { name: /without a review/ })).toBeVisible();
  await expect(page.getByRole("link", { name: /Reuse the existing/ })).toHaveCount(0);
  await page.getByRole("combobox").first().selectOption("all");
  await page.getByRole("checkbox", { name: "Select acme/demo #9", exact: true }).check();
  await page.getByRole("button", { name: "Generate selected (1)", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("needs its own confirmation");
  await expect(
    page.getByRole("button", { name: "Confirm estimate and generate", exact: true }),
  ).toBeDisabled();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.getByRole("checkbox", { name: "Select acme/demo #9", exact: true }).uncheck();
  await page.getByRole("link", { name: /A fork requiring confirmation/ }).click();
  await page.getByRole("button", { name: "Generate review", exact: true }).first().click();
  await expect(
    page.getByRole("button", { name: "Confirm estimate and generate", exact: true }),
  ).toBeDisabled();
  await page.getByRole("dialog").getByRole("checkbox").check();
  await expect(
    page.getByRole("button", { name: "Confirm estimate and generate", exact: true }),
  ).toBeEnabled();
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
  await page.getByRole("button", { name: "Generate review", exact: true }).first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByText(/Budget planning amount:/)).toBeVisible();
  await expect(
    page.getByText(/Assumes 2,000,000 charged tokens and 40 backend calls/),
  ).toBeVisible();
  await page.getByRole("button", { name: "Confirm estimate and generate" }).click();
  const progress = page.getByRole("region", { name: "Review generation", exact: true });
  await expect(progress).toBeVisible();
  await expect(page.getByText("No generated review yet.", { exact: true })).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: /Generating review|Review queued/ }),
  ).toBeDisabled();
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
  await page.getByText("Saved review usage · claude-sonnet-4-5", { exact: true }).click();
  await expect(page.getByText(/Total processed tokens 1,000/)).toBeVisible();
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  await expect(page.getByText(/completed/)).toBeVisible();
  await expect(page.getByText(/Calls 1 · Repairs 0/)).toBeVisible();
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
  await page.getByRole("button", { name: "Confirm estimate and generate", exact: true }).click();
  await expect(
    page.getByText("Adds notes, settings and a renamed guide.", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: /Generating review|Review queued/ }),
  ).toBeDisabled();
  await page.getByRole("button", { name: "Cancel job", exact: true }).click();
  await expect(page.getByRole("button", { name: "Regenerate review", exact: true })).toBeEnabled();
  await expect(page.getByText("Review generation cancelled", { exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  await expect(page.getByText(/cancelled/).first()).toBeVisible();
  await page.getByRole("link", { name: "Repositories & settings" }).click();
  const settings = page.getByRole("region", { name: "Repository acme/demo", exact: true });
  await expect(
    settings.getByText(/Model generation runs when you request and confirm it, or automatically/),
  ).toBeVisible();
  // Automatic reviews are on by default: the repository follows the global
  // setting and has no daily cap.
  await expect(settings.getByRole("combobox", { name: /Automatic reviews/ })).toHaveValue(
    "default",
  );
  await expect(settings.getByRole("spinbutton", { name: /Daily automatic cap/ })).toHaveValue("");
  await expect(
    page
      .getByRole("region", { name: "Automatic reviews", exact: true })
      .getByRole("checkbox", { name: /Generate reviews automatically/ }),
  ).toBeChecked();
  await settings.getByRole("textbox", { name: "Model override" }).fill("claude-sonnet-4-5");
  await settings.getByRole("button", { name: "Save repository settings" }).click();
  await expect(settings.getByRole("textbox", { name: "Model override" })).toHaveValue(
    "claude-sonnet-4-5",
  );
  const diagnostics = page.getByRole("region", { name: "Diagnostics", exact: true });
  await expect(diagnostics.getByText(/Overall: Problem/)).toBeVisible();
  const problem = diagnostics.getByRole("listitem").filter({ hasText: "GitHub token" });
  await expect(problem.getByText("Problem", { exact: true })).toBeVisible();
  await expect(problem.getByText(/Run gh auth login, or set github_token/)).toBeVisible();
  await expect(
    diagnostics.getByRole("listitem").filter({ hasText: "GitHub CLI" }).getByText("Warning"),
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
  await expect(page.getByText("No generated review yet.", { exact: true })).toBeVisible();
  // The fixture's discovery reports PR 10's new head once this marker exists.
  await writeFile(join(process.env.BAUM_APP_STATE_DIR ?? "", "push-10"), "");
  const progress = page.getByRole("region", { name: "Review generation", exact: true });
  await expect(
    progress.getByText("Started automatically because new commits were pushed", { exact: true }),
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
  await page.getByRole("link", { name: "Repositories & settings" }).click();
  const automatic = page.getByRole("region", { name: "Automatic reviews", exact: true });
  const toggle = automatic.getByRole("checkbox", { name: /Generate reviews automatically/ });
  // The checkbox shows the saved preference, so it changes once the server answers.
  await toggle.click();
  await expect(toggle).not.toBeChecked();
  await page.reload();
  await expect(toggle).not.toBeChecked();
  await expect(
    page
      .getByRole("region", { name: "Repository acme/demo", exact: true })
      .getByText("Automatic reviews are paused by the global switch."),
  ).toBeVisible();
  await page.screenshot({ path: "test-results/automatic-settings.png", animations: "disabled" });
  await toggle.click();
  await expect(toggle).toBeChecked();
  expect(errors).toEqual([]);
});
