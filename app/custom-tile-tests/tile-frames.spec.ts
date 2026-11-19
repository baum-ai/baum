import { createServer, type IncomingMessage, type Server } from "node:http";
import { expect, test, type Page, type Request } from "@playwright/test";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { detail, frameBox, frameReady, open, railRow, SHOTS, showOnly, tileOn } from "./helpers";

// The adversarial suite: the escape probe tiles (oss/internal/tileview/
// tileviewtest/testdata/custom/escape_probe.html) on the review page's How it
// works section the daemon places them on, their frames loaded from the
// daemon's frame route and hosted by the page's own host component. Each
// mode of the probe tries a way out of its frame; the page must stay as it
// was, no request may reach the sink the probes aim at (a server of its own
// on another loopback port) or the daemon, and no navigation may happen. The
// probes share a section, so each test serves only the ones it drives.

const SINK_PORT = 4170;

type Hit = { method: string; url: string };
let sink: Server;
const hits: Hit[] = [];

test.beforeAll(async () => {
  const record = (request: IncomingMessage) =>
    hits.push({ method: request.method ?? "", url: request.url ?? "" });
  sink = createServer((request, response) => {
    record(request);
    response.writeHead(200, {
      "Access-Control-Allow-Origin": "*",
      "Content-Type": "text/plain",
    });
    response.end("sink");
  });
  sink.on("upgrade", (request, socket) => {
    record(request);
    socket.destroy();
  });
  await new Promise<void>((resolve) => sink.listen(SINK_PORT, "127.0.0.1", resolve));
});

test.afterAll(async () => {
  await new Promise((resolve) => sink.close(resolve));
});

test.beforeEach(() => {
  hits.length = 0;
});

type Report = { probe: string; mode: string; face: string; results: Record<string, string> };

/** Collects what the probes post to the page, as the page receives it. */
async function collectReports(page: Page) {
  await page.addInitScript(() => {
    const reports: unknown[] = [];
    Object.defineProperty(window, "__probeReports", { value: reports });
    window.addEventListener("message", (event) => {
      const data = event.data as { probe?: unknown } | null;
      if (data && typeof data === "object" && data.probe === "escape_probe")
        reports.push({ ...data, origin: event.origin });
    });
  });
  return (mode: string, face: string) =>
    expect
      .poll(
        () =>
          page.evaluate(
            ([mode, face]) =>
              (Reflect.get(window, "__probeReports") as (Report & { origin: string })[]).find(
                (report) => report.mode === mode && report.face === face,
              ) ?? null,
            [mode, face],
          ),
        { timeout: 20000 },
      )
      .not.toBeNull()
      .then(() =>
        page.evaluate(
          ([mode, face]) =>
            (Reflect.get(window, "__probeReports") as (Report & { origin: string })[]).find(
              (report) => report.mode === mode && report.face === face,
            )!,
          [mode, face],
        ),
      );
}

/** Every request a frame of the page started, but loading a tile frame's own
 *  document from the frame route. Chromium reports a request its policy
 *  refuses as started and then failed, so what matters is that each failed
 *  and the sink heard nothing. */
function frameRequests(page: Page) {
  const requests: Request[] = [];
  page.on("request", (request) => {
    if (request.frame() === page.mainFrame()) return;
    const url = new URL(request.url());
    if (
      request.isNavigationRequest() &&
      /^\/tile-frames\/[0-9a-f]{64}$/.test(url.pathname) &&
      request.method() === "GET"
    )
      return;
    requests.push(request);
  });
  return requests;
}

/** The page's own state, which no frame may change. */
async function pageState(page: Page) {
  return page.evaluate(() => ({
    url: location.href,
    title: document.title,
    theme: document.documentElement.dataset.theme ?? "",
    cookie: document.cookie,
  }));
}

/** The title of the frame that has the keyboard, if one has. */
const focusedFrame = (page: Page) =>
  page.evaluate(() =>
    document.activeElement instanceof HTMLIFrameElement ? document.activeElement.title : null,
  );

const describeRequests = (requests: Request[]) =>
  requests.map((request) => `${request.method()} ${request.url()} (${request.resourceType()})`);

/** The requests a frame started that were not refused: each answered by the
 *  daemon or the sink. */
const answered = (requests: Request[]) =>
  describeRequests(requests.filter((request) => request.failure() === null));

/** Why the frames' requests failed, for the record. */
const refusals = (requests: Request[]) =>
  [...new Set(requests.map((request) => request.failure()?.errorText ?? "answered"))].toSorted();

test("a probing frame reaches neither the page, the daemon nor the network", async ({ page }) => {
  const report = await collectReports(page);
  const requests = frameRequests(page);
  const dialogs: string[] = [];
  page.on("dialog", (dialog) => {
    dialogs.push(dialog.type());
    void dialog.dismiss();
  });
  const popups: string[] = [];
  page.context().on("page", (popup) => popups.push(popup.url()));
  await showOnly(page, ["escape_probe"]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("escape_probe"));
  await expect(tileOn(page, "escape_probe")).toBeVisible();
  const before = await pageState(page);
  const probe = await report("probe", "detail");
  await frameReady(page, "escape_probe");

  const results = probe.results;
  expect(probe.origin).toBe("null");
  expect(results.origin).toBe("null");
  // The frame's address is the frame route's: it never learns the namespace.
  expect(results.location).toMatch(/\/tile-frames\/[0-9a-f]{64}$/);
  expect(results.location).not.toContain("/s/");
  for (const name of [
    "parent document",
    "top location",
    "parent location",
    "cookie",
    "localStorage",
    "sessionStorage",
    "indexedDB",
    "parent bridge",
    "top navigation",
    "parent navigation",
  ])
    expect(results[name], name).toMatch(/^threw /);
  expect(results["window.open"]).toBe("null");
  expect(results["window.open daemon"]).toBe("null");
  // Dialogs return at once, unanswered, and are never shown.
  expect(results.alert).toMatch(/^undefined after \d+ms$/);
  expect(results.confirm).toMatch(/^false after \d+ms$/);
  expect(results.prompt).toMatch(/^null after \d+ms$/);
  for (const dialog of ["alert", "confirm", "prompt"])
    expect(Number(/after (\d+)ms/.exec(results[dialog]!)![1]), dialog).toBeLessThan(100);
  expect(dialogs).toEqual([]);
  expect(popups).toEqual([]);
  // Every request fails before it is made, the daemon's included.
  for (const name of [
    "read API",
    "write API",
    "MCP",
    "frame route",
    "daemon root",
    "outside",
    "outside no-cors",
    "xhr daemon",
    "xhr outside",
  ])
    expect(results[name], name).toMatch(/^(failed|threw )/);
  expect(results["font outside"]).toMatch(/^failed/);
  expect(results["font daemon"]).toMatch(/^failed/);
  // No message handler of the shell is reachable from the frame, the cache
  // store refuses an opaque origin, and a peer connection is either absent or
  // the documented residual (SECURITY.md): its STUN server here hears nothing.
  expect(results["webkit handlers"]).toBe("");
  expect(results.caches).toMatch(/^(failed|threw) /);
  expect(results.RTCPeerConnection).toMatch(/^(absent|threw \w+|\[object RTCPeerConnection\])$/);

  // Give stragglers (beacons, a meta refresh) time to show.
  await page.waitForTimeout(1500);
  expect(hits, "requests that reached the sink").toEqual([]);
  expect(answered(requests), "requests a frame made that were answered").toEqual([]);
  console.log(`probe: ${requests.length} requests refused (${refusals(requests).join(", ")})`);
  // The page is as it was, and has the keys: the frame took no focus.
  expect(await pageState(page)).toEqual(before);
  expect(await focusedFrame(page), "a frame that took the keyboard").toBeNull();
  await expect(page).toHaveURL(/tile=escape_probe/);
});

test("spoofed messages are ignored and other tiles are not reached", async ({ page }) => {
  const report = await collectReports(page);
  const requests = frameRequests(page);
  await showOnly(page, ["escape_spoof", "widget_health"]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ colorScheme: "light" });
  await open(page, detail("escape_spoof"));
  await expect(tileOn(page, "escape_spoof")).toBeVisible();
  const url = page.url();
  const spoofed = await report("spoof", "detail");
  expect(Number(spoofed.results.others)).toBeGreaterThan(0);
  // The well-behaved tile on the same section still draws its own data in
  // the app's theme: another frame cannot speak for the page.
  await tileOn(page, "widget_health").scrollIntoViewIfNeeded();
  await frameReady(page, "widget_health");
  const widget = page.frameLocator('[data-custom-tile="widget_health"] iframe');
  await expect(widget.locator("body")).toContainText("widget_tags");
  await expect(widget.locator("body")).not.toContainText("Spoofed by another tile");
  await expect(widget.locator("html")).toHaveAttribute("data-theme", "light");
  // Jumps, asks and keys without the reader's focus go nowhere, and a resize
  // far past the bounds is held to them.
  expect(spoofed.results.sent).toContain("resize past the bounds");
  expect(spoofed.results.sent).toContain("ready past the bounds");
  // Its ask, without the reader's activation, opened nothing.
  await expect(page.locator("section.session-panel")).toHaveCount(0);
  await page.waitForTimeout(1000);
  await expect(page).toHaveURL(url);
  await expect(frameBox(page, "escape_spoof")).toHaveAttribute("data-frame-state", "ready");
  const height = (await frameBox(page, "escape_spoof").boundingBox())!.height;
  expect(height).toBeLessThanOrEqual(20000);
  expect(hits).toEqual([]);
  expect(answered(requests)).toEqual([]);
});

test("a frame that floods the page is unmounted, and only that frame", async ({ page }) => {
  await showOnly(page, ["escape_flood", "escape_probe", "escape_spoof"]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("escape_flood"));
  await expect(page.getByRole("alert")).toContainText("This tile couldn't load.", {
    timeout: 15000,
  });
  await expect(page.getByRole("alert")).toContainText("too many messages");
  if (SHOTS) {
    mkdirSync(SHOTS, { recursive: true });
    await page.screenshot({ path: join(SHOTS, "flood-detail.png") });
  }
  await expect(frameBox(page, "escape_flood")).toHaveCount(0);
  await expect(tileOn(page, "escape_flood").locator("iframe")).toHaveCount(0);
  // Other tiles of the same type on the section still load: nothing a frame
  // sent marked its type as broken.
  await tileOn(page, "escape_probe").scrollIntoViewIfNeeded();
  await frameReady(page, "escape_probe");
  await tileOn(page, "escape_spoof").scrollIntoViewIfNeeded();
  await frameReady(page, "escape_spoof");
});

for (const where of ["daemon", "outside"] as const)
  test(`a frame that navigates itself to ${where === "daemon" ? "a daemon page" : "another origin"} is unmounted`, async ({
    page,
  }) => {
    const requests = frameRequests(page);
    const report = await collectReports(page);
    await showOnly(page, [`escape_leave_${where}`]);
    await page.setViewportSize({ width: 1440, height: 900 });
    await open(page, detail(`escape_leave_${where}`));
    const url = page.url();
    await report(`leave_${where}`, "detail");
    await expect(page.getByRole("alert")).toContainText("This tile couldn't load.", {
      timeout: 15000,
    });
    await expect(page.getByRole("alert")).toContainText("It tried to leave its frame.");
    await expect(page).toHaveURL(url);
    await page.waitForTimeout(1000);
    expect(hits).toEqual([]);
    // A navigation to the daemon is a request of the frame, which the daemon
    // answers with a page that refuses to be framed; nothing else is asked.
    const asked = answered(requests);
    console.log(
      `leave ${where}: ${describeRequests(requests).join(", ")}; ${refusals(requests).join(", ")}`,
    );
    if (where === "daemon")
      expect(
        asked.every((request) => /^GET http:\/\/127\.0\.0\.1:4167\/ \(document\)$/.test(request)),
        asked.join("\n"),
      ).toBe(true);
    else expect(asked).toEqual([]);
  });

test("a frame in a busy loop holds the page only while it runs", async ({ page }) => {
  // The known limit (SECURITY.md, "Custom tiles"): where the engine runs the
  // frame on the page's thread, its busy loop freezes the page until the
  // loop ends. This tile's loop ends after 1.5 seconds.
  const report = await collectReports(page);
  await showOnly(page, ["escape_busy"]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("escape_busy"));
  await frameReady(page, "escape_busy");
  // How long the page's own timers were held while the frame looped.
  const stall = await page.evaluate(
    () =>
      new Promise<number>((resolve) => {
        let last = performance.now();
        let worst = 0;
        const timer = setInterval(() => {
          const now = performance.now();
          worst = Math.max(worst, now - last);
          last = now;
        }, 20);
        setTimeout(() => {
          clearInterval(timer);
          resolve(Math.round(worst));
        }, 3000);
      }),
  );
  await report("busy", "detail");
  test.info().annotations.push({ type: "page stall (ms)", description: String(stall) });
  console.log(`busy loop: the page's timers were held for at most ${stall}ms`);
  // The page is intact and answers again.
  await expect(page.getByRole("heading", { name: "Escape probe: busy" })).toBeVisible();
  await railRow(page, "Overview").click();
  await expect(page).not.toHaveURL(/tile=escape_busy/);
  expect(hits).toEqual([]);
});

test("a frame that navigates before it draws, or says hello again, is unmounted", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  for (const [tile, reason] of [
    ["escape_leave_early", "It didn't finish drawing in time."],
    ["escape_hello_again", "It tried to leave its frame."],
  ] as const) {
    await page.unrouteAll({ behavior: "wait" });
    await showOnly(page, [tile]);
    await open(page, detail(tile));
    const url = page.url();
    await expect(page.getByRole("alert")).toContainText("This tile couldn't load.", {
      timeout: 15000,
    });
    await expect(page.getByRole("alert")).toContainText(reason);
    await expect(page).toHaveURL(url);
  }
  expect(hits).toEqual([]);
});

test("a detail that takes the keyboard while the reader types elsewhere is taken down", async ({
  page,
}) => {
  // The reader writes in the session panel's composer, the pointer resting
  // over a detail that grabs the keyboard every 200ms. The page gives the
  // keyboard back to the composer each time, and takes the frame down the
  // fourth time: what they type stays in the composer. A key pressed in the
  // instant the frame holds the keyboard, before the page takes it back, is
  // the frame's (SECURITY.md, "The keyboard"); on a busy page that can be a
  // few keys each of the four times.
  await showOnly(page, ["escape_focus"]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await open(page, detail("escape_focus"));
  await frameReady(page, "escape_focus");
  // Ctrl+` opens the session panel with the keys in its composer.
  await page.keyboard.press("Control+Backquote");
  const composer = page
    .locator("section.session-panel")
    .getByRole("textbox", { name: "Message to the agent" });
  await expect(composer).toBeFocused();
  const box = (await frameBox(page, "escape_focus").boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + Math.min(box.height / 2, 60));
  const text = "the retry delay is capped at thirty seconds";
  await page.keyboard.type(text, { delay: 60 });
  await expect(page.getByRole("alert")).toContainText("It kept taking the keyboard from the page.");
  await expect(composer).toBeFocused();
  const typed = await composer.inputValue();
  // The keys the composer has arrived in order.
  let at = 0;
  const inOrder = [...typed].every((key) => (at = text.indexOf(key, at) + 1) > 0);
  expect(inOrder).toBe(true);
  // Once it is down, every key is the composer's.
  await page.keyboard.type(" and then retried", { delay: 60 });
  await expect(composer).toHaveValue(`${typed} and then retried`);
});
