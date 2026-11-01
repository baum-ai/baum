import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { CloseIcon } from "@baum/viewer/components/icons";
import { Stumpy } from "@baum/viewer/components/stumpy";
import { Button } from "@baum/viewer/components/ui";
import { appURL, navigate } from "./api";
import { PRIMARY, SECONDARY, SMALL } from "./action-classes";
import { PAGES } from "./pages";
import { COMMAND } from "./shortcuts";
import { uiState, useUIState } from "./ui-state";
import "./tour.css";

/** Whether the tour is open: once on this computer's first launch, kept by
 *  the daemon since the app's address can change, and again whenever Go to
 *  asks for it (`?tour=1`). Closing it clears that request, so asking again
 *  opens it. */
export function useTour(asked: boolean) {
  const saved = useUIState(uiState("api/ui-state/tour", () => ({ seen: false })));
  const firstRun = saved.ready && !saved.error && !saved.value.seen;
  const [open, setOpen] = useState(false);
  useEffect(() => {
    if (firstRun || asked) setOpen(true);
  }, [firstRun, asked]);
  const close = () => {
    if (!saved.value.seen) saved.update(() => ({ seen: true }));
    setOpen(false);
    if (asked) navigate(appURL());
  };
  return { open, close };
}

/** A button named in the copy, drawn as the button it names. */
const B = ({ children }: { children: ReactNode }) => <span className={SMALL}>{children}</span>;
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
};

const STOPS: Stop[] = [
  {
    title: "Welcome to baum",
    body: "A quick look around. Most of your day is the Inbox and a pull request.",
    at: href("inbox"),
  },
  {
    title: "Your Inbox",
    body: "What waits on you, grouped by what to do: review, merge or hand to an agent.",
    target: ".upnext-list",
    at: href("inbox"),
  },
  {
    title: "Why it's here",
    body: (
      <>
        Who's waiting, what changed and how risky it is. <B>Open</B> takes you to the full review.
      </>
    ),
    target: ".upnext-panel",
    at: href("inbox"),
  },
  {
    title: "Use the keys",
    body: (
      <>
        <kbd>j</kbd> <kbd>k</kbd> move, <kbd>Enter</kbd> opens, <kbd>?</kbd> lists every key.
      </>
    ),
    target: ".upnext-list",
    at: href("inbox"),
  },
  {
    title: "Open a pull request",
    body: "Next opens this one.",
    target: ".upnext-panel .upnext-primary",
    at: href("inbox"),
    follow: ".upnext-panel .upnext-primary",
  },
  {
    title: "Start with the summary",
    body: "What it does and its riskiest part, before any diff.",
    target: "#review-section",
    at: "review",
  },
  {
    title: "Changes, riskiest first",
    body: "Each cites its lines and the agent session that wrote them.",
    // In a narrow window the list is a sheet, and its button stands for it.
    target: "#review-rail, [aria-controls=review-rail]",
    at: "review",
  },
  {
    title: "Act on it",
    body: (
      <>
        <B>Ask</B> questions about the change, <B>Regenerate review</B> after new commits,{" "}
        <B>Merge…</B> when it's ready.
      </>
    ),
    target: "[data-tour=review-actions]",
    at: "review",
  },
  {
    title: "Timeline",
    body: "Each feature you're building: its status, people and pull requests.",
    target: ".local-main",
    at: href("timeline"),
  },
  {
    title: "Decisions",
    body: "The decisions behind the code, why they were made and the prompt behind each.",
    target: ".local-main",
    at: `${href("timeline")}&tab=all`,
  },
  {
    title: "Sessions",
    body: (
      <>
        Every agent session on this computer. <B>Focus session</B> jumps straight back into a
        running one.
      </>
    ),
    target: ".local-main",
    at: href("sessions"),
  },
  {
    title: "Analytics",
    body: "Usage, cost and what landed, from your sessions.",
    target: ".local-main",
    at: href("analytics"),
  },
  {
    title: "That's the tour",
    body: (
      <>
        Take it again from Go to (<kbd>{COMMAND}K</kbd>).
      </>
    ),
    at: href("inbox"),
  },
];

const GAP = 12;

/** Beside the target if it fits, else below or above it, else in the
 *  window's corner over a target that fills it. */
function place(target: DOMRect | null, width: number, height: number) {
  const vw = innerWidth;
  const vh = innerHeight;
  if (!target) return { left: (vw - width) / 2, top: (vh - height) / 2 };
  const top = Math.max(GAP, Math.min(vh - height - GAP, target.top));
  const left = Math.max(GAP, Math.min(vw - width - GAP, target.left));
  if (target.right + GAP + width < vw) return { left: target.right + GAP, top };
  if (target.left - GAP - width > 0) return { left: target.left - GAP - width, top };
  if (target.bottom + GAP + height < vh) return { left, top: target.bottom + GAP };
  if (target.top - GAP - height > 0) return { left, top: target.top - GAP - height };
  return { left: vw - width - 2 * GAP, top: vh - height - 2 * GAP };
}

/** The app's first-run tour: a small card that steps through the Inbox and
 *  a pull request, then the Timeline, Sessions and Analytics, pointing at
 *  each. It goes to each page itself. Finishing or skipping it is remembered
 *  on this computer. */
export function Tour({ close }: { close: () => void }) {
  const [index, setIndex] = useState(0);
  const [rect, setRect] = useState<DOMRect | null>(null);
  const card = useRef<HTMLDivElement>(null);
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
    setIndex(to);
  };

  // Follow the target as the page loads, scrolls and resizes.
  useEffect(() => {
    const measure = () => {
      if (stop.at === "review" && new URLSearchParams(location.search).has("pr"))
        review.current = `${location.pathname}${location.search}`;
      // The first match on screen: a narrow window hides some targets.
      const box =
        [...(stop.target ? document.querySelectorAll(stop.target) : [])]
          .map((element) => element.getBoundingClientRect())
          .find((found) => found.width && found.height) ?? null;
      setRect((old) =>
        old &&
        box &&
        ["top", "left", "width", "height"].every((k) => old[k as "top"] === box[k as "top"])
          ? old
          : box,
      );
    };
    measure();
    const timer = setInterval(measure, 150);
    return () => clearInterval(timer);
  }, [stop]);

  const [position, setPosition] = useState({ left: 0, top: 0 });
  useLayoutEffect(() => {
    const element = card.current;
    if (element) setPosition(place(rect, element.offsetWidth, element.offsetHeight));
  }, [rect, index]);
  useEffect(() => next.current?.focus({ preventScroll: true }), [index]);

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
        className="tour-card"
        style={position}
        // Enter and Space press the tour's buttons and Escape closes it; every
        // other key reaches the page, so the keys it teaches work.
        onKeyDown={(event) => {
          if (event.key === "Escape") close();
          if (["Escape", "Enter", " "].includes(event.key)) event.stopPropagation();
        }}
      >
        <Button
          variant="ghost"
          size="compact"
          aria-label="Close the tour"
          onClick={close}
          className="tour-close"
        >
          <CloseIcon size={14} />
        </Button>
        <div className="tour-head">
          <Stumpy size={28} />
          <h2 id="tour-title">{stop.title}</h2>
        </div>
        <p>{stop.body}</p>
        <div className="tour-foot">
          <span className="tour-count">
            {index + 1} of {STOPS.length}
          </span>
          {last ? null : index > 0 ? (
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
            {index === 0 ? "Start" : last ? "Done" : "Next"}
          </button>
        </div>
      </div>
    </div>
  );
}

/** Go to's entry that starts the tour again. */
export const TOUR_HREF = () => appURL("?tour=1");
