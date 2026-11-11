import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

const row = (page: Page, id: string) => page.locator(`[data-prompt="${id}"]`);
// The row's own button, which opens and closes its editor.
const toggle = (page: Page, id: string) => row(page, id).locator("button.prompt-row");

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
    await expect(row(page, id)).not.toContainText("Custom");

    // A global replacement starts from the default text.
    await toggle(page, id).click();
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
    await page.getByRole("link", { name: "Settings for acme/demo" }).click();
    const repository = page.getByRole("region", { name: "Repository acme/demo", exact: true });
    await repository.getByRole("button", { name: /^Prompts/ }).click();
    await expect(row(page, id)).toContainText("Global");
    await toggle(page, id).click();
    const own = repository.getByRole("textbox", { name: `${title} prompt` });
    expect(await own.inputValue()).toContain("Always answer in the language");
    await own.fill("A prompt only for acme/demo.");
    await row(page, id).getByRole("button", { name: "Save for this repository" }).click();
    await expect(row(page, id)).toContainText("Custom");

    // The page and its section are in the address, so a reload stays on them.
    await page.reload();
    await expect(row(page, id)).toContainText("Custom");
    await toggle(page, id).click();
    await row(page, id).getByRole("button", { name: "Use inherited" }).click();
    await expect(row(page, id)).toContainText("Global");
    await page.goto(await launchURL());
    await page.getByRole("link", { name: "Settings" }).click();
    await page.getByRole("button", { name: /^Prompts/ }).click();
    await toggle(page, id).click();
    await row(page, id).getByRole("button", { name: "Reset to default" }).click();
    await expect(row(page, id)).not.toContainText("Custom");
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

// Undo is the browser's own, which React's controlled textarea broke: it
// rewrote the field's text on each keystroke, so Ctrl+Z went back one letter.
test("undoes typing in the prompt editor in one step", async ({ page }) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Settings" }).click();
  await page.getByRole("button", { name: /^Prompts/ }).click();
  const first = page
    .getByRole("region", { name: "Prompts", exact: true })
    .locator("[data-prompt]")
    .first();
  await first.locator("button.prompt-row").click();
  const editor = first.getByRole("textbox");
  const seeded = await editor.inputValue();
  await editor.click();
  await page.keyboard.press("Control+End");
  await page.keyboard.type(" Keep it short.");
  await expect(editor).toHaveValue(`${seeded} Keep it short.`);
  await expect(first).toContainText("Unsaved changes");
  await page.keyboard.press("Control+z");
  await expect(editor).toHaveValue(seeded);
  await expect(first).not.toContainText("Unsaved changes");
  await page.keyboard.press("Control+Shift+z");
  await expect(editor).toHaveValue(`${seeded} Keep it short.`);
});
