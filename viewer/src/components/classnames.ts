type ClassValue = string | false | null | undefined;

/** Joins class names. Note that it does NOT resolve conflicts: two competing
 *  utilities both survive and the winner is decided by stylesheet order, not by
 *  argument order. A component that needs to vary a size or a colour must
 *  expose a variant prop rather than expect a caller's `className` to win. */
export function cn(...classes: ClassValue[]) {
  return classes.filter(Boolean).join(" ");
}

/** The keyboard focus treatment, on every interactive surface that is not a
 *  native control with its own. Uses `--focus-ring`, which the design tokens
 *  have carried since the beginning without anything consuming it. */
export const FOCUS_RING = "focus-visible:shadow-(--focus-ring) focus-visible:outline-none";
