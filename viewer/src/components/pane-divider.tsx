import { type RefObject, useEffect, useState } from "react";

import { clampWidth, CONVERSATION_LIMITS, type PaneLimits } from "../use-conversation-width";
import { cn, FOCUS_RING } from "./classnames";

/** The grab handle between the review pane and a pane beside it: the
 *  conversation after it, or the section rail before it.
 *
 *  It is a `separator` with a value, not a decorative bar, so the split is
 *  reachable from the keyboard: a pointer is not the only way a reader trades
 *  one pane against the other.
 *
 *  It measures the two panes rather than the row they sit in. The row also
 *  holds the other panes, the handles and the gaps, and counting those as
 *  space the panes share lets the sized pane take a width that pushes the
 *  review pane under its floor — on a 1280px window, a 680px conversation
 *  leaves the review pane 242px of the 520px it is promised. */
export function PaneDivider({
  width,
  onWidth,
  onFit,
  reviewRef,
  paneRef,
  label,
  limits = CONVERSATION_LIMITS,
  side = "after",
  className,
}: {
  width: number;
  /** A width the reader chose. Remembered. */
  onWidth: (width: number) => void;
  /** The chosen width brought inside what the row can give. Not remembered,
   *  so a window too narrow to show the reader's split does not erase it.
   *  Only one pane may fit itself to the row: two each giving the review pane
   *  its whole shortfall would overshoot, then take it back, forever. */
  onFit?: (available: number) => void;
  /** The two panes being traded. Their widths are what the reader is dividing;
   *  everything else in the row is fixed. */
  reviewRef: RefObject<HTMLElement | null>;
  paneRef: RefObject<HTMLElement | null>;
  label: string;
  limits?: PaneLimits;
  /** Where the sized pane sits relative to the review pane. */
  side?: "before" | "after";
  className?: string;
}) {
  const [dragging, setDragging] = useState(false);
  // Undefined rather than zero when either pane has not been laid out yet: a
  // zero would clamp the width to the minimum on the way past, and a reader
  // would find their split reset by a measurement taken too early.
  const shared = () => {
    const review = reviewRef.current?.getBoundingClientRect().width;
    const pane = paneRef.current?.getBoundingClientRect().width;
    return review && pane ? review + pane : undefined;
  };
  const clamp = (next: number) => clampWidth(next, limits, shared());
  // Dragging or stepping toward the sized pane narrows it.
  const grow = side === "after" ? "ArrowLeft" : "ArrowRight";
  const shrink = side === "after" ? "ArrowRight" : "ArrowLeft";

  // A width remembered from a wider window, or one left behind by a window
  // that has since narrowed, would otherwise hold past what the row can give.
  // The drag and keyboard paths clamp as they go; this is the same clamp for
  // the paths that do not go through them.
  useEffect(() => {
    if (!onFit) return;
    const toRow = () => {
      const available = shared();
      if (available !== undefined) onFit(available);
    };
    toRow();
    window.addEventListener("resize", toRow);
    return () => window.removeEventListener("resize", toRow);
  });

  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label={label}
      aria-valuenow={width}
      aria-valuemin={limits.min}
      aria-valuemax={clamp(limits.max)}
      tabIndex={0}
      className={cn(
        "group hidden cursor-col-resize touch-none items-stretch justify-center rounded-(--radius-sm) xl:flex",
        FOCUS_RING,
        className,
      )}
      onPointerDown={(event) => {
        event.preventDefault();
        event.currentTarget.setPointerCapture(event.pointerId);
        setDragging(true);
      }}
      onPointerMove={(event) => {
        if (!dragging) return;
        const box = paneRef.current?.getBoundingClientRect();
        if (!box) return;
        onWidth(clamp(side === "after" ? box.right - event.clientX : event.clientX - box.left));
      }}
      onPointerUp={(event) => {
        event.currentTarget.releasePointerCapture(event.pointerId);
        setDragging(false);
      }}
      onPointerCancel={() => setDragging(false)}
      onLostPointerCapture={() => setDragging(false)}
      onKeyDown={(event) => {
        const step = event.shiftKey ? 64 : 16;
        if (event.key === grow) {
          event.preventDefault();
          onWidth(clamp(width + step));
        } else if (event.key === shrink) {
          event.preventDefault();
          onWidth(clamp(width - step));
        } else if (event.key === "Home") {
          event.preventDefault();
          onWidth(clamp(limits.fallback));
        }
      }}
    >
      <span
        aria-hidden
        className={cn(
          "w-px rounded-full transition-colors",
          dragging
            ? "bg-(--border-focus)"
            : "bg-(--border-subtle) group-hover:bg-(--border-strong)",
        )}
      />
    </div>
  );
}
