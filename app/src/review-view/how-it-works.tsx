import { memo, type RefObject, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { cn } from "@baum/viewer/components/classnames";
import { centerJump, flashJump } from "@baum/viewer/components/jump-focus";
import type { ReviewPlaceContext } from "@baum/viewer/components/review-viewer";
import { scrollerOf } from "@baum/viewer/components/scroller";
import { useUrlState } from "@baum/viewer/navigation";
import type { ReviewDocument } from "@baum/viewer/review-document";
import { withSelection } from "@baum/viewer/review-selection";
import { draftsOn } from "../tiles/drafts";
import type { TileContext } from "../tiles/types";
import {
  CONTEXT_ID,
  type ListedSection,
  type ReviewV2,
  type SectionView,
  selectionOf,
} from "./model";
import { landedWrite, onJump, type PartBox, partScrollTop, readingPart } from "./scroll-spy";
import { SectionBody, WhereThisFits } from "./sections-tab";

/** How long a jump to a part takes, whatever the distance. */
const GLIDE_MS = 420;

/** How it works as one scrolling page: "Where this fits", then every section
 *  in the rail's order, each drawn as its own page draws it. The rail is its
 *  outline: the part being read is the one the address names
 *  (`pane=section&section=<id>`), which follows the scroll (written in place
 *  of the current history entry, never as a new one), and a row, a key or a
 *  link that names another part scrolls to it. */
export function HowItWorks({
  document,
  review,
  list,
  section,
  item,
  sectionView,
  ctx,
  idPrefix,
  context,
  historical,
}: {
  document: ReviewDocument;
  review: ReviewV2;
  list: ListedSection[];
  section: string;
  item?: string;
  sectionView?: SectionView;
  ctx: TileContext;
  idPrefix: string;
  context: ReviewPlaceContext;
  historical: boolean;
}) {
  const root = useRef<HTMLDivElement>(null);
  const { update } = useUrlState();
  // Each section keeps the view the reader chose in it; the address carries
  // the view of the section being read.
  const [views, setViews] = useState<Record<string, SectionView>>(() =>
    sectionView ? { [section]: sectionView } : {},
  );
  useEffect(() => {
    if (sectionView)
      setViews((was) => (was[section] === sectionView ? was : { ...was, [section]: sectionView }));
  }, [section, sectionView]);
  const latest = useRef({ section, views });
  latest.current = { section, views };
  // The sections the spy wrote to the address that it has not shown yet.
  const written = useRef<string[]>([]);
  const address = useCallback(
    (id: string, own: boolean) => {
      if (own) written.current.push(id);
      const view = latest.current.views[id];
      update(
        (current) => withSelection(current, selectionOf({ tab: "sections", section: id, view })),
        { replace: true, preventScrollReset: true },
      );
    },
    [update],
  );
  const choose = useCallback(
    (id: string, view: SectionView) => {
      latest.current.views = { ...latest.current.views, [id]: view };
      setViews((was) => ({ ...was, [id]: view }));
      // The section being read keeps its view in the address, for a reload.
      if (id === latest.current.section) address(id, false);
    },
    [address],
  );
  const jumps = useJumps(root, list, idPrefix, (id) => {
    if (id !== latest.current.section) address(id, true);
  });
  const jumpsRef = useRef(jumps);
  jumpsRef.current = jumps;
  // A place the address names that the spy did not write (a rail row, a
  // key, Back, a link to a risk or a tile) is scrolled to.
  const last = useRef<{ section: string; item?: string } | null>(null);
  // The part the reader was last taken to draws its side column at once;
  // the others when the page rests near them (`useNearParts`).
  const [taken, setTaken] = useState(section);
  useEffect(() => {
    const before = last.current;
    last.current = { section, item };
    const landed = landedWrite(written.current, section);
    written.current = landed.pending;
    if (landed.own) return;
    // The view changed, or the item was dropped: the reader is where they were.
    if (before && before.section === section && !item) return;
    setTaken(section);
    jumpsRef.current.to(section, item, before === null);
  }, [section, item]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: what the sections read of the context, which the reader's place (its selection) is not part of, so moving between sections does not draw them all again
  const steady = useMemo(
    () => context,
    [
      context.select,
      context.openFile,
      context.hunkPatches,
      context.onRetryDetails,
      context.contextPending,
      context.document,
    ],
  );
  const near = useNearParts(root, list, section);
  const ids = useMemo(() => list.map((entry) => entry.id), [list]);
  return (
    <div ref={root} data-how-it-works="" className="min-w-0">
      {list.map((entry, at) => (
        <Part
          key={entry.id}
          entry={entry}
          first={at === 0}
          reading={entry.id === section}
          near={near.has(entry.id) || entry.id === taken}
          view={views[entry.id]}
          choose={choose}
          ids={ids}
          document={document}
          review={review}
          ctx={ctx}
          idPrefix={idPrefix}
          context={steady}
          historical={historical}
        />
      ))}
    </div>
  );
}

/** One part of the page, drawn as its own page draws it, set off from the
 *  one before by a rule. Only the part being read takes the keys of its
 *  views (h, l and ]). */
const Part = memo(function Part({
  entry,
  first,
  reading,
  near,
  view,
  choose,
  ids,
  document,
  review,
  ctx,
  idPrefix,
  context,
  historical,
}: {
  entry: ListedSection;
  first: boolean;
  reading: boolean;
  near: boolean;
  view?: SectionView;
  choose: (id: string, view: SectionView) => void;
  ids: string[];
  document: ReviewDocument;
  review: ReviewV2;
  ctx: TileContext;
  idPrefix: string;
  context: ReviewPlaceContext;
  historical: boolean;
}) {
  const shown = review.sections.find((each) => each.id === entry.id);
  // The tiles being made in the session panel stand on their sections, on
  // the latest version only.
  const drafts = useMemo(
    () => (historical ? [] : draftsOn(review.drafts ?? [], entry.id, ids)),
    [historical, review.drafts, entry.id, ids],
  );
  return (
    <article
      data-part={entry.id}
      data-reading={reading ? "" : undefined}
      aria-current={reading ? "location" : undefined}
      aria-label={entry.title}
      id={`${idPrefix}part-${entry.id}`}
      className={cn(
        "min-w-0 space-y-7",
        // A rule and room between sections, so where one ends reads at a
        // glance while scrolling.
        !first && "mt-14 border-t border-strong pt-10",
      )}
    >
      {entry.id === CONTEXT_ID ? (
        <WhereThisFits
          document={document}
          review={review}
          ctx={ctx}
          idPrefix={idPrefix}
          drafts={drafts}
          pending={context.contextPending && !historical}
          keys={reading}
          near={near}
        />
      ) : shown ? (
        <SectionBody
          document={document}
          review={review}
          section={shown}
          asked={view}
          onView={(next) => choose(entry.id, next)}
          keys={reading}
          near={near}
          ctx={ctx}
          idPrefix={idPrefix}
          drafts={drafts}
          context={context}
          onRetryDetails={historical ? undefined : context.onRetryDetails}
        />
      ) : (
        <p className="text-base text-secondary">
          This section isn't in review v{document.review.version}.
        </p>
      )}
    </article>
  );
});

/** The line under the sticky bars, in the window's coordinates: where a part
 *  lands when the reader is taken to it, and what the spy measures from. */
function readingLine(scroller: HTMLElement): number {
  const page = scroller === (window.document.scrollingElement ?? window.document.documentElement);
  const top = page ? 0 : scroller.getBoundingClientRect().top;
  const padding = Number.parseFloat(getComputedStyle(scroller).scrollPaddingTop) || 0;
  let line = top + padding;
  // Below the wide layout the navigator bar sticks over the pane instead.
  const bar = window.document.querySelector<HTMLElement>(
    'button[aria-controls="review-rail"]',
  )?.parentElement;
  if (bar?.offsetParent) line = Math.max(line, bar.getBoundingClientRect().bottom);
  return line;
}

/** Scrolls `scroller` to where `to` says, over a short fixed time (at once
 *  when motion is reduced). `to` is asked again each frame, so a part that
 *  moves while drawing in above it is still where the scroll lands. */
function glide(
  scroller: HTMLElement,
  to: () => number,
  reduced: boolean,
  done: (taken: boolean) => void,
) {
  const from = scroller.scrollTop;
  if (reduced || Math.abs(to() - from) < 2) {
    scroller.scrollTo?.({ top: to(), behavior: "instant" });
    done(false);
    return () => {};
  }
  const started = performance.now();
  let frame = 0;
  let last = from;
  const step = (now: number) => {
    // The reader scrolled meanwhile: they take over.
    if (Math.abs(scroller.scrollTop - last) > 2) return done(true);
    const progress = Math.min(1, (now - started) / GLIDE_MS);
    const eased = 1 - (1 - progress) ** 3;
    scroller.scrollTo?.({ top: from + (to() - from) * eased, behavior: "instant" });
    last = scroller.scrollTop;
    if (progress < 1) frame = requestAnimationFrame(step);
    else done(false);
  };
  frame = requestAnimationFrame(step);
  return () => cancelAnimationFrame(frame);
}

const reducedMotion = () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? true;

/** Names the part the item keys (j and k) go on from: the one the reader was
 *  just taken to. The viewer reads `data-review-keys-from` on the first key
 *  and starts in that part, so the block marked in another part is unmarked
 *  here. With no part, the keys go on from their mark or from what is in
 *  sight, as on any place. */
export function keysFrom(root: Element | null, part: Element | null) {
  if (!root) return;
  for (const named of root.querySelectorAll("[data-review-keys-from]"))
    named.removeAttribute("data-review-keys-from");
  if (!part) return;
  part.setAttribute("data-review-keys-from", "");
  for (const marked of root.querySelectorAll("[data-review-block-active]"))
    marked.removeAttribute("data-review-block-active");
}

/** Taking the reader to a part, and following where they scroll to.
 *
 *  The spy keeps an anchor: what stands at the reading line, or the part
 *  (or item) the reader was taken to, with where it stands in the window.
 *  When the page reflows under it (a diagram or a column's code drawn above,
 *  every section's side column opened or folded at once, the window
 *  resized), the anchor moves within the page while the scroll stays: the
 *  page is scrolled to put it back where it stood. When the page scrolls and
 *  the anchor stays where it was within the page, that is the reader moving,
 *  by whatever means (the wheel over a tile's frame, a key, the scrollbar).
 *
 *  A part the reader was taken to is pinned: it is the part being read, and
 *  stays where it landed, until they scroll themselves. Without a pin, the
 *  spy reads which part is at the top (`readingPart`) and reports it to
 *  `read`. */
function useJumps(
  root: RefObject<HTMLDivElement | null>,
  list: ListedSection[],
  idPrefix: string,
  read: (id: string) => void,
) {
  const reading = useRef(read);
  reading.current = read;
  const state = useRef({
    pin: null as string | null,
    /** What the pin keeps in place: the part, or the item it was opened at. */
    pinned: null as Element | null,
    /** The page is moving to a part (a glide, an item's jump): not the reader. */
    moving: false,
    cancel: null as (() => void) | null,
    frame: 0,
    /** A jump the rail asked for, which the address then names. */
    asked: null as { id: string; at: number } | null,
    /** What the spy keeps in place: where it stood in the window, where in
     *  the page, and how far the page was scrolled, when it last looked. */
    anchor: null as { element: Element; top: number; at: number; scrolled: number } | null,
    /** The page reflowed and cannot yet scroll back to the anchor (what makes
     *  it long enough is still being drawn): held until it can. */
    holding: false,
    measure: () => {},
  });
  const part = useCallback(
    (id: string) =>
      root.current?.querySelector<HTMLElement>(`:scope > [data-part="${CSS.escape(id)}"]`) ?? null,
    [root],
  );
  const to = useCallback(
    (id: string, item: string | undefined, arriving: boolean) => {
      const own = state.current;
      // The rail already started this jump.
      if (!item && own.asked?.id === id && performance.now() - own.asked.at < 500) {
        own.asked = null;
        return;
      }
      own.cancel?.();
      own.cancel = null;
      cancelAnimationFrame(own.frame);
      own.pin = id;
      own.pinned = part(id);
      // The item keys go on from the part the reader was taken to, not from
      // the block they marked in another part.
      keysFrom(root.current, item ? null : own.pinned);
      own.anchor = null;
      own.holding = false;
      own.moving = false;
      // Arriving on How it works at its first part, the page starts at its
      // top as any place does, with the review's head above.
      if (arriving && !item && list[0]?.id === id) return;
      own.moving = true;
      const landed = (taken = false) => {
        own.moving = false;
        own.cancel = null;
        if (taken) {
          own.pin = null;
          own.pinned = null;
          keysFrom(root.current, null);
        }
        own.measure();
      };
      // A frame on, so the part is drawn and the viewer has placed the pane.
      own.frame = requestAnimationFrame(() => {
        const element = part(id);
        if (!element) return landed();
        own.pinned = element;
        const reduced = arriving || reducedMotion();
        const target = item
          ? [`${idPrefix}${id}-${item}`, `${idPrefix}${item}`]
              .map((each) => window.document.getElementById(each))
              .find((found) => found && element.contains(found))
          : null;
        if (target) {
          for (
            let fold = target.closest("details");
            fold;
            fold = fold.parentElement?.closest("details") ?? null
          )
            fold.open = true;
          own.pinned = target;
          const stop = centerJump(target, reduced);
          flashJump(target, reduced);
          if (!target.hasAttribute("tabindex")) target.tabIndex = -1;
          target.focus({ preventScroll: true });
          // centerJump takes a fixed 400 ms.
          const timer = window.setTimeout(landed, reduced ? 0 : 450);
          own.cancel = () => {
            stop();
            window.clearTimeout(timer);
          };
          return;
        }
        const scroller = scrollerOf(element);
        own.cancel = glide(
          scroller,
          () =>
            partScrollTop(
              scroller.scrollTop,
              element.getBoundingClientRect().top,
              readingLine(scroller),
              scroller.scrollHeight - scroller.clientHeight,
            ),
          reduced,
          landed,
        );
      });
    },
    [list, idPrefix, part],
  );
  useEffect(
    () =>
      onJump((id) => {
        if (!list.some((entry) => entry.id === id)) return;
        to(id, undefined, false);
        state.current.asked = { id, at: performance.now() };
      }),
    [list, to],
  );
  useEffect(() => {
    const element = root.current;
    if (!element) return;
    const own = state.current;
    const scroller = scrollerOf(element);
    const page = scroller === (window.document.scrollingElement ?? window.document.documentElement);
    let frame = 0;
    const measure = () => {
      cancelAnimationFrame(frame);
      frame = 0;
      const line = readingLine(scroller);
      const anchor = own.anchor;
      if (!own.moving && anchor?.element.isConnected) {
        const top = anchor.element.getBoundingClientRect().top;
        const reflowed = Math.abs(top + scroller.scrollTop - anchor.at) > 1;
        if (!reflowed && Math.abs(scroller.scrollTop - anchor.scrolled) > 1) {
          // The reader scrolled: a pin, and a place held, let go, and the
          // item keys go on from what is in sight again.
          own.pin = null;
          own.pinned = null;
          own.holding = false;
          keysFrom(element, null);
        } else if (reflowed || own.holding) {
          const want = scroller.scrollTop + top - anchor.top;
          if (Math.abs(want - scroller.scrollTop) > 1)
            scroller.scrollTo?.({ top: want, behavior: "instant" });
          const now = anchor.element.getBoundingClientRect().top;
          own.holding = Math.abs(now - anchor.top) > 1;
          anchor.at = now + scroller.scrollTop;
          anchor.scrolled = scroller.scrollTop;
        }
      }
      const bottom = page ? window.innerHeight : scroller.getBoundingClientRect().bottom;
      const parts: PartBox[] = [
        ...element.querySelectorAll<HTMLElement>(":scope > [data-part]"),
      ].map((each) => {
        const box = each.getBoundingClientRect();
        return { id: each.dataset.part!, top: box.top - line, bottom: box.bottom - line };
      });
      const atEnd = scroller.scrollTop + scroller.clientHeight >= scroller.scrollHeight - 2;
      // While the page grows back to what the reader was looking at, its
      // part is still the one being read.
      const held = own.holding
        ? (anchor?.element.closest<HTMLElement>("[data-part]")?.dataset.part ?? null)
        : null;
      const id = readingPart(parts, { height: bottom - line, atEnd }, own.pin ?? held);
      if (id) reading.current(id);
      if (own.moving || own.holding) return;
      // What to keep in place from here: the pinned part or item, else what
      // stands at the reading line.
      let next: Element | null = own.pin && own.pinned?.isConnected ? own.pinned : null;
      if (!next) {
        const hit = window.document.elementFromPoint?.(
          element.getBoundingClientRect().left + 16,
          line + 8,
        );
        next = hit && hit !== element && element.contains(hit) ? hit : id ? part(id) : null;
      }
      const top = next?.getBoundingClientRect().top ?? 0;
      own.anchor = next
        ? { element: next, top, at: top + scroller.scrollTop, scrolled: scroller.scrollTop }
        : null;
    };
    own.measure = measure;
    const onScroll = () => {
      if (!frame) frame = requestAnimationFrame(measure);
    };
    // Measured as soon as the page is laid out again, before it is painted,
    // so a reflow never shows the reader the wrong place.
    const resized = new ResizeObserver(measure);
    resized.observe(element);
    const events: EventTarget = page ? window : scroller;
    events.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      cancelAnimationFrame(frame);
      resized.disconnect();
      events.removeEventListener("scroll", onScroll);
      own.measure = () => {};
    };
  }, [root, part]);
  useEffect(
    () => () => {
      state.current.cancel?.();
      cancelAnimationFrame(state.current.frame);
    },
    [],
  );
  return useMemo(() => ({ to }), [to]);
}

/** How long the page must rest before the side columns near the reader are
 *  drawn, or those left far behind let go of their code. */
const SETTLE_MS = 180;

/** The parts within a screen of what the reader sees, whose side columns
 *  (a section's code, the file beside "Where this fits") are drawn. They are
 *  drawn once the page rests, not while it scrolls, so drawing a big diff
 *  never stutters the scroll, and let go of once far again, so the page
 *  never holds every section's code at once. */
function useNearParts(
  root: RefObject<HTMLDivElement | null>,
  list: ListedSection[],
  section: string,
) {
  const [near, setNear] = useState<ReadonlySet<string>>(() => new Set([section]));
  useEffect(() => {
    const element = root.current;
    if (!element) return;
    if (typeof IntersectionObserver !== "function") {
      setNear(new Set(list.map((entry) => entry.id)));
      return;
    }
    const scroller = scrollerOf(element);
    const page = scroller === (window.document.scrollingElement ?? window.document.documentElement);
    const inside = new Set<string>();
    let timer = 0;
    const settle = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(() => {
        timer = 0;
        setNear((was) =>
          was.size === inside.size && [...inside].every((id) => was.has(id))
            ? was
            : new Set(inside),
        );
      }, SETTLE_MS);
    };
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          const id = (entry.target as HTMLElement).dataset.part!;
          if (entry.isIntersecting) inside.add(id);
          else inside.delete(id);
        }
        settle();
      },
      { root: page ? null : scroller, rootMargin: "100% 0px" },
    );
    for (const part of element.querySelectorAll(":scope > [data-part]")) observer.observe(part);
    // While the page moves, what is near waits for it to rest.
    const events: EventTarget = page ? window : scroller;
    const onScroll = () => {
      if (timer) settle();
    };
    events.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      observer.disconnect();
      events.removeEventListener("scroll", onScroll);
      window.clearTimeout(timer);
    };
  }, [root, list]);
  return near;
}
