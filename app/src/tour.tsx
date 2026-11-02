import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { CloseIcon } from "@baum/viewer/components/icons";
import { Stumpy } from "@baum/viewer/components/stumpy";
import { Button } from "@baum/viewer/components/ui";
import { appURL, inSample, navigate, ownBase } from "./api";
import { PRIMARY, SECONDARY, SMALL } from "./action-classes";
import { PAGES } from "./pages";
import { COMMAND } from "./shortcuts";
import { uiState, useUIState } from "./ui-state";
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
    title: "Your Inbox",
    body: (
      <>
        {inSample() ? "Welcome to baum, here in a sample project. " : "Welcome to baum. "}
        Pull requests waiting on you, grouped by what to do: review, merge or hand to an agent. Next
        opens the selected one.
      </>
    ),
    target: ".upnext-list",
    at: href("inbox"),
    follow: ".upnext-panel .upnext-primary",
  },
  {
    title: "A pull request's review",
    body: (
      <>
        What the change does and its riskiest part come first, then each change, riskiest first,
        with its lines. <B>Ask</B> about it, or <B>Merge…</B> when it's ready.
      </>
    ),
    target: "#review-section",
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

/** The app's first-run tour: a small card that steps through the Inbox, a
 *  pull request's review, the Timeline and Sessions, pointing at each. It goes to each page itself. Finishing or skipping it is remembered
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

  // ← and → step between the cards and Enter presses Next or Done, wherever the focus is,
  // ahead of the page's own keys. A text field and the card's other buttons
  // and dialogs keep theirs. They are the tour's alone, so the key list leaves them out.
  const step = useRef<(key: string) => void>(() => {});
  step.current = (key) => {
    if (key === "ArrowLeft") {
      if (index > 0) go(index - 1);
    } else if (!last) go(index + 1);
    // Enter on the last card is Done; → stays there.
    else if (key === "Enter") close();
  };
  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!["ArrowLeft", "ArrowRight", "Enter"].includes(event.key)) return;
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
            {last ? "Done" : "Next"}
          </button>
        </div>
      </div>
    </div>
  );
}

/** Go to's entry that starts the tour again. */
export const TOUR_HREF = () => appURL("?tour=1");
