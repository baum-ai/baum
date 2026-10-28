import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test } from "@playwright/test";

const state = process.env.BAUM_APP_STATE_DIR ?? "";
const mode = (value: string) => writeFile(join(state, "github-auth-mode"), value);
async function launchURL() {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      const { url } = JSON.parse(await readFile(join(state, "credential.json"), "utf8"));
      if (typeof url === "string") return url;
    } catch {
      /* daemon is still starting */
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw Error("daemon did not publish its launch URL");
}

test("GitHub device lifecycle, recovery and saved reviews through the real daemon", async ({
  page,
  context,
  browser,
}) => {
  test.setTimeout(150000);
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await mode("pending");
  const launch = await launchURL();
  await page.goto(`${launch}&page=diagnostics`);
  await expect(
    page.getByRole("heading", { name: "Check this computer", exact: true }),
  ).toBeVisible();
  const base = new URL(page.url()).pathname;
  const origin = new URL(page.url()).origin;
  const mutate = (name: string) =>
    page.request.post(`${base}api/github-auth/${name}`, { headers: { Origin: origin }, data: {} });
  // The daemon is shared with other specs; reset through the production API.
  expect((await mutate("cancel")).status()).toBe(200);
  expect((await mutate("disconnect")).status()).toBe(200);
  await page.reload();
  const panel = page.getByRole("region", { name: "GitHub connection", exact: true });
  await expect(
    panel.getByRole("button", { name: "Sign in with GitHub", exact: true }),
  ).toBeEnabled();
  await context.route("https://github.com/login/device", (route) =>
    route.fulfill({ contentType: "text/html", body: "GitHub device page" }),
  );
  // Signing in opens GitHub's device page straight away.
  const signInPopup = page.waitForEvent("popup");
  await panel.getByRole("button", { name: "Sign in with GitHub", exact: true }).click();
  await expect(await signInPopup).toHaveURL("https://github.com/login/device");
  await (await signInPopup).close();
  await expect(panel.getByLabel("GitHub sign-in code")).toHaveText("ABCD-EFGH");
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await panel.getByRole("button", { name: "Copy code", exact: true }).click();
  await expect(panel.getByText("Code copied.", { exact: true })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("ABCD-EFGH");
  const popupPromise = page.waitForEvent("popup");
  await panel.getByRole("link", { name: "Open GitHub" }).click();
  const popup = await popupPromise;
  await expect(popup).toHaveURL("https://github.com/login/device");
  await popup.close();
  await page.screenshot({ path: "test-results/github-auth-waiting.png", animations: "disabled" });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(panel.getByRole("button", { name: "Cancel sign-in" })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: "test-results/github-auth-narrow.png", animations: "disabled" });
  await page.setViewportSize({ width: 1280, height: 900 });
  const attempt = (await (await page.request.get(`${base}api/github-auth`)).json()).pending.id;
  await page.reload();
  await expect(panel.getByLabel("GitHub sign-in code")).toHaveText("ABCD-EFGH");
  expect((await (await page.request.get(`${base}api/github-auth`)).json()).pending.id).toBe(
    attempt,
  );
  // Closing the panel cannot stop Go-owned polling.
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await mode("alice");
  await expect
    .poll(async () => (await (await page.request.get(`${base}api/github-auth`)).json()).login)
    .toBe("alice");
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await expect(panel).toContainText("Signed in as alice");
  await panel.screenshot({
    path: "test-results/github-auth-connected.png",
    animations: "disabled",
  });
  await page.reload();
  await expect(panel).toContainText("Signed in as alice");
  await expect(panel).toContainText("baum credential store");
  // A private GitHub read used by generation must resolve this saved account
  // through production config; the mocked external boundary rejects other tokens.
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await page.getByRole("button", { name: "Full review", exact: true }).click();
  await page.getByRole("button", { name: "Regenerate review", exact: true }).click();
  await expect(page.getByRole("button", { name: "Regenerate review", exact: true })).toBeEnabled();
  await expect.poll(() => readFile(join(state, "github-consumed-account"), "utf8")).toBe("alice");
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await mode("pending");
  await panel.getByRole("button", { name: "Reconnect", exact: true }).click();
  await expect(panel.getByLabel("GitHub sign-in code")).toBeVisible();
  await mode("bob");
  await expect(panel).toContainText("Signed in as bob");
  await expect(panel.getByLabel("GitHub sign-in code")).toHaveCount(0);
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await page.getByRole("button", { name: "Regenerate review", exact: true }).click();
  await expect(page.getByRole("button", { name: "Regenerate review", exact: true })).toBeEnabled();
  await expect.poll(() => readFile(join(state, "github-consumed-account"), "utf8")).toBe("bob");
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await panel.getByRole("button", { name: "Disconnect", exact: true }).click();
  await expect(panel).toContainText("Not connected");
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await page.getByRole("link", { name: /Reuse the existing PR page/ }).click();
  await expect(page.getByText(/Local fixture review version/).first()).toBeVisible();
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  for (const [scenario, text] of [
    ["denied", "sign-in was denied"],
    ["expired", "code expired"],
  ]) {
    await mode(scenario!);
    await panel.getByRole("button", { name: "Sign in with GitHub", exact: true }).click();
    await expect(panel).toContainText(text!);
  }
  await mode("network");
  await panel.getByRole("button", { name: "Sign in with GitHub", exact: true }).click();
  await expect(panel).toContainText("Check your connection");
  await mode("alice");
  await expect(panel).toContainText("Signed in as alice");
  await panel.getByRole("button", { name: "Disconnect", exact: true }).click();
  await mode("scopes");
  await panel.getByRole("button", { name: "Sign in with GitHub", exact: true }).click();
  await expect(panel).toContainText("grant both permissions");
  await panel.getByRole("button", { name: "Cancel sign-in" }).click();
  await mode("locked");
  await panel.getByRole("button", { name: "Check connection", exact: true }).click();
  await expect(panel).toContainText("Unlock your operating system");
  await mode("pending");
  await panel.getByRole("button", { name: "Check connection", exact: true }).click();
  await expect(panel.getByText(/Unlock your operating system/)).toHaveCount(0);
  await panel.getByRole("button", { name: "Sign in with GitHub", exact: true }).click();
  await mode("locked");
  await expect(panel).toContainText("Unlock your operating system");
  await mode("alice");
  await expect(panel).toContainText("Signed in as alice");

  // Every endpoint uses the real session namespace and mutation boundary.
  const anonymous = await browser.newContext();
  for (const suffix of ["", "/start", "/cancel", "/disconnect", "/retry"]) {
    const path = `api/github-auth${suffix}`;
    const unauth = suffix
      ? await anonymous.request.post(`${origin}${base}${path}`, {
          headers: { Origin: origin },
          data: {},
        })
      : await anonymous.request.get(`${origin}${base}${path}`);
    expect(unauth.status()).toBe(401);
    expect((await page.request.get(`/api/github-auth${suffix}`)).status()).toBe(401);
    expect((await page.request.get(`/s/wrong/api/github-auth${suffix}`)).status()).toBe(404);
    if (suffix) {
      for (const badOrigin of ["https://evil.example", "null", ""]) {
        expect(
          (
            await page.request.post(`${base}${path}`, {
              headers: badOrigin ? { Origin: badOrigin } : {},
              data: {},
            })
          ).status(),
        ).toBe(403);
      }
      expect(
        (
          await page.request.post(`${base}${path}`, {
            headers: { Origin: origin, "Content-Type": "text/plain" },
            data: "{}",
          })
        ).status(),
      ).toBe(403);
    }
  }
  await anonymous.close();
  const publicState = await (await page.request.get(`${base}api/github-auth`)).text();
  expect(publicState).not.toMatch(/fixture-access|fixture-refresh|fixture-device/);
  await mode("pending");
  await mutate("disconnect");
  expect(errors).toEqual([]);
});
