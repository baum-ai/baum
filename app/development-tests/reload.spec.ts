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
  launch.port = "5173";
  const streaming = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith("/api/events"),
  );
  await page.goto(launch.href);
  await expect(page.getByRole("heading", { name: "Pull requests", exact: true })).toBeVisible();
  expect(page.url()).not.toContain("token");
  const base = new URL(page.url()).pathname;
  expect(base).toMatch(/^\/s\/[A-Za-z0-9_-]{43}\/$/);
  expect((await streaming).headers()["content-type"]).toBe("text/event-stream");
  expect((await page.request.get(`${base}api/repos`)).status()).toBe(200);
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

  // A page reload would erase this marker. Both edits must update in place.
  const selection = page.getByRole("checkbox", { name: "Select acme/demo #9", exact: true });
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
      component.replace(">Pull requests</h1>", ">Hot reload works</h1>"),
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
