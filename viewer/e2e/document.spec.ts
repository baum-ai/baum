import { expect, sampleDocument, test } from "./fixtures.ts";

test("the sample document loads from the page's own origin and titles the page", async ({
  page,
  browserErrors,
  foreignRequests,
}) => {
  await page.goto("/");

  await expect(
    page.getByRole("heading", { level: 1, name: "#212 Add retry budgets and a dead-letter queue" }),
  ).toBeVisible();
  await expect(page).toHaveTitle("Add retry budgets and a dead-letter queue · #212");
  await expect(page.getByRole("region", { name: "Summary", exact: true })).toContainText(
    "Jobs that fail now back off exponentially",
  );
  await expect(page.getByRole("link", { name: "View on GitHub" })).toHaveAttribute(
    "href",
    "https://github.com/example-org/taskline/pull/212",
  );
  // The page is served under a strict policy and talks to nothing else.
  expect(browserErrors).toEqual([]);
  expect(foreignRequests).toEqual([]);
});

test("a failed load says why and Retry loads the document", async ({ page }) => {
  let failing = true;
  await page.route("**/api/document", (route) =>
    failing ? route.fulfill({ status: 500, json: {} }) : route.fulfill({ json: sampleDocument() }),
  );
  await page.goto("/");

  await expect(page.getByRole("alert")).toContainText("Could not load the review");
  await expect(page.getByRole("alert")).toContainText("500");

  failing = false;
  await page.getByRole("button", { name: "Retry" }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(page.getByRole("heading", { level: 1 })).toContainText("dead-letter queue");
});
