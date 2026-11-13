import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";

// Pictures of the custom tile kit's demo tiles (e2e/tile-kit/*.html, with
// synthetic data in the .json beside each), taken only on request
// (TILE_KIT_SCREENSHOTS=1) by `just local_app::tile-kit-screens`, which first
// composes each tile's frame document with the Go kit into FRAMES. A small
// server serves the frames with the daemon's frame headers and a parent page
// that mounts each in a sandboxed iframe, does the handshake and sizes it as
// the tile page does: a glance in a card's box at each width, a detail in a
// pane, in light and dark. Pictures go to test-results/tile-kit.
test.skip(!process.env.TILE_KIT_SCREENSHOTS, "screenshots are taken on request");

const DEMOS = "e2e/tile-kit";
const FRAMES = "test-results/tile-kit/frames";
const OUT = "test-results/tile-kit";
const KIT = "../tiles/kit";
const CSP =
  "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox allow-scripts";

// The tile page's card sizes (oss/app/src/tiles/card.tsx: CARD_HEIGHT, SPAN)
// in a 1056px grid of six columns 12px apart. The glance's box is what the
// card leaves under its header and a one-line title.
const CARDS = {
  third: { width: 344, height: 256 },
  half: { width: 522, height: 272 },
  full: { width: 1056, height: 208 },
} as const;
const PANES = [720, 360];
const THEMES = ["light", "dark"] as const;
type Size = keyof typeof CARDS;
type Theme = (typeof THEMES)[number];
type Demo = { title: string; data: unknown; refs: Record<string, Record<string, unknown>> };
type Mounted = {
  messages: {
    kind: string;
    code?: string;
    message?: string;
    metrics?: Record<string, unknown>;
    height?: number;
  }[];
  box: { width: number; height: number };
};

const PAGE = `<!doctype html>
<html lang="en" data-theme="light">
<head>
<meta charset="utf-8">
<link rel="stylesheet" href="/kit/fonts.css">
<link rel="stylesheet" href="/kit/tokens.css">
<style>
  body { margin: 0; padding: 24px; background: var(--surface-page); color: var(--text-primary); font-family: var(--font-text); }
  .card { box-sizing: border-box; display: flex; flex-direction: column; gap: 10px; padding: 12px 16px 14px;
    border: 1px solid var(--border-default); border-radius: var(--radius-lg); background: var(--surface-card); }
  .card header { display: flex; align-items: center; min-height: 28px; font-size: 13px; font-weight: 500;
    letter-spacing: 0.08em; text-transform: uppercase; color: var(--text-tertiary); }
  .card h3 { margin: -6px 0 0; font-size: 16px; line-height: 1.375; font-weight: 600; }
  .box { flex: 1; min-height: 0; }
  .box iframe { display: block; width: 100%; height: 100%; border: 0; }
  .pane { background: var(--surface-card); }
  .pane iframe { display: block; width: 100%; height: 120px; border: 0; }
</style>
</head>
<body><div id="stage"></div>
<script>
window.mount = (options) => new Promise((resolve) => {
  const { src, face, size, theme, data, refs, title, card, pane } = options;
  document.documentElement.dataset.theme = theme;
  const stage = document.getElementById("stage");
  stage.replaceChildren();
  const iframe = document.createElement("iframe");
  iframe.setAttribute("sandbox", "allow-scripts");
  iframe.setAttribute("referrerpolicy", "no-referrer");
  iframe.setAttribute("allow", "");
  iframe.title = title;
  let holder;
  if (face === "glance") {
    holder = document.createElement("article");
    holder.className = "card";
    holder.style.width = card.width + "px";
    holder.style.height = card.height + "px";
    const header = document.createElement("header");
    header.textContent = "Custom";
    const heading = document.createElement("h3");
    heading.textContent = title;
    const box = document.createElement("div");
    box.className = "box";
    box.append(iframe);
    holder.append(header, heading, box);
    iframe.tabIndex = -1;
  } else {
    holder = document.createElement("div");
    holder.className = "pane";
    holder.style.width = pane + "px";
    holder.append(iframe);
  }
  holder.id = "shot";
  const messages = [];
  let settled = null;
  const finish = () => {
    clearTimeout(settled);
    settled = setTimeout(() => {
      window.removeEventListener("message", listen);
      const rect = iframe.getBoundingClientRect();
      resolve({ messages, box: { width: rect.width, height: rect.height } });
    }, 400);
  };
  const listen = (event) => {
    if (event.source !== iframe.contentWindow) return;
    const message = event.data;
    messages.push(message);
    if (message.kind === "hello") {
      iframe.contentWindow.postMessage({ protocol: "baum.tile/1", kind: "init", face, size, theme, lang: "en", data, refs }, "*");
    } else if (message.kind === "resize" && face === "detail") {
      iframe.style.height = Math.min(Math.max(message.height, 120), 20000) + "px";
      finish();
    } else if (message.kind === "ready" || message.kind === "error") {
      finish();
    }
  };
  window.addEventListener("message", listen);
  stage.append(holder);
  iframe.src = src;
  setTimeout(finish, 5000);
});
</script>
</body>
</html>`;

/** What a glance receives: labels, never the excerpts. */
function glanceRefs(refs: Demo["refs"]) {
  return Object.fromEntries(
    Object.entries(refs).map(([key, payload]) => {
      const { excerpt: _excerpt, ...rest } = payload;
      return [key, rest];
    }),
  );
}

let server: Server;
let origin = "";

test.beforeAll(async () => {
  server = createServer((request, response) => {
    const url = new URL(request.url ?? "/", "http://localhost");
    if (url.pathname === "/") {
      response.writeHead(200, { "content-type": "text/html; charset=utf-8" });
      response.end(PAGE);
    } else if (url.pathname.startsWith("/kit/") && /^[a-z]+\.css$/.test(url.pathname.slice(5))) {
      response.writeHead(200, { "content-type": "text/css" });
      response.end(readFileSync(join(KIT, url.pathname.slice(5))));
    } else if (/^\/tile-frames\/[a-z-]+$/.test(url.pathname)) {
      // The daemon's frame route headers (oss/tiles/kit.CSP and the rest).
      response.writeHead(200, {
        "content-type": "text/html; charset=utf-8",
        "content-security-policy": CSP,
        "x-content-type-options": "nosniff",
        "referrer-policy": "no-referrer",
        "cache-control": "no-store",
        "cross-origin-resource-policy": "same-origin",
      });
      response.end(readFileSync(join(FRAMES, `${url.pathname.slice(13)}.html`)));
    } else {
      response.writeHead(404).end();
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  mkdirSync(OUT, { recursive: true });
});

test.afterAll(() => {
  server.close();
});

const demo = (name: string) =>
  JSON.parse(readFileSync(join(DEMOS, `${name}.json`), "utf8")) as Demo;

async function mount(
  page: Page,
  name: string,
  options: { face: "glance" | "detail"; size: Size; theme: Theme; pane?: number; data?: unknown },
) {
  const { title, data, refs } = demo(name);
  return (await page.evaluate(
    (args) => (window as unknown as { mount: (args: unknown) => Promise<unknown> }).mount(args),
    {
      src: `/tile-frames/${name}`,
      face: options.face,
      size: options.size,
      theme: options.theme,
      title,
      data: options.data ?? data,
      refs: options.face === "glance" ? glanceRefs(refs) : refs,
      card: CARDS[options.size],
      pane: options.pane ?? 720,
    },
  )) as Mounted;
}

const errors = (mounted: Mounted) => mounted.messages.filter((message) => message.kind === "error");

test("demo tiles in light and dark, glance and detail", async ({ page, browserName }) => {
  await page.setViewportSize({ width: 1200, height: 900 });
  await page.goto(origin);
  const report: unknown[] = [];
  for (const name of ["schema-change", "api-change", "test-health"]) {
    for (const theme of THEMES) {
      for (const size of Object.keys(CARDS) as Size[]) {
        const mounted = await mount(page, name, { face: "glance", size, theme });
        const ready = mounted.messages.find((message) => message.kind === "ready");
        report.push({
          name,
          face: "glance",
          size,
          theme,
          box: mounted.box,
          metrics: ready?.metrics,
          errors: errors(mounted),
        });
        expect(errors(mounted), `${name} glance ${size} ${theme}`).toEqual([]);
        expect(ready?.metrics, `${name} glance ${size} ${theme}`).toMatchObject({
          overflow_x: false,
          overflow_y: false,
        });
        await page
          .locator("#shot")
          .screenshot({ path: join(OUT, `${browserName}-${name}-glance-${size}-${theme}.png`) });
      }
      for (const pane of PANES) {
        const mounted = await mount(page, name, { face: "detail", size: "full", theme, pane });
        const ready = mounted.messages.find((message) => message.kind === "ready");
        report.push({
          name,
          face: "detail",
          pane,
          theme,
          box: mounted.box,
          metrics: ready?.metrics,
          errors: errors(mounted),
        });
        expect(errors(mounted), `${name} detail ${pane} ${theme}`).toEqual([]);
        expect(ready?.metrics, `${name} detail ${pane} ${theme}`).toMatchObject({
          overflow_x: false,
        });
        await page
          .locator("#shot")
          .screenshot({ path: join(OUT, `${browserName}-${name}-detail-${pane}-${theme}.png`) });
      }
    }
  }
  // The empty state, as a tile with nothing to show draws it.
  const nothing = {
    ...(demo("schema-change").data as Record<string, unknown>),
    tables: [],
    concerns: [],
  };
  for (const face of ["glance", "detail"] as const) {
    const mounted = await mount(page, "schema-change", {
      face,
      size: "half",
      theme: "light",
      data: nothing,
    });
    expect(mounted.messages.find((message) => message.kind === "ready")?.metrics).toMatchObject({
      empty_state: true,
    });
    await page
      .locator("#shot")
      .screenshot({ path: join(OUT, `${browserName}-schema-change-${face}-empty.png`) });
  }
  writeFileSync(join(OUT, `${browserName}-report.json`), `${JSON.stringify(report, null, 2)}\n`);
});

test("what goes wrong in a real frame is reported", async ({ page }) => {
  await page.goto(origin);
  const run = (mode: string, face: "glance" | "detail") =>
    mount(page, "broken", { face, size: "half", theme: "light", data: { mode } });

  expect(errors(await run("throw", "glance"))).toEqual([
    expect.objectContaining({
      code: "script_error",
      message: "RangeError: no rows to draw",
      line: 10,
    }),
  ]);
  expect(errors(await run("network", "glance"))).toEqual([
    expect.objectContaining({ code: "csp_violation", blocked: "https://tracker.example.invalid" }),
  ]);
  expect(errors(await run("fail", "detail"))).toEqual([
    expect.objectContaining({ code: "render_failed", message: "The data names no tables." }),
  ]);
  const loop = await run("loop", "detail");
  expect(errors(loop)).toEqual([expect.objectContaining({ code: "layout_loop" })]);
  expect(loop.messages.filter((message) => message.kind === "resize").length).toBeLessThanOrEqual(
    10,
  );
});
