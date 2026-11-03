import { mkdir, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, type Locator, type Page, test } from "@playwright/test";

import { launchURL } from "../e2e/inbox-helpers";

/** Where the frames go; video/encode.sh turns them into the website's video. */
const FRAMES = join(import.meta.dirname, "frames");

/** A pointer drawn into the page, since a headless browser shows none: it
 *  follows the mouse and dips when pressed. Made to look like a plain arrow. */
function installPointer() {
  const add = () => {
    if (document.getElementById("demo-pointer")) return;
    const pointer = document.createElement("div");
    pointer.id = "demo-pointer";
    pointer.innerHTML =
      '<svg width="22" height="26" viewBox="0 0 22 26"><path d="M2 2 L2 21 L7 16.5 L10.5 24 L14 22.5 L10.5 15 L17 15 Z" fill="#1f1b17" stroke="#fff" stroke-width="1.6" stroke-linejoin="round"/></svg>';
    Object.assign(pointer.style, {
      position: "fixed",
      left: "0",
      top: "0",
      zIndex: "2147483647",
      pointerEvents: "none",
      // Placed by `translate`, which applies after `scale`, so a press
      // shrinks the arrow where it is; inside `transform` it would scale
      // the arrow's place on the page too, and the arrow would jump.
      translate: "-100px -100px",
      transition: "scale 120ms ease",
      transformOrigin: "2px 2px",
      filter: "drop-shadow(0 2px 3px rgba(0,0,0,0.25))",
    });
    document.documentElement.append(pointer);
    addEventListener(
      "mousemove",
      (event) => {
        pointer.style.translate = `${event.clientX - 2}px ${event.clientY - 2}px`;
      },
      true,
    );
    addEventListener("mousedown", () => (pointer.style.scale = "0.82"), true);
    addEventListener("mouseup", () => (pointer.style.scale = "1"), true);
  };
  if (document.readyState === "loading") addEventListener("DOMContentLoaded", add);
  else add();
}

/** Tidies the app for the video only; the app itself is unchanged. It hides
 *  what would only distract: "Watch the agent", the fix's link to its live
 *  transcript (the video moves on while the agent works), the Inbox's "Merge
 *  all", and of the fix's banner, the step its row already says and "You can
 *  move on". The fixing row says only "Being fixed". On the Sessions page
 *  each pull request is a small tag on one line, brown, or green for #209,
 *  which is ready to merge. */
function hideControls() {
  const style = document.createElement("style");
  style.textContent = `
    .local-session-pr {
      flex: none;
      margin-left: 0 !important;
      padding: 0.05rem 0.4rem;
      border: 1px solid var(--status-caution-border);
      border-radius: var(--radius-sm);
      background: var(--status-caution-bg);
      color: var(--status-caution) !important;
      font-size: var(--text-xs);
      font-weight: 500;
      white-space: nowrap;
    }
    .local-session-pr.demo-ready {
      border-color: var(--status-positive-border);
      background: var(--status-positive-bg);
      color: var(--status-positive) !important;
    }`;
  const addStyle = () => document.head.append(style);
  if (document.head) addStyle();
  else addEventListener("DOMContentLoaded", addStyle);
  const hidden = (text: string) => text === "Watch the agent" || text.startsWith("Merge all");
  const hide = () => {
    for (const element of document.querySelectorAll<HTMLElement>("a, button")) {
      if (hidden(element.textContent?.trim() ?? "")) element.style.display = "none";
    }
    for (const element of document.querySelectorAll<HTMLElement>(".local-session-pr")) {
      if (element.textContent === "PR #209") element.classList.add("demo-ready");
    }
    for (const element of document.querySelectorAll<HTMLElement>(".upnext-row-working-text")) {
      const text = element.textContent ?? "";
      const number = /^(#\d+) · /.exec(text)?.[1];
      if (number && text !== `${number} · Being fixed`)
        element.textContent = `${number} · Being fixed`;
    }
    // The fix's banner names its step, which the row beside it already says.
    for (const element of document.querySelectorAll<HTMLElement>("span")) {
      const text = element.textContent ?? "";
      if (element.children.length === 0 && /^An agent is fixing it · .+\.$/.test(text))
        element.textContent = "An agent is fixing it.";
      if (text === "You can move on") element.style.display = "none";
    }
  };
  new MutationObserver(hide).observe(document, {
    subtree: true,
    childList: true,
    characterData: true,
  });
}

/** The video's chapters, each with the time it starts, named as the
 *  sidebar link that opens it: on the website, a click on that link in the
 *  video plays it from there. */
const chapters: { title: string; time: number }[] = [];

/** Starts the chapter `title` now. */
function chapter(title: string) {
  chapters.push({ title, time: Date.now() / 1000 });
}

type Box = { title: string; x: number; y: number; width: number; height: number };

/** Where the chapters' sidebar links are, from each time on: the website puts
 *  its click targets there. They move down once the Work inbox is added. */
const sidebar: { time: number; links: Box[] }[] = [];

/** Measures the chapters' sidebar links now. */
async function measureSidebar(page: Page) {
  const nav = page.getByRole("navigation", { name: "Main" });
  const links: Box[] = [];
  for (const title of ["Inbox", "Pull requests", "Timeline", "Sessions"]) {
    const link = nav
      .getByRole("link", title === "Inbox" ? { name: /^Inbox/ } : { name: title, exact: true })
      .first();
    const box = await link.boundingBox();
    if (!box) throw new Error(`the sidebar's ${title} is not on screen`);
    links.push({ title, ...box });
  }
  sidebar.push({ time: Date.now() / 1000, links });
}

/** Records the page as fast as Chrome draws screenshots of it, each with its
 *  time. Screenshots, unlike Chrome's screencast, are taken at the device's
 *  pixels, so the video stays sharp on high-density screens. */
async function startRecording(page: Page) {
  await rm(FRAMES, { recursive: true, force: true });
  await mkdir(FRAMES, { recursive: true });
  const cdp = await page.context().newCDPSession(page);
  const frames: { file: string; time: number }[] = [];
  const writes: Promise<void>[] = [];
  const { width, height } = page.viewportSize() ?? { width: 1056, height: 800 };
  let recording = true;
  const loop = (async () => {
    while (recording) {
      const time = Date.now() / 1000;
      const { data } = await cdp.send("Page.captureScreenshot", {
        format: "jpeg",
        quality: 92,
        optimizeForSpeed: true,
        clip: { x: 0, y: 0, width, height, scale: 2 },
      });
      const file = `${String(frames.length).padStart(6, "0")}.jpg`;
      frames.push({ file, time });
      writes.push(writeFile(join(FRAMES, file), Buffer.from(data, "base64")));
    }
  })();
  return async () => {
    recording = false;
    await loop;
    await Promise.all(writes);
    const end = Date.now() / 1000;
    await writeFile(
      join(FRAMES, "frames.json"),
      JSON.stringify({ frames, chapters, sidebar, end }),
    );
    const seconds = end - (frames[0]?.time ?? end);
    console.log(
      `${frames.length} frames in ${seconds.toFixed(1)} s, ${(frames.length / seconds).toFixed(1)} a second`,
    );
  };
}

/** Where the pointer is, so the next move starts from there. */
let at = { x: 900, y: 120 };

/** Glides the pointer to the middle of `target` as a hand would: easing in
 *  and out, paced by the clock rather than by how fast the browser takes
 *  the moves, over a time that grows only gently with the distance, so a
 *  long move is no jump and a short one no crawl. */
async function moveTo(page: Page, target: Locator, offset = { x: 0.5, y: 0.5 }) {
  await target.scrollIntoViewIfNeeded();
  const box = await target.boundingBox();
  if (!box) throw new Error("the target is not on screen");
  const to = { x: box.x + box.width * offset.x, y: box.y + box.height * offset.y };
  const from = at;
  const distance = Math.hypot(to.x - from.x, to.y - from.y);
  const duration = Math.min(1300, 650 + distance * 0.9);
  const ease = (t: number) => (t < 0.5 ? 4 * t ** 3 : 1 - (-2 * t + 2) ** 3 / 2);
  const start = Date.now();
  for (;;) {
    const t = Math.min(1, (Date.now() - start) / duration);
    const e = ease(t);
    await page.mouse.move(from.x + (to.x - from.x) * e, from.y + (to.y - from.y) * e);
    if (t === 1) break;
    await page.waitForTimeout(16);
  }
  at = to;
  await page.waitForTimeout(250);
}

/** Moves to `target` and clicks it, as a person would, starting the chapter
 *  `options.chapter` as it presses. */
async function click(
  page: Page,
  target: Locator,
  options: { button?: "left" | "right"; chapter?: string } = {},
) {
  await moveTo(page, target);
  if (options.chapter) chapter(options.chapter);
  await page.mouse.down({ button: options.button });
  await page.waitForTimeout(110);
  await page.mouse.up({ button: options.button });
}

test("product video", async ({ page }) => {
  await page.addInitScript(installPointer);
  await page.addInitScript(hideControls);
  await page.goto(await launchURL());
  const sidebar = page.getByRole("navigation", { name: "Main" });
  await expect(page.getByRole("heading", { name: "Up next", exact: true })).toBeVisible();
  // Let reviews, plan usage and the sidebar settle before the first frame.
  await page.waitForTimeout(2500);
  await page.mouse.move(at.x, at.y);

  const stop = await startRecording(page);
  chapter("Inbox");
  await measureSidebar(page);
  await page.waitForTimeout(2200);

  // The Inbox: what waits on you. Then a new inbox for work.
  await click(page, sidebar.getByRole("link", { name: /^Inbox/ }), { button: "right" });
  await page.waitForTimeout(1300);
  await click(page, page.getByRole("menuitem", { name: "New inbox…" }));
  await page.waitForTimeout(2100);
  await click(page, page.getByRole("button", { name: /^Work/ }));
  await page.waitForTimeout(1600);
  await click(page, page.getByRole("button", { name: "Save", exact: true }));
  await page.waitForTimeout(300);
  await measureSidebar(page);
  await page.waitForTimeout(1900);

  // Back to the Inbox, to the pull request that needs you, and a fix.
  await click(page, sidebar.getByRole("link", { name: /^Inbox/ }));
  await page.waitForTimeout(1200);
  await click(
    page,
    page.getByRole("button", { name: /^Retry webhook deliveries with backoff/ }).first(),
  );
  await page.waitForTimeout(1800);
  const fix = page.getByRole("button", { name: /^Fix / }).first();
  await click(page, fix);
  // The agent takes the fix from here; the app says you can move on, and the
  // video does.
  await expect(page.getByText("Being fixed").first()).toBeVisible({ timeout: 30_000 });
  // The fixture's agent takes about four seconds; leave while it works.
  await page.waitForTimeout(1800);

  // The agent sessions on this computer, and the pull request each works on.
  await click(page, sidebar.getByRole("link", { name: "Sessions", exact: true }).first(), {
    chapter: "Sessions",
  });
  await page.waitForTimeout(4220);

  // Every pull request, at a glance.
  await click(page, sidebar.getByRole("link", { name: "Pull requests", exact: true }).first(), {
    chapter: "Pull requests",
  });
  await page.waitForTimeout(3500);

  // The Timeline's features, and one of them opened.
  await click(page, sidebar.getByRole("link", { name: "Timeline", exact: true }).first(), {
    chapter: "Timeline",
  });
  await page.waitForTimeout(2200);
  await click(
    page,
    page
      .getByRole("list", { name: "Features" })
      .getByRole("button", { name: /Webhook reliability/ }),
  );
  await page.waitForTimeout(1800);
  await click(page, page.getByText("2 key decisions").first());
  await page.waitForTimeout(2900);

  await stop();
});
