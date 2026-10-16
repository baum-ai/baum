import { deferred, expect, serveSessions, test } from "./fixtures.ts";
import { firstCommit, linkedSessions, sessionId, transcript } from "./local-sessions.ts";

const rail = (page: import("@playwright/test").Page) =>
  page.getByRole("navigation", { name: "Review sections" });

test("without sessions on the machine the Sessions group lists the document's own, with the reason", async ({
  page,
}) => {
  await page.goto("/?pane=sessions");

  const pane = page.locator("#review-section");
  await expect(pane.getByRole("heading", { level: 2, name: "Sessions" })).toBeVisible();
  await expect(pane).toContainText(
    "Showing the sessions recorded in the review — the sample document has no sessions on this machine",
  );
  await expect(
    pane.getByRole("button", { name: "Add retry budget and dead letters" }),
  ).toBeVisible();
  // A restricted session says only that it is.
  await expect(pane.getByRole("button", { name: "Details restricted" })).toBeVisible();
  // No Commits group without the machine's commits.
  await expect(rail(page).getByRole("button", { name: /^Commits/ })).toHaveCount(0);
});

test("a failed sessions request falls back to the document's sessions and says so", async ({
  page,
}) => {
  await serveSessions(page, { error: "boom" }, 500);
  await page.goto("/?pane=sessions");

  await expect(page.locator("#review-section")).toContainText(
    "the sessions on this machine could not be read",
  );
  await expect(
    page
      .locator("#review-section")
      .getByRole("button", { name: "Add retry budget and dead letters" }),
  ).toBeVisible();
});

test("the groups load while the sessions are being read, so a link to them survives", async ({
  page,
}) => {
  const gate = deferred();
  await page.route("**/api/sessions", async (route) => {
    await gate.released;
    await route.fulfill({ json: linkedSessions });
  });
  await page.goto("/?pane=commits");

  await expect(page).toHaveURL(/pane=commits/);
  await expect(page.getByRole("heading", { level: 2, name: "Commits" })).toBeVisible();
  await expect(page.locator("#review-section")).not.toContainText("Add the retry budget");

  gate.release();
  await expect(page.locator("#review-section")).toContainText("Add the retry budget");
  await expect(page.locator("#review-section")).toContainText("Tidy the docs");
});

test("with the machine's sessions, Sessions and Commits list them and open each other", async ({
  page,
}) => {
  await serveSessions(page, linkedSessions);
  await page.goto("/?pane=sessions");
  const pane = page.locator("#review-section");

  await expect(pane).toContainText("1 session");
  await expect(pane).not.toContainText("Showing the sessions recorded in the review");
  await expect(pane.getByRole("button", { name: "Wire the retry budget" })).toBeVisible();
  await expect(rail(page).getByRole("button", { name: /^Commits/ })).toBeVisible();

  await rail(page)
    .getByRole("button", { name: /^Commits/ })
    .click();
  await expect(page).toHaveURL(/pane=commits/);
  await expect(pane).toContainText("Add the retry budget");
  await expect(pane).toContainText("Example Developer");

  await pane.getByRole("button", { name: /Add the retry budget/ }).click();
  await expect(page).toHaveURL(new RegExp(`commit=${firstCommit}`));
  await expect(pane).toContainText("Named in the session");
  await expect(pane).toContainText("95%");
});

test("a linked session opens its redacted transcript, asked for only then", async ({ page }) => {
  const transcriptRequests: string[] = [];
  await serveSessions(page, linkedSessions);
  await page.route("**/api/sessions/*", (route) => {
    transcriptRequests.push(new URL(route.request().url()).pathname);
    return route.fulfill({ json: transcript });
  });
  await page.goto("/?pane=sessions");
  await expect(page.getByRole("button", { name: "Wire the retry budget" })).toBeVisible();
  expect(transcriptRequests).toEqual([]);

  await page.getByRole("button", { name: "Wire the retry budget" }).click();

  await expect(page).toHaveURL(new RegExp(`pane=session&session=${sessionId}`));
  const card = page.getByLabel("Transcript");
  await expect(card).toContainText("Add a retry budget to the worker");
  await expect(card).toContainText("Now cover it with tests");
  await expect(card).toContainText("The budget is wired.");
  await expect(card).not.toContainText("~/src/taskline");
  expect(transcriptRequests).toEqual([`/api/sessions/${sessionId}`]);

  await page.getByRole("button", { name: "Agent activity for prompt 1" }).click();
  await expect(card).toContainText("Reading the worker first.");
  await expect(card).toContainText("go test ./...");
  // The activity came with the transcript.
  expect(transcriptRequests).toHaveLength(1);
});

test("a transcript that fails to load says so and Retry loads it", async ({ page }) => {
  let failing = true;
  await serveSessions(page, linkedSessions);
  await page.route("**/api/sessions/*", (route) =>
    failing
      ? route.fulfill({ status: 500, json: { error: "boom" } })
      : route.fulfill({ json: transcript }),
  );
  await page.goto(`/?pane=session&session=${sessionId}`);

  await expect(page.locator("#review-section")).toContainText(
    "The transcript could not be loaded.",
  );

  failing = false;
  await page.locator("#review-section").getByRole("button", { name: "Retry", exact: true }).click();
  await expect(page.getByLabel("Transcript")).toContainText("Add a retry budget to the worker");
});

test("a session the machine does not link falls back to the list", async ({ page }) => {
  await serveSessions(page, linkedSessions);
  await page.route("**/api/sessions/*", (route) => route.fulfill({ status: 404, json: {} }));
  await page.goto("/?pane=session&session=bbbbbbbb-0000-4000-8000-000000000009");

  await expect(page.locator("#review-section")).not.toContainText("Add a retry budget");
});

test("the Activity tab lists the document's own sessions, each card opening its session", async ({
  page,
}) => {
  await page.goto("/");

  await page.getByRole("tab", { name: "Activity" }).click();

  const activity = page.getByRole("tabpanel", { name: "Activity" });
  // CI leads the tab, before the exchange.
  await expect(activity.getByRole("region").first()).toHaveAccessibleName("Checks");
  await expect(activity).toContainText("Acme-agent");
  // One of the sample's five threads is still open, a must-fix finding: it
  // leads the exchange with the way to it, and the critical count is red.
  const exchange = activity.getByRole("region", { name: "Review exchange" });
  await expect(exchange).toContainText("1 unresolved thread of 5");
  await expect(exchange).toContainText("RoleFromRequest trusts a client-controlled header");
  await expect(exchange.getByRole("link", { name: "api/auth.go:28" })).toHaveAttribute(
    "href",
    /discussion_r7202$/,
  );
  const critical = exchange
    .locator("dt", { hasText: "critical issue found" })
    .locator("~ dd")
    .first();
  await expect(critical).toHaveCSS("color", "rgb(186, 58, 49)");

  // The card is the target, not only the harness at its head: its prompts
  // are in the session it opens. The harness's hit area lies over the title,
  // so the click lands where a pointer would rather than on the title itself.
  await activity
    .getByRole("region", { name: "Linked sessions" })
    .getByText("Add retry budget and dead letters")
    .click({ force: true });
  await expect(page).toHaveURL(/pane=session&session=785faefe-40b5-5521-9932-4dc2d659ec1d/);
});
