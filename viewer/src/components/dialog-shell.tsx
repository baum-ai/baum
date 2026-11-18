import type { ComponentProps, ReactNode } from "react";

import { cn } from "./classnames";

/** The parts of a dialog drawn with `dialogClassName` (`primitive-classes.ts`).
 *
 *  A dialog's head: its title and detail on the left, an `aside` (a close
 *  button) on the right. */
export function DialogHeader({
  children,
  aside,
  className,
}: {
  children: ReactNode;
  aside?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("mb-4 flex items-start justify-between gap-4", className)}>
      <div>{children}</div>
      {aside}
    </div>
  );
}

export function DialogTitle({ className, children, ...props }: ComponentProps<"h2">) {
  return (
    <h2 {...props} className={cn("text-lg leading-normal font-semibold", className)}>
      {children}
    </h2>
  );
}

export function DialogDetail({ className, ...props }: ComponentProps<"p">) {
  return <p {...props} className={cn("mt-0.5 text-xs text-tertiary", className)} />;
}

/** A dialog's actions, at its end. `ruled` sets them below a hairline, for a
 *  dialog whose body is long enough to need the break. */
export function DialogActions({
  children,
  ruled = false,
  className,
}: {
  children: ReactNode;
  ruled?: boolean;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-wrap items-center justify-end gap-2",
        ruled && "border-t border-subtle pt-4",
        className,
      )}
    >
      {children}
    </div>
  );
}
