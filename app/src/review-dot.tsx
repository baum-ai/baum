import { LiveDot } from "@baum/viewer/components/ui";
import { useEffect, useRef, useState } from "react";
import { NineDots } from "./nine-dots";

export type ReviewDotState = "queued" | "generating" | "ready" | "outdated" | "failed";

const DOTS: Record<ReviewDotState, { label: string; note: string; color: string }> = {
  queued: {
    label: "Review queued",
    note: "baum will generate this review shortly.",
    color: "text-secondary",
  },
  generating: {
    label: "Generating review",
    note: "baum is generating this review now.",
    color: "text-secondary",
  },
  ready: {
    label: "Review ready",
    note: "The review covers the latest changes.",
    color: "text-status-positive",
  },
  outdated: {
    label: "Review outdated",
    note: "There are changes since the review was written.",
    color: "text-status-caution",
  },
  failed: {
    label: "Review failed",
    note: "The review could not be generated.",
    color: "text-status-critical",
  },
};

/** How a pull request's review stands, for its dot: null when it has none
 *  (or the daemon can't tell), so the row keeps an empty slot. A review job
 *  waiting in the queue is queued until the daemon marks it generating. */
export function reviewDotState(status: string, queued = false): ReviewDotState | null {
  if (status === "generating") return "generating";
  if (queued) return "queued";
  if (status === "current") return "ready";
  if (status === "outdated" || status === "failed") return status;
  return null;
}

/** A group review's dot: none for a stack, which has no combined review, or
 *  for a group not yet reviewed. Its job running or queued reads generating. */
export function groupDotState(
  group: { stack?: boolean; version_count: number; outdated?: boolean },
  generating: boolean,
): ReviewDotState | null {
  if (group.stack) return null;
  if (generating) return "generating";
  if (group.version_count === 0) return null;
  return group.outdated ? "outdated" : "ready";
}

const CLOSE_MS = 150;

/** The dot before a row's title for how its review stands. A list row
 *  carries no buttons (.agents/guides/ui.md), so the dot is not one: its
 *  label is read with the row, and its note opens on hover with a mouse and
 *  stays while the pointer moves onto it. The row's own page says the same in
 *  words for the keyboard. A native popover keeps the note out of the list's
 *  clipping; it opens above the dot when there is no room below, and closes
 *  on scroll, which would leave it behind. */
export function ReviewDot({
  state,
  detail,
}: {
  state: ReviewDotState | null;
  /** Said after a failed review's note: why it failed. */
  detail?: string;
}) {
  const anchor = useRef<HTMLSpanElement>(null);
  const popup = useRef<HTMLSpanElement>(null);
  const closing = useRef<ReturnType<typeof setTimeout>>(undefined);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!open) return;
    const hide = () => popup.current?.hidePopover();
    window.addEventListener("scroll", hide, { capture: true, passive: true });
    return () => window.removeEventListener("scroll", hide, { capture: true });
  }, [open]);
  useEffect(() => () => clearTimeout(closing.current), []);

  if (!state) return <span aria-hidden className="review-dot" />;
  const dot = DOTS[state];

  const show = (event: React.PointerEvent) => {
    if (event.pointerType !== "mouse") return;
    clearTimeout(closing.current);
    const element = popup.current;
    const rect = anchor.current?.getBoundingClientRect();
    if (!element || !rect || element.matches(":popover-open")) return;
    element.showPopover();
    element.style.left = `${Math.max(8, Math.min(rect.left, window.innerWidth - element.offsetWidth - 8))}px`;
    const below = rect.bottom + 4 + element.offsetHeight + 8 <= window.innerHeight;
    element.style.top = `${below ? rect.bottom + 4 : Math.max(8, rect.top - 4 - element.offsetHeight)}px`;
  };
  const hide = (event: React.PointerEvent) => {
    if (event.pointerType !== "mouse") return;
    clearTimeout(closing.current);
    closing.current = setTimeout(() => popup.current?.hidePopover(), CLOSE_MS);
  };

  return (
    <span className="review-dot">
      <span
        ref={anchor}
        className={`-m-1.5 flex items-center justify-center p-1.5 ${dot.color}`}
        onPointerEnter={show}
        onPointerLeave={hide}
      >
        {state === "queued" ? (
          <NineDots size={14} />
        ) : state === "generating" ? (
          <LiveDot />
        ) : (
          <span aria-hidden className="block h-2 w-2 rounded-full bg-current" />
        )}
        <span className="sr-only">{dot.label}</span>
      </span>
      <span
        ref={popup}
        popover="manual"
        aria-hidden
        onPointerEnter={show}
        onPointerLeave={hide}
        onToggle={(event) => setOpen(event.newState === "open")}
        className="fixed m-0 w-max max-w-[min(20rem,calc(100vw-1rem))] rounded-md border border-default bg-surface-card px-2.5 py-1.5 text-left text-xs font-normal whitespace-normal text-secondary shadow-overlay"
      >
        <span className="font-medium text-primary">{dot.label}.</span> {dot.note}
        {state === "failed" && detail ? ` ${detail}` : null}
      </span>
    </span>
  );
}
