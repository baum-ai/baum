import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

const row = (page: Page, id: string) => page.locator(`[data-prompt="${id}"]`);

// The fixture is a real daemon with a real registry, so the prompt overrides
// are stored and read back as they are on a person's computer.
test("replaces a global prompt, overrides it for a repository and resets both", async ({
  page,
}) => {
  test.setTimeout(90000);
  await page.setViewportSize({ width: 1280, height: 1100 });
  let id = "";
  try {
    await page.goto(await launchURL());
    await page.getByRole("link", { name: "Settings" }).click();
    await page.getByRole("button", { name: /^Prompts/ }).click();
    const section = page.getByRole("region", { name: "Prompts", exact: true });
    await expect(section.getByRole("region", { name: "Reviews" })).toBeVisible();
    await expect(section.getByRole("region", { name: "Fixes" })).toBeVisible();
    await expect(section.getByRole("region", { name: "Data extraction" })).toBeVisible();
    const first = section.locator("[data-prompt]").first();
    id = (await first.getAttribute("data-prompt"))!;
    const title = (await first.locator(".settings-row-label").innerText()).replace(
      /\s+(Default|Custom)$/,
      "",
    );
    await expect(row(page, id)).toContainText("Default");

    // A global replacement starts from the default text.
    await row(page, id)
      .getByRole("button", { name: /^Customise/ })
      .click();
    const editor = page.getByRole("textbox", { name: `${title} prompt` });
    const seeded = await editor.inputValue();
    expect(seeded.length).toBeGreaterThan(20);
    await editor.fill(`${seeded}\nAlways answer in the language of the pull request.`);
    await row(page, id).getByRole("button", { name: "Save", exact: true }).click();
    await expect(row(page, id).getByRole("status")).toHaveText("Saved");
    await expect(row(page, id)).toContainText("Custom");
    await page.reload();
    await expect(row(page, id)).toContainText("Custom");

    // A repository can override it, and falls back to the global one.
    await page.getByRole("link", { name: "Repositories", exact: true }).click();
    await page.getByRole("button", { name: "Settings for acme/demo" }).click();
    const dialog = page.getByRole("dialog", { name: "Repository acme/demo", exact: true });
    await dialog.getByText(/^Prompts/).click();
    await expect(row(page, id)).toContainText("Uses global");
    await row(page, id)
      .getByRole("button", { name: /^Customise for this repository/ })
      .click();
    const own = dialog.getByRole("textbox", { name: `${title} prompt` });
    expect(await own.inputValue()).toContain("Always answer in the language");
    await own.fill("A prompt only for acme/demo.");
    await row(page, id).getByRole("button", { name: "Save for this repository" }).click();
    await expect(row(page, id)).toContainText("Custom for this repository");
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();

    await page.reload();
    await page.getByRole("button", { name: "Settings for acme/demo" }).click();
    await dialog.getByText(/^Prompts/).click();
    await expect(row(page, id)).toContainText("Custom for this repository");
    await row(page, id).getByRole("button", { name: /^Edit/ }).click();
    await row(page, id).getByRole("button", { name: "Use inherited" }).click();
    await expect(row(page, id)).toContainText("Uses global");
    await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
    await page.goto(await launchURL());
    await page.getByRole("link", { name: "Settings" }).click();
    await page.getByRole("button", { name: /^Prompts/ }).click();
    await row(page, id).getByRole("button", { name: /^Edit/ }).click();
    await row(page, id).getByRole("button", { name: "Reset to default" }).click();
    await expect(row(page, id)).toContainText("Default");
  } finally {
    // Leave the fixture as it was, whatever failed above.
    if (id) {
      await page.goto(await launchURL());
      await expect(page.getByRole("link", { name: "Settings" })).toBeVisible();
      await page.evaluate(async (prompt) => {
        for (const path of [
          `api/repositories/acme/demo/prompts/${prompt}`,
          `api/settings/prompts/${prompt}`,
        ])
          await fetch(new URL(path, location.href), {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ text: null }),
          });
      }, id);
    }
  }
});
