import { cn } from "./classnames";

/** The review's source and code chips: quiet, boxed, one line. */
export const chipClass = cn(
  "inline-flex max-w-full min-w-0 items-center gap-1 rounded-sm border border-subtle bg-surface-sunken px-1.5 py-0.5 text-xs leading-[1.4] text-secondary",
);

export const chipLinkClass = cn(
  chipClass,
  "hover:border-default hover:text-primary hover:no-underline",
);
