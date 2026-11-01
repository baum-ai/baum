import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test, type Page, type Route } from "@playwright/test";

// The fixture daemon publishes its launch URL in its credential file.
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

/** Opens the app, then `search` on its namespaced address. */
async function open(page: Page, search = "") {
  await page.goto(await launchURL());
  await expect(page.getByRole("heading", { name: "Up next", exact: true })).toBeVisible();
  if (search) await page.goto(new URL(search, page.url()).toString());
}

/** Holds every request to `path` until `release`, as a daemon that never
 *  answers; `count` is how many were asked. */
async function hold(page: Page, path: string) {
  const held: Route[] = [];
  const pattern = (url: URL) => url.pathname.endsWith(`/${path}`);
  await page.route(pattern, (route) => {
    held.push(route);
  });
  return {
    count: () => held.length,
    /** Answers the next requests, leaving those held unanswered. */
    stop: () => page.unroute(pattern),
    release: async () => {
      await page.unroute(pattern);
      // One the app dropped is already aborted.
      for (const route of held.splice(0)) await route.continue().catch(() => {});
    },
  };
}

/** Records whether each Ctrl+R reached the browser, which would reload the
 *  app: the app listens on the window, so the probe reads the event after. */
async function probeRefreshKey(page: Page) {
  await page.addInitScript(() => {
    const seen: boolean[] = [];
    (window as unknown as { refreshKeys: boolean[] }).refreshKeys = seen;
    window.addEventListener("keydown", (event) => {
      if (event.ctrlKey && event.key.toLowerCase() === "r")
        setTimeout(() => seen.push(event.defaultPrevented));
    });
  });
  return async () => {
    await page.keyboard.press("Control+r");
    await expect
      .poll(() =>
        page.evaluate(() => (window as unknown as { refreshKeys: boolean[] }).refreshKeys),
      )
      .not.toContain(false);
  };
}

test("the refresh key starts a first read that hangs again, and never reloads the app", async ({
  page,
}) => {
  const pressRefresh = await probeRefreshKey(page);
  const index = await hold(page, "api/index");
  await page.goto(await launchURL());
  await expect(
    page.getByText("Loading local inbox…").or(page.locator(".nine-dots")).first(),
  ).toBeVisible();
  await expect.poll(index.count).toBe(1);
  await pressRefresh();
  await expect.poll(index.count).toBeGreaterThan(1);
  await index.release();
  await expect(
    page.getByRole("button", { name: "Refresh from GitHub" }).first(),
  ).not.toHaveAttribute("aria-busy", "true");
});

test("the refresh key reads again on pages still loading, without a button", async ({ page }) => {
  const pressRefresh = await probeRefreshKey(page);
  await open(page);
  // Inboxes waiting on their first read.
  const inboxes = await hold(page, "api/inboxes");
  await page.goto(new URL("?page=inboxes", page.url()).toString());
  await expect(page.getByText("Loading inboxes…")).toBeVisible();
  await expect.poll(inboxes.count).toBe(1);
  await pressRefresh();
  await expect.poll(inboxes.count).toBe(2);
  await inboxes.release();
  await expect(page.getByText("Loading inboxes…")).toHaveCount(0);

  // A review group waiting on the groups.
  const groups = await hold(page, "api/groups");
  await page.goto(new URL("?group=not-read-yet", page.url()).toString());
  await expect(page.getByText("Loading review group…")).toBeVisible();
  await pressRefresh();
  await expect.poll(groups.count).toBe(2);
  await groups.release();
  await expect(page.getByText("Loading review group…")).toHaveCount(0);
});

test("pressing a refresh under way starts it again", async ({ page }) => {
  const pressRefresh = await probeRefreshKey(page);
  await open(page, "?page=pulls");
  const button = page.getByRole("button", { name: "Refresh from GitHub" });
  await expect(button).not.toHaveAttribute("aria-busy", "true");
  const refresh = await hold(page, "api/prs/refresh");
  await button.click();
  await expect(button).toHaveAttribute("aria-busy", "true");
  await expect.poll(refresh.count).toBe(1);
  // The first never answers; the key asks GitHub again without waiting on it.
  await pressRefresh();
  await expect.poll(refresh.count).toBe(2);
  await refresh.release();
  await expect(button).not.toHaveAttribute("aria-busy", "true");
});

test("a refresh pressed again drops one that never answers on another page", async ({ page }) => {
  const pressRefresh = await probeRefreshKey(page);
  await open(page, "?page=pulls");
  const refresh = await hold(page, "api/prs/refresh");
  await page.getByRole("button", { name: "Refresh from GitHub" }).click();
  await expect.poll(refresh.count).toBe(1);
  await page
    .getByRole("link", { name: /^Inbox/ })
    .first()
    .click();
  const inboxRefresh = page.getByRole("button", { name: "Refresh from GitHub" }).first();
  // The Inbox shows the refresh from GitHub under way, which never answers.
  await expect(inboxRefresh).toHaveAttribute("aria-busy", "true");
  await refresh.stop();
  await pressRefresh();
  await expect(inboxRefresh).not.toHaveAttribute("aria-busy", "true");
});

test("a page's refresh still shows running after visiting another page", async ({ page }) => {
  await open(page, "?page=sessions");
  const sessionsRefresh = page.getByRole("button", { name: "Refresh", exact: true });
  await expect(sessionsRefresh).not.toHaveAttribute("aria-busy", "true");
  const live = await hold(page, "api/live-sessions");
  await sessionsRefresh.click();
  await expect(sessionsRefresh).toHaveAttribute("aria-busy", "true");
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  const jobsRefresh = page.getByRole("button", { name: "Refresh", exact: true });
  await expect(jobsRefresh).not.toHaveAttribute("aria-busy", "true");
  await jobsRefresh.click();
  await expect(jobsRefresh).not.toHaveAttribute("aria-busy", "true");
  await page.getByRole("link", { name: "Sessions", exact: true }).click();
  await expect(sessionsRefresh).toHaveAttribute("aria-busy", "true");
  await live.release();
  await expect(sessionsRefresh).not.toHaveAttribute("aria-busy", "true");
});

test("the refresh key and button read the open repository picker from GitHub again", async ({
  page,
}) => {
  const pressRefresh = await probeRefreshKey(page);
  await open(page, "?page=repositories");
  await page.getByRole("button", { name: "Add repositories", exact: true }).click();
  const picker = page.getByRole("region", { name: "Choose GitHub repositories" });
  await expect(picker.getByRole("checkbox", { name: "Pick acme/demo", exact: true })).toBeVisible();
  // Only a refresh asks GitHub for the list again rather than its cache.
  const fromGitHub = () =>
    page.waitForRequest(
      (request) =>
        new URL(request.url()).pathname.endsWith("/api/github/repositories") &&
        new URL(request.url()).searchParams.get("refresh") === "1",
    );
  const byKey = fromGitHub();
  await pressRefresh();
  await byKey;
  const byButton = fromGitHub();
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await byButton;
  await expect(picker.getByRole("checkbox", { name: "Pick acme/demo", exact: true })).toBeVisible();
});
