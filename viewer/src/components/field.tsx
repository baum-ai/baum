import type { ReactNode } from "react";

import { cn } from "./classnames";
import { FIELD_LABEL } from "./primitive-classes";

/** A labelled field: the label in small secondary text above its control,
 *  which the caller renders with `fieldClassName` (`primitive-classes.ts`). */
export function Field({
  label,
  children,
  className,
}: {
  label: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <label className={cn(FIELD_LABEL, className)}>
      {label}
      {children}
    </label>
  );
}
