import { extendTailwindMerge } from "tailwind-merge";

type ClassValue = string | false | null | undefined;

/** tailwind-merge, taught the names our theme adds (`viewer.css` and the
 *  hosts' type steps). Without them it reads a step it does not know, such as
 *  `text-3xs` or `text-public-note`, as a text colour and drops it next to
 *  `text-secondary`, and reads `shadow-card` as a shadow colour. */
const merge = extendTailwindMerge({
  extend: {
    theme: {
      text: ["3xs", (value: string) => value.startsWith("public-")],
      shadow: ["card", "raised", "overlay"],
    },
  },
});

/** Joins class names, and resolves conflicting utilities in favour of the
 *  later one: `cn("px-4 text-secondary", className)` lets a caller's `px-2`
 *  or `text-primary` replace the default rather than compete with it. */
export function cn(...classes: ClassValue[]) {
  return merge(...classes);
}

/** The keyboard focus treatment, on every interactive surface that is not a
 *  native control with its own. Uses `--focus-ring`, which the design tokens
 *  have carried since the beginning without anything consuming it. */
export const FOCUS_RING = "focus-visible:shadow-(--focus-ring) focus-visible:outline-none";
