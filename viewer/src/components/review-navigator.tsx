import type { ReactNode, Ref } from "react";

import { cn, FOCUS_RING } from "./classnames";
import {
  ChecksIcon,
  ChevronDownIcon,
  ChevronLeftIcon,
  ChevronRightIcon,
  type IconProps,
  TeamChatIcon,
} from "./icons";
import { IconButton } from "./ui";

/** A place the reader can step to from the one they are on. */
export type ReviewStep = { label: string; go: () => void };

/** The bar that stands in for the rail where the panes stack (below `xl`):
 *  where the reader is, a way to every other place, and the previous and next
 *  place in reading order. A rail stacked above the pane is a screen of rows
 *  to scroll past before the review starts, and a tap on one of them changes
 *  a pane that is out of sight; this bar keeps both questions, where am I and
 *  where next, one line tall and in view.
 *
 *  It sticks under whatever bar the page keeps at the top of the window: the
 *  host's, named by `--review-host-bar` (below `lg`), or the head's compact
 *  bar (from `lg`). */
export function ReviewNavigator({
  ref,
  label,
  icon: Icon,
  position,
  check,
  previous,
  next,
  expanded,
  onOpen,
  aside,
}: {
  ref?: Ref<HTMLDivElement>;
  /** The place on screen, as the pane names it. */
  label: string;
  icon: (props: IconProps) => ReactNode;
  /** "2 of 6", for a section. */
  position?: string;
  /** The host's read mark or approval for the place, when it has one. */
  check?: ReactNode;
  previous: ReviewStep | null;
  next: ReviewStep | null;
  /** Whether the list of places is open. */
  expanded: boolean;
  onOpen: () => void;
  /** The side pane, which below `xl` is a sheet this bar opens rather than
   *  a second page under the review: its name ("Conversation", or "Details"
   *  without one), whether one of its tabs flags something, and whether it is
   *  open. */
  aside: { label: string; alert: boolean; expanded: boolean; onOpen: () => void };
}) {
  return (
    <div
      ref={ref}
      className="sticky top-(--review-host-bar,0px) z-(--z-sticky) -mb-px flex h-12 items-center gap-0.5 border-b border-(--border-subtle) bg-(--surface-card) pr-2 pl-1 lg:top-12 xl:hidden"
    >
      <button
        type="button"
        aria-expanded={expanded}
        aria-controls="review-rail"
        title="All sections"
        onClick={onOpen}
        className={cn(
          "flex h-10 min-w-0 flex-1 cursor-pointer items-center gap-1.5 rounded-(--radius-sm) px-1.5 text-left hover:bg-(--surface-hover)",
          FOCUS_RING,
        )}
      >
        <Icon className="flex-none text-(--action-accent)" />
        <span className="min-w-0 truncate text-base font-medium text-(--text-primary)">
          {label}
        </span>
        {position ? (
          <span className="flex-none text-xs tabular-nums text-(--text-tertiary)">{position}</span>
        ) : null}
        <ChevronDownIcon size={12} className="flex-none text-(--text-tertiary)" />
      </button>
      {check}
      <IconButton
        label={previous ? `Previous: ${previous.label}` : "Previous"}
        disabled={!previous}
        onClick={previous?.go}
        className="h-10 w-9"
      >
        <ChevronLeftIcon />
      </IconButton>
      <IconButton
        label={next ? `Next: ${next.label}` : "Next"}
        disabled={!next}
        onClick={next?.go}
        className="h-10 w-9"
      >
        <ChevronRightIcon />
      </IconButton>
      <span aria-hidden className="mx-0.5 h-6 w-px flex-none bg-(--border-subtle)" />
      <IconButton
        label={aside.alert ? `${aside.label}: CI checks failing` : aside.label}
        aria-expanded={aside.expanded}
        aria-controls="review-aside"
        onClick={aside.onOpen}
        className="relative h-10 w-9 text-(--text-secondary)"
      >
        {aside.label === "Conversation" ? <TeamChatIcon /> : <ChecksIcon />}
        {aside.alert ? (
          <span
            aria-hidden
            className="absolute top-2 right-2 h-1.5 w-1.5 rounded-full bg-(--status-critical)"
          />
        ) : null}
      </IconButton>
    </div>
  );
}

/** The foot of a pane where the panes stack: the way on to the next place in
 *  reading order, so a reader who has reached the end of a section does not
 *  have to go back up for the next one. */
export function ReviewPaneFoot({
  previous,
  next,
}: {
  previous: ReviewStep | null;
  next: ReviewStep | null;
}) {
  if (!previous && !next) return null;
  const step = cn(
    "flex min-w-0 cursor-pointer flex-col gap-0.5 rounded-(--radius-sm) border border-(--border-default) px-3 py-2.5 hover:bg-(--surface-hover)",
    FOCUS_RING,
  );
  return (
    <nav
      aria-label="Next in review"
      className="mt-8 flex gap-3 border-t border-(--border-subtle) pt-4 xl:hidden"
    >
      {previous ? (
        <button type="button" onClick={previous.go} className={cn(step, "max-w-[45%] text-left")}>
          <span className="flex items-center gap-1 text-xs text-(--text-tertiary)">
            <ChevronLeftIcon size={12} />
            Previous
          </span>
          <span className="truncate text-base text-(--text-secondary)">{previous.label}</span>
        </button>
      ) : null}
      {next ? (
        <button
          type="button"
          onClick={next.go}
          className={cn(step, "ml-auto flex-1 text-right sm:max-w-sm")}
        >
          <span className="flex items-center justify-end gap-1 text-xs text-(--text-tertiary)">
            Next
            <ChevronRightIcon size={12} />
          </span>
          <span className="line-clamp-2 text-base font-medium text-(--text-primary)">
            {next.label}
          </span>
        </button>
      ) : null}
    </nav>
  );
}
