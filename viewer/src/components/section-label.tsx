import type { HTMLAttributes } from "react";

import { cn } from "./classnames";
import { SECTION_LABEL } from "./primitive-classes";

type SectionLabelProps = HTMLAttributes<HTMLElement> & {
  /** The element: a heading where the label names a section (the default),
   *  otherwise text. */
  as?: "h2" | "h3" | "h4" | "p" | "span" | "dt" | "div";
};

/** A section's label (`SECTION_LABEL`, `primitive-classes.ts`). */
export function SectionLabel({ as: Tag = "h3", className, ...props }: SectionLabelProps) {
  return <Tag {...props} className={cn(SECTION_LABEL, className)} />;
}
