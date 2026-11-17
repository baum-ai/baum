import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, type Page, type Request, test } from "@playwright/test";

// Untrusted HTML (a document written from pull request text) may only run in
// a frame with `sandbox="allow-scripts"` and no `allow-same-origin`: an opaque
// origin, served from the daemon's origin with its own policy. These checks
// pin what that frame must not reach: the page embedding it, the daemon's API
// and the network. The frame's document is answered by route interception on
// the daemon's origin; every API request it makes goes to the real daemon.

// The fixture daemon publishes its launch URL in its credential file.
async function launchURL(): Promise<string> {
  for (let attempt = 0; attempt < 100; attempt++) {
    try {
      const path = join(process.env.BAUM_APP_STATE_DIR ?? "", "credential.json");
      const { url } = JSON.parse(await readFile(path, "utf8"));
      if (typeof url === "string") return url;
    } catch {
      // Not written yet.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error("the daemon did not publish its credential");
}

const OUTSIDE = "http://outside.invalid";
const SANDBOX = "sandbox allow-scripts; frame-ancestors 'self'";
// The frame's own policy: no network at all, inline script and style only.
const FRAME_POLICY = `default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; ${SANDBOX}`;

// Runs in the frame: every way to the page, storage and the daemon, then the
// network, reported to the page by postMessage.
function probe(outside: string, name: string) {
  const results: Record<string, string> = {};
  const attempt = (name: string, f: () => unknown) => {
    try {
      results[name] = String(f());
    } catch (error) {
      results[name] = `threw ${(error as Error).name}`;
    }
  };
  attempt("origin", () => self.origin);
  attempt("parent document", () => parent.document.title);
  attempt("top location", () => top?.location.href);
  attempt("cookie", () => document.cookie);
  attempt("localStorage", () => localStorage.length);
  attempt(
    "parent bridge",
    () => (parent as unknown as { __TAURI_INTERNALS__: unknown }).__TAURI_INTERNALS__,
  );
  // The tile is served under the app's namespace, so its own path leads to
  // the API; the frame's requests carry Origin: null.
  const api = `${location.pathname.match(/^\/s\/[^/]+\//)?.[0]}api/settings`;
  const request = (name: string, url: string, init?: RequestInit) =>
    fetch(url, init).then(
      async (response) => {
        results[name] = `read ${response.status} ${await response.text()}`;
      },
      () => {
        results[name] = "failed";
      },
    );
  navigator.sendBeacon(`${outside}/${name}/beacon`, "data");
  new Image().src = `${outside}/${name}/image`;
  Promise.all([
    request("read API", api, { credentials: "include" }),
    request("write API", api, {
      method: "PUT",
      credentials: "include",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    }),
    request("outside", `${outside}/${name}/fetch`),
  ]).then(() => parent.postMessage({ results }, "*"));
}

function frameDocument(script: string) {
  return `<!doctype html><html><head><meta charset="utf-8"></head><body><script>${script}</script></body></html>`;
}

/** Answers `__frame-probe?<name>` on the daemon's origin with the named document. */
async function serveFrames(page: Page) {
  const documents: Record<string, { policy: string; body: string }> = {
    // The intended frame: its own policy forbids every request.
    tile: {
      policy: FRAME_POLICY,
      body: frameDocument(`(${probe})(${JSON.stringify(OUTSIDE)}, "tile")`),
    },
    // No frame policy at all: what is left is the opaque origin, the daemon's
    // own checks and the page's policy.
    bare: {
      policy: SANDBOX,
      body: frameDocument(`(${probe})(${JSON.stringify(OUTSIDE)}, "bare")`),
    },
    // Navigating itself elsewhere is governed by the embedding page's
    // frame-src, whatever the frame's own policy says.
    leave: {
      policy: SANDBOX,
      body: frameDocument(`location.href = ${JSON.stringify(`${OUTSIDE}/navigate`)}`),
    },
  };
  await page.route(
    (url) => url.pathname.endsWith("/__frame-probe"),
    (route) => {
      const name = new URL(route.request().url()).search.slice(1);
      const document = documents[name];
      return route.fulfill({
        status: 200,
        headers: {
          "Content-Type": "text/html; charset=utf-8",
          "Content-Security-Policy": document.policy,
        },
        body: document.body,
      });
    },
  );
}

/** Mounts the named frames as a tile would be and returns what each reported. */
function mountFrames(page: Page, names: string[]) {
  return page.evaluate(
    (names) =>
      new Promise<Record<string, Record<string, string>>>((resolve) => {
        const results: Record<string, Record<string, string>> = {};
        const frames = new Map<MessageEventSource, string>();
        window.addEventListener("message", (event) => {
          // event.origin is "null" for every sandboxed frame; only the source
          // window tells them apart.
          const name = event.source && frames.get(event.source);
          if (name) results[name] = event.data.results;
        });
        for (const name of names) {
          const frame = document.createElement("iframe");
          frame.setAttribute("sandbox", "allow-scripts");
          frame.src = `${location.pathname.match(/^\/s\/[^/]+\//)?.[0]}__frame-probe?${name}`;
          document.body.append(frame);
          if (frame.contentWindow) frames.set(frame.contentWindow, name);
        }
        setTimeout(() => resolve(results), 4000);
      }),
    names,
  );
}

test("a sandboxed frame reaches neither the page, the daemon nor the network", async ({ page }) => {
  const outside: string[] = [];
  await page.context().route(`${OUTSIDE}/**`, (route) => {
    outside.push(new URL(route.request().url()).pathname);
    return route.fulfill({
      status: 200,
      body: "",
      headers: { "Access-Control-Allow-Origin": "*" },
    });
  });
  // The frames' API requests, and those that failed; a CORS failure hides
  // the daemon's status, so only the outcome is visible here.
  const api = new Set<Request>();
  const failed = new Set<Request>();
  page.on("request", (request) => {
    if (request.frame() !== page.mainFrame() && request.url().includes("/api/")) api.add(request);
  });
  page.on("requestfailed", (request) => failed.add(request));
  await serveFrames(page);
  await page.goto(await launchURL());
  await page.waitForURL(/\/s\/[^/]+\//);

  const results = await mountFrames(page, ["tile", "bare", "leave"]);

  for (const name of ["tile", "bare"]) {
    const result = results[name];
    expect(result, `${name} frame ran`).toBeDefined();
    expect(result.origin).toBe("null");
    for (const access of [
      "parent document",
      "top location",
      "cookie",
      "localStorage",
      "parent bridge",
    ]) {
      expect(result[access], `${name}: ${access}`).toMatch(/^threw /);
    }
    // Nothing the daemon returns is readable, and a write never gets past
    // its preflight (the daemon refuses Origin: null as well).
    expect(result["read API"]).toBe("failed");
    expect(result["write API"]).toBe("failed");
  }
  // The frame's own policy stops every request before it is made.
  expect(results.tile.outside).toBe("failed");
  // Without it the bare frame's API requests are made, and every one fails.
  expect(api.size).toBeGreaterThan(0);
  for (const request of api) expect(failed.has(request), request.method()).toBe(true);
  // The tile's policy kept it from every request, and the page's frame-src
  // keeps a frame from navigating itself off the daemon's origin. The bare
  // frame's own requests leave: only its policy would stop them.
  expect(outside.filter((path) => !path.startsWith("/bare/"))).toEqual([]);
  expect(outside, "the bare frame's requests are observed").toContain("/bare/fetch");
});
