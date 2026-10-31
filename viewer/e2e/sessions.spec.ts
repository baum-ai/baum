import { expect, serveSessions, test } from "./fixtures.ts";
import { linkedSessions, sessionId, transcript } from "./local-sessions.ts";

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
