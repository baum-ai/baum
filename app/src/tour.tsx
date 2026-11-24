import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { Stumpy } from "@baum/viewer/components/stumpy";
import { cn } from "@baum/viewer/components/classnames";
import { CloseButton } from "@baum/viewer/components/ui";
import { appURL, inSample, navigate, ownBase } from "./api";
import { PRIMARY, SECONDARY, SMALL } from "./action-classes";
import { PAGES } from "./pages";
import { SAMPLE_PULL_REQUEST } from "./sample";
import { COMMAND } from "./shortcuts";
import { uiState, useUIState } from "./ui-state";
import { visibleInterval } from "./visible-interval";
import "./tour.css";

/** Whether the tour is open: once on this computer's first launch, kept by
 *  the daemon since the app's address can change, and again whenever Go to
 *  asks for it (`?tour=1`). Closing it clears that request, so asking again
 *  opens it.
 *
 *  The first tour always runs in the sample project; a later one runs on the
 *  person's own pull requests when they have any, and else in the sample too.
 *  Closing it leaves the sample again. */
export function useTour(asked: boolean, pullRequests: boolean | undefined) {
  const saved = useUIState(uiState("api/ui-state/tour", () => ({ seen: false })));
  const firstRun = saved.ready && !saved.error && !saved.value.seen && !closedHere();
  const [open, setOpen] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const askedFirst = useRef(asked);
  useEffect(() => {
    // The sample is only for the tour: a reload of it without one goes home.
    if (inSample() && !askedFirst.current) location.replace(ownBase());
  }, []);
  useEffect(() => {
    if (!firstRun && !asked) return;
    if (inSample()) return setOpen(true);
    // Until the saved state says whether this is the first tour, wait.
    if (!saved.ready && !saved.error) return;
    if (firstRun || pullRequests === false) location.replace(`${ownBase()}sample/?tour=1`);
    else if (pullRequests) setOpen(true);
  }, [firstRun, asked, pullRequests, saved.ready, saved.error]);
  // Leaving the sample reloads the page, so the tour's state is saved first;
  // a save that fails still leaves, and the tour shows again on a later
  // launch, but not in this window, which remembers the close.
  useEffect(() => {
    if (leaving && ((saved.value.seen && !saved.saving) || saved.error)) location.assign(ownBase());
  }, [leaving, saved.value.seen, saved.saving, saved.error]);
  const close = () => {
    if (!saved.value.seen) saved.update(() => ({ seen: true }));
    try {
      sessionStorage.setItem(CLOSED, "1");
    } catch {}
    setOpen(false);
    if (inSample()) setLeaving(true);
    else if (asked) navigate(appURL());
  };
  return { open, close };
}

/** That the tour was closed in this window, so a save of that which failed
 *  does not bring it straight back. */
const CLOSED = "baum-tour-closed";
function closedHere() {
  try {
    return sessionStorage.getItem(CLOSED) === "1";
  } catch {
    return false;
  }
}

/** A button named in the copy, drawn as the button it names but inert: the
 *  pointer neither hovers nor presses it. */
const B = ({ children }: { children: ReactNode }) => (
  <span className={cn(SMALL, "pointer-events-none")}>{children}</span>
);
const href = (page: string) => PAGES.find((item) => item.page === page)!.href();

type Stop = {
  title: string;
  body: ReactNode;
  /** What the stop points at, the first match on screen; without one, or
   *  until it shows, the card is centred. */
  target?: string;
  /** Where the stop is: a page's address, or "review" for the pull request
   *  the Inbox stops opened. */
  at?: string;
  /** Next clicks this, as the reader would, rather than going to an address. */
  follow?: string;
  /** Clicked once when it shows, to pick what the stop is about. */
  select?: string;
  /** The opening card: larger, centred, with Stumpy. */
  welcome?: boolean;
};

const STOPS: Stop[] = [
  {
    title: "You're all set!",
    body: (
      <>
        Welcome to baum. Press <kbd>?</kbd> anytime to see how to navigate the app. Let's start with
        a little tour{inSample() ? ", in a sample project." : "."}
      </>
    ),
    at: href("inbox"),
    welcome: true,
  },
  {
    title: "Your Inbox",
    body: (
      <>
        {inSample() ? "This sample project's pull requests" : "Pull requests"} waiting on you,
        grouped by what to do: review, merge or hand to an agent.
      </>
    ),
    target: ".upnext-list",
    at: href("inbox"),
    follow: ".upnext-panel .upnext-primary",
    // In the sample, the person's own pull request, written with their agent.
    select: inSample() ? `[data-row="${SAMPLE_PULL_REQUEST}"] .upnext-row` : undefined,
  },
  {
    title: "A pull request's review",
    body: (
      <>
        What the change does and its riskiest part come first, then each change, riskiest first,
        with its lines. <B>Ask</B> an agent about it in the session panel, or <B>Merge…</B> when
        it's ready.
      </>
    ),
    // The whole page but the app's sidebar, the review's Details too.
    target: ".local-main",
    at: "review",
  },
  {
    title: "Timeline",
    body: "Each feature you're building, read from your agent sessions: its status, the people on it and its pull requests. All decisions shows the choices behind the code.",
    target: ".local-main",
    at: href("timeline"),
  },
  {
    title: "Sessions",
    body: (
      <>
        Every agent session on this computer, running, waiting or ended. <B>Focus session</B> jumps
        back into a running one. Take this tour again from Go to (<kbd>{COMMAND}K</kbd>).
      </>
    ),
    target: ".local-main",
    at: href("sessions"),
  },
];

const GAP = 12;
type ReviewSpace = { height: number; bottom?: number };

function spaceAroundActions(actions: DOMRect | undefined): ReviewSpace | undefined {
  if (!actions || actions.bottom <= 0 || actions.top >= innerHeight) return;
  const below = innerHeight - actions.bottom - 3 * GAP;
  const above = actions.top - 2 * GAP;
  // Prefer the lower corner when it can fit the heading, copy and navigation.
  if (below >= 144) return { height: below };
  if (above >= 144) return { height: above, bottom: actions.top - GAP };
  // If the bar is near the middle of a zoomed-in window, use the larger side.
  if (below >= 100 && below >= above) return { height: below };
  if (above >= 100) return { height: above, bottom: actions.top - GAP };
}

/** Beside the target if it fits, else below or above it, else in the
 *  window's corner over a target that fills it. */
function place(target: DOMRect | null, width: number, height: number, bottom?: number) {
  const vw = innerWidth;
  const vh = innerHeight;
  if (!target) return { left: (vw - width) / 2, top: (vh - height) / 2 };
  const top = Math.max(GAP, Math.min(vh - height - GAP, target.top));
  const left = Math.max(GAP, Math.min(vw - width - GAP, target.left));
  if (target.right + GAP + width < vw) return { left: target.right + GAP, top };
  if (target.left - GAP - width > 0) return { left: target.left - GAP - width, top };
  if (target.bottom + GAP + height < vh) return { left, top: target.bottom + GAP };
  if (target.top - GAP - height > 0) return { left, top: target.top - GAP - height };
  return {
    left: Math.max(GAP, vw - width - 2 * GAP),
    top: Math.max(GAP, Math.min(bottom ?? vh, vh - 2 * GAP) - height),
  };
}

/** The app's first-run tour: a welcome, then a small card that steps
 *  through the Inbox, a pull request's review, the Timeline and Sessions,
 *  pointing at each. It goes to each page itself. Finishing or skipping it is remembered
 *  on this computer. */
export function Tour({ close }: { close: () => void }) {
  const [index, setIndex] = useState(0);
  const [rect, setRect] = useState<DOMRect | null>(null);
  const [reviewSpace, setReviewSpace] = useState<ReviewSpace>();
  const [copyScrollable, setCopyScrollable] = useState(false);
  const card = useRef<HTMLDivElement>(null);
  const copy = useRef<HTMLParagraphElement>(null);
  const next = useRef<HTMLButtonElement>(null);
  const review = useRef<string | null>(null);
  const stop = STOPS[index]!;
  const last = index === STOPS.length - 1;

  const go = (to: number) => {
    const target = STOPS[to]!;
    const where = target.at === "review" ? review.current : target.at;
    if (to > index && stop.follow) {
      document.querySelector<HTMLElement>(stop.follow)?.click();
    } else if (where && `${location.pathname}${location.search}` !== where) {
      navigate(where);
    }
    setRect(null);
    setReviewSpace(undefined);
    setIndex(to);
  };

  // Follow the target as the page loads, scrolls and resizes.
  useEffect(() => {
    let selected = false;
    const measure = () => {
      const pick =
        !selected && stop.select ? document.querySelector<HTMLElement>(stop.select) : null;
      if (pick) {
        pick.click();
        selected = true;
        next.current?.focus({ preventScroll: true });
      }
      if (stop.at === "review" && new URLSearchParams(location.search).has("pr")) {
        review.current = `${location.pathname}${location.search}`;
        const actions = document
          .querySelector('[data-tour="review-actions"]')
          ?.getBoundingClientRect();
        const space = spaceAroundActions(actions);
        setReviewSpace((old) =>
          old?.height === space?.height && old?.bottom === space?.bottom ? old : space,
        );
      }
      // Every match on screen, together: a narrow window hides some targets.
      const shown = [...(stop.target ? document.querySelectorAll(stop.target) : [])]
        .map((element) => element.getBoundingClientRect())
        .filter((found) => found.width && found.height);
      const box = shown.length
        ? new DOMRect(
            Math.min(...shown.map((r) => r.left)),
            Math.min(...shown.map((r) => r.top)),
            Math.max(...shown.map((r) => r.right)) - Math.min(...shown.map((r) => r.left)),
            Math.max(...shown.map((r) => r.bottom)) - Math.min(...shown.map((r) => r.top)),
          )
        : null;
      setRect((old) =>
        old &&
        box &&
        ["top", "left", "width", "height"].every((k) => old[k as "top"] === box[k as "top"])
          ? old
          : box,
      );
    };
    measure();
    return visibleInterval(measure, 150);
  }, [stop]);

  const [position, setPosition] = useState({ left: 0, top: 0 });
  useLayoutEffect(() => {
    const element = card.current;
    const paragraph = copy.current;
    if (!element || !paragraph) return;
    const update = () => {
      const placed = place(rect, element.offsetWidth, element.offsetHeight, reviewSpace?.bottom);
      setPosition((old) => (old.left === placed.left && old.top === placed.top ? old : placed));
      setCopyScrollable(paragraph.scrollHeight > paragraph.clientHeight);
    };
    update();
    // The welcome has no target, and fonts and viewport changes can resize the card.
    const observer = new ResizeObserver(update);
    observer.observe(element);
    observer.observe(paragraph);
    addEventListener("resize", update);
    return () => {
      observer.disconnect();
      removeEventListener("resize", update);
    };
  }, [rect, index, reviewSpace]);
  useEffect(() => next.current?.focus({ preventScroll: true }), [index]);

  // ← and → step between the cards, Enter presses Next or Done and Escape
  // closes the tour, wherever the focus is, ahead of the page's own keys. A
  // text field, the card's other buttons and dialogs keep theirs. They are
  // the tour's alone, so the key list leaves them out.
  const step = useRef<(key: string) => void>(() => {});
  step.current = (key) => {
    if (key === "Escape") close();
    else if (key === "ArrowLeft") {
      if (index > 0) go(index - 1);
    } else if (!last) go(index + 1);
    // Enter on the last card is Done; → stays there.
    else if (key === "Enter") close();
  };
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!["ArrowLeft", "ArrowRight", "Enter", "Escape"].includes(event.key)) return;
      if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey || event.isComposing)
        return;
      const target = event.target instanceof Element ? event.target : null;
      if (target?.closest("input, textarea, select, [contenteditable]")) return;
      // A dialog over the tour, as Merge… or the key list, keeps its keys.
      const dialog = target?.closest("dialog, [role=dialog]");
      if (dialog && dialog !== card.current) return;
      const button = target?.closest("button, a[href]");
      if (event.key === "Enter" && button && button !== next.current) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      step.current(event.key);
    };
    addEventListener("keydown", onKeyDown, true);
    return () => removeEventListener("keydown", onKeyDown, true);
  }, []);

  // The target may be taller than the window; the spotlight keeps to it.
  const spot = rect && {
    left: rect.left - 4,
    top: Math.max(rect.top - 4, 0),
    width: rect.width + 8,
    height: Math.min(rect.bottom + 4, innerHeight) - Math.max(rect.top - 4, 0),
  };
  return (
    <div className="tour">
      <div className={spot ? "tour-spot" : "tour-scrim"} style={spot ?? undefined} />
      <div
        ref={card}
        role="dialog"
        aria-labelledby="tour-title"
        className={cn(
          "tour-card",
          stop.welcome && "tour-welcome",
          stop.at === "review" && "tour-review",
        )}
        style={{ ...position, maxHeight: reviewSpace?.height }}
        // Enter and Space press the tour's buttons; every other key reaches
        // the page, so the keys it teaches work.
        onKeyDown={(event) => {
          if (["Enter", " "].includes(event.key)) event.stopPropagation();
        }}
      >
        <CloseButton label="Close the tour" onClick={close} className="tour-close" />
        {stop.welcome ? (
          <div className="tour-band" aria-hidden="true">
            {LEAVES.map((leaf) => (
              <svg key={leaf.left} className="tour-leaf" viewBox="2 -31 21 19" style={leaf}>
                <path d="M3 -18 C7 -30 22 -30 21 -22 C20 -15 9 -13 3 -18 Z" />
              </svg>
            ))}
            <span className="tour-stumpy">
              <Stumpy size={150} followPointer />
            </span>
          </div>
        ) : null}
        <div className="tour-body">
          <div className="tour-head">
            {stop.welcome ? null : <Stumpy size={28} />}
            <h2 id="tour-title">{stop.title}</h2>
          </div>
          <p ref={copy} tabIndex={copyScrollable ? 0 : undefined}>
            {stop.body}
          </p>
          <div className="tour-foot">
            <span className="tour-count">
              {index + 1} of {STOPS.length}
            </span>
            {index > 0 ? (
              <button type="button" className={SECONDARY} onClick={() => go(index - 1)}>
                Back
              </button>
            ) : (
              <button type="button" className={SECONDARY} onClick={close}>
                Skip
              </button>
            )}
            <button
              ref={next}
              type="button"
              className={PRIMARY}
              onClick={() => (last ? close() : go(index + 1))}
            >
              {stop.welcome ? "Start the tour" : last ? "Done" : "Next"}
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}

/** Leaves drifting around Stumpy on the welcome card. */
const LEAVES = [
  { left: "12%", top: "16%", transform: "rotate(-20deg)" },
  { left: "80%", top: "22%", transform: "rotate(30deg) scale(0.8)" },
  { left: "20%", top: "64%", transform: "rotate(50deg) scale(0.7)" },
  { left: "74%", top: "68%", transform: "rotate(-35deg) scale(0.95)" },
];

/** Go to's entry that starts the tour again. */
export const TOUR_HREF = () => appURL("?tour=1");
