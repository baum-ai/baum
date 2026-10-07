import { cn, FOCUS_RING } from "./classnames";

/** The segmented control's classes, kept apart from `ui.tsx` so a group of
 *  toggles elsewhere can read as the same control. */

export const SEGMENT_BASE =
  "inline-flex cursor-pointer items-center gap-1.5 text-base whitespace-nowrap transition-colors duration-150";

/** The sunken track of the `segmented` control, for a group of toggles that
 *  should read as the same control. */
export const SEGMENTED_TRACK =
  "inline-flex items-center gap-0.5 rounded-(--radius-md) bg-(--surface-sunken) p-[3px]";

/** A tile in that track, raised when chosen. */
export function segmentedTileClass(selected: boolean): string {
  return cn(
    SEGMENT_BASE,
    FOCUS_RING,
    "h-7 rounded-(--radius-sm) px-2.5",
    selected
      ? "bg-(--surface-card) font-medium text-(--text-primary) shadow-(--shadow-card)"
      : "text-(--text-secondary) hover:text-(--text-primary)",
  );
}
