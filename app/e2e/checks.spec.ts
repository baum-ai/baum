import { expect, test } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

test("live checks keep their rows aligned in the review Activity pane", async ({ page }) => {
  await page.route("**/api/prs/checks?*", async (route) => {
    await route.fulfill({
      json: {
        checks: [
          {
            name: "CI / Platform",
            status: "completed",
            conclusion: "failure",
            job_id: 21,
            url: "https://github.com/acme/demo/actions/runs/1/job/21",
          },
          {
            name: `Workers Builds: ${"a-long-project-name-".repeat(8)}`,
            title: "Build completed successfully",
            status: "completed",
            conclusion: "success",
            url: "https://github.com/acme/demo/actions/runs/2",
          },
          { name: "CI / Desktop app", status: "completed", conclusion: "skipped" },
        ],
      },
    });
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await page.getByRole("tab", { name: /^Activity/ }).click();
  const checks = page.getByRole("list", { name: "Checks", exact: true });
  await expect(checks.getByRole("listitem")).toHaveCount(1);
  await page.getByRole("button", { name: "Show 2 other checks", exact: true }).click();
  await expect(checks.getByRole("listitem")).toHaveCount(3);

  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 1000 });
    if (width < 1280) await page.keyboard.press("]");
    await expect(checks).toBeVisible();
    const layout = await checks.evaluate((list) => {
      const bounds = list.getBoundingClientRect();
      return [...list.children].map((row) => {
        const icon = row.firstElementChild!.getBoundingClientRect();
        const text = row.querySelector(".upnext-block-text")!.getBoundingClientRect();
        const actions = row.querySelector(".upnext-check-actions")!.getBoundingClientRect();
        return {
          textAfterIcon: text.left >= icon.right,
          actionsAfterText: actions.left >= text.right,
          actionsWithinList: actions.right <= bounds.right,
          textWidth: text.width,
        };
      });
    });
    for (const row of layout) {
      expect(row.textAfterIcon).toBe(true);
      expect(row.actionsAfterText).toBe(true);
      expect(row.actionsWithinList).toBe(true);
      expect(row.textWidth).toBeGreaterThan(40);
    }
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
    await page.screenshot({ path: `test-results/checks-${width}.png` });
  }
  await page.getByRole("button", { name: "Hide other checks", exact: true }).click();
  await expect(checks.getByRole("listitem")).toHaveCount(1);
});

test("a failing check shows the end of its log, folded until asked for", async ({ page }) => {
  let logReads = 0;
  await page.route("**/api/prs/checks?*", async (route) => {
    await route.fulfill({
      json: {
        checks: [
          {
            name: "CI / Platform",
            status: "completed",
            conclusion: "failure",
            id: 21,
            job_id: 21,
            has_log: true,
            url: "https://github.com/acme/demo/actions/runs/1/job/21",
          },
        ],
      },
    });
  });
  await page.route("**/api/prs/check-log?*", async (route) => {
    logReads++;
    const lines = Array.from({ length: 120 }, (_, i) => `step output line ${i + 1}`);
    await route.fulfill({
      json: {
        id: 21,
        name: "CI / Platform",
        available: true,
        truncated: true,
        log: [...lines, "--- FAIL: TestSearch (0.01s)", "FAIL"].join("\n"),
      },
    });
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await page.getByRole("tab", { name: /^Activity/ }).click();
  const show = page.getByRole("button", { name: "Show log", exact: true });
  await expect(show).toHaveAttribute("aria-expanded", "false");
  expect(logReads).toBe(0);

  // The keyboard opens it.
  await show.focus();
  await page.keyboard.press("Enter");
  const log = page.locator("pre[aria-label='Log of CI / Platform']");
  await expect(log).toContainText("--- FAIL: TestSearch");
  expect(logReads).toBe(1);
  await expect(page.getByRole("button", { name: "Hide log", exact: true })).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  // It is bounded, scrolls inside itself and starts at its end.
  const metrics = await log.evaluate((pre) => ({
    height: pre.getBoundingClientRect().height,
    scrollable: pre.scrollHeight > pre.clientHeight,
    atEnd: pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 2,
  }));
  expect(metrics.height).toBeLessThanOrEqual(340);
  expect(metrics.scrollable).toBe(true);
  expect(metrics.atEnd).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(1440);
  await page.screenshot({ path: "test-results/check-log.png" });

  // Copy sits at the right end of the log's footer, and its status text is
  // not drawn: "Copied" shows once, on the button.
  const copyAligned = async () => {
    const copy = page.getByRole("button", { name: "Copy the log of CI / Platform" });
    const [copyBox, logBox] = await Promise.all([copy.boundingBox(), log.boundingBox()]);
    expect(Math.abs(copyBox!.x + copyBox!.width - (logBox!.x + logBox!.width))).toBeLessThanOrEqual(
      2,
    );
  };
  await copyAligned();
  await page
    .context()
    .grantPermissions(["clipboard-read", "clipboard-write"])
    .catch(() => {});
  await page.getByRole("button", { name: "Copy the log of CI / Platform" }).click();
  await expect(page.getByRole("button", { name: "Copy the log of CI / Platform" })).toContainText(
    "Copied",
  );
  await expect(page.locator("[role=status].sr-only")).toHaveCSS("position", "absolute");

  // The same at a narrow width.
  await page.setViewportSize({ width: 390, height: 1000 });
  await page.keyboard.press("]");
  await expect(log).toBeVisible();
  await copyAligned();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.screenshot({ path: "test-results/check-log-390.png" });

  await page.getByRole("button", { name: "Hide log", exact: true }).click();
  await expect(log).toHaveCount(0);
});
