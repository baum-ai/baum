import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";

// Tab moves by place (`useTabStops`), and nothing changes for whoever uses the
// pointer or j and k: a click gives up its focus, so no ring shows after it.

/** The focused element, in a few words: its sidebar or list place and text. */
const focused = (page: Page) =>
  page.evaluate(() => {
    const element = document.activeElement as HTMLElement | null;
    if (!element || element === document.body) return { place: "body", text: "" };
    const place = element.closest(".local-rail")
      ? "sidebar"
      : element.closest("[data-row-id], [data-row]")
        ? "row"
        : element.closest("dialog")
          ? "dialog"
          : "page";
    return {
      place,
      text: (element.getAttribute("aria-label") || element.textContent || "").trim(),
      selected: !!element.closest("[data-focused]"),
      ring: element.matches(":focus-visible"),
      settings: element.closest(".settings-nav")
        ? "sections"
        : element.closest(".settings-content")
          ? "content"
          : "",
    };
  });

/** Presses Tab until the focus is in `place`, at most `limit` times. */
async function tabTo(page: Page, place: string, limit = 30, key = "Tab") {
  for (let i = 0; i < limit; i++) {
    await page.keyboard.press(key);
    if ((await focused(page)).place === place) return;
  }
  throw new Error(`Tab never reached the ${place}`);
}

test("Tab on the Inbox: the sidebar and the list are one stop each", async ({ page }) => {
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible();
  await page.keyboard.press("Tab");
  // The sidebar's stop is its current page; ↓ moves within it.
  const inboxLink = await focused(page);
  expect(inboxLink).toMatchObject({ place: "sidebar", text: expect.stringMatching(/^Inbox/) });
  await page.keyboard.press("ArrowDown");
  expect((await focused(page)).place).toBe("sidebar");
  expect((await focused(page)).text).not.toBe(inboxLink.text);
  // One more Tab leaves the sidebar, however many links it has.
  await page.keyboard.press("Tab");
  expect((await focused(page)).place).not.toBe("sidebar");
  // The list's stop is its selected row, so j and k go on from it.
  await tabTo(page, "row");
  expect(await focused(page)).toMatchObject({ place: "row", selected: true });
  await expect(page.locator("button.upnext-row[aria-current]")).toBeFocused();
  // The next Tab leaves the list rather than going through its rows.
  await page.keyboard.press("Tab");
  expect((await focused(page)).place).not.toBe("row");
  // Shift+Tab comes back to it.
  await page.keyboard.press("Shift+Tab");
  await expect(page.locator("button.upnext-row[aria-current]")).toBeFocused();
});

test("Tab on Pull requests lands on the focused row; Escape from a control returns to it", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  const inbox = page.getByRole("region", { name: "Pull request inbox" });
  await expect(inbox.locator("[data-row-id]").first()).toBeVisible();
  await page.keyboard.press("j");
  await page.keyboard.press("j");
  const row = inbox.locator("[data-row-id][data-focused]");
  await expect(row).toHaveCount(1);
  const title = (await row.locator(".inbox-pr-title").textContent())!.trim();
  // Shift+Tab leaves the list for a control of the page.
  await page.keyboard.press("Shift+Tab");
  expect((await focused(page)).place).not.toBe("row");
  // Escape there hands the focus back to the row, which stays focused.
  await page.keyboard.press("Escape");
  await expect(row.locator(".inbox-pr-title")).toBeFocused();
  await expect(row.locator(".inbox-pr-title")).toHaveText(title);
  // From the sidebar, Tab comes back into the list on that same row.
  await page.keyboard.press("Shift+Tab");
  await tabTo(page, "sidebar", 30, "Shift+Tab");
  await tabTo(page, "row");
  await expect(row.locator(".inbox-pr-title")).toBeFocused();
  // j still moves on from it.
  await page.keyboard.press("j");
  await expect(inbox.locator("[data-row-id][data-focused] .inbox-pr-title")).not.toHaveText(title);
});

test("Tab into a list with no row focused focuses its first row", async ({ page }) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  const inbox = page.getByRole("region", { name: "Pull request inbox" });
  await expect(inbox.locator("[data-row-id]").first()).toBeVisible();
  await expect(inbox.locator("[data-row-id][data-focused]")).toHaveCount(0);
  await tabTo(page, "row");
  const row = inbox.locator("[data-row-id][data-focused]");
  await expect(row).toHaveCount(1);
  await expect(row.locator(".inbox-pr-title")).toBeFocused();
  const first = (await row.locator(".inbox-pr-title").textContent())!;
  // j goes on to the next row rather than choosing the first again.
  await page.keyboard.press("j");
  await expect(inbox.locator("[data-row-id][data-focused] .inbox-pr-title")).not.toHaveText(first);
});

test("Tab on Settings reaches its controls past the sidebar", async ({ page }) => {
  await page.goto(await launchURL());
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  const sections = page.getByRole("navigation", { name: "Settings sections" });
  await expect(sections).toBeVisible();
  const seen: Awaited<ReturnType<typeof focused>>[] = [];
  for (let i = 0; i < 10; i++) {
    await page.keyboard.press("Tab");
    seen.push(await focused(page));
  }
  // The sidebar is one stop, on Settings; then the section list and its
  // controls, each a stop.
  expect(seen[0]).toMatchObject({ place: "sidebar", text: expect.stringMatching(/^Settings/) });
  expect(seen.slice(1).map((stop) => stop.place)).not.toContain("sidebar");
  const settings = seen.map((stop) => stop.settings);
  expect(settings).toContain("sections");
  expect(settings.slice(settings.indexOf("sections"))).toContain("content");
});

test("Tab stays inside a dialog and goes through its controls", async ({ page }) => {
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible();
  // `?` opens the help menu on Show shortcuts; Enter shows the sheet.
  await page.keyboard.press("?");
  await expect(page.getByRole("menuitem", { name: "Show shortcuts" })).toBeFocused();
  await page.keyboard.press("Enter");
  const sheet = page.getByRole("dialog", { name: "Keyboard shortcuts" });
  await expect(sheet).toBeVisible();
  const close = sheet.getByRole("button", { name: "Close", exact: true });
  await page.keyboard.press("Tab");
  await expect(close).toBeFocused();
  // Its only control: Tab and Shift+Tab stay on it.
  await page.keyboard.press("Tab");
  await expect(close).toBeFocused();
  await page.keyboard.press("Shift+Tab");
  await expect(close).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(sheet).toBeHidden();
});

test("a click leaves no focus ring behind, and j and k still move the selection", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible();
  const sidebar = page.getByRole("navigation", { name: "Main" });
  const pulls = sidebar.getByRole("link", { name: "Pull requests", exact: true });
  await pulls.click();
  const inbox = page.getByRole("region", { name: "Pull request inbox" });
  await expect(inbox.locator("[data-row-id]").first()).toBeVisible();
  // The click left no focus behind for a key to paint a ring on.
  await expect.poll(() => page.evaluate(() => document.activeElement === document.body)).toBe(true);
  await page.keyboard.press("j");
  // No ring on the link clicked: the focus went to the row j chose.
  await expect(pulls).not.toBeFocused();
  expect(await page.evaluate(() => !!document.querySelector(".local-rail :focus-visible"))).toBe(
    false,
  );
  expect((await focused(page)).place).toBe("row");
  const first = (await inbox.locator("[data-row-id][data-focused] .inbox-pr-title").textContent())!;

  // A button clicked gives up its focus too, and j and k go on.
  await page
    .getByRole("button", { name: /^Refresh/ })
    .first()
    .click();
  await expect.poll(() => page.evaluate(() => document.activeElement === document.body)).toBe(true);
  await page.keyboard.press("j");
  const second = inbox.locator("[data-row-id][data-focused] .inbox-pr-title");
  await expect(second).not.toHaveText(first);
  await page.keyboard.press("k");
  await expect(second).toHaveText(first);
  expect(
    await page.evaluate(() => {
      const ring = document.querySelector(":focus-visible");
      return !ring || !!ring.closest("[data-row-id]");
    }),
  ).toBe(true);

  // The Inbox: a sidebar click and j show no ring on the sidebar either.
  await sidebar
    .getByRole("link", { name: /^Inbox/ })
    .first()
    .click();
  await expect(page.locator("button.upnext-row").first()).toBeVisible();
  await page.keyboard.press("j");
  expect(await page.evaluate(() => !!document.querySelector(".local-rail :focus-visible"))).toBe(
    false,
  );
});

test("Tab goes through a dialog's controls, a radio group as one, and wraps inside it", async ({
  page,
}) => {
  await page.goto(await launchURL());
  await expect(page.locator("button.upnext-row").first()).toBeVisible();
  // A dialog of every kind of control, opened over the app as its own are.
  await page.evaluate(() => {
    const dialog = document.createElement("dialog");
    dialog.innerHTML = `
      <input type="text" aria-label="Name">
      <label><input type="radio" name="method" value="a"> A</label>
      <label><input type="radio" name="method" value="b" checked> B</label>
      <label><input type="radio" name="method" value="c"> C</label>
      <button type="button" disabled>Off</button>
      <button type="button">Cancel</button>
      <button type="button">Save</button>`;
    document.body.append(dialog);
    dialog.showModal();
    dialog.querySelector("input")!.focus();
  });
  const seen: string[] = [];
  for (let i = 0; i < 8; i++) {
    await page.keyboard.press("Tab");
    seen.push(
      await page.evaluate(() => {
        const element = document.activeElement as HTMLElement;
        if (!element.closest("dialog")) return "outside";
        return element instanceof HTMLInputElement
          ? `${element.type}${element.type === "radio" ? `:${element.value}` : ""}`
          : (element.textContent ?? "").trim();
      }),
    );
  }
  expect(seen).toEqual(["radio:b", "Cancel", "Save", "text", "radio:b", "Cancel", "Save", "text"]);
  await page.keyboard.press("Shift+Tab");
  expect(await page.evaluate(() => document.activeElement?.textContent)).toBe("Save");
});
