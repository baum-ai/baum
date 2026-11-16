import { basename } from "node:path";
import { expect, type Page } from "@playwright/test";

// What the real-review screenshot spec uses: waiting for a steady page,
// finding what a picture would not show, and taking a viewport and a
// full-page picture.

/** A problem a picture does not show clearly. `truncated` names a full-page
 *  picture that does not hold the whole page. */
export type Issue = { where: string; what: string; truncated?: string };

/** Waits until the page has stopped changing: nothing spins, fonts and images
 *  have loaded, no excerpt is loading. What did not settle is returned. */
export async function settle(page: Page, where: string, tiles: boolean): Promise<Issue[]> {
  const issues: Issue[] = [];
  // What is still going on, as names; empty once the page has settled.
  const pending = () =>
    page.evaluate(() => {
      const what: string[] = [];
      if (document.querySelector(".animate-spin")) what.push("a spinner");
      if (/Loading (excerpt|historical)/.test(document.body.innerText)) what.push("an excerpt");
      // A lazy image below the window loads only once scrolled to, so it is
      // not waited for: the window's picture does not show it.
      const below = (i: HTMLImageElement) =>
        i.loading === "lazy" && i.getBoundingClientRect().top > innerHeight;
      if ([...document.images].some((i) => !i.complete && !below(i))) what.push("an image");
      return what;
    });
  await page.evaluate(() => document.fonts.ready);
  try {
    await expect.poll(pending, { timeout: 20_000 }).toEqual([]);
  } catch {
    issues.push({
      where,
      what: `the page did not settle: ${(await pending()).join(", ")} still loading`,
    });
  }
  issues.push(...(await steady(page, where, tiles)));
  return issues;
}

/** Waits until the review pane holds its content (the review page's own
 *  loading status gone; on the tiles page, the landing page's band or a
 *  tile's heading drawn), every finite animation has finished, and its
 *  layout is the same twice running: the first picture of a run was taken
 *  while the pane was still arriving. */
export async function steady(page: Page, where: string, tiles: boolean): Promise<Issue[]> {
  const layout = () =>
    page.evaluate((tiles) => {
      const pane = document.querySelector("#review-section");
      if (!pane || pane.querySelector(".loading-status")) return "loading";
      if (tiles && !pane.querySelector('[aria-label="Review at a glance"], h2:not(.sr-only)'))
        return "no content";
      const moving = document
        .getAnimations()
        .some(
          (animation) =>
            animation.playState === "running" &&
            animation.effect?.getComputedTiming().iterations !== Infinity,
        );
      if (moving) return "animating";
      return [pane, ...pane.querySelectorAll("article, h2, h3, section")]
        .slice(0, 40)
        .map((element) => {
          const box = element.getBoundingClientRect();
          return `${Math.round(box.x)},${Math.round(box.y)},${Math.round(box.width)},${Math.round(box.height)}`;
        })
        .join(";");
    }, tiles);
  const deadline = Date.now() + 20_000;
  let previous = "";
  while (Date.now() < deadline) {
    const now = await layout();
    if (now === previous && !["loading", "no content", "animating"].includes(now)) {
      // Transitions after the last change.
      await page.waitForTimeout(200);
      return [];
    }
    previous = now;
    await page.waitForTimeout(250);
  }
  return [{ where, what: `the review pane did not settle (${previous.split(";")[0]})` }];
}

/** What is wrong with a settled page that a picture would not show clearly. */
export async function inspect(page: Page, where: string): Promise<Issue[]> {
  const found = await page.evaluate(() => {
    const what: string[] = [];
    for (const alert of document.querySelectorAll("[role='alert']")) {
      const text = (alert as HTMLElement).innerText.trim();
      if (text) what.push(`notice: ${text.slice(0, 160)}`);
    }
    const body = document.body.innerText;
    // An excerpt of a binary file is unavailable by nature: there is no text
    // to show. Said apart, so it is not taken for a missing commit.
    const unavailable = [...body.matchAll(/Excerpt unavailable\s*·\s*View (\S+)/g)].map(
      (match) => match[1]!,
    );
    const binary = /\.(png|jpe?g|gif|webp|ico|mp4|webm|mov|pdf|zip|gz|woff2?|ttf|otf|sqlite)$/i;
    if (unavailable.some((path) => !binary.test(path))) what.push("an excerpt is unavailable");
    else if (unavailable.length > 0)
      what.push("an excerpt of a binary file has no text (expected)");
    if ([...document.images].some((i) => i.complete && i.naturalWidth === 0 && i.currentSrc))
      what.push("an image failed to load");
    if (document.documentElement.scrollWidth > innerWidth + 1)
      what.push("the page scrolls sideways");
    return what;
  });
  return found.map((what) => ({ where, what }));
}

/** The tallest scrolling region and how far it scrolls: the app keeps its
 *  panes in regions of their own, so a full-page picture is a viewport as
 *  tall as the content. */
export function scrollExtra(page: Page): Promise<number> {
  return page.evaluate(() => {
    let extra = document.documentElement.scrollHeight - innerHeight;
    for (const el of document.querySelectorAll<HTMLElement>("*")) {
      const overflow = getComputedStyle(el).overflowY;
      if ((overflow === "auto" || overflow === "scroll") && el.clientHeight > innerHeight / 3)
        extra = Math.max(extra, el.scrollHeight - el.clientHeight);
    }
    return Math.max(extra, 0);
  });
}

// Chromium cannot draw a picture much taller than this.
const MAX_HEIGHT = 16000;

export async function capture(
  page: Page,
  base: string,
  size: { width: number; height: number },
  issues: Issue[],
  options: { fullPage: boolean; tiles: boolean },
) {
  await page.screenshot({ path: `${base}.png`, animations: "disabled" });
  if (!options.fullPage) return;
  // Growing the viewport can lengthen the content (regions that render what
  // fits), so grow until it all fits. A region whose overflow does not shrink
  // as the viewport grows (it is sized to the viewport) is not more content.
  let height = size.height;
  let previous = Number.POSITIVE_INFINITY;
  let truncated = false;
  for (let round = 0; ; round++) {
    const extra = await scrollExtra(page);
    if (extra === 0 || extra >= previous) break;
    if (round === 6 || height >= MAX_HEIGHT) {
      truncated = true;
      break;
    }
    previous = extra;
    height = Math.min(height + extra, MAX_HEIGHT);
    await page.setViewportSize({ width: size.width, height });
    issues.push(...(await settle(page, `${base} (full page)`, options.tiles)));
  }
  if (height === size.height) return;
  if (truncated)
    issues.push({
      where: base,
      what: "the full-page picture is truncated (the page kept growing or hit the height limit)",
      truncated: `${basename(base)}-full.png`,
    });
  await page.screenshot({ path: `${base}-full.png`, animations: "disabled" });
  await page.setViewportSize(size);
  await page.waitForTimeout(200);
}
