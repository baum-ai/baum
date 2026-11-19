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
  await page.getByRole("button", { name: /^Appearance/ }).click();
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  expect(await paper(page)).toBe("");

  const group = (name: string) => page.getByRole("radiogroup", { name, exact: true });
  const thumb = (slot: string, theme: string) => group(slot).locator(`label[title="${theme}"]`);
  const light = group("Light theme");
  const dark = group("Dark theme");
  const mode = (name: string) =>
    page.getByRole("group", { name: "Theme mode" }).getByRole("button", { name, exact: true });
  await expect(light.getByRole("radio")).toHaveCount(7);
  await expect(light.getByRole("radio", { name: "Dusk" })).toBeAttached();
  await expect(page.getByRole("heading", { name: "Light theme · in use" })).toBeVisible();

  // A dark miniature on a light page computes a dark page colour, a light one a light colour.
  const mini = (theme: string) =>
    page.locator(`label[title="${theme}"]`).locator(".theme-mini").first();
  const lightness = (theme: string) =>
    mini(theme)
      .locator(".theme-mini-panel")
      .evaluate((element) => {
        const [r, g, b] = getComputedStyle(element).backgroundColor.match(/\d+/g)!.map(Number);
        return (r! + g! + b!) / 3;
      });
  expect(await lightness("Default light")).toBeGreaterThan(200);
  expect(await lightness("Default dark")).toBeLessThan(80);
  const surface = (theme: string) =>
    mini(theme).evaluate((element) =>
      getComputedStyle(element).getPropertyValue("--surface-page").trim(),
    );
  expect(await surface("Default dark")).not.toBe(await surface("Default light"));
  await page.screenshot({ path: "test-results/appearance-light.png", animations: "disabled" });

  // Any base sits in either slot: the dark theme in the light slot gives a dark page.
  await mode("Light").click();
  await thumb("Light theme", "Dusk").click();
  await expect(light.getByRole("radio", { name: "Dusk" })).toBeChecked();
  await expect(html(page)).toHaveAttribute("data-theme", "dark");
  await expect.poll(() => paper(page)).toBe("#e8eef4");
  await page.screenshot({
    path: "test-results/themes-dark-in-light-slot.png",
    animations: "disabled",
  });
  await page.screenshot({ path: "test-results/appearance-dark.png", animations: "disabled" });

  // Arrow keys move within a row of thumbnails, as native radios do.
  await light.getByRole("radio", { name: "Dusk" }).focus();
  await page.keyboard.press("ArrowLeft");
  await expect(light.getByRole("radio", { name: "Ember" })).toBeChecked();
  await thumb("Light theme", "Dusk").click();

  // The dark slot is used when the theme mode is dark.
  await mode("Dark").click();
  await expect(html(page)).toHaveAttribute("data-theme", "dark");
  await expect.poll(() => paper(page)).toBe("");
  await thumb("Dark theme", "Dusk").click();
  await expect.poll(() => paper(page)).toBe("#e8eef4");
  await thumb("Light theme", "Default light").click();
  await mode("Light").click();
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  await expect.poll(() => paper(page)).toBe("");
  await expect(dark.getByRole("radio", { name: "Dusk" })).toBeChecked();

  // The custom theme is renamed and deleted from its menu.
  await page.getByRole("button", { name: "Actions for Dusk in light theme" }).click();
  await page.getByRole("menuitem", { name: "Rename" }).click();
  await page.getByLabel("Name of Dusk").fill("Midnight");
  await page.getByRole("button", { name: "Rename", exact: true }).click();
  await expect(light.getByRole("radio")).toHaveCount(7);
  await expect(light.getByRole("radio", { name: "Midnight" })).toBeAttached();
  await page.getByRole("button", { name: "Actions for Midnight in light theme" }).click();
  await page.getByRole("menuitem", { name: "Delete" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  await expect(light.getByRole("radio")).toHaveCount(6);
  await expect(dark.getByRole("radio", { name: "Default dark" })).toBeChecked();
});

test("lists the six built-in themes in order and applies Pine in the dark slot", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1600, height: 1000 });
  await page.goto(await launchURL());
  await expect(page.getByRole("link", { name: "Settings", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await page.getByRole("button", { name: /^Appearance/ }).click();

  const names = ["Default light", "Default dark", "Birch", "Parchment", "Pine", "Ember"];
  for (const slot of ["Light theme", "Dark theme"]) {
    const radios = page.getByRole("radiogroup", { name: slot, exact: true }).getByRole("radio");
    await expect(radios).toHaveCount(6);
    expect(await radios.evaluateAll((all) => all.map((r) => r.getAttribute("aria-label")))).toEqual(
      names,
    );
    // Built-ins have no menu: they cannot be renamed or deleted.
    await expect(page.getByRole("button", { name: /^Actions for/ })).toHaveCount(0);
  }
  // Every thumbnail draws its own base and colours.
  await expect(page.locator(".theme-mini")).toHaveCount(12);

  const api = await page.evaluate(async () => {
    const answer = (await (await fetch("api/settings/appearance")).json()) as Appearance & {
      themes: { id: string; tokens: Record<string, string> }[];
    };
    return answer.themes.map((t) => [t.id, t.tokens["--color-paper"] ?? ""]);
  });
  expect(api.map(([id]) => id)).toEqual([
    "default-light",
    "default-dark",
    "birch",
    "parchment",
    "pine",
    "ember",
  ]);

  // Pine in the dark slot, with the mode set to dark, puts its tokens on the page.
  await page
    .getByRole("group", { name: "Theme mode" })
    .getByRole("button", { name: "Dark", exact: true })
    .click();
  await expect(html(page)).toHaveAttribute("data-theme", "dark");
  expect(await paper(page)).toBe("");
  await page
    .getByRole("radiogroup", { name: "Dark theme", exact: true })
    .locator('label[title="Pine"]')
    .click();
  await expect.poll(() => paper(page)).toBe("#dce9df");
  await expect(html(page)).toHaveAttribute("data-theme", "dark");
  const surface = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue("--surface-page").trim(),
  );
  expect(surface.toLowerCase()).toBe("#0b130f");

  // A built-in cannot be renamed or deleted through the API either.
  const refused = await page.evaluate(async () => {
    const send = (method: string, body: string) =>
      fetch("api/settings/appearance/themes/pine", {
        method,
        headers: { "Content-Type": "application/json" },
        body,
      }).then((r) => r.status);
    return [await send("PATCH", '{"name":"x"}'), await send("DELETE", "{}")];
  });
  expect(refused).toEqual([400, 400]);
});

test("shows a custom thumbnail's menu on hover and focus, rings it in page colours, and opens it by right click and Ctrl+.", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
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
  await page.getByRole("button", { name: /^Appearance/ }).click();

  const light = page.getByRole("radiogroup", { name: "Light theme", exact: true });
  const option = (name: string) =>
    light.locator(".theme-option").filter({ has: page.locator(`label[title="${name}"]`) });
  const dots = page.getByRole("button", { name: "Actions for Dusk in light theme" });
  const opacity = () => dots.evaluate((el) => getComputedStyle(el).opacity);

  // All six built-ins fit one row at 1440, with the custom theme wrapping after them.
  const tops = await light
    .locator(".theme-option")
    .evaluateAll((all) => all.slice(0, 6).map((el) => Math.round(el.getBoundingClientRect().top)));
  expect(new Set(tops).size).toBe(1);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);

  // Playwright counts opacity: 0 as visible, so read the computed value.
  await page.mouse.move(0, 0);
  expect(await opacity()).toBe("0");
  await option("Dusk").hover();
  await expect.poll(opacity).toBe("1");
  await page.mouse.move(0, 0);
  await expect.poll(opacity).toBe("0");
  await light.getByRole("radio", { name: "Dusk" }).focus();
  await expect.poll(opacity).toBe("1");
  await light.getByRole("radio", { name: "Dusk" }).blur();
  // The chip has a background of its own, so it reads over a dark thumbnail.
  expect(await dots.evaluate((el) => getComputedStyle(el).backgroundColor)).not.toBe(
    "rgba(0, 0, 0, 0)",
  );

  // The selected ring is drawn in the page's colour, whatever the theme in the thumbnail.
  await page
    .getByRole("group", { name: "Theme mode" })
    .getByRole("button", { name: "Light", exact: true })
    .click();
  for (const name of [
    "Default light",
    "Default dark",
    "Birch",
    "Parchment",
    "Pine",
    "Ember",
    "Dusk",
  ]) {
    await light.locator(`label[title="${name}"]`).click();
    const ring = await option(name).evaluate((el) => {
      const frame = el.querySelector(".theme-mini-frame") as HTMLElement;
      const probe = document.createElement("span");
      probe.style.color = "var(--text-primary)";
      el.append(probe);
      const page = getComputedStyle(probe).color;
      probe.remove();
      const outline = getComputedStyle(frame);
      return { color: outline.outlineColor, width: outline.outlineWidth, page };
    });
    expect(ring.width).toBe("2px");
    expect(ring.color).toBe(ring.page);
  }
  // Selecting Dusk made the page dark; the ring follows the page, not the thumbnail.

  // Right click opens the menu; Escape closes it; Ctrl+. on the focused radio does too.
  await option("Dusk").locator(".theme-mini").click({ button: "right" });
  await expect(page.getByRole("menuitem", { name: "Rename" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("menu")).toHaveCount(0);
  await light.getByRole("radio", { name: "Dusk" }).focus();
  await page.keyboard.press("Control+.");
  await expect(page.getByRole("menuitem", { name: "Delete" })).toBeVisible();
  await page.keyboard.press("Escape");
  // A built-in has neither: Ctrl+. opens nothing.
  await light.getByRole("radio", { name: "Birch" }).focus();
  await page.keyboard.press("Control+.");
  await expect(page.getByRole("menu")).toHaveCount(0);
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

  // A newer proposal replaces the open one: only Sand is left and previewed.
  await expect(panel(page).getByText("Working", { exact: true })).toHaveCount(0);
  await composer.fill("Propose a theme named Sand");
  await composer.press("Enter");
  await expect(proposal.getByLabel("Name")).toHaveValue("Sand");
  await expect(proposal).toHaveCount(1);
  await expect.poll(() => paper(page)).toBe("#fdf6e3");
  await expect(proposal.getByLabel("Use it as")).toHaveValue("light");

  // Dismissing it restores the saved theme; the replaced Dusk does not come back.
  await proposal.getByRole("button", { name: "Dismiss" }).click();
  await expect(proposal).toHaveCount(0);
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  expect(await paper(page)).toBe("");

  // Another Sand, saved under a different name in the dark slot.
  await expect(panel(page).getByText("Working", { exact: true })).toHaveCount(0);
  await composer.fill("Propose a theme named Sand");
  await composer.press("Enter");
  await expect(proposal.getByLabel("Name")).toHaveValue("Sand");
  await expect.poll(() => paper(page)).toBe("#fdf6e3");
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

test("tweaking a custom theme twice updates it in place and adds no theme", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto(await launchURL());
  await expect(html(page)).toHaveAttribute("data-theme", "light");
  const read = () =>
    page.evaluate(async () => {
      const answer = (await (await fetch("api/settings/appearance")).json()) as Appearance & {
        themes: { id: string; name: string; tokens: Record<string, string> }[];
      };
      return answer;
    });
  const id = await page.evaluate(async () => {
    const answer = await (
      await fetch("api/settings/appearance/themes", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: "Mine",
          base: "dark",
          tokens: { "--color-paper": "#111111" },
          slot: "light",
        }),
      })
    ).json();
    return answer.themes.find((t: { name: string }) => t.name === "Mine").id as string;
  });
  const before = await read();
  await page.reload();
  await page.keyboard.press("Control+`");
  const composer = panel(page).getByRole("textbox", { name: "Message to the agent" });
  await expect(composer).toBeFocused();
  const proposal = panel(page).getByRole("article", { name: "Proposal" });

  for (const colour of ["#e8ee01", "#e8ee02"]) {
    await expect(panel(page).getByText("Working", { exact: true })).toHaveCount(0);
    await composer.fill(`Tweak theme ${id}`);
    await composer.press("Enter");
    // The card updates the theme it edits; the slot choice is not offered.
    await expect(proposal.getByRole("button", { name: "Update Mine" })).toBeVisible();
    await expect(proposal.getByRole("button", { name: "Save as new theme" })).toBeVisible();
    await expect(proposal.getByLabel("Use it as")).toHaveCount(0);
    await expect.poll(() => paper(page)).toBe(colour);
    await proposal.getByRole("button", { name: "Update Mine" }).click();
    await expect(proposal).toHaveCount(0);
    // The saved theme is what the page shows now: the preview ended on it.
    await expect.poll(() => paper(page)).toBe(colour);
    const now = await read();
    expect(now.themes.length).toBe(before.themes.length);
    expect(now.light_theme).toBe(id);
    expect(now.themes.find((t) => t.id === id)?.tokens["--color-paper"]).toBe(colour);
  }
});
