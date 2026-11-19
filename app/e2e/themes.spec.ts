import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// Custom colour themes against the fixture daemon: the light and dark slots in
// Settings, and a theme the (scripted) agent proposes, which the app previews
// until the person saves or dismisses it.

const html = (page: Page) => page.locator("html");
const paper = (page: Page) =>
  page.evaluate(() => document.documentElement.style.getPropertyValue("--color-paper"));
const panel = (page: Page) => page.locator("section.session-panel");

type Appearance = {
  theme: string;
  light_theme: string;
  dark_theme: string;
  themes: { id: string; name: string; base: string; builtin?: boolean }[];
};

/** Puts the themes back as a fresh installation has them, so the specs that
 *  follow see the built-ins in both slots and the system setting. */
async function reset(page: Page) {
  if (!page.url().includes("/s/")) return;
  await page.evaluate(async () => {
    await fetch("api/settings/appearance", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        theme: "system",
        light_theme: "default-light",
        dark_theme: "default-dark",
      }),
    });
    const { themes } = (await (await fetch("api/settings/appearance")).json()) as Appearance;
    for (const theme of themes)
      if (!theme.builtin)
        await fetch(`api/settings/appearance/themes/${theme.id}`, {
          method: "DELETE",
          headers: { "Content-Type": "application/json" },
          body: "{}",
        });
  });
}

test.afterEach(async ({ page }) => reset(page));

test("puts a dark theme in the light slot and the page follows its base", async ({ page }) => {
  await page.goto(await launchURL());
  await expect(page.getByRole("link", { name: "Settings", exact: true })).toBeVisible();
  await page.evaluate(() =>
    fetch("api/settings/appearance/themes", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: "Dusk",
        base: "dark",
        tokens: { "--color-paper": "#e8eef4", "--color-shadow": "#10202e" },
        slot: "",
      }),
    }),
  );
  await page.reload();
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  expect(await paper(page)).toBe("");

  const light = page.getByLabel("Light theme", { exact: true });
  const dark = page.getByLabel("Dark theme", { exact: true });
  await expect(light.locator("option")).toHaveText(["Default light", "Default dark", "Dusk"]);
  // Any base sits in either slot: the dark theme in the light slot gives a dark page.
  await page.getByLabel("Preferred theme").selectOption("light");
  await light.selectOption({ label: "Dusk" });
  await expect(html(page)).toHaveAttribute("data-theme", "dark");
  await expect.poll(() => paper(page)).toBe("#e8eef4");
  await page.screenshot({
    path: "test-results/themes-dark-in-light-slot.png",
    animations: "disabled",
  });

  // The dark slot is used when the preferred theme is dark.
  await page.getByLabel("Preferred theme").selectOption("dark");
  await expect(html(page)).toHaveAttribute("data-theme", "dark");
  await expect.poll(() => paper(page)).toBe("");
  await dark.selectOption({ label: "Dusk" });
  await expect.poll(() => paper(page)).toBe("#e8eef4");
  await light.selectOption({ label: "Default light" });
  await page.getByLabel("Preferred theme").selectOption("light");
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  await expect.poll(() => paper(page)).toBe("");

  // The custom theme is renamed and deleted from its menu.
  await page.getByRole("button", { name: "Actions for Dusk" }).click();
  await page.getByRole("menuitem", { name: "Rename" }).click();
  await page.getByLabel("Name of Dusk").fill("Midnight");
  await page.getByRole("button", { name: "Rename", exact: true }).click();
  await expect(light.locator("option")).toHaveText(["Default light", "Default dark", "Midnight"]);
  await page.getByRole("button", { name: "Actions for Midnight" }).click();
  await page.getByRole("menuitem", { name: "Delete" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  await expect(light.locator("option")).toHaveText(["Default light", "Default dark"]);
  await expect(dark).toHaveValue("default-dark");
});

test("previews a proposed theme, restores it on dismiss and saves it with a name and slot", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(await launchURL());
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  await page.keyboard.press("Control+`");
  const composer = panel(page).getByRole("textbox", { name: "Message to the agent" });
  await expect(composer).toBeFocused();
  await composer.fill("Propose a theme");
  await composer.press("Enter");

  // The proposal is for no page, so it is never "elsewhere", and it shows at once.
  const proposal = panel(page).getByRole("article", { name: "Proposal" });
  await expect(proposal.getByLabel("Name")).toHaveValue("Dusk");
  await expect(proposal.getByLabel("Colours")).toContainText("--color-paper");
  await expect(proposal).not.toContainText("open it to save");
  await expect(html(page)).toHaveAttribute("data-theme", "dark");
  await expect.poll(() => paper(page)).toBe("#e8eef4");
  await expect(proposal.getByLabel("Use it as")).toHaveValue("dark");
  await page.screenshot({ path: "test-results/themes-preview.png", animations: "disabled" });

  // Dismissing restores the saved theme.
  await proposal.getByRole("button", { name: "Dismiss" }).click();
  await expect(proposal).toHaveCount(0);
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  expect(await paper(page)).toBe("");

  // A second proposal, once the first turn is over: a light theme, saved under another name in the dark slot.
  await expect(panel(page).getByText("Working", { exact: true })).toHaveCount(0);
  await composer.fill("Propose a theme named Sand");
  await composer.press("Enter");
  await expect(proposal.getByLabel("Name")).toHaveValue("Sand");
  await expect.poll(() => paper(page)).toBe("#fdf6e3");
  await expect(proposal.getByLabel("Use it as")).toHaveValue("light");
  await proposal.getByLabel("Name").fill("Warm paper");
  await proposal.getByLabel("Use it as").selectOption("dark");
  await proposal.getByRole("button", { name: "Save", exact: true }).click();
  await expect(proposal).toHaveCount(0);

  // Saved into the dark slot: the preview ended and the light slot still rules the page.
  await expect.poll(() => paper(page)).toBe("");
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  const saved = await page.evaluate(async () => {
    const answer = (await (await fetch("api/settings/appearance")).json()) as Appearance;
    return { dark: answer.dark_theme, light: answer.light_theme, themes: answer.themes };
  });
  const theme = saved.themes.find((t) => t.name === "Warm paper");
  expect(theme).toMatchObject({ base: "light" });
  expect(saved.dark).toBe(theme!.id);
  expect(saved.light).toBe("default-light");
});
