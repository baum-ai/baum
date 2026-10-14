import { expect, sampleDocument, serveDocument, test } from "./fixtures.ts";

type Page = import("@playwright/test").Page;

// CI leads the side pane's Activity tab.
const details = (page: Page) =>
  page.getByRole("tabpanel", { name: "Activity" }).getByRole("region", { name: "Checks" });

async function openActivity(page: Page) {
  await page.getByRole("tab", { name: "Activity" }).click();
}

test("the Activity tab names the failing check and lists every check on the reviewed head", async ({
  page,
}) => {
  await page.goto("/");
  // Failing CI flags the tab that holds it, not Details.
  const dot = (name: string) =>
    page.getByRole("tab", { name, exact: true }).locator("span[aria-hidden]");
  await expect(dot("Activity")).toHaveCount(1);
  await expect(dot("Details")).toHaveCount(0);
  await openActivity(page);
  const checks = details(page);

  await expect(checks.getByRole("status")).toHaveText("CI failing · 1 check failed");
  await expect(checks).toContainText("3 of 6 passed");
  await expect(checks.getByRole("link", { name: "lint" })).toHaveAttribute(
    "href",
    "https://github.com/example-org/taskline/actions/runs/9001/job/9003",
  );

  await checks.getByText("6 checks on this head").click();
  const list = checks.getByRole("list", { name: "Checks on this head" });
  await expect(list.getByRole("listitem")).toHaveCount(6);
  await expect(list).toContainText("test (go 1.22)");
  await expect(list).toContainText("docs-preview");
  await expect(list).toContainText("ci/deploy-preview");
});

test("a required check that fails blocks, and an all-green head reads green", async ({ page }) => {
  const blocked = sampleDocument();
  for (const check of blocked.index.checks) if (check.name === "lint") check.required = true;
  await serveDocument(page, blocked);
  await page.goto("/");
  await openActivity(page);
  await expect(details(page)).toContainText("Checks blocking");
  await expect(
    details(page).getByRole("listitem").filter({ hasText: "lint" }).first(),
  ).toContainText("Required");

  const green = sampleDocument();
  for (const check of green.index.checks) {
    if (check.conclusion === "failure" || check.conclusion === "pending")
      check.conclusion = "success";
  }
  await page.unroute("**/api/document");
  await serveDocument(page, green);
  await page.goto("/");
  await openActivity(page);
  await expect(details(page).getByRole("status")).toContainText("CI green");
  await expect(details(page)).not.toContainText("Checks failing");
});

test("a check from an earlier head does not paint this one red", async ({ page }) => {
  const document = sampleDocument();
  for (const check of document.index.checks) {
    if (check.conclusion === "failure") check.head = "0".repeat(40);
  }
  await serveDocument(page, document);
  await page.goto("/");
  await openActivity(page);

  // The status is drawn, and it is not red.
  await expect(details(page).getByRole("status")).toContainText("CI pending");
  await expect(details(page).getByRole("status")).not.toContainText("CI failing");
});

test("a section marks each piece of evidence as verified, missing or pending", async ({ page }) => {
  await page.goto("/?pane=section&section=sec_retry");
  const pane = page.locator("#review-section");

  const behavior = pane.getByRole("region", { name: "Behavior", exact: true });
  await expect(behavior.getByRole("list", { name: "Supporting evidence" }).first()).toContainText(
    "Verified",
  );
  const attention = pane.getByRole("region", { name: "Attention" });
  const missing = attention
    .getByRole("listitem")
    .filter({ hasText: "Workers retrying one failing handler together stay spread out" });
  await expect(missing).toContainText("Missing");
});

test("the summary's risk score gives its reasons and the Details tab sizes the change", async ({
  page,
}) => {
  await page.goto("/");

  const score = page.getByRole("region", { name: "Risk score" });
  await expect(score).toContainText("5 of 5 · Critical impact");
  await expect(score).toContainText("Two new HTTP operations can read failure reasons");

  const size = page.getByRole("region", { name: "Change size" });
  await expect(size).toContainText("+401 −17");
  await expect(size.getByRole("img", { name: /Lines by risk tier: critical 15/ })).toBeVisible();
});
