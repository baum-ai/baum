import { expect, test } from "./fixtures.ts";

type Rgb = [number, number, number];

/** The sRGB colour a computed `rgb(…)` or `color(srgb …)` value names. */
function parse(color: string): Rgb {
  const numbers = color.match(/-?\d*\.?\d+/g)?.map(Number) ?? [];
  if (color.startsWith("color(srgb")) return numbers.slice(0, 3).map((n) => n * 255) as Rgb;
  return numbers.slice(0, 3) as Rgb;
}

function luminance([r, g, b]: Rgb) {
  const [lr, lg, lb] = [r, g, b].map((channel) => {
    const value = channel / 255;
    return value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  }) as Rgb;
  return 0.2126 * lr + 0.7152 * lg + 0.0722 * lb;
}

function contrast(foreground: Rgb, background: Rgb) {
  const [light, dark] = [luminance(foreground), luminance(background)].toSorted((a, b) => b - a);
  return (light! + 0.05) / (dark! + 0.05);
}

async function colours(page: import("@playwright/test").Page) {
  return page.evaluate(() => {
    const heading = document.querySelector("h2")!;
    return {
      theme: document.documentElement.dataset.theme ?? null,
      page: getComputedStyle(document.body).backgroundColor,
      heading: getComputedStyle(heading).color,
      scheme: getComputedStyle(document.documentElement).colorScheme,
    };
  });
}

for (const scheme of ["light", "dark"] as const) {
  test(`${scheme} follows the system and keeps the text readable on its page`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto("/?pane=section&section=sec_retry");
    await expect(
      page.getByRole("heading", { level: 2, name: "Retry policy and backoff" }),
    ).toBeVisible();

    const seen = await colours(page);

    expect(seen.theme).toBe(scheme === "dark" ? "dark" : null);
    expect(contrast(parse(seen.heading), parse(seen.page))).toBeGreaterThan(7);
    const surface = luminance(parse(seen.page));
    if (scheme === "dark") expect(surface).toBeLessThan(0.1);
    else expect(surface).toBeGreaterThan(0.7);
  });
}

test("the theme changes with the system while the page is open", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/");
  await expect(page.locator("html")).not.toHaveAttribute("data-theme", "dark");

  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");

  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("html")).not.toHaveAttribute("data-theme", "dark");
});

test("highlighted code is coloured in both themes, with a different colour in each", async ({
  page,
}) => {
  const tokenColour = async (colorScheme: "light" | "dark") => {
    await page.emulateMedia({ colorScheme });
    await page.goto("/?pane=section&section=sec_retry");
    await page
      .locator("#review-section")
      .getByRole("region", { name: "Behavior", exact: true })
      .locator("details summary")
      .first()
      .click();
    // Highlighting is loaded after the plain text is drawn.
    const token = page.locator('#review-section article span[style*="--syntax"]').first();
    await expect(token).toBeVisible();
    return token.evaluate((element) => getComputedStyle(element).color);
  };

  const light = await tokenColour("light");
  const dark = await tokenColour("dark");

  expect(light).not.toBe(dark);
});
