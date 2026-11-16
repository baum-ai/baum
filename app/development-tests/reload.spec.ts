import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test } from "@playwright/test";

test("development login, CSRF, streaming and React/CSS hot reload", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const credential = JSON.parse(
    await readFile(join(process.env.BAUM_APP_STATE_DIR!, "credential.json"), "utf8"),
  );
  const launch = new URL(credential.url);
  launch.port = "1420";
  const streaming = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith("/api/events"),
  );
  await page.goto(launch.href);
  await expect(page.getByRole("heading", { name: "Default", exact: true })).toBeVisible();
  await page.getByRole("link", { name: "Pull requests", exact: true }).first().click();
  await expect(page.getByRole("heading", { name: "Pull requests", exact: true })).toBeVisible();
  expect(page.url()).not.toContain("token");
  const base = new URL(page.url()).pathname;
  expect(base).toMatch(/^\/s\/[A-Za-z0-9_-]{43}\/$/);
  expect((await streaming).headers()["content-type"]).toBe("text/event-stream");
  expect((await page.request.get(`${base}api/repos`)).status()).toBe(200);
  // The tour's sample project's API reaches the daemon too, not Vite's page.
  const sample = await page.request.get(`${base}sample/api/health`);
  expect(sample.status()).toBe(200);
  expect(sample.headers()["content-type"]).toContain("application/json");
  expect((await page.request.get("/api/repos")).status()).toBe(401);
  expect((await page.request.get("/s/wrong/api/repos")).status()).toBe(404);
  const status = await page.evaluate(async (url) => {
    const response = await fetch(url, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ enabled: false }),
    });
    return response.status;
  }, `${base}api/automatic`);
  expect(status).toBe(200);
  const rejected = await page.request.put(`${base}api/automatic`, {
    headers: { Origin: "http://other.example" },
    data: { enabled: true },
  });
  expect(rejected.status()).toBe(403);

  // A custom tile's frame reaches the daemon with its fetch metadata, not
  // Vite's page: this suite starts the fixture with BAUM_APP_CUSTOM_TILES=1,
  // so acme/demo#7 has synthetic custom tiles.
  const clones: { id: string; repository: string }[] = await (
    await page.request.get(`${base}api/repos`)
  ).json();
  const clone = clones.find((candidate) => candidate.repository === "acme/demo")!;
  const tiled = await (await page.request.get(`${base}api/tiles?clone=${clone.id}&pr=7`)).json();
  const widget = tiled.view.tiles.find((tile: { id: string }) => tile.id === "widget_health");
  const framePath = `/tile-frames/${widget.definition.digest.slice("sha256:".length)}`;
  const frameResponse = page.waitForResponse(
    (response) => new URL(response.url()).pathname === framePath,
  );
  await page.evaluate((src) => {
    const frame = document.createElement("iframe");
    frame.id = "development-frame";
    frame.sandbox.add("allow-scripts");
    frame.src = src;
    document.body.append(frame);
  }, framePath);
  const loaded = await frameResponse;
  expect(loaded.status()).toBe(200);
  expect(await loaded.text()).toContain('<style id="baum-kit-style">');
  await page.evaluate(() => document.getElementById("development-frame")?.remove());
  // Without the frame's metadata the daemon refuses it, still not Vite.
  expect((await page.request.get(framePath)).status()).toBe(404);

  // A page reload would erase this marker. Both edits must update in place.
  const selection = page.getByRole("checkbox", { name: "Select acme/demo #7", exact: true });
  await selection.check();
  await page.evaluate(() => Object.assign(window, { hotReloadMarker: "preserved" }));
  const cssPath = "src/style.css";
  const componentPath = "src/app.tsx";
  const css = await readFile(cssPath, "utf8");
  const component = await readFile(componentPath, "utf8");
  try {
    await writeFile(cssPath, `${css}\nbody { --hot-reload-probe: ready; }\n`);
    await expect
      .poll(() =>
        page.evaluate(() =>
          getComputedStyle(document.body).getPropertyValue("--hot-reload-probe").trim(),
        ),
      )
      .toBe("ready");
    await writeFile(
      componentPath,
      component.replace('title="Pull requests"', 'title="Hot reload works"'),
    );
    await expect(page.getByRole("heading", { name: "Hot reload works" })).toBeVisible();
    expect(await page.evaluate(() => Reflect.get(window, "hotReloadMarker"))).toBe("preserved");
    await expect(selection).toBeChecked();
    await page.reload();
    await expect(page.getByRole("heading", { name: "Hot reload works" })).toBeVisible();
    expect(errors).toEqual([]);
  } finally {
    await writeFile(cssPath, css);
    await writeFile(componentPath, component);
  }
});
