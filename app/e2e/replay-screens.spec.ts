import { copyFile, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { launchURL } from "./inbox-helpers";
import { capture, inspect, scrollExtra, settle, type Issue } from "./review-screens";

// Pictures of a replay run's layouts, taken only on request
// (REPLAY_SCREENS=1) against the real-reviews fixture daemon serving the run
// (BAUM_APP_REPLAY_RUN; see `just replay-screens`). Each case's review is
// served twice, with the run's composition and with the template's, and both
// tiles pages are photographed at each size and theme, as a viewport picture
// and a full-page one, into <run>/screens/<case>/. At 1440×900 in the light
// theme the spec also measures the first screen, written to
// first-screen.json (variants), and the phone's at 390×844 (phone). The pictures and facts hold private content and stay in
// the run directory. Settings, all optional:
//   REPLAY_SCREENS_OUT      where the pictures go (default /replay-run/screens)
//   REPLAY_SCREENS_THEMES   light (default), or light,dark
//   REPLAY_SCREENS_FULL=0   viewport pictures only
//   REPLAY_SCREENS_MINUTES  the time one case may take (default 6)
test.skip(!process.env.REPLAY_SCREENS, "screenshots are taken on request");

const OUT = process.env.REPLAY_SCREENS_OUT || "/replay-run/screens";
const THEMES = (process.env.REPLAY_SCREENS_THEMES || "light").split(",") as ("light" | "dark")[];
const FULL_PAGE = process.env.REPLAY_SCREENS_FULL !== "0";
const BUDGET_MS = Number(process.env.REPLAY_SCREENS_MINUTES || 6) * 60_000;

const SIZES = [
  { width: 1440, height: 900 },
  { width: 900, height: 900 },
  { width: 390, height: 844 },
];
/** The size whose first screen is measured, and the phone's, measured too. */
const MEASURED = SIZES[0]!;
const PHONE = SIZES[2]!;

type Pick = {
  repository: string;
  number: number;
  version: number;
  clone: string;
  case: string;
  variant: string;
  size?: string;
};

type ReplayCase = { id: string; size?: string; problem?: string };

/** A tile card as the first screen shows it. */
type Placed = {
  id: string;
  type: string;
  size: string;
  /** full: wholly inside the window; partial: cut by its edge or a region. */
  visible: "full" | "partial";
};

/** One must-know signal: whether the first screen shows it. */
type Signal = { id: string; visible: boolean };

/** What the first screen of a tiles page shows, measured from the page. */
export type FirstScreen = {
  width: number;
  height: number;
  /** The scrolling content's height: how long the page is. */
  pageHeight: number;
  /** The cards wholly or partly in the window, in reading order. */
  tiles: Placed[];
  /** Cards on the landing page, and those below the first screen. */
  landingTiles: number;
  belowTiles: number;
  /** Grid units (6 a row) left empty on rows another row follows, on the
   *  first screen and on the whole landing page, from the cards' widths, so
   *  a one-column phone grid wastes none. */
  wastedUnits: number;
  wastedUnitsLanding: number;
  recommendation: { given: boolean; visible: boolean };
  /** The active critical and high risks of current changes. */
  severeRisks: Signal[];
  /** The changes the recommendation cites, as their tiles. */
  namedChanges: Signal[];
  /** The current changes rated highest (assessment level 3 or more), as
   *  their tiles. */
  riskiestChanges: Signal[];
  /** The first three active medium risks the Risks tile ranks. */
  mediumRisks: Signal[];
  breaking: { given: boolean; visible: boolean };
  missingEvidence: { count: number; visible: boolean };
  /** The share of signals given that are visible; null with none given. */
  recall: number | null;
};

test("pictures of a replay run's layouts", async ({ page }) => {
  test.setTimeout(0);
  const state = process.env.BAUM_APP_STATE_DIR ?? "";
  const picks: Pick[] = JSON.parse(await readFile(join(state, "picks.json"), "utf8"));
  const cases: ReplayCase[] = JSON.parse(await readFile(join(state, "replay-cases.json"), "utf8"));
  await mkdir(OUT, { recursive: true });
  await copyFile(join(state, "replay-cases.json"), join(OUT, "cases.json"));
  expect(picks.length).toBeGreaterThan(0);
  await page.goto(await launchURL());
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  const launch = new URL(page.url());
  launch.search = "";
  const consoleErrors: string[] = [];
  page.on("console", (m) => m.type() === "error" && consoleErrors.push(m.text()));
  page.on("pageerror", (e) => consoleErrors.push(String(e)));

  const shown: string[] = [];
  for (const c of cases) {
    const variants = picks.filter((p) => p.case === c.id);
    if (variants.length === 0) continue;
    shown.push(c.id);
    const dir = join(OUT, c.id);
    await rm(dir, { recursive: true, force: true });
    await mkdir(dir, { recursive: true });
    const issues: Issue[] = [];
    const facts: Record<string, FirstScreen> = {};
    const phone: Record<string, FirstScreen> = {};
    const started = Date.now();
    await test.step(c.id, async () => {
      for (const pick of variants) {
        const address = () => {
          const url = new URL(launch);
          url.search = new URLSearchParams({
            clone: pick.clone,
            pr: String(pick.number),
            view: "tiles",
            review: String(pick.version),
          }).toString();
          return url.href;
        };
        try {
          for (const theme of THEMES) {
            await page.emulateMedia({ colorScheme: theme });
            for (const size of SIZES) {
              if (Date.now() - started > BUDGET_MS)
                throw new Error("the case's time budget ran out");
              const name = `${pick.variant}-${size.width}-${theme}`;
              await page.setViewportSize(size);
              await page.goto(address());
              issues.push(...(await settle(page, name, true)));
              issues.push(...(await inspect(page, name)));
              if (size === MEASURED && theme === THEMES[0])
                facts[pick.variant] = await firstScreen(page, pick);
              if (size === PHONE && theme === THEMES[0])
                phone[pick.variant] = await firstScreen(page, pick);
              await capture(page, join(dir, name), size, issues, {
                fullPage: FULL_PAGE,
                tiles: true,
              });
            }
          }
        } catch (error) {
          issues.push({
            where: pick.variant,
            what: `left unfinished: ${String(error).split("\n")[0]}`,
          });
        }
      }
    });
    const unique = [...new Map(issues.map((i) => [`${i.where}|${i.what}`, i])).values()];
    await writeFile(
      join(dir, "first-screen.json"),
      JSON.stringify(
        {
          case: c.id,
          size: c.size ?? "",
          version: variants[0]!.version,
          viewport: MEASURED,
          variants: facts,
          phone,
          issues: unique,
        },
        null,
        2,
      ),
    );
  }
  const unexpected = consoleErrors.filter(
    (e) =>
      !/^Failed to load resource: the server responded with a status of (404|502|503)\b/.test(e),
  );
  await writeFile(
    join(OUT, "screens.json"),
    JSON.stringify(
      {
        sizes: SIZES,
        themes: THEMES,
        cases: shown,
        problems: cases.filter((c) => c.problem).map((c) => ({ id: c.id, problem: c.problem })),
        unexpectedConsoleErrors: [...new Set(unexpected)],
      },
      null,
      2,
    ),
  );
});

/** Measures the first screen of the tiles page the window shows: which cards
 *  it holds and whether each must-know signal is in it, unscrolled. A signal
 *  is visible when at least half of its element is inside the window and
 *  every region that clips it. */
async function firstScreen(page: Page, pick: Pick): Promise<FirstScreen> {
  const extra = await scrollExtra(page);
  return page.evaluate(
    async ({ pick, extra }) => {
      const query = new URLSearchParams({
        clone: pick.clone,
        pr: String(pick.number),
        version: String(pick.version),
      });
      const answer = await fetch(`api/tiles?${query}`);
      type Doc = {
        changes: {
          id: string;
          kind: string;
          derived: { state: string };
          assessment?: { level?: number } | null;
        }[];
        risks: { id: string; change: string; tier: string; state?: string }[];
        evidence: { id: string; change: string; status: string }[];
        tiles: { id: string; type: string; data: unknown }[];
        recommendation?: { decision: string; refs?: string[] } | null;
      };
      const doc: Doc = (await answer.json()).tiles;
      const current = new Set(
        doc.changes.filter((c) => c.derived.state !== "stale").map((c) => c.id),
      );

      /** The part of an element inside the window and every clipping
       *  ancestor, as a share of its area: 0 to 1. */
      const shown = (element: Element | null): number => {
        if (!element) return 0;
        const box = element.getBoundingClientRect();
        if (box.width === 0 || box.height === 0) return 0;
        let top = Math.max(box.top, 0);
        let bottom = Math.min(box.bottom, innerHeight);
        let left = Math.max(box.left, 0);
        let right = Math.min(box.right, innerWidth);
        for (let up = element.parentElement; up; up = up.parentElement) {
          const style = getComputedStyle(up);
          if (style.overflowX === "visible" && style.overflowY === "visible") continue;
          const clip = up.getBoundingClientRect();
          top = Math.max(top, clip.top);
          bottom = Math.min(bottom, clip.bottom);
          left = Math.max(left, clip.left);
          right = Math.min(right, clip.right);
        }
        if (bottom <= top || right <= left) return 0;
        return ((bottom - top) * (right - left)) / (box.height * box.width);
      };
      const visible = (element: Element | null) => shown(element) >= 0.5;

      const cards = [...document.querySelectorAll<HTMLElement>("article[data-tile]")];
      const tiles = cards.flatMap((card) => {
        const part = shown(card);
        if (part === 0) return [];
        return [
          {
            id: card.dataset.tile ?? "",
            type: card.dataset.tileType ?? "",
            size: card.dataset.tileSize ?? "",
            visible: (part > 0.99 ? "full" : "partial") as "full" | "partial",
          },
        ];
      });
      // Rows of the grid, by the cards' tops; a row whose cards leave part
      // of the grid's width empty while another row follows wastes it,
      // counted in sixths of the width (the wide grid's units).
      const grid = cards[0]?.parentElement?.getBoundingClientRect();
      const gap = cards[0]?.parentElement
        ? parseFloat(getComputedStyle(cards[0].parentElement).columnGap) || 0
        : 0;
      const wasted = (list: HTMLElement[]) => {
        if (!grid) return 0;
        const rows = new Map<number, number>();
        for (const card of list) {
          const box = card.getBoundingClientRect();
          const top = Math.round(box.top);
          rows.set(top, (rows.get(top) ?? -gap) + box.width + gap);
        }
        const tops = [...rows.keys()].sort((a, b) => a - b);
        return tops
          .slice(0, -1)
          .reduce(
            (sum, top) =>
              sum +
              Math.max(Math.round((6 * (grid.width - rows.get(top)!)) / (grid.width + gap)), 0),
            0,
          );
      };
      const firstScreenCards = cards.filter((card) => shown(card) > 0);
      // A row of the first screen counts while a further row exists below,
      // seen or not.
      const firstRows = new Set(
        firstScreenCards.map((c) => Math.round(c.getBoundingClientRect().top)),
      );
      const nextRow = cards.find(
        (c) => Math.round(c.getBoundingClientRect().top) > Math.max(...firstRows),
      );
      const wastedUnits = wasted(nextRow ? [...firstScreenCards, nextRow] : firstScreenCards);

      const tileOfChange = (change: string) =>
        doc.tiles.find((t) => t.id === change) ??
        doc.tiles.find(
          (t) =>
            t.type === "everything_else" &&
            typeof t.data === "object" &&
            t.data !== null &&
            (t.data as { change?: string }).change === change,
        );
      const cardVisible = (tile: string) => {
        const card = document.querySelector(`article[data-tile="${CSS.escape(tile)}"]`);
        return visible(card?.querySelector("header") ?? null);
      };

      const severe = doc.risks.filter(
        (r) =>
          current.has(r.change) &&
          r.state !== "mitigated" &&
          (r.tier === "critical" || r.tier === "high"),
      );
      const severeRisks = severe.map((r) => ({
        id: r.id,
        visible: [...document.querySelectorAll(`[data-risk="${CSS.escape(r.id)}"]`)].some(visible),
      }));
      // The changes the recommendation cites: a change itself, or the
      // change of a risk or evidence item it cites.
      const owner = new Map<string, string>();
      for (const c of doc.changes) owner.set(c.id, c.id);
      for (const r of doc.risks) owner.set(r.id, r.change);
      for (const e of doc.evidence) owner.set(e.id, e.change);
      const named = [
        ...new Set((doc.recommendation?.refs ?? []).flatMap((ref) => owner.get(ref) ?? [])),
      ];
      const namedChanges = named.map((change) => {
        const tile = tileOfChange(change);
        return { id: change, visible: tile ? cardVisible(tile.id) : false };
      });
      const levels = doc.changes
        .filter((c) => current.has(c.id) && c.kind !== "mechanical")
        .map((c) => ({ id: c.id, level: c.assessment?.level ?? 0 }));
      const highest = Math.max(0, ...levels.map((c) => c.level));
      const riskiestChanges =
        highest >= 3
          ? levels
              .filter((c) => c.level === highest)
              .map((c) => {
                const tile = tileOfChange(c.id);
                return { id: c.id, visible: tile ? cardVisible(tile.id) : false };
              })
          : [];
      const risksTile = doc.tiles.find((t) => t.type === "risks");
      const ranked = ((risksTile?.data as { ranked?: string[] } | undefined)?.ranked ?? []).flatMap(
        (id) => doc.risks.filter((r) => r.id === id),
      );
      const mediumRisks = ranked
        .filter((r) => r.tier === "medium" && r.state !== "mitigated" && current.has(r.change))
        .slice(0, 3)
        .map((r) => ({
          id: r.id,
          visible: [...document.querySelectorAll(`[data-risk="${CSS.escape(r.id)}"]`)].some(
            visible,
          ),
        }));
      const overview = doc.tiles.find((t) => t.type === "overview");
      const breakingGiven = Boolean(
        (overview?.data as { breaking_change?: string } | undefined)?.breaking_change,
      );
      const gaps = doc.evidence.filter(
        (e) => (e.status === "missing" || e.status === "failed") && current.has(e.change),
      );
      const recommendation = {
        given: Boolean(doc.recommendation),
        visible: visible(document.querySelector('[aria-label="Recommendation"]')),
      };
      const breaking = {
        given: breakingGiven,
        visible: breakingGiven && visible(document.querySelector("[data-breaking]")),
      };
      const missingEvidence = {
        count: gaps.length,
        visible: gaps.length > 0 && visible(document.querySelector("[data-evidence-gaps]")),
      };
      const signals = [
        ...(recommendation.given ? [recommendation.visible] : []),
        ...severeRisks.map((s) => s.visible),
        ...namedChanges.map((s) => s.visible),
        ...riskiestChanges.map((s) => s.visible),
        ...mediumRisks.map((s) => s.visible),
        ...(breaking.given ? [breaking.visible] : []),
        ...(missingEvidence.count > 0 ? [missingEvidence.visible] : []),
      ];
      return {
        width: innerWidth,
        height: innerHeight,
        pageHeight: Math.round(innerHeight + extra),
        tiles,
        landingTiles: cards.length,
        belowTiles: cards.length - firstScreenCards.length,
        wastedUnits,
        wastedUnitsLanding: wasted(cards),
        recommendation,
        severeRisks,
        namedChanges,
        riskiestChanges,
        mediumRisks,
        breaking,
        missingEvidence,
        recall: signals.length ? signals.filter(Boolean).length / signals.length : null,
      };
    },
    { pick, extra },
  );
}
