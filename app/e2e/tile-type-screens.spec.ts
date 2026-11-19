import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";

// Pictures of tile types: every sample of every type the Go library check
// wrote into DIR (`just local_app::tile-type-screens`, which runs
// tiles/library's TestRenderDirs first), as glances at each declared size
// and on a phone, and as details in the pane and on a phone, in light and
// dark, mounted as the tile page mounts them (a sandboxed frame under the
// frame route's headers, the handshake, a card's glance box, a pane's
// auto-height detail). It fails on a script error, a horizontal overflow, a
// citation that does not resolve and a glance that overflows its box (the
// maximal edge sample only warns: the card's fade covers it), and writes
// the pictures, a glance contact sheet per type and report.json into DIR.
// Taken only on request (TILE_TYPE_SCREENSHOTS=1).
test.skip(!process.env.TILE_TYPE_SCREENSHOTS, "screenshots are taken on request");

const DIR = "test-results/tile-types";
const KIT = "../tiles/kit";
const CSP =
  "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox allow-scripts";

// The tile page's cards (oss/app/src/tiles/card.tsx) in a 1056px grid of six
// columns, and a phone, where every card takes the width less the gutters.
const CARDS = {
  third: { width: 344, height: 256 },
  half: { width: 522, height: 272 },
  full: { width: 1056, height: 208 },
} as const;
const PHONE = 343;
const PANES = [720, 360];
const THEMES = ["light", "dark"] as const;
type Size = keyof typeof CARDS;
type Theme = (typeof THEMES)[number];
type Sample = {
  name: string;
  edge?: boolean;
  data: unknown;
  glance: Record<string, unknown>;
  detail: Record<string, unknown>;
};
type TileType = { name: string; title: string; sizes: Size[]; samples: Sample[] };
type Message = {
  kind: string;
  code?: string;
  message?: string;
  metrics?: Metrics;
  height?: number;
};
type Metrics = {
  overflow_x?: boolean;
  overflow_y?: boolean;
  unresolved?: number;
  empty_state?: boolean;
};
type Mounted = { messages: Message[]; box: { width: number; height: number } };

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
  .pane { box-sizing: content-box; padding: 16px 24px 24px; background: var(--surface-card); }
  .pane header { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; font-size: 13px;
    font-weight: 500; letter-spacing: 0.08em; text-transform: uppercase; color: var(--text-tertiary); }
  .pane h2 { margin: 0 0 16px; font-family: var(--font-heading); font-size: 24px; line-height: 1.25; font-weight: 800; }
  .pane iframe { display: block; width: calc(100% + 8px); height: 120px; margin: -4px; border: 0; }
  .sheet { display: flex; flex-wrap: wrap; gap: 16px; align-items: flex-start; }
  .sheet figure { margin: 0; }
  .sheet figcaption { font: 12px/1.4 var(--font-mono); color: var(--text-secondary); margin-bottom: 4px; }
  .sheet img { display: block; }
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
    const header = document.createElement("header");
    header.textContent = "← Back · Custom";
    const heading = document.createElement("h2");
    heading.textContent = title;
    holder.append(header, heading, iframe);
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
    }, 300);
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
window.sheet = (title, shots) => {
  document.documentElement.dataset.theme = "light";
  const stage = document.getElementById("stage");
  stage.replaceChildren();
  const heading = document.createElement("h2");
  heading.textContent = title;
  const sheet = document.createElement("div");
  sheet.className = "sheet";
  sheet.id = "shot";
  for (const shot of shots) {
    const figure = document.createElement("figure");
    const caption = document.createElement("figcaption");
    caption.textContent = shot.caption;
    const image = document.createElement("img");
    image.src = shot.src;
    figure.append(caption, image);
    sheet.append(figure);
  }
  stage.append(heading, sheet);
  return Promise.all([...sheet.querySelectorAll("img")].map((image) => image.decode()));
};
</script>
</body>
</html>`;

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
    } else if (/^\/tile-frames\/[a-z][a-z0-9_]*$/.test(url.pathname)) {
      response.writeHead(200, {
        "content-type": "text/html; charset=utf-8",
        "content-security-policy": CSP,
        "x-content-type-options": "nosniff",
        "referrer-policy": "no-referrer",
        "cache-control": "no-store",
        "cross-origin-resource-policy": "same-origin",
      });
      response.end(readFileSync(join(DIR, `${url.pathname.slice(13)}.html`)));
    } else {
      response.writeHead(404).end();
    }
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  origin = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  mkdirSync(DIR, { recursive: true });
});

test.afterAll(() => {
  server.close();
});

async function mount(
  page: Page,
  type: TileType,
  sample: Sample,
  options: {
    face: "glance" | "detail";
    size: Size;
    theme: Theme;
    card?: { width: number; height: number };
    pane?: number;
  },
) {
  return (await page.evaluate(
    (args) => (window as unknown as { mount: (args: unknown) => Promise<unknown> }).mount(args),
    {
      src: `/tile-frames/${type.name}`,
      face: options.face,
      size: options.size,
      theme: options.theme,
      title: type.title,
      data: sample.data,
      refs: options.face === "glance" ? sample.glance : sample.detail,
      card: options.card ?? CARDS[options.size],
      pane: options.pane ?? 720,
    },
  )) as Mounted;
}

test("tile types in light and dark, glance and detail", async ({ page }) => {
  const index = join(DIR, "index.json");
  expect(existsSync(index), "run the Go check first (just local_app::tile-type-screens)").toBe(
    true,
  );
  const names = JSON.parse(readFileSync(index, "utf8")) as string[];
  const only = (process.env.TILE_TYPE_SAMPLES ?? "").split(",").filter(Boolean);
  await page.setViewportSize({ width: 1200, height: 900 });
  await page.goto(origin);
  const report: unknown[] = [];
  const failures: string[] = [];
  for (const name of names) {
    const type = JSON.parse(readFileSync(join(DIR, `${name}.json`), "utf8")) as TileType;
    const sheet: { caption: string; src: string }[] = [];
    for (const sample of type.samples) {
      if (only.length && !only.includes(sample.name)) continue;
      const glances: { label: string; size: Size; card?: { width: number; height: number } }[] = [
        ...type.sizes.map((size) => ({ label: size, size })),
        {
          label: "phone",
          size: type.sizes[0],
          card: { width: PHONE, height: CARDS[type.sizes[0]].height },
        },
      ];
      for (const theme of THEMES) {
        for (const glance of glances) {
          const where = `${name}/${sample.name} glance ${glance.label} ${theme}`;
          const mounted = await mount(page, type, sample, {
            face: "glance",
            size: glance.size,
            theme,
            card: glance.card,
          });
          const check = judge(mounted, sample, "glance");
          if (check.metrics.overflow_x) check.errors.push(`too wide: ${await widest(page)}`);
          report.push({
            name,
            sample: sample.name,
            face: "glance",
            size: glance.label,
            theme,
            box: mounted.box,
            ...check,
          });
          failures.push(...check.errors.map((error) => `${where}: ${error}`));
          const path = join(DIR, `${name}-${sample.name}-glance-${glance.label}-${theme}.png`);
          await page.locator("#shot").screenshot({ path });
          sheet.push({
            caption: `${sample.name} · ${glance.label} · ${theme}`,
            src: `data:image/png;base64,${readFileSync(path).toString("base64")}`,
          });
        }
        for (const pane of PANES) {
          const where = `${name}/${sample.name} detail ${pane} ${theme}`;
          const mounted = await mount(page, type, sample, {
            face: "detail",
            size: type.sizes[0],
            theme,
            pane,
          });
          const check = judge(mounted, sample, "detail");
          if (check.metrics.overflow_x) check.errors.push(`too wide: ${await widest(page)}`);
          report.push({
            name,
            sample: sample.name,
            face: "detail",
            pane,
            theme,
            box: mounted.box,
            ...check,
          });
          failures.push(...check.errors.map((error) => `${where}: ${error}`));
          await page
            .locator("#shot")
            .screenshot({ path: join(DIR, `${name}-${sample.name}-detail-${pane}-${theme}.png`) });
        }
      }
    }
    for (const theme of THEMES) {
      const shots = sheet.filter((shot) => shot.caption.endsWith(theme));
      if (shots.length === 0) continue;
      await page.evaluate(
        ([title, list]) =>
          (window as unknown as { sheet: (t: string, s: unknown) => Promise<unknown> }).sheet(
            title,
            list,
          ),
        [`${type.title}: glances, ${theme}`, shots] as const,
      );
      await page
        .locator("#stage")
        .screenshot({ path: join(DIR, `${name}-sheet-glance-${theme}.png`) });
    }
  }
  writeFileSync(join(DIR, "report.json"), `${JSON.stringify({ failures, report }, null, 2)}\n`);
  expect(failures).toEqual([]);
});

/** The innermost elements of the mounted frame that reach past its right
 *  edge, as tag.class, so an author sees what to wrap or truncate. */
async function widest(page: Page) {
  const frame = page.frames().find((f) => f !== page.mainFrame());
  if (!frame) return "";
  return frame.evaluate(() => {
    const width = document.documentElement.clientWidth;
    const wide = [...document.querySelectorAll("#baum-root *")].filter(
      (el) =>
        el.getBoundingClientRect().right > width + 2 &&
        ![...el.children].some((child) => child.getBoundingClientRect().right > width + 2),
    );
    return wide
      .slice(0, 3)
      .map(
        (el) =>
          `${el.localName}${el.className && typeof el.className === "string" ? `.${el.className.trim().split(/\s+/).join(".")}` : ""} "${(el.textContent ?? "").slice(0, 40)}"`,
      )
      .join(", ");
  });
}

/** What a render did wrong. A glance that overflows its box is an error for
 *  the type's own samples and the minimal one, and a warning for the
 *  maximal one, whose overflow the card's fade covers. */
function judge(mounted: Mounted, sample: Sample, face: "glance" | "detail") {
  const errors: string[] = [];
  const warnings: string[] = [];
  for (const message of mounted.messages) {
    if (message.kind === "error") errors.push(`${message.code}: ${message.message}`);
  }
  const ready = mounted.messages.find((message) => message.kind === "ready");
  if (!ready) errors.push("never ready");
  const metrics = ready?.metrics ?? {};
  if (metrics.overflow_x) errors.push("overflows sideways");
  if (metrics.unresolved) errors.push(`${metrics.unresolved} citations do not resolve`);
  if (face === "glance" && metrics.overflow_y) {
    (sample.name === "maximal" ? warnings : errors).push("overflows the glance box");
  }
  return { errors, warnings, metrics };
}
