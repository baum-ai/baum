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

/** The sample with the reviewer's bottom line and prose that names things by ID. */
function documentWithRecommendation() {
  const document = sampleDocument();
  document.summary.recommendation = {
    decision: "look_first",
    text: "The operator role is read from a header the client can set; check it before anything else.",
    refs: ["sec_api", "rsk_api_header"],
  };
  document.summary.text = `${document.summary.text} The open thread PRRT_example_03 and rsk_api_header both concern it.`;
  return document;
}

test("the recommendation follows the summary and its chips open the section", async ({
  page,
  browserErrors,
}, testInfo) => {
  await serveDocument(page, documentWithRecommendation());
  await page.goto("/");

  const summary = page.getByRole("region", { name: "Summary", exact: true });
  const recommendation = summary.getByRole("region", { name: "Recommendation" });
  await expect(recommendation).toContainText("Look first");
  await expect(recommendation).toContainText("read from a header the client can set");
  // It follows the summary prose as a normal section.
  const prose = summary.getByText("Jobs that fail now back off exponentially");
  const [recommendationTop, proseTop] = await Promise.all([
    recommendation.evaluate((element) => element.getBoundingClientRect().top),
    prose.evaluate((element) => element.getBoundingClientRect().top),
  ]);
  expect(recommendationTop).toBeGreaterThan(proseTop);

  // A reference in the prose is a chip for the thread's place and state.
  const thread = summary.getByRole("link", { name: /auth\.go:28/ });
  await expect(thread).toContainText("open");
  await expect(thread).toHaveAttribute("href", /discussion_r7202$/);

  if (process.env.E2E_SCREENSHOTS) {
    // Let the page finish fading in.
    await page.waitForTimeout(800);
    await recommendation.scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath("summary-desktop.png") });
    await page.setViewportSize({ width: 390, height: 844 });
    await recommendation.scrollIntoViewIfNeeded();
    await page.screenshot({ path: testInfo.outputPath("summary-phone.png") });
    await page.setViewportSize({ width: 1280, height: 720 });
  }

  await recommendation.getByRole("button", { name: "Operator-only dead-letter endpoints" }).click();
  await expect(page).toHaveURL(/section=sec_api/);
  expect(browserErrors).toEqual([]);
});

test("a recommendation does not widen the page on a phone", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await serveDocument(page, documentWithRecommendation());
  await page.goto("/");
  await expect(page.getByRole("region", { name: "Recommendation" })).toBeVisible();
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
});
