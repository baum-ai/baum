import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test } from "@playwright/test";

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

test("a local feature opens its decision and redacted source transcript", async ({ page }) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Timeline", exact: true }).click();
  const features = page.getByRole("list", { name: "Features" });
  // A wide screen opens the feature beside the timeline.
  await features
    .getByRole("button", { name: /Local decision log/ })
    .first()
    .click();
  await expect(page).toHaveURL(/feature=feature-local/);
  const panel = page.locator("#opened-feature-panel");
  await expect(panel).toContainText("Serve the decision log locally");
  await panel.getByRole("button", { name: /#12 Serve the decision log locally/ }).click();
  await expect(panel).toContainText("Adds the local Timeline API and page.");
  await panel.getByRole("button", { name: /Serve the Timeline from the daemon/ }).click();
  await expect(panel).toContainText("Sync the hosted log down");
  // The prompt it cites, from the transcript on this computer, masked.
  await expect(panel).toContainText("Serve the Timeline from the daemon's own store; ask");
  await expect(panel).not.toContainText("ada.lovelace@example.com");
  // The session opens at the cited turn, though no tracker lists it.
  await panel.getByRole("button", { name: "Open session at turn 1" }).click();
  const card = panel.getByRole("region", { name: "Transcript" });
  await expect(card).toContainText("Draw it with the review viewer's decision log components.");
  await expect(card).not.toContainText("ada.lovelace@example.com");
});
