import { expect, test } from "@playwright/test";
import type { Entry, ReviewGroup, Snapshot } from "../src/api";
import { launchURL } from "./inbox-helpers";

test.afterEach(async ({ page }) => {
  // A background index poll may still be in flight when the page closes.
  await page.unrouteAll({ behavior: "ignoreErrors" });
});

test("a stack follows its first open PR from waiting to ready while another member needs fixing", async ({
  page,
}) => {
  let ready = false;
  const group: ReviewGroup = {
    id: "placement-stack",
    repository: "acme/demo",
    name: "Session loading stack",
    members: [1109, 1111],
    stack: true,
    created_at: "",
    updated_at: "",
    version_count: 0,
    latest: null,
    risk_score: null,
    reviewed: [],
    outdated: false,
  };
  await page.route(
    (url) => url.pathname.endsWith("/api/index"),
    async (route) => {
      const response = await route.fetch();
      const snapshot = (await response.json()) as Snapshot;
      const original = snapshot.entries[0]!;
      const member = (
        number: number,
        state: Pick<Entry, "checks" | "merge" | "unresolved">,
      ): Entry => ({
        ...original,
        ...state,
        review: "commented",
        historical: false,
        version_count: 0,
        latest: null,
        risk_score: null,
        pull_request: {
          ...original.pull_request,
          ...state,
          review: "commented",
          repository: "acme/demo",
          number,
          title: number === 1109 ? "First PR" : "Later PR",
          url: `https://github.com/acme/demo/pull/${number}`,
          head_ref: number === 1109 ? "first" : "later",
          base_ref: number === 1109 ? "main" : "first",
          authored_by_me: true,
          review_requested_from_me: false,
          review_required: false,
          draft: false,
          state: "OPEN",
          behind: false,
        },
      });
      await route.fulfill({
        response,
        json: {
          ...snapshot,
          entries: [
            member(1109, {
              checks: ready ? "passing" : "running",
              merge: ready ? "ready" : "blocked",
              unresolved: 0,
            }),
            member(1111, { checks: "passing", merge: "ready", unresolved: 1 }),
          ],
        },
      });
    },
  );
  await page.route(
    (url) => url.pathname.endsWith("/api/groups"),
    (route) => route.fulfill({ json: { groups: [group] } }),
  );
  await page.route(
    (url) => url.pathname.endsWith("/api/prs"),
    (route) => route.fulfill({ json: [] }),
  );
  await page.route(
    (url) => url.pathname.endsWith("/api/jobs"),
    (route) => route.fulfill({ json: { jobs: [] } }),
  );
  await page.goto(await launchURL());
  const waiting = page.getByRole("region", { name: "Waiting", exact: true });
  await expect(waiting.locator(".upnext-row-title")).toHaveText("Session loading stack");
  await expect(page.getByRole("heading", { name: /^Hand to an agent/ })).toHaveCount(0);
  await page
    .getByRole("button", { name: "Show the pull requests of Session loading stack", exact: true })
    .click();
  await expect(waiting.locator("button.upnext-row").filter({ hasText: "First PR" })).toContainText(
    "Checks running",
  );
  await expect(waiting.locator("button.upnext-row").filter({ hasText: "Later PR" })).toContainText(
    "1 unresolved thread",
  );
  ready = true;
  await page.reload();
  await expect(
    page
      .getByRole("region", { name: "Ready to merge", exact: true })
      .locator(".upnext-row-title")
      .first(),
  ).toHaveText("Session loading stack");
  await expect(page.getByRole("heading", { name: /^Hand to an agent/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Merge all/ })).toHaveCount(0);
});
