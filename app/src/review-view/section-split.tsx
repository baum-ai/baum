import {
  type CSSProperties,
  type ReactNode,
  type RefObject,
  startTransition,
  useEffect,
  useRef,
  useState,
} from "react";
import { cn } from "@baum/viewer/components/classnames";
import { ReviewFilesProvider } from "@baum/viewer/components/code-file-link";
import { ChevronLeftIcon, ChevronRightIcon } from "@baum/viewer/components/icons";
import { flashJump } from "@baum/viewer/components/jump-focus";
import { PaneDivider } from "@baum/viewer/components/pane-divider";
import { useReviewFiles } from "@baum/viewer/components/review-contexts";
import { tabPanelProps } from "@baum/viewer/components/tabs";
import { IconButton } from "@baum/viewer/components/ui";
import { type PaneLimits, usePaneWidth } from "@baum/viewer/use-conversation-width";
import { useStoredSetting } from "@baum/viewer/use-stored-setting";
import { SECTION_VIEWS, type SectionView, type SectionViews, viewOfPath } from "./model";
import { SectionViewTabs } from "./section-views";
import { SideRevealContext } from "./side-reveal";

type SideView = Exclude<SectionView, "overview">;

/** A file an excerpt in the Overview asked the side column to show, at a
 *  line when it names one. `at` tells a second press of the same excerpt
 *  from the first, so it scrolls there again. */
export type Reveal = { path: string; line?: number; at: number };

/** The side column's width: about as wide as a diff reads well to start,
 *  remembered once the reader drags it, and always leaving the Overview
 *  enough to read. */
const SIDE_LIMITS: PaneLimits = {
  min: 360,
  max: 1600,
  fallback: 640,
  keep: 360,
  storageKey: "baum.review.section-side-width",
};

const COLLAPSED_KEY = "baum.review.section-side-collapsed";

/** A wide section: its Overview on the left and its other views in a column
 *  beside it that stays in sight while the Overview scrolls. The column can
 *  be dragged wider or narrower, or collapsed to a strip that gives the
 *  Overview the section (collapsed at first, then as the reader leaves it). An excerpt of code in the Overview
 *  opens in the column, at its lines, when the section draws that file,
 *  expanding it if it was collapsed; any other file opens where it did. */
export function SectionSplit({
  overview,
  views,
  side,
  tablist,
  select,
  body,
}: {
  overview: ReactNode;
  views: SectionViews;
  side: SideView;
  tablist: string;
  select: (view: SectionView) => void;
  body: (view: SideView, reveal: Reveal | null) => ReactNode;
}) {
  const grid = useRef<HTMLDivElement>(null);
  const left = useRef<HTMLDivElement>(null);
  const column = useRef<HTMLDivElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const pane = usePaneWidth(SIDE_LIMITS);
  const [stored, store] = useStoredSetting(COLLAPSED_KEY);
  // Collapsed until the reader first opens it: the Overview is what a
  // section is read by, and a big diff then costs nothing until asked for.
  const collapsed = stored !== "false";
  const collapse = (next: boolean) => store(String(next));
  const [reveal, setReveal] = useState<Reveal | null>(null);
  // The column's code comes a frame after the section, as work React can
  // set aside: a big diff then never holds the Overview back, and moving on
  // to another section before it is drawn drops it.
  const [drawn, setDrawn] = useState(false);
  useEffect(() => {
    const frame = requestAnimationFrame(() => startTransition(() => setDrawn(true)));
    return () => cancelAnimationFrame(frame);
  }, []);
  const show = (path: string, line?: number) => {
    const view = viewOfPath(views, path);
    if (!view) return false;
    setReveal({ path, line, at: performance.now() });
    if (view !== side) select(view);
    return true;
  };
  // A row opened in the Overview shows its first excerpt here, while the
  // column is open: collapsed, it stays as the reader left it.
  const peek = (path: string, line?: number) => !collapsed && show(path, line);
  const parent = useReviewFiles();
  const files = parent && {
    has: parent.has,
    open: (path: string, line?: number) => {
      if (!viewOfPath(views, path)) return parent.open(path, line);
      if (collapsed) collapse(false);
      return show(path, line);
    },
  };
  useRevealIn(scroller, collapsed ? null : reveal);
  useBracketKey(() => {
    setReveal(null);
    collapse(!collapsed);
  });
  return (
    <div
      ref={grid}
      data-section-split=""
      data-collapsed={collapsed ? "" : undefined}
      style={{ "--side-width": `${pane.width}px` } as CSSProperties}
      className={cn(
        "grid items-start",
        collapsed
          ? "grid-cols-[minmax(0,1fr)_2rem] gap-4"
          : "grid-cols-[minmax(0,1fr)_2rem_minmax(0,var(--side-width))]",
      )}
    >
      <div ref={left} className="min-w-0">
        <ReviewFilesProvider files={files}>
          <SideRevealContext.Provider value={peek}>{overview}</SideRevealContext.Provider>
        </ReviewFilesProvider>
      </div>
      {collapsed ? (
        // The strip the column folds to, at the top of the page as it scrolls.
        <div className="sticky top-12 flex justify-center pt-1">
          <IconButton
            label="Expand section views"
            title="Expand section views (])"
            aria-keyshortcuts="]"
            aria-expanded={false}
            onClick={() => collapse(false)}
          >
            <ChevronLeftIcon size={14} />
          </IconButton>
        </div>
      ) : (
        <>
          <PaneDivider
            width={pane.width}
            onWidth={pane.choose}
            onPreview={(width) => grid.current?.style.setProperty("--side-width", `${width}px`)}
            onFit={pane.fit}
            reviewRef={left}
            paneRef={column}
            limits={SIDE_LIMITS}
            label="Resize section views"
            className="self-stretch"
          />
          <div
            ref={column}
            data-section-side=""
            className="sticky top-12 flex max-h-[calc(var(--review-visible,100dvh)-1rem)] min-w-0 flex-col"
          >
            {/* The views' row stays at the top of the column, above what
                scrolls, so the scrollbar never runs over its chevron. */}
            <div
              data-section-side-tabs=""
              className="flex flex-none items-center justify-between gap-2 border-b border-subtle pt-1 pb-2"
            >
              <SectionViewTabs
                tablist={tablist}
                value={side}
                counts={views.counts}
                onChange={select}
                overview={false}
              />
              <IconButton
                label="Collapse section views"
                title="Collapse section views (])"
                aria-keyshortcuts="]"
                aria-expanded
                onClick={() => {
                  setReveal(null);
                  collapse(true);
                }}
              >
                <ChevronRightIcon size={14} />
              </IconButton>
            </div>
            <div
              ref={scroller}
              data-section-side-body=""
              className="min-h-0 flex-1 overflow-y-auto overscroll-contain pt-4"
            >
              {SECTION_VIEWS.map(({ value }) =>
                value === "overview" ? null : (
                  <div
                    key={value}
                    {...tabPanelProps(tablist, value, value === side)}
                    className="min-w-0"
                  >
                    {value === side && drawn ? body(value, reveal) : null}
                  </div>
                ),
              )}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

/** `]` by itself, anywhere but a text field or a dialog: the side column's
 *  fold. (The review's right-hand sidebar takes Control+] in the app.) */
function useBracketKey(toggle: () => void) {
  const latest = useRef(toggle);
  latest.current = toggle;
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (
        event.key !== "]" ||
        event.defaultPrevented ||
        event.ctrlKey ||
        event.metaKey ||
        event.altKey ||
        event.isComposing ||
        document.querySelector("dialog[open]")
      )
        return;
      if (
        event.target instanceof HTMLElement &&
        event.target.closest('input, textarea, select, [contenteditable], [role="textbox"]')
      )
        return;
      event.preventDefault();
      latest.current();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, []);
}

/** Scrolls the side column (only it: the Overview stays where the reader
 *  is) to the file an excerpt named, at the first line from the one it
 *  names, and marks it. The diff may still be loading, so it waits for it a
 *  few seconds. */
function useRevealIn(column: RefObject<HTMLElement | null>, reveal: Reveal | null) {
  useEffect(() => {
    const box = column.current;
    if (!reveal || !box) return;
    const find = () => {
      const article = Array.from(box.querySelectorAll<HTMLElement>("article[aria-label]")).find(
        (each) => each.getAttribute("aria-label") === `${reveal.path} diff`,
      );
      if (!article || reveal.line === undefined) return article;
      const rows = Array.from(article.querySelectorAll<HTMLElement>("tr[data-new-line]"));
      return rows.find((row) => Number(row.dataset.newLine) >= reveal.line!) ?? article;
    };
    const reduced = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? true;
    const go = () => {
      const target = find();
      if (!target) return false;
      const top =
        target.getBoundingClientRect().top -
        box.getBoundingClientRect().top +
        box.scrollTop -
        box.clientHeight / 4;
      box.scrollTo({ top: Math.max(0, top), behavior: reduced ? "auto" : "smooth" });
      flashJump(target, reduced);
      return true;
    };
    if (go()) return;
    const observer = new MutationObserver(() => {
      if (go()) observer.disconnect();
    });
    observer.observe(box, { childList: true, subtree: true });
    const timer = window.setTimeout(() => observer.disconnect(), 5000);
    return () => {
      observer.disconnect();
      window.clearTimeout(timer);
    };
  }, [column, reveal]);
}
