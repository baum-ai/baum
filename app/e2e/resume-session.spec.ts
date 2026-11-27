import { rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// Continuing an ended session in the session panel, over the fixture daemon's
// real conversations service and its scripted agent. The fixture reads a
// `terminal-<id>` marker in its state directory as a terminal that runs the
// session again (`claude --resume`), as Claude Code's process registry or the
// agent's hooks tell the daemon.

const panel = (page: Page) => page.locator("section.session-panel");

const terminalMarker = (id: string) => join(process.env.BAUM_APP_STATE_DIR ?? "", `terminal-${id}`);

async function openTranscript(page: Page, id: string) {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Sessions", exact: true }).first().click();
  await page.goto(page.url().replace(/\?.*$/, `?page=sessions&session=claude-code%3A${id}`));
}

test.afterEach(() => {
  for (const id of ["ended-e2e", "terminal-e2e"]) rmSync(terminalMarker(id), { force: true });
});

test("continues an ended session in the panel, and waits while a terminal runs it", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await openTranscript(page, "ended-e2e");
  const transcript = page.getByRole("region", { name: "Transcript", exact: true });
  await expect(transcript.getByText("Cover it with a test", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Continue in panel" }).click();
  await expect(panel(page)).toBeVisible();
  const composer = panel(page).getByRole("textbox", { name: "Message to the agent" });
  await composer.fill("Pick up where we left off: is the retry delay capped?");
  await composer.press("Enter");
  await expect(panel(page).getByRole("region", { name: "Transcript" })).toContainText(
    "the retry delay is capped",
  );
  await expect(panel(page).getByRole("button", { name: "Send" })).toBeVisible();

  // The person resumes it in their terminal: the panel does not write to it.
  writeFileSync(terminalMarker("ended-e2e"), "");
  await composer.fill("And how long is the upload timeout?");
  await composer.press("Enter");
  await expect(panel(page).getByText(/running in a terminal/)).toBeVisible();

  // Once that terminal exits it, the panel goes on.
  rmSync(terminalMarker("ended-e2e"));
  await composer.fill("And how long is the upload timeout?");
  await composer.press("Enter");
  await expect(panel(page).getByRole("region", { name: "Transcript" })).toContainText(
    "You asked: And how long is the upload timeout?",
  );
});

test("does not continue a session a terminal runs", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  writeFileSync(terminalMarker("terminal-e2e"), "");
  await openTranscript(page, "terminal-e2e");
  const transcript = page.getByRole("region", { name: "Transcript", exact: true });
  await expect(transcript.getByText("Tighten the upload timeout", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Continue in panel" }).click();
  await expect(page.getByText("The session could not be continued")).toBeVisible();
  await expect(page.getByText(/running in a terminal/)).toBeVisible();
  await expect(panel(page)).toHaveCount(0);
});
