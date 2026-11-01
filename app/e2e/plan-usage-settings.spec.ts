import { expect, test } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// The fixture's backend is Claude Code, so its plan shows by default and
// Codex's does not, though the daemon can read both.
test("chooses which plans the sidebar shows and keeps the choice", async ({ page }) => {
  await page.goto(await launchURL());
  const rail = page.locator(".plan-rail");
  await expect(rail.locator('[data-provider="claude"]')).toBeVisible();
  await expect(rail.locator('[data-provider="codex"]')).toHaveCount(0);

  await page.getByRole("link", { name: "Settings" }).click();
  const codex = page.getByRole("switch", { name: "Show Codex plan usage" });
  const claude = page.getByRole("switch", { name: "Show Claude Code plan usage" });
  try {
    await expect(claude).toBeChecked();
    await expect(codex).not.toBeChecked();
    await expect(page.getByText("Hidden because baum does not use it.")).toBeVisible();

    await codex.click();
    await expect(rail.locator('[data-provider="codex"]')).toBeVisible();
    await page.reload();
    await expect(page.getByRole("switch", { name: "Show Codex plan usage" })).toBeChecked();
    await expect(rail.locator('[data-provider="codex"]')).toBeVisible();

    await claude.click();
    await expect(rail.locator('[data-provider="claude"]')).toHaveCount(0);
    await expect(rail.locator('[data-provider="codex"]')).toBeVisible();
  } finally {
    // Leave the fixture's choices as they were for the specs that follow.
    for (const name of ["Codex", "Claude Code"]) {
      const reset = page.getByRole("button", { name: /^Use baum's default/ });
      const before = await reset.count();
      if (before > 0) {
        await reset.first().click();
        await expect(page.getByRole("button", { name: /^Use baum's default/ })).toHaveCount(
          before - 1,
        );
      }
      await expect(page.getByRole("switch", { name: `Show ${name} plan usage` })).toBeVisible();
    }
  }
  await expect(rail.locator('[data-provider="codex"]')).toHaveCount(0);
  await expect(rail.locator('[data-provider="claude"]')).toBeVisible();
});
