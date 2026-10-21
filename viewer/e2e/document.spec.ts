import { expect, sampleDocument, serveDocument, test } from "./fixtures.ts";

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

test("a file's document is the one on screen, whatever it holds", async ({ page }) => {
  const document = sampleDocument();
  document.summary.title = "A document read from a file";
  document.review.pull_request = 7;
  await serveDocument(page, document);
  await page.goto("/");

  await expect(
    page.getByRole("heading", { level: 1, name: "#7 A document read from a file" }),
  ).toBeVisible();
  await expect(page).toHaveTitle("A document read from a file · #7");
});

test("the page offers no file picker or upload: documents come from the command line", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.getByRole("navigation", { name: "Review sections" })).toBeVisible();

  await expect(page.locator("input[type=file]")).toHaveCount(0);
  await expect(page.getByRole("button", { name: /upload|choose|open file|browse/i })).toHaveCount(
    0,
  );
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

test("a body that is not a review document is refused", async ({ page }) => {
  await serveDocument(page, { hello: "world" });
  await page.goto("/");

  await expect(page.getByRole("alert")).toContainText("baum.review/1");
  await expect(page.getByRole("navigation", { name: "Review sections" })).toHaveCount(0);
});

test("a document that fails to render offers a retry instead of a blank page", async ({
  page,
  browserErrors,
}) => {
  const document = sampleDocument();
  // Valid at the top level, but a section the viewer cannot read.
  (document.sections as unknown[]).push(null);
  await serveDocument(page, document);
  await page.goto("/");

  await expect(page.getByRole("alert")).toContainText("Could not load the review");
  await expect(page.getByRole("button", { name: "Retry" })).toBeVisible();
  expect(browserErrors.length).toBeGreaterThan(0);
});

test("the head states the review time estimate when the document has one", async ({ page }) => {
  const document = sampleDocument();
  document.summary.review_minutes = 7;
  await serveDocument(page, document);
  await page.goto("/");

  await expect(page.getByText("~7 min to review")).toBeVisible();
  await expect(
    page.getByTitle("Estimated time to read this review: about 7 minutes"),
  ).toBeVisible();
});

test("the head shows no estimate when the document has none or null", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect(page.getByText("min to review")).toHaveCount(0);

  const document = sampleDocument();
  document.summary.review_minutes = null;
  await serveDocument(page, document);
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect(page.getByText("min to review")).toHaveCount(0);
});

test("the summary's context starts collapsed under the overview and expands", async ({ page }) => {
  await page.goto("/");
  const summary = page.getByRole("region", { name: "Summary", exact: true });
  const context = summary.getByRole("region", { name: "Context" });
  const toggle = context.locator("summary");
  await expect(toggle).toHaveText("ContextWhere this change sits in the codebase");
  await expect(context.getByRole("heading", { name: "Area" })).toBeHidden();

  await toggle.click();
  for (const label of ["Area", "How it's reached", "Contracts", "Related code"])
    await expect(context.getByRole("heading", { level: 4, name: label })).toBeVisible();
  await expect(context).toContainText("runs taskline's background jobs");
  await expect(context.getByText("queue/worker.go · Worker.Run")).toBeVisible();

  await toggle.press("Enter");
  await expect(context.getByRole("heading", { name: "Area" })).toBeHidden();
});

test("a document without a context shows none", async ({ page }) => {
  const document = sampleDocument();
  delete document.summary.context;
  await serveDocument(page, document);
  await page.goto("/");
  await expect(page.getByRole("region", { name: "Summary", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Context" })).toHaveCount(0);
});
