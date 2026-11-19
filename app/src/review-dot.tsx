import { cn, FOCUS_RING } from "@baum/viewer/components/classnames";
import { useId, useRef } from "react";
import { NineDots } from "./nine-dots";

export type ReviewDotState = "generating" | "ready" | "outdated" | "failed";

const DOTS: Record<ReviewDotState, { label: string; note: string; color: string }> = {
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

/** How a pull request's saved review stands, for its inbox dot: null when it
 *  has none (or the daemon can't tell), so the row keeps an empty slot. A
 *  queued review counts as generating before the daemon marks it so. */
export function reviewDotState(status: string, queued = false): ReviewDotState | null {
  if (queued || status === "generating") return "generating";
  if (status === "current") return "ready";
  if (status === "outdated" || status === "failed") return status;
  return null;
}

/** The dot before an inbox row's title. Its meaning opens in a note on hover
 *  with a mouse and on tap or Enter otherwise, since a `title` never shows on
 *  a touch screen; a native popover keeps the note out of the list's
 *  clipping, and the note stays the dot's accessible description. */
export function ReviewDot({
  state,
  detail,
}: {
  state: ReviewDotState | null;
  /** Said after the state's own note, such as why a review failed. */
  detail?: string;
}) {
  const id = useId();
  const trigger = useRef<HTMLButtonElement>(null);
  const popup = useRef<HTMLDivElement>(null);
  const pointer = useRef("");
  if (!state) return <span aria-hidden className="inbox-review-dot" />;
  const dot = DOTS[state];

  const place = () => {
    const button = trigger.current;
    const element = popup.current;
    if (!button || !element) return;
    const rect = button.getBoundingClientRect();
    element.style.left = `${Math.max(8, Math.min(rect.left, window.innerWidth - element.offsetWidth - 8))}px`;
    element.style.top = `${rect.bottom + 4}px`;
  };
  const show = () => {
    const element = popup.current;
    if (!element || element.matches(":popover-open")) return;
    element.showPopover();
    place();
  };
  const hide = () => {
    const element = popup.current;
    if (element?.matches(":popover-open")) element.hidePopover();
  };

  return (
    <span className="inbox-review-dot">
      <button
        ref={trigger}
        type="button"
        popoverTarget={id}
        aria-describedby={id}
        className={cn(
          "-m-1.5 flex cursor-help items-center justify-center rounded-sm p-1.5",
          FOCUS_RING,
          dot.color,
        )}
        onPointerDown={(event) => {
          pointer.current = event.pointerType;
        }}
        onPointerEnter={(event) => {
          if (event.pointerType === "mouse") show();
        }}
        onPointerLeave={(event) => {
          if (event.pointerType === "mouse") hide();
        }}
        onClick={(event) => {
          // Hover already opened it; a click must not toggle it shut again.
          if (pointer.current === "mouse") event.preventDefault();
          pointer.current = "";
        }}
      >
        {state === "generating" ? (
          <NineDots size={14} />
        ) : (
          <span aria-hidden className="block h-2 w-2 rounded-full bg-current" />
        )}
        <span className="sr-only">{dot.label}</span>
      </button>
      <span
        ref={popup}
        id={id}
        popover="auto"
        role="tooltip"
        onToggle={(event) => {
          if (event.newState === "open") place();
        }}
        className="fixed m-0 w-max max-w-[min(20rem,calc(100vw-1rem))] rounded-md border border-default bg-surface-card px-2.5 py-1.5 text-xs font-normal whitespace-normal text-secondary shadow-overlay"
      >
        <span className="font-medium text-primary">{dot.label}.</span> {dot.note}
        {detail ? ` ${detail}` : null}
      </span>
    </span>
  );
}
