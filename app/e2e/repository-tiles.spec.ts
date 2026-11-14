import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test } from "@playwright/test";

// The fixture runs the real daemon, which publishes its launch URL in a private
// credential file once it serves.
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

test("a repository's tile layout and pinned tiles save from its page and survive a reload", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Repositories", exact: true }).click();
  await page.getByRole("link", { name: "Settings for acme/demo" }).click();
  const repository = page.getByRole("region", { name: "Repository acme/demo", exact: true });
  const text = repository.getByLabel("Tile layout", { exact: true });
  await expect(text).toBeVisible();
  await expect(text).toHaveValue("");

  // Leaving the box saves it, as the page's other controls save.
  await text.fill("Lead with what changed in the data model.");
  await text.blur();
  await expect(repository.getByRole("status").first()).toHaveText("Saved");
  await repository.locator("#repository-tile-pin").selectOption("schema_change");
  const pinned = repository.getByRole("list", { name: "Pinned tiles" });
  await expect(pinned).toContainText("Schema changes");
  await expect(repository.getByRole("status").first()).toHaveText("Saved");

  // The daemon kept both, and a reload shows them.
  await page.reload();
  await expect(text).toHaveValue("Lead with what changed in the data model.", { timeout: 15000 });
  await expect(pinned).toContainText("Schema changes");

  // Another control's save keeps them.
  await repository.getByRole("switch", { name: /Capture decisions/ }).click();
  await expect(repository.getByRole("status").first()).toHaveText("Saved");
  await repository.getByRole("switch", { name: /Capture decisions/ }).click();
  await expect(repository.getByRole("status").first()).toHaveText("Saved");
  await page.reload();
  await expect(text).toHaveValue("Lead with what changed in the data model.", { timeout: 15000 });
  await expect(pinned).toContainText("Schema changes");

  // Clearing both leaves the repository with none.
  await repository.getByRole("button", { name: "Unpin Schema changes" }).click();
  await expect(repository.getByRole("status").first()).toHaveText("Saved");
  await text.fill("");
  await text.blur();
  await expect(repository.getByRole("status").first()).toHaveText("Saved");
  await page.reload();
  await expect(text).toHaveValue("", { timeout: 15000 });
  await expect(repository.getByRole("list", { name: "Pinned tiles" })).toHaveCount(0);
  expect(errors).toEqual([]);
});
